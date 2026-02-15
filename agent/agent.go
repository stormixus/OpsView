package main

import (
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/opsview/opsview/proto"
)

// Agent orchestrates capture → tile → compress → send pipeline.
type Agent struct {
	cfg      AgentConfig
	conn     *websocket.Conn
	connMu   sync.Mutex
	seq      atomic.Uint32
	capturer Capturer
	stopped  chan struct{}
}

func NewAgent(cfg AgentConfig) *Agent {
	return &Agent{
		cfg:     cfg,
		stopped: make(chan struct{}),
	}
}

func (a *Agent) Run() {
	for {
		select {
		case <-a.stopped:
			return
		default:
		}

		// 1) Connect to relay
		if err := a.connect(); err != nil {
			log.Printf("[agent] connect error: %v", err)
			a.backoff()
			continue
		}

		// 2) Initialize capturer
		cap, err := NewCapturer(a.cfg)
		if err != nil {
			log.Printf("[agent] capturer init error: %v", err)
			a.closeConn()
			a.backoff()
			continue
		}
		a.capturer = cap

		// 3) Capture loop
		a.captureLoop()

		// Cleanup
		a.capturer.Close()
		a.closeConn()
	}
}

func (a *Agent) Stop() {
	close(a.stopped)
	a.closeConn()
}

func (a *Agent) connect() error {
	log.Printf("[agent] connecting to %s", a.cfg.RelayURL)
	conn, _, err := websocket.DefaultDialer.Dial(a.cfg.RelayURL, nil)
	if err != nil {
		return err
	}

	// Send HELLO
	hello := proto.Hello{
		Role:          "publisher",
		Client:        "opsview-agent",
		ClientVersion: "0.1.0",
		Supports:      []string{"zstd"},
	}
	profileStr := "1080"
	if a.cfg.Profile == 720 {
		profileStr = "720"
	}
	hello.WantProfile = &profileStr

	helloPayload, _ := json.Marshal(hello)
	helloMsg := proto.MarshalMessage(proto.MsgHello, helloPayload)
	if err := conn.WriteMessage(websocket.BinaryMessage, helloMsg); err != nil {
		conn.Close()
		return err
	}

	// Send AUTH
	auth := proto.Auth{Token: a.cfg.Token}
	authPayload, _ := json.Marshal(auth)
	authMsg := proto.MarshalMessage(proto.MsgAuth, authPayload)
	if err := conn.WriteMessage(websocket.BinaryMessage, authMsg); err != nil {
		conn.Close()
		return err
	}

	a.connMu.Lock()
	a.conn = conn
	a.connMu.Unlock()

	log.Println("[agent] connected and authenticated")

	// Start reading control messages in background
	go a.readPump(conn)

	return nil
}

func (a *Agent) readPump(conn *websocket.Conn) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if len(data) < proto.HeaderSize {
			continue
		}
		hdr, err := proto.DecodeHeader(data)
		if err != nil {
			continue
		}
		if hdr.Type == proto.MsgError {
			var errMsg proto.ErrorMsg
			if json.Unmarshal(data[proto.HeaderSize:], &errMsg) == nil {
				log.Printf("[agent] relay error: %d %s", errMsg.Code, errMsg.Message)
			}
		} else if hdr.Type == proto.MsgControl {
			var ctrl proto.Control
			if json.Unmarshal(data[proto.HeaderSize:], &ctrl) == nil {
				log.Printf("[agent] control: %s profile=%s", ctrl.Cmd, ctrl.Profile)
				if ctrl.Cmd == "set_profile" {
					if ctrl.Profile == "720" {
						a.cfg.Profile = 720
					} else {
						a.cfg.Profile = 1080
					}
					// Capturer will pick up new profile on next init
				}
			}
		}
	}
}

func (a *Agent) captureLoop() {
	ticker := time.NewTicker(time.Second / time.Duration(a.cfg.FPSMax))
	defer ticker.Stop()

	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()

	consecutiveErrors := 0
	maxConsecutiveErrors := 10

	for {
		select {
		case <-a.stopped:
			return

		case <-heartbeat.C:
			a.sendHeartbeat()

		case <-ticker.C:
			tiles, width, height, err := a.capturer.CaptureFrame()
			if err != nil {
				consecutiveErrors++
				if consecutiveErrors > maxConsecutiveErrors {
					log.Printf("[agent] too many capture errors (%d), reinitializing", consecutiveErrors)
					return // Will reinitialize in Run() loop
				}
				log.Printf("[agent] capture error (%d/%d): %v", consecutiveErrors, maxConsecutiveErrors, err)
				continue
			}
			consecutiveErrors = 0

			if len(tiles) == 0 {
				continue // No changes
			}

			seq := a.seq.Add(1)
			fd := &proto.FrameDelta{
				Seq:       seq,
				TsMs:      uint64(time.Now().UnixMilli()),
				Profile:   uint16(a.cfg.Profile),
				Width:     uint16(width),
				Height:    uint16(height),
				TileSize:  uint16(a.cfg.TileSize),
				TileCount: uint16(len(tiles)),
				Tiles:     tiles,
			}

			payload := proto.EncodeFrameDelta(fd)
			msg := proto.MarshalMessage(proto.MsgFrameDelta, payload)

			a.connMu.Lock()
			conn := a.conn
			a.connMu.Unlock()

			if conn == nil {
				return
			}

			if err := conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
				log.Printf("[agent] send error: %v", err)
				return // Will reconnect in Run() loop
			}
		}
	}
}

func (a *Agent) sendHeartbeat() {
	a.connMu.Lock()
	conn := a.conn
	a.connMu.Unlock()
	if conn == nil {
		return
	}
	msg := proto.MarshalMessage(proto.MsgHeartbeat, nil)
	_ = conn.WriteMessage(websocket.BinaryMessage, msg)
}

func (a *Agent) closeConn() {
	a.connMu.Lock()
	if a.conn != nil {
		a.conn.Close()
		a.conn = nil
	}
	a.connMu.Unlock()
}

var backoffDurations = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
}

var backoffIdx int

func (a *Agent) backoff() {
	d := backoffDurations[backoffIdx]
	if backoffIdx < len(backoffDurations)-1 {
		backoffIdx++
	}
	log.Printf("[agent] backing off %v", d)
	select {
	case <-time.After(d):
	case <-a.stopped:
	}
}
