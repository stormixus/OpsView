package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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

	mu        sync.RWMutex
	publisher *websocket.Conn
	watchers  map[*Watcher]struct{}

	// Metrics
	publishCount  atomic.Int64
	lastPublishAt atomic.Int64 // unix ms
	bytesIn       atomic.Int64
	bytesOut      atomic.Int64
	watcherCount  atomic.Int32

	broadcast chan []byte
	done      chan struct{}
}

// Watcher wraps a viewer WebSocket connection with a send queue.
type Watcher struct {
	conn *websocket.Conn
	send chan []byte
	ip   string
}

func NewHub(cfg Config) *Hub {
	return &Hub{
		cfg:      cfg,
		watchers: make(map[*Watcher]struct{}),
		broadcast: make(chan []byte, 64),
		done:     make(chan struct{}),
	}
}

// Run is the main hub loop that fans out messages to watchers.
func (h *Hub) Run() {
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
	if !h.authenticatePublisher(conn) {
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
	h.mu.Unlock()

	log.Printf("[relay] publisher authenticated from %s", ip)

	defer func() {
		h.mu.Lock()
		if h.publisher == conn {
			h.publisher = nil
		}
		h.mu.Unlock()
		conn.Close()
		log.Printf("[relay] publisher disconnected: %s", ip)
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

		h.publishCount.Add(1)
		h.lastPublishAt.Store(time.Now().UnixMilli())
		h.bytesIn.Add(int64(len(data)))

		// Forward complete OVP message to all watchers
		h.broadcast <- data
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
		conn: conn,
		send: make(chan []byte, h.cfg.MaxWatcherQueue),
		ip:   ip,
	}

	h.mu.Lock()
	h.watchers[watcher] = struct{}{}
	h.watcherCount.Add(1)
	h.mu.Unlock()

	log.Printf("[relay] watcher authenticated from %s (total: %d)", ip, h.watcherCount.Load())

	defer func() {
		h.removeWatcher(watcher)
		log.Printf("[relay] watcher disconnected: %s", ip)
	}()

	// Write pump
	go h.watcherWritePump(watcher)

	// Read pump: just keep reading to detect close / handle CONTROL
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if msgType == websocket.BinaryMessage && len(data) >= proto.HeaderSize {
			hdr, hdrErr := proto.DecodeHeader(data)
			if hdrErr == nil && hdr.Type == proto.MsgControl {
				// Forward control message to publisher
				h.mu.RLock()
				pub := h.publisher
				h.mu.RUnlock()
				if pub != nil {
					_ = pub.WriteMessage(websocket.BinaryMessage, data)
				}
			}
		}
	}
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

func (h *Hub) authenticatePublisher(conn *websocket.Conn) bool {
	// Expect HELLO then AUTH
	_, hello, auth, err := h.readHelloAuth(conn)
	if err != nil {
		sendError(conn, 400, err.Error())
		return false
	}
	if hello.Role != "publisher" {
		sendError(conn, 403, "expected publisher role")
		return false
	}
	if auth.Token != h.cfg.PublisherToken {
		sendError(conn, 401, "invalid publisher token")
		return false
	}
	return true
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
	if !h.cfg.WatcherTokens[auth.Token] {
		sendError(conn, 401, "invalid watcher token")
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
		"publish_count":    h.publishCount.Load(),
		"last_publish_at":  lastPubStr,
		"bytes_in":         h.bytesIn.Load(),
		"bytes_out":        h.bytesOut.Load(),
		"watcher_count":    h.watcherCount.Load(),
	})
}
