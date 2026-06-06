package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/opsview/opsview/proto"
)

// allowedOrigins is the WebSocket Origin allowlist (RELAY_ALLOWED_ORIGINS).
// Empty = permissive (accept all), preserving prior behavior unless configured.
var allowedOrigins []string

var upgrader = websocket.Upgrader{
	ReadBufferSize:  64 * 1024,
	WriteBufferSize: 256 * 1024,
	CheckOrigin: func(r *http.Request) bool {
		return originAllowed(r.Header.Get("Origin"), r.Host, allowedOrigins)
	},
}

// maxWSMessageBytes caps a single inbound WebSocket message so a malicious peer
// cannot force an unbounded allocation. Far above any legitimate frame.
const maxWSMessageBytes = 32 << 20 // 32 MiB

// Hub manages all agent sessions (tenants). Each agentSession owns its own
// publisher, watchers, surveillance proxy/config, frame buffer, and metrics.
type Hub struct {
	cfg Config

	mu       sync.RWMutex
	sessions map[string]*agentSession // keyed by agentID ("default" included)

	startedAt    time.Time
	watcherIDSeq atomic.Uint32
	done         chan struct{}
	testPattern  *TestPattern
	pinLimiter   *pinLimiter

	dashTokMu sync.RWMutex
	dashTokDB string // dashboard password stored in the DB ("" = use env)

	ipLabelMu sync.RWMutex
	ipLabels  map[string]string // operator-assigned watcher IP -> display name

	alertMu  sync.RWMutex
	alertCfg alertConfig // fault-alert delivery settings (telegram/webhook)

	hiddenMu     sync.RWMutex
	hiddenAgents map[string]bool // operator-hidden agent ids (excluded from dashboard)

	rec *Recorder // NVR recorder (nil when recording is disabled)
}

// effectiveDashToken returns the active dashboard password: the DB-stored value
// if set, otherwise the env-configured RELAY_DASHBOARD_TOKEN.
func (h *Hub) effectiveDashToken() string {
	h.dashTokMu.RLock()
	db := h.dashTokDB
	h.dashTokMu.RUnlock()
	if db != "" {
		return db
	}
	return h.cfg.DashboardToken
}

// setDashToken persists a new dashboard password to the DB.
func (h *Hub) setDashToken(tok string) error {
	if h.cfg.Store == nil {
		return fmt.Errorf("dashboard password is read-only (no RELAY_DB configured)")
	}
	if err := h.cfg.Store.setSetting(settingDashboardToken, tok); err != nil {
		return err
	}
	h.dashTokMu.Lock()
	h.dashTokDB = tok
	h.dashTokMu.Unlock()
	return nil
}

// Watcher wraps a viewer WebSocket connection with a send queue.
type Watcher struct {
	id          uint32
	conn        *websocket.Conn
	send        chan []byte
	ip          string
	connectedAt time.Time
}

func NewHub(cfg Config) *Hub {
	h := &Hub{
		cfg:        cfg,
		sessions:   make(map[string]*agentSession),
		startedAt:  time.Now(),
		done:       make(chan struct{}),
		pinLimiter: newPinLimiter(),
	}
	// Pre-create the default session so the legacy flat path always resolves.
	h.sessions["default"] = newAgentSession("default", "default")
	// Load any DB-stored dashboard password (overrides the env one for login).
	if cfg.Store != nil {
		if v, err := cfg.Store.getSetting(settingDashboardToken); err == nil {
			h.dashTokDB = v
		}
		if m, err := cfg.Store.ipLabels(); err == nil {
			h.ipLabels = m
		}
	}
	if h.ipLabels == nil {
		h.ipLabels = make(map[string]string)
	}
	h.alertCfg = loadAlertConfig(cfg.Store)
	h.hiddenAgents = make(map[string]bool)
	if cfg.Store != nil {
		if ids, err := cfg.Store.hiddenAgents(); err == nil {
			for _, id := range ids {
				h.hiddenAgents[id] = true
			}
		}
	}
	h.testPattern = NewTestPattern(h)
	return h
}

// isAgentHidden reports whether an agent id is operator-hidden.
func (h *Hub) isAgentHidden(id string) bool {
	h.hiddenMu.RLock()
	defer h.hiddenMu.RUnlock()
	return h.hiddenAgents[id]
}

