# Relay 멀티 에이전트(멀티테넌트) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** relay가 단일 publisher 대신 **여러 에이전트(지점)** 를 동시에 호스팅한다. 각 에이전트가 자기만의 Ops/CCTV/시청자/PIN을 갖고 테넌트 간 격리되며, 기존 단일 에이전트 뷰어는 무수정 동작한다.

**Architecture:** `Hub`의 단일 publisher 상태를 `map[string]*agentSession`(키=agentID)로 이동. 각 `agentSession`이 publisher conn·watcher PIN·survConfig·**자기 SurvProxy**·frameBuf·metrics·watchers·broadcast 루프를 소유. /publish는 `agent_id`+token(레지스트리)으로 세션을 점유, /watch는 **PIN으로 세션을 찾아** 라우팅(기존 뷰어 무수정). CCTV는 HTTP 라우팅에서 agentID 세그먼트로 세션 분기(default=평면 경로 유지). surv_proxy **내부 키는 변경 없음**(세션마다 별도 proxy라 충돌 없음).

**Tech Stack:** Go(net/http, crypto/subtle, encoding/json), gorilla/websocket. 모듈 `github.com/opsview/opsview/relay`. 스펙: `docs/superpowers/specs/2026-06-06-relay-multi-agent-design.md`.

> **하위호환 핵심:** `agent_id` 없는 publish = **default 세션**(token=`RELAY_PUBLISHER_TOKEN`). default 세션의 CCTV는 **평면 경로**(`/surv/dvr1_ch1/...`) 유지 → 기존 뷰어 그대로. 명명된 에이전트는 `/surv/{agentID}/...`.

---

## File Structure

- `proto/json_messages.go` (수정) — `Hello.AgentID`
- `relay/agent_registry.go` (생성) — 레지스트리 파싱/조회/검증
- `relay/agent_session.go` (생성) — `agentSession` 타입 + 메서드(broadcast/run/메트릭/getter)
- `relay/hub.go` (수정) — Hub에 `sessions map[string]*agentSession`, 단일 publisher 필드 제거, HandlePublish/HandleWatch/Run/snapshot 재작성
- `relay/surv_proxy.go` (수정 없음 — 내부 키 유지). serving 분기는 Hub에서.
- `relay/surv_router.go` (생성) — `Hub.ServeSurvHLS`/`ServeSurvWS` (agentID 분기 → 세션 proxy)
- `relay/main.go` (수정) — surv 라우트를 Hub 디스패처로 교체, 레지스트리 로드
- 테스트: `relay/agent_registry_test.go`, `relay/agent_session_test.go`, `relay/hub_multiagent_test.go`

---

## Task 1: proto — Hello.AgentID 추가

**Files:**
- Modify: `proto/json_messages.go` (Hello 구조체)
- Test: `proto/json_messages_test.go` (없으면 생성)

- [ ] **Step 1: 실패 테스트** — `proto/json_messages_test.go`

```go
package proto

import (
	"encoding/json"
	"testing"
)

func TestHelloAgentID(t *testing.T) {
	var h Hello
	if err := json.Unmarshal([]byte(`{"role":"publisher","agent_id":"gangnam"}`), &h); err != nil {
		t.Fatal(err)
	}
	if h.AgentID != "gangnam" {
		t.Fatalf("AgentID=%q want gangnam", h.AgentID)
	}
	// 하위호환: 없으면 빈 문자열
	var h2 Hello
	json.Unmarshal([]byte(`{"role":"publisher"}`), &h2)
	if h2.AgentID != "" {
		t.Fatalf("missing agent_id => %q want empty", h2.AgentID)
	}
}
```

- [ ] **Step 2: 실패 확인** — `cd proto && go test ./ -run TestHelloAgentID` → FAIL (`AgentID undefined`)

- [ ] **Step 3: Hello에 필드 추가** (`Role` 다음 줄)

```go
	AgentID       string   `json:"agent_id,omitempty"` // publisher only: tenant/agent id; empty = default agent
```

- [ ] **Step 4: 통과 확인** — `cd proto && go test ./ -run TestHelloAgentID` → PASS

