package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	profile  atomic.Int32
	capturer Capturer
	survMgr  *SurveillanceManager
	stopped  chan struct{}
}

func NewAgent(cfg AgentConfig) *Agent {
	a := &Agent{
		cfg:     cfg,
		stopped: make(chan struct{}),
	}
	a.profile.Store(int32(cfg.Profile))
	return a
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
		backoffIdx = 0

		// 2) Initialize capturer
		capCfg := a.cfg
		capCfg.Profile = int(a.profile.Load())
		cap, err := NewCapturer(capCfg)
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
		ClientVersion: Version,
		Supports:      []string{"zstd"},
	}
	profileStr := "1080"
	if a.profile.Load() == 720 {
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
	auth := proto.Auth{Token: a.cfg.PIN}
	authPayload, _ := json.Marshal(auth)
	authMsg := proto.MarshalMessage(proto.MsgAuth, authPayload)
	if err := conn.WriteMessage(websocket.BinaryMessage, authMsg); err != nil {
		conn.Close()
		return err
	}

	// Wait for relay response to confirm authentication
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, data, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{}) // clear deadline
	if err != nil {
		conn.Close()
		return fmt.Errorf("auth response: %w", err)
	}
	if len(data) >= proto.HeaderSize {
		hdr, hdrErr := proto.DecodeHeader(data)
		if hdrErr == nil && hdr.Type == proto.MsgError {
			var errMsg proto.ErrorMsg
			if json.Unmarshal(data[proto.HeaderSize:], &errMsg) == nil {
				conn.Close()
				return fmt.Errorf("relay rejected: %d %s", errMsg.Code, errMsg.Message)
			}
		}
	}

	a.connMu.Lock()
	a.conn = conn
	a.connMu.Unlock()

	log.Println("[agent] connected and authenticated")

	// Send surveillance config to relay
	a.sendSurvConfig()

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
						a.profile.Store(720)
					} else {
						a.profile.Store(1080)
					}
				}
			}
		} else if hdr.Type == proto.MsgSurvSnapshot {
			go a.handleSnapshotRequest(data[proto.HeaderSize:])
		}
	}
}

func (a *Agent) handleSnapshotRequest(payload []byte) {
	var req proto.SnapshotRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		log.Printf("[agent] snapshot request parse error: %v", err)
		return
	}

	resp := proto.SnapshotResponse{
		ReqID: req.ReqID,
		DVRID: req.DVRID,
		ChNum: req.ChNum,
	}

	if a.survMgr == nil {
		resp.Error = "surveillance manager not initialized"
	} else {
		data, err := a.survMgr.FetchSnapshot(req.DVRID, req.ChNum)
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Data = base64.StdEncoding.EncodeToString(data)
		}
	}

	respPayload, _ := json.Marshal(resp)
	msg := proto.MarshalMessage(proto.MsgSurvSnapshot, respPayload)

	a.connMu.Lock()
	conn := a.conn
	a.connMu.Unlock()
	if conn != nil {
		if err := conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
			log.Printf("[agent] snapshot response send error: %v", err)
		}
	}
}

func (a *Agent) sendSurvConfig() {
	if a.survMgr == nil {
		return
	}

	dvrs, err := a.survMgr.ListDVRs()
	if err != nil {
		log.Printf("[agent] sendSurvConfig: list DVRs: %v", err)
		return
	}

	cfg := proto.SurvConfig{}
	for _, d := range dvrs {
		cfg.DVRs = append(cfg.DVRs, proto.DVRInfo{
			ID: d.ID, Name: d.Name, Addr: d.Addr, Port: d.Port,
			Username: d.Username, Password: d.Password,
			RefreshRate: d.RefreshRate, StreamQuality: d.StreamQuality, Protocol: d.Protocol,
		})
		chs, err := a.survMgr.ListChannels(d.ID)
		if err != nil {
			log.Printf("[agent] sendSurvConfig: list channels DVR %d: %v", d.ID, err)
			continue
		}
		for _, ch := range chs {
			cfg.Channels = append(cfg.Channels, proto.ChannelInfo{
				ID: ch.ID, DVRID: ch.DVRID, ChNum: ch.ChNum,
				Name: ch.Name, Order: ch.Order, Enabled: ch.Enabled,
				Width: ch.Width, Height: ch.Height,
			})
		}
	}

	payload, _ := json.Marshal(cfg)
	msg := proto.MarshalMessage(proto.MsgSurvConfig, payload)

	a.connMu.Lock()
	conn := a.conn
	a.connMu.Unlock()
	if conn != nil {
		if err := conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
			log.Printf("[agent] sendSurvConfig send error: %v", err)
		} else {
			log.Printf("[agent] sent surveillance config: %d DVRs, %d channels", len(cfg.DVRs), len(cfg.Channels))
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
			profile := uint16(a.profile.Load())
			fd := &proto.FrameDelta{
				Seq:       seq,
				TsMs:      uint64(time.Now().UnixMilli()),
				Profile:   profile,
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