// setAgentHidden hides or unhides an agent and persists it. Requires a store.
func (h *Hub) setAgentHidden(id string, hidden bool) error {
	if h.cfg.Store == nil {
		return fmt.Errorf("hidden agents are read-only (no RELAY_DB configured)")
	}
	if err := h.cfg.Store.setAgentHidden(id, hidden); err != nil {
		return err
	}
	h.hiddenMu.Lock()
	if hidden {
		h.hiddenAgents[id] = true
	} else {
		delete(h.hiddenAgents, id)
	}
	h.hiddenMu.Unlock()
	return nil
}

// getAlertConfig returns the current fault-alert settings (thread-safe copy).
func (h *Hub) getAlertConfig() alertConfig {
	h.alertMu.RLock()
	defer h.alertMu.RUnlock()
	return h.alertCfg
}

// setAlertConfig persists and applies new fault-alert settings. Requires a store.
func (h *Hub) setAlertConfig(c alertConfig) error {
	if h.cfg.Store == nil {
		return fmt.Errorf("alerts are read-only (no RELAY_DB configured)")
	}
	en := "0"
	if c.Enabled {
		en = "1"
	}
	vals := map[string]string{
		settingAlertEnabled: en,
		settingAlertTGToken: c.TelegramToken,
		settingAlertTGChat:  c.TelegramChat,
		settingAlertWebhook: c.WebhookURL,
	}
	for k, v := range vals {
		if err := h.cfg.Store.setSetting(k, v); err != nil {
			return err
		}
	}
	h.alertMu.Lock()
	h.alertCfg = c
	h.alertMu.Unlock()
	return nil
}

// getIPLabel returns the operator-assigned name for a watcher IP ("" if none).
func (h *Hub) getIPLabel(ip string) string {
	h.ipLabelMu.RLock()
	defer h.ipLabelMu.RUnlock()
	return h.ipLabels[ip]
}

// setIPLabel assigns (or clears, when label=="") a name for a watcher IP and
// persists it. Requires a configured store (RELAY_DB).
func (h *Hub) setIPLabel(ip, label string) error {
	if h.cfg.Store == nil {
		return fmt.Errorf("ip labels are read-only (no RELAY_DB configured)")
	}
	if err := h.cfg.Store.setIPLabel(ip, label); err != nil {
		return err
	}
	h.ipLabelMu.Lock()
	if label == "" {
		delete(h.ipLabels, ip)
	} else {
		h.ipLabels[ip] = label
	}
	h.ipLabelMu.Unlock()
	return nil
}

// getOrCreateSession returns the session for agentID, creating + starting it if absent.
func (h *Hub) getOrCreateSession(agentID, name string) *agentSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.sessions[agentID]; ok {
		return s
	}
	s := newAgentSession(agentID, name)
	go s.run()
	h.sessions[agentID] = s
	return s
}

// sessionByPIN finds the online session advertising the given watcher PIN.
func (h *Hub) sessionByPIN(pin string) *agentSession {
	if pin == "" {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.sessions {
		s.mu.RLock()
		match := s.publisher != nil && subtle.ConstantTimeCompare([]byte(s.pin), []byte(pin)) == 1
		s.mu.RUnlock()
		if match {
			return s
		}
	}
	return nil
}

// sessionByID returns the session for an agentID or nil.
func (h *Hub) sessionByID(agentID string) *agentSession {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessions[agentID]
}

// defaultSession returns the always-present default (legacy) session.
func (h *Hub) defaultSession() *agentSession { return h.sessionByID("default") }

// allSessions snapshots the session pointers (for dashboard/state aggregation).
func (h *Hub) allSessions() []*agentSession {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*agentSession, 0, len(h.sessions))
	for _, s := range h.sessions {
		out = append(out, s)
	}
	return out
}

// Run starts the default session loop and the test pattern, then blocks until Stop.
func (h *Hub) Run() {
	go h.defaultSession().run()
	h.testPattern.Start()
	<-h.done
}

// Stop signals the hub to shut down.
func (h *Hub) Stop() {
	h.testPattern.Stop()
	for _, s := range h.allSessions() {
		s.survProxy.StopAll()
	}
	select {
	case <-h.done:
	default:
		close(h.done)
	}
}