- [ ] **Step 5: 커밋**

```bash
git add proto/json_messages.go proto/json_messages_test.go
git commit -m "feat(proto): Hello.AgentID for multi-agent publish"
```

---

## Task 2: 에이전트 레지스트리

**Files:**
- Create: `relay/agent_registry.go`
- Modify: `relay/main.go` (Config에 `Agents`, loadConfig 파싱)
- Test: `relay/agent_registry_test.go`

레지스트리 = 명명된 에이전트의 publisher 토큰. PIN은 런타임에 에이전트가 advertise하므로 레지스트리에 없다. `agent_id` 없는 publish는 default(토큰=`PublisherToken`).

- [ ] **Step 1: 실패 테스트** — `relay/agent_registry_test.go`

```go
package main

import "testing"

func TestRegistryParse(t *testing.T) {
	reg, err := parseAgentRegistry(`[{"id":"gangnam","name":"강남점","token":"tA"},{"id":"hongdae","name":"홍대점","token":"tB"}]`, "legacyTok")
	if err != nil {
		t.Fatal(err)
	}
	// default agent (agent_id "") authenticated by legacy token
	if e, ok := reg.lookup(""); !ok || e.Token != "legacyTok" || e.ID != "default" {
		t.Fatalf("default lookup=%+v ok=%v", e, ok)
	}
	if e, ok := reg.lookup("gangnam"); !ok || e.Token != "tA" || e.Name != "강남점" {
		t.Fatalf("gangnam lookup=%+v ok=%v", e, ok)
	}
	if _, ok := reg.lookup("unknown"); ok {
		t.Fatal("unknown agent must not resolve")
	}
}

func TestRegistryEmptyJSONJustDefault(t *testing.T) {
	reg, err := parseAgentRegistry("", "legacyTok")
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := reg.lookup(""); !ok || e.Token != "legacyTok" {
		t.Fatalf("default-only lookup=%+v ok=%v", e, ok)
	}
}

func TestRegistryDuplicateIDRejected(t *testing.T) {
	if _, err := parseAgentRegistry(`[{"id":"x","token":"1"},{"id":"x","token":"2"}]`, "leg"); err == nil {
		t.Fatal("duplicate agent id must error")
	}
}
```

- [ ] **Step 2: 실패 확인** — `cd relay && go test ./ -run TestRegistry` → FAIL

- [ ] **Step 3: agent_registry.go 구현**

```go
package main

import (
	"encoding/json"
	"fmt"
)

// agentEntry is one configured agent's publish-time identity.
type agentEntry struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token"`
}

// agentRegistry maps agent_id -> entry. The empty key "" is the default
// (legacy) agent, authenticated by RELAY_PUBLISHER_TOKEN.
type agentRegistry struct {
	byID map[string]agentEntry
}

// parseAgentRegistry builds the registry from the RELAY_AGENTS JSON array plus
// the legacy publisher token (the default agent). jsonStr may be empty.
func parseAgentRegistry(jsonStr, legacyToken string) (*agentRegistry, error) {
	reg := &agentRegistry{byID: map[string]agentEntry{}}
	// default agent (agent_id "")
	reg.byID[""] = agentEntry{ID: "default", Name: "default", Token: legacyToken}

	if jsonStr != "" {
		var entries []agentEntry
		if err := json.Unmarshal([]byte(jsonStr), &entries); err != nil {
			return nil, fmt.Errorf("RELAY_AGENTS parse: %w", err)
		}
		for _, e := range entries {
			if e.ID == "" || e.ID == "default" {
				return nil, fmt.Errorf("agent id must be non-empty and not 'default'")
			}
			if e.Token == "" {
				return nil, fmt.Errorf("agent %q: token required", e.ID)
			}
			if _, dup := reg.byID[e.ID]; dup {
				return nil, fmt.Errorf("duplicate agent id %q", e.ID)
			}
			if e.Name == "" {
				e.Name = e.ID
			}
			reg.byID[e.ID] = e
		}
	}
	return reg, nil
}

