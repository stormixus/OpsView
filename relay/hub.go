package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/opsview/opsview/proto"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  64 * 1024,
	WriteBufferSize: 256 * 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// Hub manages the publisher and all watchers.
type Hub struct {
	cfg Config

	mu           sync.RWMutex
	publisher    *websocket.Conn
	pubWriteMu   sync.Mutex
	watchers     map[*Watcher]struct{}
	publisherPIN string // PIN set by the publisher; watchers must match

	// Metrics
	publishCount  atomic.Int64
	lastPublishAt atomic.Int64 // unix ms
	bytesIn       atomic.Int64
	bytesOut      atomic.Int64
	watcherCount  atomic.Int32

	broadcast   chan []byte
	done        chan struct{}
	testPattern *TestPattern

	// Surveillance config cache (last MsgSurvConfig from publisher)
	survConfig   []byte
	survConfigMu sync.RWMutex

	// Watcher ID counter for snapshot routing
	watcherIDSeq atomic.Uint32

	// Surveillance RTSP→HLS proxy
	survProxy *SurvProxy

	// Full-frame accumulator for REST snapshot
	frameBuf *FrameBuffer
}

// Watcher wraps a viewer WebSocket connection with a send queue.
type Watcher struct {
	id   uint32
	conn *websocket.Conn
	send chan []byte
	ip   string
}

func NewHub(cfg Config) *Hub {
	h := &Hub{
		cfg:       cfg,
		watchers:  make(map[*Watcher]struct{}),
		broadcast: make(chan []byte, 64),
		done:      make(chan struct{}),
		survProxy: NewSurvProxy(),
		frameBuf:  NewFrameBuffer(),
	}
	h.testPattern = NewTestPattern(h)
	return h
}

// Run is the main hub loop that fans out messages to watchers.
func (h *Hub) Run() {
	// Start test pattern immediately (no publisher yet)
	h.testPattern.Start()

	for {
		select {
		case msg := <-h.broadcast:
			h.mu.RLock()
			for w := range h.watchers {
				select {
				case w.send <- msg:
					// queued
				default:
					// backpressure: queue full, drop oldest
					select {
					case <-w.send:
						// dropped oldest
					default:
					}
					select {
					case w.send <- msg:
					default:
						// still full — disconnect slow watcher
						log.Printf("[relay] disconnecting slow watcher %s", w.ip)
						go h.removeWatcher(w)
					}
				}
			}
			h.mu.RUnlock()
		case <-h.done:
			return
		}
	}
}

// HandlePublish handles the /publish WebSocket endpoint (publisher only).
func (h *Hub) HandlePublish(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[relay] publish upgrade error: %v", err)
		return
	}

	ip := r.RemoteAddr
	log.Printf("[relay] publisher connecting from %s", ip)

	// Read HELLO + AUTH
	auth, ok := h.authenticatePublisher(conn)
	if !ok {
		conn.Close()
		return
	}

	// Enforce single publisher
	h.mu.Lock()
	if h.publisher != nil {
		h.mu.Unlock()
		sendError(conn, 409, "publisher already connected")
		conn.Close()
		log.Printf("[relay] rejected duplicate publisher from %s", ip)
		return
	}
	h.publisher = conn
	h.publisherPIN = auth.PIN
	h.mu.Unlock()

	// Stop test pattern now that a real publisher is connected
	h.testPattern.Stop()

	// Notify agent that authentication succeeded
	readyMsg := proto.MarshalMessage(proto.MsgReady, nil)
	conn.WriteMessage(websocket.BinaryMessage, readyMsg)

	log.Printf("[relay] publisher authenticated from %s (PIN stored)", ip)

	defer func() {
		h.mu.Lock()
		if h.publisher == conn {
			h.publisher = nil
			h.publisherPIN = ""
		}
		h.mu.Unlock()
		conn.Close()
		log.Printf("[relay] publisher disconnected: %s", ip)
		// Resume test pattern since publisher is gone
		h.testPattern.Start()
	}()

	// Read loop: receive frames and broadcast
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[relay] publisher read error: %v", err)
			return
		}
		if msgType != websocket.BinaryMessage {
			continue
		}
		if len(data) < proto.HeaderSize {
			continue
		}

		hdr, hdrErr := proto.DecodeHeader(data)
		if hdrErr != nil {
			continue
		}

		h.publishCount.Add(1)
		h.lastPublishAt.Store(time.Now().UnixMilli())
		h.bytesIn.Add(int64(len(data)))

		switch hdr.Type {
		case proto.MsgSurvConfig:
			// Cache and broadcast surveillance config
			configData := make([]byte, len(data))
			copy(configData, data)
			h.survConfigMu.Lock()
			h.survConfig = configData
			h.survConfigMu.Unlock()
			log.Printf("[relay] cached surveillance config (%d bytes)", len(data))
			h.broadcast <- data

			// Start RTSP→HLS proxy streams
			h.survProxy.HandleSurvConfig(data[proto.HeaderSize:])

		case proto.MsgSurvSnapshot:
			// Snapshot response from publisher — route to specific watcher
			if len(data) > proto.HeaderSize {
				var resp proto.SnapshotResponse
				if json.Unmarshal(data[proto.HeaderSize:], &resp) == nil {
					h.routeSnapshotResponse(resp.ReqID, data)
				}
			}

		case proto.MsgFrameDelta:
			fd, err := proto.DecodeFrameDelta(data[proto.HeaderSize:])
			if err == nil {
				h.frameBuf.Update(fd)
			}
			h.broadcast <- data

		default:
			h.broadcast <- data
		}
	}
}