// HandlePublish handles the /publish WebSocket endpoint (an agent's publisher).
func (h *Hub) HandlePublish(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[relay] publish upgrade error: %v", err)
		return
	}
	conn.SetReadLimit(maxWSMessageBytes)

	hello, auth, ok := h.authenticatePublisher(conn)
	if !ok {
		conn.Close()
		return
	}
	entry, _ := h.cfg.Agents.lookup(hello.AgentID)
	sess := h.getOrCreateSession(entry.ID, entry.Name)

	// Enforce a single publisher per session + globally-unique PIN among online agents.
	if other := h.sessionByPIN(auth.PIN); other != nil && other != sess {
		sendError(conn, 409, "viewer PIN already in use by another agent")
		conn.Close()
		return
	}
	sess.mu.Lock()
	if sess.publisher != nil {
		sess.mu.Unlock()
		sendError(conn, 409, "publisher already connected")
		conn.Close()
		log.Printf("[relay:%s] rejected duplicate publisher", entry.ID)
		return
	}
	sess.publisher = conn
	sess.pin = auth.PIN
	sess.mu.Unlock()
	sess.connectedAt.Store(time.Now().UnixMilli())

	if entry.ID == "default" {
		h.testPattern.Stop()
	}
	conn.WriteMessage(websocket.BinaryMessage, proto.MarshalMessage(proto.MsgReady, nil))
	log.Printf("[relay:%s] publisher authenticated from %s", entry.ID, r.RemoteAddr)

	defer func() {
		sess.mu.Lock()
		if sess.publisher == conn {
			sess.publisher = nil
			sess.pin = ""
		}
		sess.mu.Unlock()
		conn.Close()
		if entry.ID == "default" {
			h.testPattern.Start()
		}
		log.Printf("[relay:%s] publisher disconnected", entry.ID)
	}()

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[relay:%s] publisher read error: %v", entry.ID, err)
			return
		}
		if msgType != websocket.BinaryMessage || len(data) < proto.HeaderSize {
			continue
		}
		hdr, hdrErr := proto.DecodeHeader(data)
		if hdrErr != nil {
			continue
		}

		sess.publishCount.Add(1)
		sess.lastPublishAt.Store(time.Now().UnixMilli())
		sess.bytesIn.Add(int64(len(data)))

		switch hdr.Type {
		case proto.MsgSurvConfig:
			// Stamp the session (tenant) id into the config so watchers learn the
			// /surv path scope to use (named agents are namespaced as
			// /surv/<id>/dvrN_chM; the publisher itself doesn't know its tenant id).
			// Always returns a fresh buffer, safe to hand the proxy goroutine + broadcast.
			cfgCopy := stampSurvConfigAgentID(data, sess.id)
			sess.survConfigMu.Lock()
			sess.survConfig = cfgCopy
			sess.survConfigMu.Unlock()
			sess.broadcast <- cfgCopy
			// Start RTSP→HLS proxy streams off the read loop (blocking DVR connects
			// must not stall publisher frame ingestion). cfgCopy is a private copy.
			go sess.survProxy.HandleSurvConfig(cfgCopy[proto.HeaderSize:])

		case proto.MsgSurvSnapshot:
			if len(data) > proto.HeaderSize {
				var resp proto.SnapshotResponse
				if json.Unmarshal(data[proto.HeaderSize:], &resp) == nil {
					h.routeSnapshotResponse(sess, resp.ReqID, data)
				}
			}

		case proto.MsgFrameDelta:
			if fd, err := proto.DecodeFrameDelta(data[proto.HeaderSize:]); err == nil {
				sess.frameBuf.Update(fd)
			}
			sess.broadcast <- data

		default:
			sess.broadcast <- data
		}
	}
}