// lookup resolves an agent_id (possibly "") to its entry.
func (r *agentRegistry) lookup(agentID string) (agentEntry, bool) {
	e, ok := r.byID[agentID]
	return e, ok
}

// ids returns all configured agent ids (including "default").
func (r *agentRegistry) ids() []string {
	out := make([]string, 0, len(r.byID))
	for id := range r.byID {
		out = append(out, id)
	}
	return out
}
```

- [ ] **Step 4: main.go에 배선** — `Config`에 추가:

```go
	Agents *agentRegistry
```

`loadConfig()` 의 `return Config{...}` **직전**에 레지스트리 빌드(에러 시 fail-closed):

```go
	reg, err := parseAgentRegistry(os.Getenv("RELAY_AGENTS"), token)
	if err != nil {
		log.Fatalf("[relay] invalid RELAY_AGENTS: %v", err)
	}
```

`return Config{...}` 에 `Agents: reg,` 추가. (loadConfig가 `log`를 import하는지 확인; main.go는 이미 import.)

- [ ] **Step 5: 통과/빌드** — `cd relay && go test ./ -run TestRegistry && go build ./...` → PASS

- [ ] **Step 6: 커밋**

```bash
git add relay/agent_registry.go relay/agent_registry_test.go relay/main.go
git commit -m "feat(relay): agent registry (per-agent publisher tokens + default)"
```

---

## Task 3: agentSession 타입

**Files:**
- Create: `relay/agent_session.go`
- Test: `relay/agent_session_test.go`

기존 Hub의 per-publisher 상태를 세션 단위로 캡슐화. 각 세션은 자기 broadcast 루프와 SurvProxy/frameBuf/metrics를 가진다.

- [ ] **Step 1: 실패 테스트** — `relay/agent_session_test.go`

```go
package main

import "testing"

func TestNewAgentSessionDefaults(t *testing.T) {
	s := newAgentSession("gangnam", "강남점")
	if s.id != "gangnam" || s.name != "강남점" {
		t.Fatalf("id/name = %q/%q", s.id, s.name)
	}
	if s.survProxy == nil || s.frameBuf == nil || s.watchers == nil {
		t.Fatal("session must init survProxy/frameBuf/watchers")
	}
	if s.online() {
		t.Fatal("new session has no publisher => offline")
	}
}
```

- [ ] **Step 2: 실패 확인** — `cd relay && go test ./ -run TestNewAgentSession` → FAIL

- [ ] **Step 3: agent_session.go 구현**

```go
package main

