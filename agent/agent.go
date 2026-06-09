package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/opsview/opsview/proto"
)

// isPlaintextPublicRelay reports whether relayURL sends traffic unencrypted
// (ws://) to a non-local, non-private host — i.e. the PIN and DVR credentials
// would cross the public internet in the clear. LAN ws:// (private/loopback)
// is an intentional supported mode and is not flagged.
func isPlaintextPublicRelay(relayURL string) bool {
	u, err := url.Parse(relayURL)
	if err != nil || u.Scheme != "ws" {
		return false
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return false
		}
		return true
	}
	return host != "localhost" && host != ""
}

// Agent orchestrates capture → tile → compress → send pipeline.
type Agent struct {
	cfg         AgentConfig
	conn        *websocket.Conn
	connMu      sync.Mutex
	writeMu     sync.Mutex // serializes all WriteMessage calls (gorilla allows only one concurrent writer)
	seq         atomic.Uint32
	profile     atomic.Int32
	capturer    Capturer
	survMgr     *SurveillanceManager
	stopped     chan struct{}
	stopOnce    sync.Once
	snapshotSem chan struct{} // bounds concurrent snapshot handlers

	thumbMu     sync.Mutex       // guards lastThumbAt
	lastThumbAt map[string]int64 // chID -> last event-thumb send (unix ms); throttles bursts
}

func NewAgent(cfg AgentConfig) *Agent {
	a := &Agent{
		cfg:         cfg,
		stopped:     make(chan struct{}),
		snapshotSem: make(chan struct{}, 8),
	}
	a.profile.Store(int32(cfg.Profile))
	return a
}

func (a *Agent) Run() {
	a.superviseLoop("selfHealLoop", a.selfHealLoop)    // lifetime DVR self-recovery (independent of the connect loop)
	go a.runGuarded("eventManager", a.runEventManager) // ISAPI DVR event consumers (lifetime-scoped, independent of connect loop)
	go a.runGuarded("autoUpdate", func() { AutoUpdateLoop(a.stopped) })
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

		// 3) Capture loop (guarded: a capture/encode panic reconnects instead of crashing)
		a.runGuarded("captureLoop", a.captureLoop)

		// Cleanup
		a.capturer.Close()
		a.closeConn()
	}
}

func (a *Agent) Stop() {
	a.stopOnce.Do(func() {
		close(a.stopped)
		a.closeConn()
	})
}

// runGuarded runs fn, recovering and logging any panic — so one failing subsystem
// can't take down the whole agent (keep-moving-zombie: a dead head, not a dead body).
func (a *Agent) runGuarded(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[agent] recovered panic in %s: %v\n%s", name, r, debug.Stack())
		}
	}()
	fn()
}

// superviseLoop runs fn in a goroutine, restarting it (after a short backoff) if it
// returns or panics, until the agent stops — for lifetime subsystems that must stay
// alive (event consumers, DVR self-heal).
func (a *Agent) superviseLoop(name string, fn func()) {
	go func() {
		for {
			select {
			case <-a.stopped:
				return
			default:
			}
			a.runGuarded(name, fn)
			select {
			case <-a.stopped:
				return
			case <-time.After(3 * time.Second):
			}
		}
	}()
}

// wsWrite serializes all writes to the relay websocket. gorilla/websocket permits
// only one concurrent writer; the frame/heartbeat/snapshot/survConfig/survEvent
// goroutines all write, so without this lock concurrent writes panic the process
// ("concurrent write to websocket connection").
func (a *Agent) wsWrite(conn *websocket.Conn, msg []byte) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return conn.WriteMessage(websocket.BinaryMessage, msg)
}