// HandleWatch handles the /watch WebSocket endpoint (viewer clients). The viewer
// PIN selects + authenticates the agent session (tenant) it watches.
func (h *Hub) HandleWatch(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[relay] watch upgrade error: %v", err)
		return
	}
	conn.SetReadLimit(maxWSMessageBytes)

	// rateIP is the socket peer (unspoofable) used for rate limiting; the
	// display IP below honors proxy/tunnel headers for the real client address.
	rateIP := clientIP(r.RemoteAddr)
	if !h.pinLimiter.allowed(rateIP) {
		sendError(conn, 429, "too many attempts; try again later")
		conn.Close()
		return
	}

	_, hello, auth, err := h.readHelloAuth(conn)
	if err != nil {
		sendError(conn, 400, err.Error())
		conn.Close()
		return
	}
	if hello.Role != "watcher" {
		sendError(conn, 403, "expected watcher role")
		conn.Close()
		return
	}

	sess := h.sessionByPIN(auth.Token)
	if sess == nil {
		h.pinLimiter.recordFailure(rateIP)
		sendError(conn, 401, "invalid PIN")
		log.Printf("[relay] watcher PIN mismatch from %s", conn.RemoteAddr())
		conn.Close()
		return
	}
	h.pinLimiter.recordSuccess(rateIP)

	watcher := &Watcher{
		id:          h.watcherIDSeq.Add(1),
		conn:        conn,
		send:        make(chan []byte, h.cfg.MaxWatcherQueue),
		ip:          displayClientIP(r),
		connectedAt: time.Now(),
	}
	sess.mu.Lock()
	sess.watchers[watcher] = struct{}{}
	sess.watcherCount.Add(1)
	sess.mu.Unlock()

	conn.WriteMessage(websocket.BinaryMessage, proto.MarshalMessage(proto.MsgReady, nil))

	// Send cached surveillance config + the cached full Ops frame so the watcher
	// sees the whole screen immediately.
	sess.survConfigMu.RLock()
	cachedConfig := sess.survConfig
	sess.survConfigMu.RUnlock()
	if len(cachedConfig) > 0 {
		conn.WriteMessage(websocket.BinaryMessage, cachedConfig)
	}
	if frameMsg, ok := sess.frameBuf.FullFrameMessage(); ok {
		conn.WriteMessage(websocket.BinaryMessage, frameMsg)
	}

	log.Printf("[relay:%s] watcher authenticated from %s (id=%d)", sess.id, watcher.ip, watcher.id)

	defer func() {
		sess.removeWatcher(watcher)
		log.Printf("[relay:%s] watcher disconnected: %s", sess.id, watcher.ip)
	}()

	go h.watcherWritePump(sess, watcher)

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if msgType == websocket.BinaryMessage && len(data) >= proto.HeaderSize {
			hdr, hdrErr := proto.DecodeHeader(data)
			if hdrErr != nil {
				continue
			}
			switch hdr.Type {
			case proto.MsgControl:
				sess.sendControlToPublisher(data)
			case proto.MsgSurvSnapshot:
				if len(data) > proto.HeaderSize {
					var req proto.SnapshotRequest
					if json.Unmarshal(data[proto.HeaderSize:], &req) == nil {
						req.ReqID = fmt.Sprintf("%d:%s", watcher.id, req.ReqID)
						payload, _ := json.Marshal(req)
						sess.sendToPublisher(proto.MarshalMessage(proto.MsgSurvSnapshot, payload))
					}
				}
			}
		}
	}
}

// routeSnapshotResponse routes a snapshot response to the watcher (within the
// session) whose ID is prefixed in reqID.
func (h *Hub) routeSnapshotResponse(s *agentSession, reqID string, rawMsg []byte) {
	// reqID format: "{watcherID}:{originalReqID}"
	parts := strings.SplitN(reqID, ":", 2)
	if len(parts) != 2 {
		return
	}
	watcherID, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return
	}
	// Rebuild message with original reqID (strip watcher prefix).
	var resp proto.SnapshotResponse
	if json.Unmarshal(rawMsg[proto.HeaderSize:], &resp) != nil {
		return
	}
	resp.ReqID = parts[1]
	payload, _ := json.Marshal(resp)
	msg := proto.MarshalMessage(proto.MsgSurvSnapshot, payload)

	s.mu.RLock()
	defer s.mu.RUnlock()
	for w := range s.watchers {
		if w.id == uint32(watcherID) {
			select {
			case w.send <- msg:
			default:
				log.Printf("[relay:%s] snapshot response dropped for slow watcher %s", s.id, w.ip)
			}
			return
		}
	}
}

// stampSurvConfigAgentID returns a fresh copy of a MsgSurvConfig message with its
// AgentID set to the session (tenant) id, so watchers know the /surv path scope.
// The default/legacy session keeps the flat path (no stamp). Always returns a
// private buffer; on any parse/marshal error it falls back to a plain copy.
func stampSurvConfigAgentID(msg []byte, agentID string) []byte {
	cp := make([]byte, len(msg))
	copy(cp, msg)
	if agentID == "" || agentID == "default" || len(msg) <= proto.HeaderSize {
		return cp
	}
	var cfg proto.SurvConfig
	if err := json.Unmarshal(msg[proto.HeaderSize:], &cfg); err != nil {
		return cp
	}
	cfg.AgentID = agentID
	payload, err := json.Marshal(cfg)
	if err != nil {
		return cp
	}
	return proto.MarshalMessage(proto.MsgSurvConfig, payload)
}