import (
	"log"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// agentSession holds all per-agent (per-tenant) state: its publisher, watchers,
// surveillance proxy/config, Ops frame buffer, metrics, and broadcast loop.
type agentSession struct {
	id   string
	name string

	mu           sync.RWMutex
	publisher    *websocket.Conn
	pubWriteMu   sync.Mutex
	watchers     map[*Watcher]struct{}
	pin          string // watcher PIN advertised by this agent's publisher

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
```

> **참고:** `Watcher`, `NewSurvProxy`, `NewFrameBuffer`는 기존 정의 재사용. `removeWatcher`의 send 채널 close/conn close는 기존 `Hub.removeWatcher` 로직과 동일(중복 close 방지 위해 map 멤버십 가드).

- [ ] **Step 4: 통과/빌드** — `cd relay && go test ./ -run TestNewAgentSession && go build ./...` → PASS

- [ ] **Step 5: 커밋**

```bash
git add relay/agent_session.go relay/agent_session_test.go
git commit -m "feat(relay): agentSession type (per-tenant state + broadcast loop)"
```

---

## Task 4: Hub을 세션 맵으로 전환

**Files:**
- Modify: `relay/hub.go` (Hub 구조체, NewHub, Run, removeWatcher 제거/이전)
- Test: `relay/hub_multiagent_test.go`

Hub의 단일 publisher/metrics/survProxy/frameBuf/broadcast 필드를 제거하고 `sessions map[string]*agentSession` + 조회 헬퍼로 교체. 테스트 패턴은 default 세션에 귀속.

- [ ] **Step 1: 실패 테스트** — `relay/hub_multiagent_test.go`

```go
package main

import "testing"

func TestHubSessionLifecycle(t *testing.T) {
	h := NewHub(testConfig())
	s := h.getOrCreateSession("gangnam", "강남점")
	if s == nil || s.id != "gangnam" {
		t.Fatal("getOrCreateSession failed")
	}
	if got := h.getOrCreateSession("gangnam", "강남점"); got != s {
		t.Fatal("must return the same session for same id")
	}
	// resolve by PIN
	s.mu.Lock(); s.pin = "481922"; s.mu.Unlock()
	if found := h.sessionByPIN("481922"); found != s {
		t.Fatal("sessionByPIN must find the session")
	}
	if h.sessionByPIN("000000") != nil {
		t.Fatal("unknown PIN must resolve to nil")
	}
}

// testConfig returns a minimal Config with a default-only registry.
func testConfig() Config {
	reg, _ := parseAgentRegistry("", "tok")
	return Config{PublisherToken: "tok", MaxWatcherQueue: 4, Agents: reg}
}
```

- [ ] **Step 2: 실패 확인** — `cd relay && go test ./ -run TestHubSessionLifecycle` → FAIL

- [ ] **Step 3: Hub 구조체 교체** — `relay/hub.go`

`Hub` 구조체에서 다음 필드 **삭제**: `publisher`, `pubWriteMu`, `watchers`, `publisherPIN`, `publishCount`, `lastPublishAt`, `bytesIn`, `bytesOut`, `watcherCount`, `broadcast`, `survConfig`, `survConfigMu`, `survProxy`, `frameBuf`. 다음으로 **교체**:

```go
type Hub struct {
	cfg Config

	mu       sync.RWMutex
	sessions map[string]*agentSession // keyed by agentID ("default" included)

	watcherIDSeq atomic.Uint32
	done         chan struct{}
	testPattern  *TestPattern
	pinLimiter   *pinLimiter
}
```

`NewHub` 교체:

```go
func NewHub(cfg Config) *Hub {
	h := &Hub{
		cfg:        cfg,
		sessions:   make(map[string]*agentSession),
		done:       make(chan struct{}),
		pinLimiter: newPinLimiter(),
	}
	// Pre-create the default session so the legacy flat path always resolves.
	h.sessions["default"] = newAgentSession("default", "default")
	h.testPattern = NewTestPattern(h)
	return h
}

// getOrCreateSession returns the session for agentID, creating it if absent.
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

// allSessions snapshots the session pointers (for dashboard/state).
func (h *Hub) allSessions() []*agentSession {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*agentSession, 0, len(h.sessions))
	for _, s := range h.sessions {
		out = append(out, s)
	}
	return out
}
```

> `subtle` 는 hub.go에 이미 import됨.

- [ ] **Step 4: Run 교체** — 기존 `Run`(전역 broadcast 루프)을 default 세션 run + 테스트패턴 시작으로 교체. 세션별 broadcast는 각 세션 `run()`이 담당하므로 Hub.Run은 default 세션 루프 기동 + 종료 처리만:

```go
// Run starts the default session loop and the test pattern.
func (h *Hub) Run() {
	go h.sessions["default"].run()
	h.testPattern.Start()
	<-h.done
}

func (h *Hub) Stop() { close(h.done) }
```

> 기존 `Stop`이 있으면 위 내용으로 통합. 기존 `Hub.removeWatcher`는 `agentSession.removeWatcher`로 이전됐으므로 hub.go에서 **삭제**(다음 태스크에서 호출부 수정).

- [ ] **Step 5: 통과 확인** (이 시점 hub.go의 HandlePublish/HandleWatch/snapshot은 아직 옛 필드 참조 → 빌드 깨짐. 다음 태스크에서 수정하므로, 본 태스크는 **테스트만** 격리 실행하지 말고 Task 5까지 묶어 빌드한다.)

Run: `cd relay && go vet ./ 2>&1 | head` — HandlePublish/HandleWatch 관련 컴파일 에러 예상(다음 태스크에서 해소).

- [ ] **Step 6: 커밋 보류** — Task 5와 함께 커밋(빌드가 깨진 중간 상태이므로).

---

## Task 5: HandlePublish / HandleWatch / snapshot 재작성

**Files:**
- Modify: `relay/hub.go` (HandlePublish, HandleWatch, authenticatePublisher, snapshot/control 라우팅, TestPattern 연동)
- Test: `relay/hub_multiagent_test.go`

- [ ] **Step 1: authenticatePublisher가 AgentID 반환하도록 수정**

`authenticatePublisher`가 `(proto.Hello, proto.Auth, bool)`을 반환하도록 시그니처 변경하고, 토큰 검증을 **레지스트리 기반**으로:

```go
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
		return hello, auth, false
	}
	if !validPublisherToken(auth.Token, entry.Token) {
		sendError(conn, 401, "invalid publisher token")
		return hello, auth, false
	}
	if auth.PIN == "" {
		sendError(conn, 400, "missing viewer PIN")
		return hello, auth, false
	}
	return hello, auth, true
}
```

- [ ] **Step 2: HandlePublish 재작성** (세션 점유 + PIN 유일성 + 세션별 ingest)

`HandlePublish` 본문을 교체 (upgrade/ReadLimit 부분은 유지):

```go
	hello, auth, ok := h.authenticatePublisher(conn)
	if !ok {
		conn.Close()
		return
	}
	entry, _ := h.cfg.Agents.lookup(hello.AgentID)
	sess := h.getOrCreateSession(entry.ID, entry.Name)

	// Enforce single publisher per session + globally-unique PIN among online agents.
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
			cfgCopy := make([]byte, len(data))
			copy(cfgCopy, data)
			sess.survConfigMu.Lock()
			sess.survConfig = cfgCopy
			sess.survConfigMu.Unlock()
			sess.broadcast <- data
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
```

- [ ] **Step 3: HandleWatch 재작성** (PIN → 세션 라우팅)

`authenticateWatcher`는 현재 PIN을 `h.publisherPIN`과 비교한다. 이를 **세션 해석**으로 대체. HandleWatch 본문 교체(upgrade 유지):

```go
	ip := r.RemoteAddr
	if !h.pinLimiter.allowed(clientIP(ip)) {
		sendError(conn, 429, "too many attempts")
		conn.Close()
		return
	}
	// Read HELLO + AUTH; AUTH.Token is the viewer PIN.
	_, _, auth, err := h.readHelloAuth(conn)
	if err != nil {
		sendError(conn, 400, err.Error())
		conn.Close()
		return
	}
	sess := h.sessionByPIN(auth.Token)
	if sess == nil {
		h.pinLimiter.recordFailure(clientIP(ip))
		sendError(conn, 401, "invalid PIN")
		conn.Close()
		return
	}
	h.pinLimiter.recordSuccess(clientIP(ip))

	watcher := &Watcher{
		id:          h.watcherIDSeq.Add(1),
		conn:        conn,
		send:        make(chan []byte, h.cfg.MaxWatcherQueue),
		ip:          ip,
		connectedAt: time.Now(),
	}
	sess.mu.Lock()
	sess.watchers[watcher] = struct{}{}
	sess.watcherCount.Add(1)
	sess.mu.Unlock()

	conn.WriteMessage(websocket.BinaryMessage, proto.MarshalMessage(proto.MsgReady, nil))

	sess.survConfigMu.RLock()
	cachedConfig := sess.survConfig
	sess.survConfigMu.RUnlock()
	if len(cachedConfig) > 0 {
		conn.WriteMessage(websocket.BinaryMessage, cachedConfig)
	}
	if frameMsg, ok := sess.frameBuf.FullFrameMessage(); ok {
		conn.WriteMessage(websocket.BinaryMessage, frameMsg)
	}

	defer sess.removeWatcher(watcher)
	go h.watcherWritePump(watcher) // write pump is session-agnostic (uses watcher.send/conn)

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
```

- [ ] **Step 4: 세션으로 이전되는 보조 메서드** — `Watcher`에 `connectedAt time.Time` 필드 추가. `sendToPublisher`/`sendControlToPublisher`/`routeSnapshotResponse`를 **agentSession 메서드로 이전**(기존 Hub 버전 삭제). 예:

```go
// agent_session.go
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