// HandleWatch handles the /watch WebSocket endpoint (viewer clients).
func (h *Hub) HandleWatch(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[relay] watch upgrade error: %v", err)
		return
	}

	ip := r.RemoteAddr
	log.Printf("[relay] watcher connecting from %s", ip)

	if !h.authenticateWatcher(conn) {
		conn.Close()
		return
	}

	watcher := &Watcher{
		id:   h.watcherIDSeq.Add(1),
		conn: conn,
		send: make(chan []byte, h.cfg.MaxWatcherQueue),
		ip:   ip,
	}

	h.mu.Lock()
	h.watchers[watcher] = struct{}{}
	h.watcherCount.Add(1)
	h.mu.Unlock()

	// Notify watcher that authentication succeeded
	readyMsg := proto.MarshalMessage(proto.MsgReady, nil)
	conn.WriteMessage(websocket.BinaryMessage, readyMsg)

	// Send cached surveillance config if available
	h.survConfigMu.RLock()
	cachedConfig := h.survConfig
	h.survConfigMu.RUnlock()
	if len(cachedConfig) > 0 {
		conn.WriteMessage(websocket.BinaryMessage, cachedConfig)
		log.Printf("[relay] sent cached surveillance config to watcher %s", ip)
	}

	log.Printf("[relay] watcher authenticated from %s (id=%d, total: %d)", ip, watcher.id, h.watcherCount.Load())

	defer func() {
		h.removeWatcher(watcher)
		log.Printf("[relay] watcher disconnected: %s", ip)
	}()

	// Write pump
	go h.watcherWritePump(watcher)

	// Read pump: just keep reading to detect close / handle CONTROL and SNAPSHOT
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
				h.sendControlToPublisher(data)
			case proto.MsgSurvSnapshot:
				// Snapshot request from watcher — prefix req_id with watcher ID and forward to publisher
				if len(data) > proto.HeaderSize {
					var req proto.SnapshotRequest
					if json.Unmarshal(data[proto.HeaderSize:], &req) == nil {
						req.ReqID = fmt.Sprintf("%d:%s", watcher.id, req.ReqID)
						payload, _ := json.Marshal(req)
						msg := proto.MarshalMessage(proto.MsgSurvSnapshot, payload)
						h.sendToPublisher(msg)
					}
				}
			}
		}
	}
}

func (h *Hub) sendControlToPublisher(data []byte) {
	h.sendToPublisher(data)
}

func (h *Hub) sendToPublisher(data []byte) {
	h.mu.RLock()
	pub := h.publisher
	h.mu.RUnlock()
	if pub == nil {
		return
	}

	h.pubWriteMu.Lock()
	defer h.pubWriteMu.Unlock()
	_ = pub.WriteMessage(websocket.BinaryMessage, data)
}

// routeSnapshotResponse routes a snapshot response to the watcher whose ID is prefixed in reqID.
func (h *Hub) routeSnapshotResponse(reqID string, rawMsg []byte) {
	// reqID format: "{watcherID}:{originalReqID}"
	parts := strings.SplitN(reqID, ":", 2)
	if len(parts) != 2 {
		return
	}
	watcherID, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return
	}

	// Rebuild message with original reqID (strip watcher prefix)
	var resp proto.SnapshotResponse
	if json.Unmarshal(rawMsg[proto.HeaderSize:], &resp) != nil {
		return
	}
	resp.ReqID = parts[1]
	payload, _ := json.Marshal(resp)
	msg := proto.MarshalMessage(proto.MsgSurvSnapshot, payload)

	h.mu.RLock()
	defer h.mu.RUnlock()
	for w := range h.watchers {
		if w.id == uint32(watcherID) {
			select {
			case w.send <- msg:
			default:
				log.Printf("[relay] snapshot response dropped for slow watcher %s", w.ip)
			}
			return
		}
	}
}

// redactSurvConfigPayload parses a SurvConfig JSON payload and returns a copy
// with DVR credentials (username/password) removed. Used for any client-facing
// surface so DVR admin logins never leave the relay.
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

// pinFromRequest extracts the viewer PIN from a REST request (query param or
// header), so /api/surv can be gated like the /watch WebSocket.
func pinFromRequest(r *http.Request) string {
	if p := r.URL.Query().Get("pin"); p != "" {
		return p
	}
	return r.Header.Get("X-OpsView-PIN")
}