// redactSurvConfigPayload parses a SurvConfig JSON payload and returns a copy
// with DVR credentials (username/password) removed.
func redactSurvConfigPayload(payload []byte) ([]byte, error) {
	var cfg proto.SurvConfig
	if err := json.Unmarshal(payload, &cfg); err != nil {
		return nil, err
	}
	for i := range cfg.DVRs {
		cfg.DVRs[i].Username = ""
		cfg.DVRs[i].Password = ""
	}
	return json.Marshal(cfg)
}

// pinFromRequest extracts the viewer PIN from a REST request (query param or header).
func pinFromRequest(r *http.Request) string {
	if p := r.URL.Query().Get("pin"); p != "" {
		return p
	}
	return r.Header.Get("X-OpsView-PIN")
}

// survSessionFromRequest resolves the target session from a ?agent= query
// param, defaulting to the legacy default session.
func (h *Hub) survSessionFromRequest(r *http.Request) *agentSession {
	if s := h.sessionByID(r.URL.Query().Get("agent")); s != nil {
		return s
	}
	return h.defaultSession()
}

// HandleSurvConfig returns the cached surveillance config via REST (PIN-gated,
// credentials stripped), scoped to the requested agent session.
func (h *Hub) HandleSurvConfig(w http.ResponseWriter, r *http.Request) {
	s := h.survSessionFromRequest(r)
	s.mu.RLock()
	pin := s.pin
	hasPub := s.publisher != nil
	s.mu.RUnlock()

	if !hasPub || pin == "" || subtle.ConstantTimeCompare([]byte(pinFromRequest(r)), []byte(pin)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	s.survConfigMu.RLock()
	data := s.survConfig
	s.survConfigMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	if len(data) <= proto.HeaderSize {
		w.Write([]byte(`{"dvrs":[],"channels":[]}`))
		return
	}
	redacted, err := redactSurvConfigPayload(data[proto.HeaderSize:])
	if err != nil {
		w.Write([]byte(`{"dvrs":[],"channels":[]}`))
		return
	}
	w.Write(redacted)
}

// HandleSurvStreams returns the active HLS streams for the requested agent session.
func (h *Hub) HandleSurvStreams(w http.ResponseWriter, r *http.Request) {
	s := h.survSessionFromRequest(r)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(s.survProxy.ListStreams())
}

// HandleSnapshot returns the current accumulated Ops frame (per agent) as PNG.
func (h *Hub) HandleSnapshot(w http.ResponseWriter, r *http.Request) {
	s := h.survSessionFromRequest(r)
	data, err := s.frameBuf.SnapshotPNG()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

// HandleHealth returns basic health status aggregated across agent sessions.
func (h *Hub) HandleHealth(w http.ResponseWriter, r *http.Request) {
	online := 0
	var watchers int32
	for _, s := range h.allSessions() {
		if s.online() {
			online++
		}
		watchers += s.watcherCount.Load()
	}
	status := "no_publisher"
	if online > 0 {
		status = "ok"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":        status,
		"agents_online": online,
		"watchers":      watchers,
	})
}

// HandleMetrics returns operational metrics aggregated across agent sessions.
func (h *Hub) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	var bin, bout, pubCount int64
	var watchers int32
	var lastPub int64
	for _, s := range h.allSessions() {
		bin += s.bytesIn.Load()
		bout += s.bytesOut.Load()
		pubCount += s.publishCount.Load()
		watchers += s.watcherCount.Load()
		if lp := s.lastPublishAt.Load(); lp > lastPub {
			lastPub = lp
		}
	}
	var lastPubStr string
	if lastPub > 0 {
		lastPubStr = time.UnixMilli(lastPub).Format(time.RFC3339)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"publish_count":   pubCount,
		"last_publish_at": lastPubStr,
		"bytes_in":        bin,
		"bytes_out":       bout,
		"watcher_count":   watchers,
	})
}