func (s *agentSession) sendControlToPublisher(msg []byte) { s.sendToPublisher(msg) }
```

`routeSnapshotResponse`는 기존 로직(watcherID prefix 파싱 → 해당 watcher.send)을 세션의 watchers 대상으로 이전:

```go
func (h *Hub) routeSnapshotResponse(s *agentSession, reqID string, data []byte) {
	parts := strings.SplitN(reqID, ":", 2)
	if len(parts) != 2 {
		return
	}
	wid, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return
	}
	// strip the watcher prefix so the client sees its original req_id
	var resp proto.SnapshotResponse
	if json.Unmarshal(data[proto.HeaderSize:], &resp) == nil {
		resp.ReqID = parts[1]
		payload, _ := json.Marshal(resp)
		data = proto.MarshalMessage(proto.MsgSurvSnapshot, payload)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for w := range s.watchers {
		if w.id == uint32(wid) {
			select {
			case w.send <- data:
			default:
			}
			return
		}
	}
}
```

> 기존 `routeSnapshotResponse`/`sendToPublisher`/`sendControlToPublisher`의 정확한 본문은 현재 hub.go를 참조해 동일 동작을 세션 범위로 옮긴다(여기 코드는 동작 명세).

- [ ] **Step 5: 테스트 추가** (격리 + 라우팅)

```go
func TestSessionByPINIsolation(t *testing.T) {
	h := NewHub(testConfig())
	a := h.getOrCreateSession("a", "A")
	b := h.getOrCreateSession("b", "B")
	// simulate online publishers with distinct PINs
	a.mu.Lock(); a.publisher = &websocket.Conn{}; a.pin = "111111"; a.mu.Unlock()
	b.mu.Lock(); b.publisher = &websocket.Conn{}; b.pin = "222222"; b.mu.Unlock()
	if h.sessionByPIN("111111") != a {
		t.Fatal("PIN 111111 must resolve to session a")
	}
	if h.sessionByPIN("222222") != b {
		t.Fatal("PIN 222222 must resolve to session b")
	}
	if h.sessionByPIN("111111") == b {
		t.Fatal("isolation: a's PIN must not resolve to b")
	}
}
```

> `&websocket.Conn{}`는 nil 아님 표식용(네트워크 미사용). online() 판정에만 쓰임.

- [ ] **Step 6: TestPattern 연동 확인** — `TestPattern`이 기존에 `h.broadcast`/`h.watchers`를 참조하면, default 세션을 쓰도록 수정(`h.sessions["default"].broadcast` 등). `grep -n "h\.\(broadcast\|watchers\|frameBuf\|survProxy\)" testpattern.go` 로 잔존 참조 제거.

- [ ] **Step 7: 빌드/테스트/커밋** (Task 4+5 합본)

```bash
cd relay && go build ./... && go test ./
git add relay/hub.go relay/agent_session.go relay/hub_multiagent_test.go
git commit -m "feat(relay): route publish/watch/snapshot per agent session"
```

---

## Task 6: CCTV surv 라우팅 (agentID 분기)

**Files:**
- Create: `relay/surv_router.go`
- Modify: `relay/main.go` (surv 라우트를 Hub 디스패처로 교체)
- Test: `relay/hub_multiagent_test.go`

`/surv/...`·`/surv/ws/...`의 첫 경로 세그먼트가 **알려진 (non-default) agentID면** 그 세션 proxy로, 아니면 **default 세션** proxy로 위임. → 레거시 평면 경로 유지 + 신규 네임스페이스.

- [ ] **Step 1: 실패 테스트**

```go
func TestSurvAgentSplit(t *testing.T) {
	h := NewHub(testConfig())
	h.getOrCreateSession("gangnam", "강남점")
	// known agent prefix
	id, rest := h.splitSurvPath("gangnam/dvr1_ch1/index.m3u8")
	if id != "gangnam" || rest != "dvr1_ch1/index.m3u8" {
		t.Fatalf("split named => %q,%q", id, rest)
	}
	// legacy flat (default)
	id2, rest2 := h.splitSurvPath("dvr1_ch1/index.m3u8")
	if id2 != "default" || rest2 != "dvr1_ch1/index.m3u8" {
		t.Fatalf("split flat => %q,%q", id2, rest2)
	}
}
```

- [ ] **Step 2: 실패 확인** — `cd relay && go test ./ -run TestSurvAgentSplit` → FAIL

- [ ] **Step 3: surv_router.go 구현**

```go
package main