func (a *Agent) connect() error {
	log.Printf("[agent] connecting to %s", a.cfg.RelayURL)
	if isPlaintextPublicRelay(a.cfg.RelayURL) {
		log.Printf("[agent] WARNING: %s is plaintext ws:// to a public host — the PIN and DVR credentials are sent unencrypted. Use wss:// (TLS) for internet relays.", a.cfg.RelayURL)
	}
	conn, _, err := websocket.DefaultDialer.Dial(a.cfg.RelayURL, nil)
	if err != nil {
		return err
	}

	// Send HELLO
	hello := proto.Hello{
		Role:          "publisher",
		AgentID:       a.cfg.AgentID,
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
	if err := a.wsWrite(conn, helloMsg); err != nil {
		conn.Close()
		return err
	}

	// Send AUTH: Token authenticates the publisher against the relay secret;
	// PIN is the separate viewer PIN the relay advertises to watchers.
	auth := proto.Auth{Token: a.cfg.PublisherToken, PIN: a.cfg.PIN}
	authPayload, _ := json.Marshal(auth)
	authMsg := proto.MarshalMessage(proto.MsgAuth, authPayload)
	if err := a.wsWrite(conn, authMsg); err != nil {
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
	go a.runGuarded("readPump", func() { a.readPump(conn) })

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
		} else if hdr.Type == proto.MsgSurvMeta {
			a.applySurvMeta(data[proto.HeaderSize:])
		} else if hdr.Type == proto.MsgAgentControl {
			a.handleAgentControl(data[proto.HeaderSize:])
		} else if hdr.Type == proto.MsgSurvSnapshot {
			// Bound concurrent snapshot handlers; drop when saturated so a flood
			// of requests cannot exhaust goroutines/connections.
			payload := append([]byte(nil), data[proto.HeaderSize:]...)
			select {
			case a.snapshotSem <- struct{}{}:
				go func() {
					defer func() { <-a.snapshotSem }()
					a.runGuarded("snapshot", func() { a.handleSnapshotRequest(payload) })
				}()
			default:
				log.Printf("[agent] snapshot request dropped (too many in flight)")
			}
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
		if err := a.wsWrite(conn, msg); err != nil {
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
			ExtAddr: d.ExtAddr, ExtPort: d.ExtPort,
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
				RtspURI: ch.RtspURI,
			})
		}
	}

	payload, _ := json.Marshal(cfg)
	msg := proto.MarshalMessage(proto.MsgSurvConfig, payload)

	a.connMu.Lock()
	conn := a.conn
	a.connMu.Unlock()
	if conn != nil {
		if err := a.wsWrite(conn, msg); err != nil {
			log.Printf("[agent] sendSurvConfig send error: %v", err)
		} else {
			log.Printf("[agent] sent surveillance config: %d DVRs, %d channels", len(cfg.DVRs), len(cfg.Channels))
		}
	}
}

// sendSurvEvent forwards one DVR event edge to the relay. AgentID is left empty;
// the relay stamps it (mirroring SurvConfig). No-op while disconnected.
func (a *Agent) sendSurvEvent(chID, kind string, active bool, tsMs int64) {
	payload, _ := json.Marshal(proto.SurvEvent{ChID: chID, Kind: kind, Active: active, TS: tsMs})
	msg := proto.MarshalMessage(proto.MsgSurvEvent, payload)
	a.connMu.Lock()
	conn := a.conn
	a.connMu.Unlock()
	if conn == nil {
		return
	}
	if err := a.wsWrite(conn, msg); err != nil {
		log.Printf("[agent] sendSurvEvent send error: %v", err)
	}
}

// eventThumbThrottle is the minimum gap between event-thumb snapshots for a single
// channel, so a burst of motion edges doesn't fetch dozens of snapshots.
const eventThumbThrottle = 3 * time.Second

// sendEventThumb fetches a live DVR snapshot for the channel and ships it to the
// relay as a MsgSurvEventThumb so the dashboard can show an instant event
// thumbnail without extracting from recordings. ChID/TS match the SurvEvent edge
// that opened the event. Best-effort: bails on fetch error and no-ops while
// disconnected; per-channel throttled. Run off the event loop (it does network I/O).
func (a *Agent) sendEventThumb(dvr DVRConfig, chNum int, tsMs int64) {
	if a.survMgr == nil {
		return
	}
	chID := fmt.Sprintf("dvr%d_ch%d", dvr.ID, chNum)

	// Throttle per channel.
	now := time.Now().UnixMilli()
	a.thumbMu.Lock()
	if a.lastThumbAt == nil {
		a.lastThumbAt = make(map[string]int64)
	}
	if last, ok := a.lastThumbAt[chID]; ok && now-last < eventThumbThrottle.Milliseconds() {
		a.thumbMu.Unlock()
		return
	}
	a.lastThumbAt[chID] = now
	a.thumbMu.Unlock()

	jpeg, err := a.survMgr.FetchSnapshot(dvr.ID, chNum)
	if err != nil || len(jpeg) == 0 {
		if err != nil {
			log.Printf("[agent] sendEventThumb %s: snapshot: %v", chID, err)
		}
		return
	}

	payload, _ := json.Marshal(proto.SurvEventThumb{ChID: chID, TS: tsMs, Jpeg: jpeg})
	msg := proto.MarshalMessage(proto.MsgSurvEventThumb, payload)
	a.connMu.Lock()
	conn := a.conn
	a.connMu.Unlock()
	if conn == nil {
		return
	}
	if err := a.wsWrite(conn, msg); err != nil {
		log.Printf("[agent] sendEventThumb send error: %v", err)
	}
}