func (h *Hub) watcherWritePump(s *agentSession, w *Watcher) {
	defer w.conn.Close()
	for msg := range w.send {
		if err := w.conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
			return
		}
		s.bytesOut.Add(int64(len(msg)))
	}
}

// validPublisherToken reports whether the provided publisher token matches the
// configured secret. Fails closed; constant-time.
func validPublisherToken(provided, configured string) bool {
	if configured == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(configured)) == 1
}

// authenticatePublisher validates the HELLO/AUTH handshake against the agent
// registry. The advertised viewer PIN (auth.PIN) is not an authenticator.
func (h *Hub) authenticatePublisher(conn *websocket.Conn) (proto.Hello, proto.Auth, bool) {
	_, hello, auth, err := h.readHelloAuth(conn)
	if err != nil {
		sendError(conn, 400, err.Error())
		return hello, auth, false
	}
	if hello.Role != "publisher" {
		sendError(conn, 403, "expected publisher role")
		return hello, auth, false
	}
	entry, ok := h.cfg.Agents.lookup(hello.AgentID)
	if !ok {
		sendError(conn, 403, "unknown agent")
		log.Printf("[relay] publisher with unknown agent_id %q from %s", hello.AgentID, conn.RemoteAddr())
		return hello, auth, false
	}
	if !validPublisherToken(auth.Token, entry.Token) {
		sendError(conn, 401, "invalid publisher token")
		log.Printf("[relay:%s] publisher auth failed from %s", entry.ID, conn.RemoteAddr())
		return hello, auth, false
	}
	if auth.PIN == "" {
		sendError(conn, 400, "missing viewer PIN")
		return hello, auth, false
	}
	return hello, auth, true
}

// clientIP strips the port from a RemoteAddr ("ip:port" -> "ip").
func clientIP(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// displayClientIP returns the best-effort real client IP for display, honoring
// the Cloudflare Tunnel / reverse-proxy forwarding headers — behind a tunnel the
// socket peer is cloudflared, not the viewer. Falls back to the socket address.
// For display only: security decisions (rate limiting) must keep using the
// socket peer, which the client cannot spoof.
func displayClientIP(r *http.Request) string {
	if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cf != "" {
		return cf
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i]) // first hop = original client
		}
		return strings.TrimSpace(xff)
	}
	return clientIP(r.RemoteAddr)
}

func (h *Hub) readHelloAuth(conn *websocket.Conn) (proto.Header, proto.Hello, proto.Auth, error) {
	var hello proto.Hello
	var auth proto.Auth

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	_, data, err := conn.ReadMessage()
	if err != nil {
		return proto.Header{}, hello, auth, fmt.Errorf("read HELLO: %w", err)
	}
	if len(data) < proto.HeaderSize {
		return proto.Header{}, hello, auth, fmt.Errorf("HELLO message too short")
	}
	hdr, err := proto.DecodeHeader(data)
	if err != nil {
		return hdr, hello, auth, err
	}
	if hdr.Type != proto.MsgHello {
		return hdr, hello, auth, fmt.Errorf("expected HELLO, got %s", hdr.Type)
	}
	if err := json.Unmarshal(data[proto.HeaderSize:], &hello); err != nil {
		return hdr, hello, auth, fmt.Errorf("parse HELLO: %w", err)
	}

	_, data, err = conn.ReadMessage()
	if err != nil {
		return hdr, hello, auth, fmt.Errorf("read AUTH: %w", err)
	}
	if len(data) < proto.HeaderSize {
		return hdr, hello, auth, fmt.Errorf("AUTH message too short")
	}
	hdr2, err := proto.DecodeHeader(data)
	if err != nil {
		return hdr2, hello, auth, err
	}
	if hdr2.Type != proto.MsgAuth {
		return hdr2, hello, auth, fmt.Errorf("expected AUTH, got %s", hdr2.Type)
	}
	if err := json.Unmarshal(data[proto.HeaderSize:], &auth); err != nil {
		return hdr2, hello, auth, fmt.Errorf("parse AUTH: %w", err)
	}

	return hdr, hello, auth, nil
}

func sendError(conn *websocket.Conn, code int, message string) {
	errMsg := proto.ErrorMsg{Code: code, Message: message}
	payload, _ := json.Marshal(errMsg)
	msg := proto.MarshalMessage(proto.MsgError, payload)
	_ = conn.WriteMessage(websocket.BinaryMessage, msg)
}