import (
	"net/http"
	"strings"
)

// splitSurvPath separates an optional leading agentID from the channel path.
// If the first segment matches a non-default session id, it is the agent scope;
// otherwise the path belongs to the default (legacy) session.
func (h *Hub) splitSurvPath(p string) (agentID, rest string) {
	first := p
	if i := strings.IndexByte(p, '/'); i >= 0 {
		first = p[:i]
	}
	if first != "" && first != "default" {
		if s := h.sessionByID(first); s != nil {
			return first, strings.TrimPrefix(p, first+"/")
		}
	}
	return "default", p
}

// ServeSurvHLS dispatches /surv/[agentID/]chID/... to the right session proxy.
func (h *Hub) ServeSurvHLS(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/surv/")
	agentID, rest := h.splitSurvPath(p)
	s := h.sessionByID(agentID)
	if s == nil {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	// rewrite path to the flat form the per-session proxy expects
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/surv/" + rest
	s.survProxy.ServeHLS(w, r2)
}

// ServeSurvWS dispatches /surv/ws/[agentID/]chID to the right session proxy.
func (h *Hub) ServeSurvWS(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/surv/ws/")
	agentID, rest := h.splitSurvPath(p)
	s := h.sessionByID(agentID)
	if s == nil {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/surv/ws/" + rest
	s.survProxy.ServeWS(w, r2)
}
```

> `SurvProxy.ServeHLS`/`ServeWS`는 기존대로 `/surv/`·`/surv/ws/` prefix를 trim해서 chID를 얻으므로, 위처럼 rest를 평면 경로로 재작성해 위임하면 내부 변경 없이 동작.

- [ ] **Step 4: main.go 라우트 교체** — 기존

```go
	mux.HandleFunc("/surv/ws/", hub.survProxy.ServeWS)
	mux.HandleFunc("/surv/", hub.survProxy.ServeHLS)
```

를:

```go
	mux.HandleFunc("/surv/ws/", hub.ServeSurvWS)
	mux.HandleFunc("/surv/", hub.ServeSurvHLS)
```

- [ ] **Step 5: 통과/빌드** — `cd relay && go test ./ -run TestSurvAgentSplit && go build ./...` → PASS

- [ ] **Step 6: 커밋**

```bash
git add relay/surv_router.go relay/main.go relay/hub_multiagent_test.go
git commit -m "feat(relay): surv routing splits agentID scope (legacy flat preserved)"
```

---

## Task 7: 스냅샷/health/metrics 엔드포인트 세션화

**Files:**
- Modify: `relay/hub.go` (HandleSnapshot, HandleHealth, HandleMetrics, HandleSurvConfig, HandleSurvStreams)

기존 단일 publisher 가정 핸들러를 세션 인지로. 하위호환: agent 미지정 시 default 세션.

- [ ] **Step 1: HandleSnapshot 세션화** — 쿼리 `?agent=<id>`(없으면 default)로 세션 선택 후 그 세션 frameBuf 사용:

```go
func (h *Hub) HandleSnapshot(w http.ResponseWriter, r *http.Request) {
	s := h.sessionByID(r.URL.Query().Get("agent"))
	if s == nil {
		s = h.sessionByID("default")
	}
	data, err := s.frameBuf.SnapshotPNG()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}
```

- [ ] **Step 2: HandleSurvConfig / HandleSurvStreams 세션화** — `?agent=` 또는 default. (PIN 게이트가 있던 `/api/surv`는 해당 세션 PIN 기준으로 — 기존 동작이 default 세션과 동일하면 유지.) `HandleSurvStreams`는 `s.survProxy.ListStreams()`.

- [ ] **Step 3: HandleHealth / HandleMetrics** — 전역 합계로 갱신:

```go
func (h *Hub) HandleHealth(w http.ResponseWriter, r *http.Request) {
	online := 0
	for _, s := range h.allSessions() {
		if s.online() {
			online++
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":        map[bool]string{true: "ok", false: "no_publisher"}[online > 0],
		"agents_online": online,
	})
}
```

> `HandleMetrics`도 세션 합계(bytesIn/out/watcherCount)로 갱신. 정확한 필드는 대시보드 스펙의 state와 일치시킨다(대시보드 구현 시 `buildDashboardState`가 `allSessions()`를 사용).

- [ ] **Step 4: 빌드/테스트** — `cd relay && go build ./... && go test ./` → PASS

- [ ] **Step 5: 커밋**

```bash
git add relay/hub.go
git commit -m "feat(relay): session-scoped snapshot/health/metrics/surv endpoints"
```

---

## Task 8: 하위호환 + 격리 end-to-end 테스트

**Files:**
- Modify: `relay/hub_multiagent_test.go`

- [ ] **Step 1: 레거시 publish(agent_id 없음) → default 세션 + 기존 watcher 흐름** 테스트 (httptest + websocket dialer, 기존 `hub_security_test.go`의 e2e 패턴 참고).

```go
// TestLegacyPublisherDefaultSession: an agent that omits agent_id lands in the
// default session, and a watcher with the advertised PIN attaches to it.
// (구현: 기존 TestPublisherWatcherAuthEndToEnd 패턴을 재사용하되 agent_id 없이
//  publish → /watch PIN 일치 → MsgReady 수신까지 확인.)
```

- [ ] **Step 2: 격리** — 에이전트 A PIN으로 접속한 watcher가 B의 survConfig/스냅샷/스트림에 접근 불가(다른 PIN→다른 세션, A PIN으로는 B 세션 해석 안 됨)를 단언.

- [ ] **Step 3: PIN 충돌 거부** — 두 세션이 같은 PIN을 advertise하려 하면 두 번째 publish가 409.

- [ ] **Step 4: 전체 테스트/커밋**

```bash
cd relay && go test ./ -count=1
git add relay/hub_multiagent_test.go
git commit -m "test(relay): multi-agent backward-compat + tenant isolation"
```

---

## Self-Review (작성자 체크)

- **스펙 커버리지**: 레지스트리(§3.1)→T2, agentSession/세션맵(§3.2)→T3·T4, /publish·/watch 라우팅(§3.3)→T5, surv 네임스페이스(§3.4)→T6, proto agent_id(§4)→T1, 스냅샷/health 세션화→T7, 하위호환/격리(§10 테스트)→T8. 전 항목 매핑됨.
- **Placeholder**: T5 step4·T8은 "기존 본문을 세션 범위로 이전"·"e2e 패턴 재사용"을 **동작 명세 + 참조 위치**로 제시(거대한 기존 함수 전체 복붙 대신). 구현 시 현재 hub.go 해당 함수를 보고 동일 동작을 옮긴다 — 코드 위치/시그니처/동작은 명시.
- **타입 일관성**: `agentSession`(id/name/publisher/pin/survProxy/frameBuf/metrics), `Watcher.connectedAt`, `getOrCreateSession`/`sessionByPIN`/`sessionByID`/`allSessions`, `splitSurvPath`/`ServeSurvHLS`/`ServeSurvWS`, registry `lookup`/`ids` — 태스크 전반 명칭 일치.
- **주의**: T4·T5는 빌드가 중간에 깨지는 큰 리팩터라 **묶어서** 커밋. `testpattern.go`·`HandleMetrics` 등 옛 단일 필드 참조처를 grep으로 모두 세션 기반으로 옮길 것.
- **위험**: 이건 relay 최핵심 보안 경로(인증/격리) 변경 → T8 격리 테스트를 반드시 통과시키고, 가능하면 실DVR/실에이전트 2개로 수동 검증.