// runEventManager starts one ISAPI alertStream consumer per ISAPI DVR for the
// agent's lifetime; each emits event edges to the relay via sendSurvEvent. The
// consumers talk to the DVRs (not the relay), so they persist across relay
// reconnects. Runs until a.stopped is closed.
func (a *Agent) runEventManager() {
	if a.survMgr == nil {
		return
	}
	dvrs := a.survMgr.ISAPIEventDVRs()
	for _, ed := range dvrs {
		ed := ed
		log.Printf("[isapi-events] starting consumer for DVR %d (%s), %d channels", ed.dvr.ID, ed.dvr.Name, len(ed.chNums))
		a.superviseLoop(fmt.Sprintf("isapi-events-dvr%d", ed.dvr.ID), func() {
			isapiAlertLoop(a.survMgr.client, ed.dvr, ed.chNums, a.stopped, func(e alertEvent) {
				chID := fmt.Sprintf("dvr%d_ch%d", ed.dvr.ID, e.chNum)
				a.sendSurvEvent(chID, e.kind, e.active, e.tsMs)
				if e.active {
					// Grab a live snapshot for this event's start and pre-store it on the
					// relay. Off the event loop (snapshot is network I/O) and throttled
					// per channel inside sendEventThumb.
					ed := ed
					e := e
					go a.runGuarded("eventThumb", func() { a.sendEventThumb(ed.dvr, e.chNum, e.tsMs) })
				}
			})
		})
	}
}

// handleAgentControl processes a relay-originated operator command (MsgAgentControl),
// relayed from the dashboard.
func (a *Agent) handleAgentControl(payload []byte) {
	var ctrl proto.AgentControl
	if json.Unmarshal(payload, &ctrl) != nil {
		return
	}
	switch ctrl.Action {
	case "reconnect", "rediscover":
		log.Printf("[agent] agent-control: %s — re-discovering all DVRs", ctrl.Action)
		go a.runGuarded("reconnectAllDVRs", a.reconnectAllDVRs)
	default:
		log.Printf("[agent] agent-control: unknown action %q", ctrl.Action)
	}
}

// reconnectAllDVRs forces a fresh channel discovery for every DVR and re-publishes
// the surveillance config, recovering streams the relay dropped (each
// DiscoverChannels fires onChange -> sendSurvConfig; the final send is a backstop).
func (a *Agent) reconnectAllDVRs() {
	if a.survMgr == nil {
		return
	}
	dvrs, err := a.survMgr.ListDVRs()
	if err != nil {
		log.Printf("[agent] reconnect: list DVRs: %v", err)
		return
	}
	for _, d := range dvrs {
		if _, err := a.survMgr.DiscoverChannels(d.ID); err != nil {
			log.Printf("[agent] reconnect: rediscover DVR %d (%s): %v", d.ID, d.Name, err)
		}
	}
	a.sendSurvConfig()
	log.Printf("[agent] reconnect: re-discovered %d DVRs and re-published config", len(dvrs))
}

// selfHealLoop runs for the agent's lifetime, periodically recovering DVRs that
// are reachable but have lost their channels (e.g. a DVR rebooted and the relay
// stopped its streams) — so dropped streams come back without a manual restart.
func (a *Agent) selfHealLoop() {
	ticker := time.NewTicker(90 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.stopped:
			return
		case <-ticker.C:
			a.healDVRs()
		}
	}
}

// healDVRs re-discovers any DVR that is reachable yet has zero channels in the DB.
func (a *Agent) healDVRs() {
	if a.survMgr == nil {
		return
	}
	dvrs, err := a.survMgr.ListDVRs()
	if err != nil {
		return
	}
	for _, d := range dvrs {
		chs, err := a.survMgr.ListChannels(d.ID)
		if err != nil || len(chs) > 0 {
			continue // healthy (has channels) or transient DB error
		}
		if !dvrReachable(d) {
			continue // DVR genuinely down — nothing to recover yet
		}
		log.Printf("[agent] self-heal: DVR %d (%s) reachable but 0 channels — re-discovering", d.ID, d.Name)
		if _, err := a.survMgr.DiscoverChannels(d.ID); err != nil {
			log.Printf("[agent] self-heal: rediscover DVR %d: %v", d.ID, err)
		}
	}
}

// dvrReachable reports whether the DVR's configured port accepts a TCP connection.
func dvrReachable(d DVRConfig) bool {
	if d.Addr == "" || d.Port <= 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", d.Addr, d.Port), 3*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// applySurvMeta applies a relay-originated channel metadata edit (reorder/rename)
// to the local DB. Each mutation fires onChange -> sendSurvConfig, re-broadcasting
// the updated config so all viewers + the dashboard reflect it.
func (a *Agent) applySurvMeta(payload []byte) {
	if a.survMgr == nil {
		return
	}
	var m proto.SurvMeta
	if err := json.Unmarshal(payload, &m); err != nil {
		log.Printf("[agent] surv meta parse: %v", err)
		return
	}
	for _, rn := range m.Renames {
		if err := a.survMgr.RenameChannel(m.DVRID, rn.ChNum, rn.Name); err != nil {
			log.Printf("[agent] surv meta rename ch %d: %v", rn.ChNum, err)
		}
	}
	if len(m.Order) > 0 {
		if err := a.survMgr.ReorderChannels(m.DVRID, m.Order); err != nil {
			log.Printf("[agent] surv meta reorder dvr %d: %v", m.DVRID, err)
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

			if err := a.wsWrite(conn, msg); err != nil {
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
	_ = a.wsWrite(conn, msg)
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
