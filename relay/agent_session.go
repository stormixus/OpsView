package main

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// agentSession holds all per-agent (per-tenant) state: its publisher, watchers,
// surveillance proxy/config, Ops frame buffer, metrics, and broadcast loop.
type agentSession struct {
	id   string
	name string

	mu         sync.RWMutex
	publisher  *websocket.Conn
	pubWriteMu sync.Mutex
	watchers   map[*Watcher]struct{}
	pin        string // watcher PIN advertised by this agent's publisher

	publishCount  atomic.Int64
	lastPublishAt atomic.Int64 // unix ms
	bytesIn       atomic.Int64
	bytesOut      atomic.Int64
	watcherCount  atomic.Int32
	connectedAt   atomic.Int64 // unix ms; 0 = never

	survConfig   []byte
	survConfigMu sync.RWMutex
	survProxy    *SurvProxy
	frameBuf     *FrameBuffer

	broadcast chan []byte
	done      chan struct{}
}

func newAgentSession(id, name string) *agentSession {
	return &agentSession{
		id:        id,
		name:      name,
		watchers:  make(map[*Watcher]struct{}),
		survProxy: NewSurvProxy(),
		frameBuf:  NewFrameBuffer(),
		broadcast: make(chan []byte, 64),
		done:      make(chan struct{}),
	}
}

// online reports whether a publisher is currently connected.
func (s *agentSession) online() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.publisher != nil
}

// run fans broadcast messages out to this session's watchers (per-session loop).
func (s *agentSession) run() {
	for {
		select {
		case msg := <-s.broadcast:
			s.mu.RLock()
			for w := range s.watchers {
				select {
				case w.send <- msg:
				default:
					select {
					case <-w.send:
					default:
					}
					select {
					case w.send <- msg:
					default:
						log.Printf("[relay:%s] disconnecting slow watcher %s", s.id, w.ip)
						go s.removeWatcher(w)
					}
				}
			}
			s.mu.RUnlock()
		case <-s.done:
			return
		}
	}
}

// removeWatcher detaches a watcher from this session.
func (s *agentSession) removeWatcher(w *Watcher) {
	s.mu.Lock()
	if _, ok := s.watchers[w]; ok {
		delete(s.watchers, w)
		s.watcherCount.Add(-1)
		close(w.send)
	}
	s.mu.Unlock()
	w.conn.Close()
}

// WatcherInfo is one watcher's public state for the dashboard.
type WatcherInfo struct {
	ID    uint32 `json:"id"`
	IP    string `json:"ip"`
	Since string `json:"since"` // RFC3339; empty if connectedAt is zero
}

// watcherList snapshots this session's connected watchers.
func (s *agentSession) watcherList() []WatcherInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]WatcherInfo, 0, len(s.watchers))
	for w := range s.watchers {
		since := ""
		if !w.connectedAt.IsZero() {
			since = w.connectedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, WatcherInfo{ID: w.id, IP: w.ip, Since: since})
	}
	return out
}

// sendToPublisher writes a message to this session's publisher (if connected).
func (s *agentSession) sendToPublisher(msg []byte) {
	s.mu.RLock()
	conn := s.publisher
	s.mu.RUnlock()
	if conn == nil {
		return
	}
	s.pubWriteMu.Lock()
	conn.WriteMessage(websocket.BinaryMessage, msg)
	s.pubWriteMu.Unlock()
}

// sendControlToPublisher forwards a CONTROL message to this session's publisher.
func (s *agentSession) sendControlToPublisher(msg []byte) { s.sendToPublisher(msg) }