// HandleSurvConfig returns the cached surveillance config via REST. It requires
// the viewer PIN and always strips DVR credentials from the response.
func (h *Hub) HandleSurvConfig(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	pin := h.publisherPIN
	hasPub := h.publisher != nil
	h.mu.RUnlock()

	if !hasPub || pin == "" || subtle.ConstantTimeCompare([]byte(pinFromRequest(r)), []byte(pin)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	h.survConfigMu.RLock()
	data := h.survConfig
	h.survConfigMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	if len(data) <= proto.HeaderSize {
		w.Write([]byte(`{"dvrs":[],"channels":[]}`))
		return
	}
	// Strip OVP header, then redact credentials before returning the JSON.
	redacted, err := redactSurvConfigPayload(data[proto.HeaderSize:])
	if err != nil {
		// Never fall back to the raw (credential-bearing) payload on error.
		w.Write([]byte(`{"dvrs":[],"channels":[]}`))
		return
	}
	w.Write(redacted)
}

func (h *Hub) watcherWritePump(w *Watcher) {
	defer w.conn.Close()
	for msg := range w.send {
		if err := w.conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
			return
		}
		h.bytesOut.Add(int64(len(msg)))
	}
}

func (h *Hub) removeWatcher(w *Watcher) {
	h.mu.Lock()
	if _, ok := h.watchers[w]; ok {
		delete(h.watchers, w)
		h.watcherCount.Add(-1)
		close(w.send)
	}
	h.mu.Unlock()
}

// validPublisherToken reports whether the provided publisher token matches the
// configured secret. It fails closed: an empty configured secret (or empty
// provided token) is never valid. The comparison is constant-time.
func validPublisherToken(provided, configured string) bool {
	if configured == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(configured)) == 1
}

func (h *Hub) authenticatePublisher(conn *websocket.Conn) (proto.Auth, bool) {
	// Expect HELLO then AUTH
	_, hello, auth, err := h.readHelloAuth(conn)
	if err != nil {
		sendError(conn, 400, err.Error())
		return auth, false
	}
	if hello.Role != "publisher" {
		sendError(conn, 403, "expected publisher role")
		return auth, false
	}
	// The publisher must prove it knows the shared relay secret. The viewer PIN
	// it advertises (auth.PIN) is a separate value and is NOT an authenticator.
	if !validPublisherToken(auth.Token, h.cfg.PublisherToken) {
		sendError(conn, 401, "invalid publisher token")
		log.Printf("[relay] publisher auth failed from %s", conn.RemoteAddr())
		return auth, false
	}
	if auth.PIN == "" {
		sendError(conn, 400, "missing viewer PIN")
		return auth, false
	}
	return auth, true
}

func (h *Hub) authenticateWatcher(conn *websocket.Conn) bool {
	_, hello, auth, err := h.readHelloAuth(conn)
	if err != nil {
		sendError(conn, 400, err.Error())
		return false
	}
	if hello.Role != "watcher" {
		sendError(conn, 403, "expected watcher role")
		return false
	}

	h.mu.RLock()
	pin := h.publisherPIN
	hasPub := h.publisher != nil
	h.mu.RUnlock()

	if !hasPub {
		sendError(conn, 503, "no publisher connected")
		return false
	}
	if auth.Token != pin {
		sendError(conn, 401, "invalid PIN")
		log.Printf("[relay] watcher PIN mismatch from %s", conn.RemoteAddr())
		return false
	}
	return true
}

func (h *Hub) readHelloAuth(conn *websocket.Conn) (proto.Header, proto.Hello, proto.Auth, error) {
	var hello proto.Hello
	var auth proto.Auth

	// Set read deadline for handshake
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	// Read HELLO
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

	// Read AUTH
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

// Stop signals the hub to shut down.
func (h *Hub) Stop() {
	h.testPattern.Stop()
	h.survProxy.StopAll()
	select {
	case <-h.done:
		// already closed
	default:
		close(h.done)
	}
}

// HandleSurvStreams returns the list of active HLS streams.
func (h *Hub) HandleSurvStreams(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(h.survProxy.ListStreams())
}

// HandleSnapshot returns the current accumulated frame as PNG.
func (h *Hub) HandleSnapshot(w http.ResponseWriter, r *http.Request) {
	data, err := h.frameBuf.SnapshotPNG()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

// HandleHealth returns basic health status.
func (h *Hub) HandleHealth(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	hasPub := h.publisher != nil
	h.mu.RUnlock()

	status := "ok"
	if !hasPub {
		status = "no_publisher"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    status,
		"publisher": hasPub,
		"watchers":  h.watcherCount.Load(),
	})
}

// HandleMetrics returns operational metrics.
func (h *Hub) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	lastPub := h.lastPublishAt.Load()
	var lastPubStr string
	if lastPub > 0 {
		lastPubStr = time.UnixMilli(lastPub).Format(time.RFC3339)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"publish_count":   h.publishCount.Load(),
		"last_publish_at": lastPubStr,
		"bytes_in":        h.bytesIn.Load(),
		"bytes_out":       h.bytesOut.Load(),
		"watcher_count":   h.watcherCount.Load(),
	})
}
