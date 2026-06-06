# Relay Dashboard (Milestone 1: Status + Live Grid) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** relay가 직접 서빙하는 admin 인증 웹 대시보드 — 상태 탭(publisher/watcher/스트림/메트릭)과 라이브 탭(전 채널 라이브 그리드)을 제공한다.

**Architecture:** relay가 `RELAY_DASHBOARD_TOKEN` 설정 시에만 `/dashboard*` 라우트를 등록(미설정=404). 로그인은 stateless HMAC 서명 쿠키. 상태 패널은 admin 게이트된 단일 집계 JSON(`/dashboard/api/state`)을 2초 폴링. 라이브 그리드는 기존 `/surv/ws/`·`/surv/`를 재사용(검증된 sequence-mode MSE 플레이어).

**Tech Stack:** Go(net/http, crypto/hmac, embed), gorilla/websocket(기존), vanilla HTML/CSS/JS(빌드 스텝 없음). 모듈 경로 `github.com/opsview/opsview/relay`.

> **참고:** 시각 디자인(HTML/CSS)은 claude.ai/design 산출물로 추후 reskinning한다. 이 계획의 프론트엔드 태스크는 **동작 로직(JS) + 기능적 HTML 스켈레톤(문서화된 element id)** 을 구현하며, 디자인이 오면 같은 id에 스타일만 입힌다.

**스펙:** `docs/superpowers/specs/2026-06-06-relay-dashboard-design.md`

---

## File Structure

- `relay/version.go` (생성) — `var relayVersion = "dev"` (ldflags 주입용)
- `relay/dashboard_session.go` (생성) — 쿠키 서명/검증
- `relay/dashboard_state.go` (생성) — 집계 state 타입 + `Hub.buildDashboardState()`
- `relay/dashboard.go` (생성) — 로그인/로그아웃/state/static 핸들러 + 라우트 등록 헬퍼
- `relay/dashboard_assets/{index.html,app.js,style.css}` (생성) — `//go:embed`
- `relay/dashboard_session_test.go`, `relay/dashboard_test.go` (생성)
- `relay/main.go` (수정) — Config.DashboardToken, 라우트 등록
- `relay/hub.go` (수정) — `Hub.startedAt`, `Watcher.connectedAt`, `Hub.WatcherList()`
- `relay/surv_ws.go` (수정) — `survWSHub.ClientCount()`, `fragMuxer.Codec()`
- `relay/surv_proxy.go` (수정) — `SurvProxy.StreamStats()`

---

## Task 1: 버전 변수 + Config.DashboardToken

**Files:**
- Create: `relay/version.go`
- Modify: `relay/main.go` (Config 구조체 + loadConfig)
- Test: `relay/dashboard_test.go`

- [ ] **Step 1: 실패 테스트 작성** — `relay/dashboard_test.go`

```go
package main

import (
	"os"
	"testing"
)

func TestLoadConfigDashboardToken(t *testing.T) {
	os.Setenv("RELAY_PUBLISHER_TOKEN", "pub")
	os.Setenv("RELAY_DASHBOARD_TOKEN", "dash-secret")
	defer os.Unsetenv("RELAY_DASHBOARD_TOKEN")
	cfg := loadConfig()
	if cfg.DashboardToken != "dash-secret" {
		t.Fatalf("DashboardToken=%q want %q", cfg.DashboardToken, "dash-secret")
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `cd relay && go test ./ -run TestLoadConfigDashboardToken`
Expected: FAIL — `cfg.DashboardToken undefined`

- [ ] **Step 3: version.go 생성**

```go
package main

// relayVersion is injected at build time via -ldflags "-X main.relayVersion=v0.3.8".
var relayVersion = "dev"
```

- [ ] **Step 4: main.go에 DashboardToken 추가**

`Config` 구조체에 필드 추가 (`AllowedOrigins []string` 다음 줄):

```go
	// DashboardToken gates the operator dashboard. Empty => dashboard disabled
	// (routes not registered), so it is never exposed unauthenticated.
	DashboardToken string
```

`loadConfig()`의 `return Config{...}` 에 필드 추가:

```go
		DashboardToken: os.Getenv("RELAY_DASHBOARD_TOKEN"),
```

- [ ] **Step 5: 통과 확인**

Run: `cd relay && go test ./ -run TestLoadConfigDashboardToken`
Expected: PASS

- [ ] **Step 6: 커밋**

```bash
git add relay/version.go relay/main.go relay/dashboard_test.go
git commit -m "feat(relay): dashboard token config + version var"
```

---

## Task 2: 세션 쿠키 서명/검증

**Files:**
- Create: `relay/dashboard_session.go`
- Test: `relay/dashboard_session_test.go`

- [ ] **Step 1: 실패 테스트 작성** — `relay/dashboard_session_test.go`

```go
package main

import (
	"testing"
	"time"
)

func TestSessionRoundTrip(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tok := "dash-secret"
	v := signSession(tok, now.Add(time.Hour))
	if !verifySession(tok, v, now) {
		t.Fatal("freshly signed session should verify")
	}
}

func TestSessionExpired(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tok := "dash-secret"
	v := signSession(tok, now.Add(-time.Second))
	if verifySession(tok, v, now) {
		t.Fatal("expired session must not verify")
	}
}

func TestSessionTampered(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tok := "dash-secret"
	v := signSession(tok, now.Add(time.Hour))
	if verifySession(tok, v+"x", now) {
		t.Fatal("tampered signature must not verify")
	}
	if verifySession("other-token", v, now) {
		t.Fatal("wrong token must not verify")
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `cd relay && go test ./ -run TestSession`
Expected: FAIL — `signSession undefined`

- [ ] **Step 3: dashboard_session.go 구현**

```go
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

const (
	dashboardCookieName = "opsview_dash"
	dashboardSessionTTL = 12 * time.Hour
)

// dashboardKey derives a fixed-length HMAC key from the token so the raw token
// is never used directly as the key.
func dashboardKey(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// signSession returns "b64url(exp).hexHMAC(b64url(exp))".
func signSession(token string, exp time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(exp.Unix(), 10)))
	mac := hmac.New(sha256.New, dashboardKey(token))
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

// verifySession returns true iff the value's HMAC matches and exp is in the future.
func verifySession(token, value string, now time.Time) bool {
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	mac := hmac.New(sha256.New, dashboardKey(token))
	mac.Write([]byte(parts[0]))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[1])) != 1 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	exp, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return false
	}
	return now.Unix() < exp
}
```

- [ ] **Step 4: 통과 확인**

Run: `cd relay && go test ./ -run TestSession`
Expected: PASS

- [ ] **Step 5: 커밋**

```bash
git add relay/dashboard_session.go relay/dashboard_session_test.go
git commit -m "feat(relay): dashboard session cookie sign/verify"
```

---

## Task 3: 상태 집계용 getter (Watcher/WS/codec)

**Files:**
- Modify: `relay/hub.go` (Watcher 구조체, HandleWatch, Hub 구조체, NewHub)
- Modify: `relay/surv_ws.go` (survWSHub, fragMuxer)
- Modify: `relay/surv_proxy.go` (SurvProxy)
- Test: `relay/dashboard_test.go`

- [ ] **Step 1: 실패 테스트 추가** — `relay/dashboard_test.go`

```go
func TestSurvWSClientCount(t *testing.T) {
	h := newSurvWSHub()
	if h.ClientCount() != 0 {
		t.Fatalf("empty hub count=%d want 0", h.ClientCount())
	}
	c := h.add()
	if h.ClientCount() != 1 {
		t.Fatalf("after add count=%d want 1", h.ClientCount())
	}
	h.remove(c)
	if h.ClientCount() != 0 {
		t.Fatalf("after remove count=%d want 0", h.ClientCount())
	}
}

func TestFragMuxerCodec(t *testing.T) {
	sps := []byte{0x67, 0x42, 0xc0, 0x28, 0xd9, 0x00, 0x78, 0x02, 0x27, 0xe5, 0x84, 0x00, 0x00, 0x03, 0x00, 0x04, 0x00, 0x00, 0x03, 0x00, 0xf0, 0x3c, 0x60, 0xc9, 0x20}
	pps := []byte{0x08}
	m := newFragMuxerH264(sps, pps)
	if got := m.Codec(); got != "h264" {
		t.Fatalf("Codec()=%q want h264", got)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `cd relay && go test ./ -run 'TestSurvWSClientCount|TestFragMuxerCodec'`
Expected: FAIL — `ClientCount`/`Codec` undefined

- [ ] **Step 3: surv_ws.go에 메서드 추가**

`survWSHub` 메서드 (파일 끝 근처, `remove` 다음):

```go
// ClientCount returns the number of currently connected WS watchers.
func (h *survWSHub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}
```

`fragMuxer` 메서드 (`writeAU` 다음):

```go
// Codec returns "h264" | "h265" once the init segment exists, else "".
func (m *fragMuxer) Codec() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.initSeg == nil {
		return ""
	}
	if m.isH265 {
		return "h265"
	}
	return "h264"
}
```

- [ ] **Step 4: Watcher.connectedAt + Hub.startedAt + WatcherList 추가** — `relay/hub.go`

`Watcher` 구조체에 필드 추가:

```go
	connectedAt time.Time
```

`Hub` 구조체에 필드 추가 (`cfg Config` 다음):

```go
	startedAt time.Time
```

`NewHub` 의 `&Hub{` 리터럴에 추가:

```go
		startedAt: time.Now(),
```

HandleWatch에서 `watcher := &Watcher{...}` 리터럴에 `connectedAt: time.Now(),` 추가 (line ~264).

파일 끝에 메서드 추가:

```go
// WatcherInfo is one Ops watcher's public state for the dashboard.
type WatcherInfo struct {
	ID    uint32 `json:"id"`
	IP    string `json:"ip"`
	Since string `json:"since"` // RFC3339
}

// WatcherList snapshots the currently connected Ops watchers.
func (h *Hub) WatcherList() []WatcherInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]WatcherInfo, 0, len(h.watchers))
	for w := range h.watchers {
		out = append(out, WatcherInfo{ID: w.id, IP: w.ip, Since: w.connectedAt.UTC().Format(time.RFC3339)})
	}
	return out
}
```

- [ ] **Step 5: SurvProxy.StreamStats 추가** — `relay/surv_proxy.go`

`ListStreams` 다음에 추가:

```go
// StreamStat is per-stream detail for the dashboard.
type StreamStat struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Active     bool   `json:"active"`
	Codec      string `json:"codec"`
	WSWatchers int    `json:"ws_watchers"`
}

// StreamStats snapshots active streams with codec and WS-watcher counts.
func (sp *SurvProxy) StreamStats() []StreamStat {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	out := make([]StreamStat, 0, len(sp.streams))
	for id, e := range sp.streams {
		codec := ""
		if e.frag != nil {
			codec = e.frag.Codec()
		}
		ws := 0
		if e.wsHub != nil {
			ws = e.wsHub.ClientCount()
		}
		out = append(out, StreamStat{ID: id, Name: e.name, Active: true, Codec: codec, WSWatchers: ws})
	}
	return out
}
```

- [ ] **Step 6: 통과 확인**

Run: `cd relay && go test ./ -run 'TestSurvWSClientCount|TestFragMuxerCodec' && go build ./...`
Expected: PASS, build OK

- [ ] **Step 7: 커밋**

```bash
git add relay/hub.go relay/surv_ws.go relay/surv_proxy.go relay/dashboard_test.go
git commit -m "feat(relay): dashboard getters (watcher list, ws count, codec, uptime)"
```

---

## Task 4: state 집계 빌더

**Files:**
- Create: `relay/dashboard_state.go`
- Test: `relay/dashboard_test.go`

- [ ] **Step 1: 실패 테스트 추가**

```go
func TestBuildDashboardState(t *testing.T) {
	h := NewHub(Config{})
	st := h.buildDashboardState()
	if st.Relay.Version != relayVersion {
		t.Fatalf("version=%q want %q", st.Relay.Version, relayVersion)
	}
	if st.Relay.PublisherConnected {
		t.Fatal("no publisher connected, want false")
	}
	if st.Watchers.List == nil {
		t.Fatal("watchers.list must be non-nil (JSON [])")
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `cd relay && go test ./ -run TestBuildDashboardState`
Expected: FAIL — `buildDashboardState undefined`

- [ ] **Step 3: dashboard_state.go 구현**

```go
package main

import (
	"encoding/json"
	"time"

	"github.com/opsview/opsview/proto"
)

type dashboardState struct {
	Relay    relayInfo     `json:"relay"`
	Watchers watchersInfo  `json:"watchers"`
	Streams  []StreamStat  `json:"streams"`
	DVRs     []dvrSummary  `json:"dvrs"`
}

type relayInfo struct {
	Version            string `json:"version"`
	UptimeSec          int64  `json:"uptime_sec"`
	PublisherConnected bool   `json:"publisher_connected"`
	PublisherPINSet    bool   `json:"publisher_pin_set"`
	LastPublishAt      string `json:"last_publish_at"`
	BytesIn            int64  `json:"bytes_in"`
	BytesOut           int64  `json:"bytes_out"`
	PublishCount       int64  `json:"publish_count"`
}

type watchersInfo struct {
	Count int           `json:"count"`
	List  []WatcherInfo `json:"list"`
}

type dvrSummary struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Channels int    `json:"channels"`
}

// buildDashboardState snapshots all dashboard data into one serializable struct.
func (h *Hub) buildDashboardState() dashboardState {
	h.mu.RLock()
	hasPub := h.publisher != nil
	pinSet := h.publisherPIN != ""
	h.mu.RUnlock()

	lastPub := ""
	if ms := h.lastPublishAt.Load(); ms > 0 {
		lastPub = time.UnixMilli(ms).UTC().Format(time.RFC3339)
	}

	watchers := h.WatcherList()

	dvrs := []dvrSummary{}
	h.survConfigMu.RLock()
	raw := h.survConfig
	h.survConfigMu.RUnlock()
	if len(raw) > 0 {
		var cfg proto.SurvConfig
		if json.Unmarshal(raw, &cfg) == nil {
			counts := map[int64]int{}
			for _, ch := range cfg.Channels {
				counts[ch.DVRID]++
			}
			for _, d := range cfg.DVRs {
				dvrs = append(dvrs, dvrSummary{ID: d.ID, Name: d.Name, Channels: counts[d.ID]})
			}
		}
	}

	streams := h.survProxy.StreamStats()

	return dashboardState{
		Relay: relayInfo{
			Version:            relayVersion,
			UptimeSec:          int64(time.Since(h.startedAt).Seconds()),
			PublisherConnected: hasPub,
			PublisherPINSet:    pinSet,
			LastPublishAt:      lastPub,
			BytesIn:            h.bytesIn.Load(),
			BytesOut:           h.bytesOut.Load(),
			PublishCount:       h.publishCount.Load(),
		},
		Watchers: watchersInfo{Count: int(h.watcherCount.Load()), List: watchers},
		Streams:  streams,
		DVRs:     dvrs,
	}
}
```

> **확인됨:** `proto/json_messages.go` — `DVRInfo.ID int64`, `DVRInfo.Name string`, `ChannelInfo.DVRID int64`, `ChannelInfo.Name`, `ChannelInfo.Order`. 위 코드는 이 타입(int64) 기준으로 작성됨.

- [ ] **Step 4: 통과 확인**

Run: `cd relay && go test ./ -run TestBuildDashboardState`
Expected: PASS

- [ ] **Step 5: 커밋**

```bash
git add relay/dashboard_state.go relay/dashboard_test.go
git commit -m "feat(relay): aggregate dashboard state builder"
```

---

## Task 5: 핸들러 (로그인/로그아웃/state/static) + 라우트 등록

**Files:**
- Create: `relay/dashboard.go`
- Create: `relay/dashboard_assets/index.html` (Task 7에서 채움 — 여기선 빈 셸로 생성)
- Test: `relay/dashboard_test.go`

- [ ] **Step 1: 임시 빈 asset 생성** (embed 컴파일용)

`relay/dashboard_assets/index.html`:

```html
<!doctype html><meta charset="utf-8"><title>OpsView Relay</title>
<body>dashboard placeholder</body>
```

- [ ] **Step 2: 실패 테스트 추가** — `relay/dashboard_test.go`

```go
import (
	"net/http"
	"net/http/httptest"
)

func newDashHub(token string) *Hub {
	h := NewHub(Config{DashboardToken: token})
	return h
}

func TestStateRequiresAuth(t *testing.T) {
	h := newDashHub("dash-secret")
	req := httptest.NewRequest("GET", "/dashboard/api/state", nil)
	rec := httptest.NewRecorder()
	h.HandleDashboardState(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie => %d want 401", rec.Code)
	}
}

func TestLoginThenState(t *testing.T) {
	h := newDashHub("dash-secret")

	// wrong password
	bad := httptest.NewRequest("POST", "/dashboard/api/login", strings.NewReader(`{"password":"nope"}`))
	badRec := httptest.NewRecorder()
	h.HandleDashboardLogin(badRec, bad)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login => %d want 401", badRec.Code)
	}

	// correct password
	ok := httptest.NewRequest("POST", "/dashboard/api/login", strings.NewReader(`{"password":"dash-secret"}`))
	okRec := httptest.NewRecorder()
	h.HandleDashboardLogin(okRec, ok)
	if okRec.Code != http.StatusOK {
		t.Fatalf("good login => %d want 200", okRec.Code)
	}
	cookie := okRec.Result().Cookies()
	if len(cookie) == 0 {
		t.Fatal("login must set a cookie")
	}

	// state with cookie
	req := httptest.NewRequest("GET", "/dashboard/api/state", nil)
	req.AddCookie(cookie[0])
	rec := httptest.NewRecorder()
	h.HandleDashboardState(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("state with cookie => %d want 200", rec.Code)
	}
}
```

- [ ] **Step 3: 실패 확인**

Run: `cd relay && go test ./ -run 'TestStateRequiresAuth|TestLoginThenState'`
Expected: FAIL — handlers undefined

- [ ] **Step 4: dashboard.go 구현**

```go
package main

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"time"
)

//go:embed dashboard_assets
var dashboardAssets embed.FS

// dashboardEnabled reports whether the operator dashboard is configured.
func (h *Hub) dashboardEnabled() bool { return h.cfg.DashboardToken != "" }

// authedDashboard checks the session cookie.
func (h *Hub) authedDashboard(r *http.Request) bool {
	c, err := r.Cookie(dashboardCookieName)
	if err != nil {
		return false
	}
	return verifySession(h.cfg.DashboardToken, c.Value, time.Now())
}

// HandleDashboardLogin authenticates the admin password (rate-limited, constant-time).
func (h *Hub) HandleDashboardLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip := clientIP(r.RemoteAddr)
	if !h.pinLimiter.allowed(ip) {
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(h.cfg.DashboardToken)) != 1 {
		h.pinLimiter.recordFailure(ip)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	h.pinLimiter.recordSuccess(ip)
	exp := time.Now().Add(dashboardSessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     dashboardCookieName,
		Value:    signSession(h.cfg.DashboardToken, exp),
		Path:     "/dashboard",
		Expires:  exp,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	w.WriteHeader(http.StatusOK)
}

// HandleDashboardLogout clears the session cookie.
func (h *Hub) HandleDashboardLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: dashboardCookieName, Value: "", Path: "/dashboard", MaxAge: -1})
	w.WriteHeader(http.StatusOK)
}

// HandleDashboardState returns the aggregated state JSON (cookie-gated).
func (h *Hub) HandleDashboardState(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(h.buildDashboardState())
}

// HandleDashboardStatic serves index.html (at /dashboard) and assets.
func (h *Hub) HandleDashboardStatic(w http.ResponseWriter, r *http.Request) {
	sub, _ := fs.Sub(dashboardAssets, "dashboard_assets")
	if r.URL.Path == "/dashboard" || r.URL.Path == "/dashboard/" {
		b, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
		return
	}
	// /dashboard/assets/...
	http.StripPrefix("/dashboard/", http.FileServer(http.FS(sub))).ServeHTTP(w, r)
}

// registerDashboard wires routes onto mux only when the dashboard is enabled.
func (h *Hub) registerDashboard(mux *http.ServeMux) {
	if !h.dashboardEnabled() {
		return
	}
	mux.HandleFunc("/dashboard/api/login", h.HandleDashboardLogin)
	mux.HandleFunc("/dashboard/api/logout", h.HandleDashboardLogout)
	mux.HandleFunc("/dashboard/api/state", h.HandleDashboardState)
	mux.HandleFunc("/dashboard", h.HandleDashboardStatic)
	mux.HandleFunc("/dashboard/", h.HandleDashboardStatic)
}
```

- [ ] **Step 5: 통과 확인**

Run: `cd relay && go test ./ -run 'TestStateRequiresAuth|TestLoginThenState'`
Expected: PASS

- [ ] **Step 6: 커밋**

```bash
git add relay/dashboard.go relay/dashboard_assets/index.html relay/dashboard_test.go
git commit -m "feat(relay): dashboard auth + state/static handlers"
```

---

## Task 6: main.go 라우트 등록 + 404(미설정) 테스트

**Files:**
- Modify: `relay/main.go`
- Test: `relay/dashboard_test.go`

- [ ] **Step 1: 실패 테스트 추가** (mux 통합 — 토큰 미설정 시 404)

```go
func TestDashboardDisabledWhenNoToken(t *testing.T) {
	h := NewHub(Config{}) // no DashboardToken
	mux := http.NewServeMux()
	h.registerDashboard(mux)
	req := httptest.NewRequest("GET", "/dashboard", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled dashboard => %d want 404", rec.Code)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `cd relay && go test ./ -run TestDashboardDisabledWhenNoToken`
Expected: PASS or FAIL — `registerDashboard`은 Task 5에서 정의됨. mux에 미등록이면 404 → 이미 PASS 예상. FAIL이면 registerDashboard 구현 확인.

- [ ] **Step 3: main.go에 등록 추가**

`runServer()`의 mux 핸들러 등록 블록(마지막 `mux.HandleFunc("/surv/", ...)` 다음)에 추가:

```go
	hub.registerDashboard(mux)
	if cfg.DashboardToken != "" {
		log.Printf("[relay] dashboard enabled at /dashboard")
	}
```

- [ ] **Step 4: 전체 빌드/테스트**

Run: `cd relay && go build ./... && go test ./`
Expected: PASS

- [ ] **Step 5: 커밋**

```bash
git add relay/main.go relay/dashboard_test.go
git commit -m "feat(relay): register dashboard routes when token set"
```

---

## Task 7: 프론트엔드 — 로그인 + 셸 (기능 스켈레톤)

**Files:**
- Modify: `relay/dashboard_assets/index.html` (실제 셸로 교체)
- Create: `relay/dashboard_assets/app.js`
- Create: `relay/dashboard_assets/style.css`

> 시각 디자인은 추후 claude.ai/design 산출물로 교체. 여기선 **문서화된 element id**로 동작만 구현한다.

- [ ] **Step 1: index.html (기능 셸)**

```html
<!doctype html>
<html lang="ko">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>OpsView Relay</title>
  <link rel="stylesheet" href="/dashboard/assets/style.css">
</head>
<body>
  <!-- 로그인 -->
  <div id="login" hidden>
    <form id="loginForm">
      <h1>OpsView Relay</h1>
      <input id="password" type="password" placeholder="비밀번호" autocomplete="current-password">
      <button type="submit">입장</button>
      <p id="loginError" class="err"></p>
    </form>
  </div>

  <!-- 대시보드 -->
  <div id="app" hidden>
    <header>
      <strong>OpsView Relay</strong>
      <nav>
        <button data-tab="status" class="tab active">상태</button>
        <button data-tab="live" class="tab">라이브</button>
      </nav>
      <button id="logout">로그아웃</button>
    </header>
    <div id="banner" class="banner" hidden>relay 연결 끊김</div>

    <section id="tab-status" class="tab-panel">
      <div class="cards">
        <div class="card"><span class="card-label">Publisher</span><span id="cPublisher" class="card-value">—</span></div>
        <div class="card"><span class="card-label">Watchers</span><span id="cWatchers" class="card-value">—</span></div>
        <div class="card"><span class="card-label">Streams</span><span id="cStreams" class="card-value">—</span></div>
        <div class="card"><span class="card-label">Throughput</span><span id="cThroughput" class="card-value">—</span></div>
      </div>
      <div class="panel"><h2>Publisher</h2><div id="pubPanel"></div></div>
      <div class="panel"><h2>Watchers</h2><table id="watcherTable"><thead><tr><th>ID</th><th>IP</th><th>접속</th></tr></thead><tbody></tbody></table></div>
      <div class="panel"><h2>Streams</h2><table id="streamTable"><thead><tr><th>채널</th><th>코덱</th><th>WS시청</th><th>상태</th></tr></thead><tbody></tbody></table></div>
    </section>

    <section id="tab-live" class="tab-panel" hidden>
      <div id="liveGrid" class="live-grid"></div>
    </section>
  </div>

  <script src="/dashboard/assets/app.js"></script>
</body>
</html>
```

- [ ] **Step 2: style.css (최소 기능 스타일 — 추후 디자인으로 교체)**

```css
:root { color-scheme: dark; }
body { margin: 0; font-family: system-ui, sans-serif; background: #0b1020; color: #e2e8f0; }
.mono, .card-value, td:nth-child(2) { font-family: ui-monospace, Menlo, monospace; }
header { display: flex; align-items: center; gap: 16px; padding: 10px 16px; background: #111830; }
header nav { display: flex; gap: 6px; }
.tab { background: transparent; color: #94a3b8; border: 0; padding: 8px 14px; cursor: pointer; border-radius: 6px; }
.tab.active { background: #1e293b; color: #e2e8f0; }
#logout { margin-left: auto; }
button { background: #22d3ee; color: #06202a; border: 0; padding: 8px 14px; border-radius: 6px; cursor: pointer; }
.banner { background: #7f1d1d; color: #fff; padding: 8px 16px; text-align: center; }
.cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px,1fr)); gap: 12px; padding: 16px; }
.card { background: #111830; border-radius: 10px; padding: 14px; }
.card-label { color: #94a3b8; font-size: 12px; display: block; }
.card-value { font-size: 22px; }
.panel { background: #111830; margin: 16px; border-radius: 10px; padding: 14px; }
table { width: 100%; border-collapse: collapse; }
th, td { text-align: left; padding: 6px 8px; border-bottom: 1px solid #1e293b; font-size: 14px; }
.live-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px,1fr)); gap: 6px; padding: 8px; }
.live-cell { position: relative; aspect-ratio: 16/9; background: #000; border-radius: 6px; overflow: hidden; }
.live-cell video { width: 100%; height: 100%; object-fit: cover; }
.live-cell .label { position: absolute; bottom: 4px; left: 6px; background: rgba(0,0,0,.5); padding: 2px 6px; border-radius: 4px; font-size: 12px; }
#login { display: grid; place-items: center; height: 100vh; }
#loginForm { background: #111830; padding: 28px; border-radius: 12px; display: grid; gap: 12px; width: 280px; }
#loginForm input { padding: 10px; border-radius: 6px; border: 1px solid #334155; background: #0b1020; color: #e2e8f0; }
.err { color: #f87171; font-size: 13px; min-height: 18px; margin: 0; }
[hidden] { display: none !important; }
```

- [ ] **Step 3: app.js — 로그인/셸/탭 (state 폴링·라이브는 Task 8·9에서 추가)**

```js
'use strict';

const $ = (id) => document.getElementById(id);

async function checkSession() {
  const res = await fetch('/dashboard/api/state', { method: 'GET' });
  return res.status === 200;
}

function showLogin() { $('login').hidden = false; $('app').hidden = true; }
function showApp() { $('login').hidden = true; $('app').hidden = false; startStatusPolling(); }

$('loginForm').addEventListener('submit', async (e) => {
  e.preventDefault();
  $('loginError').textContent = '';
  const res = await fetch('/dashboard/api/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password: $('password').value }),
  });
  if (res.status === 200) { showApp(); }
  else if (res.status === 429) { $('loginError').textContent = '잠시 후 다시 시도하세요'; }
  else { $('loginError').textContent = '비밀번호가 틀렸습니다'; }
});

$('logout').addEventListener('click', async () => {
  await fetch('/dashboard/api/logout', { method: 'POST' });
  stopStatusPolling(); stopLive();
  showLogin();
});

// 탭 전환
document.querySelectorAll('.tab').forEach((btn) => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.tab').forEach((b) => b.classList.remove('active'));
    btn.classList.add('active');
    const tab = btn.dataset.tab;
    $('tab-status').hidden = tab !== 'status';
    $('tab-live').hidden = tab !== 'live';
    if (tab === 'live') startLive(); else stopLive();
  });
});

// Task 8·9에서 정의되는 함수들의 no-op 기본값 (로드 순서 안전장치)
window.startStatusPolling = window.startStatusPolling || function () {};
window.stopStatusPolling = window.stopStatusPolling || function () {};
window.startLive = window.startLive || function () {};
window.stopLive = window.stopLive || function () {};

// 진입
checkSession().then((ok) => ok ? showApp() : showLogin());
```

- [ ] **Step 4: 수동 확인**

Run: `cd relay && RELAY_PUBLISHER_TOKEN=t RELAY_DASHBOARD_TOKEN=dash go run .`
브라우저에서 `http://localhost:8080/dashboard` → 로그인 화면 표시, 비번 `dash` 입력 → 빈 대시보드 셸 + 탭 전환 동작. 잘못된 비번 → 오류 메시지.

- [ ] **Step 5: 커밋**

```bash
git add relay/dashboard_assets/
git commit -m "feat(relay): dashboard login + shell (functional skeleton)"
```

---

## Task 8: 상태 탭 — state 폴링 + 렌더 + throughput

**Files:**
- Modify: `relay/dashboard_assets/app.js`

- [ ] **Step 1: app.js에 상태 폴링/렌더 추가** (파일 상단 `const $` 다음에 삽입; Task 7의 no-op 기본값 블록은 제거)

```js
let statusTimer = null;
let lastBytes = null; // { in, out, t }

function fmtKbps(deltaBytes, deltaMs) {
  if (deltaMs <= 0) return 0;
  return Math.round((deltaBytes * 8) / deltaMs); // bytes/ms*8 = kbps
}
function ago(iso) {
  if (!iso) return '—';
  const s = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), ss = s % 60;
  return (h ? h + ':' : '') + String(m).padStart(2, '0') + ':' + String(ss).padStart(2, '0');
}

function renderState(st) {
  $('banner').hidden = true;
  const r = st.relay;
  $('cPublisher').textContent = r.publisher_connected ? '● 연결됨' : '○ 끊김';
  $('cWatchers').textContent = st.watchers.count;
  $('cStreams').textContent = st.streams.filter((s) => s.active).length;

  const now = Date.now();
  if (lastBytes) {
    const dt = now - lastBytes.t;
    $('cThroughput').textContent =
      '↓' + fmtKbps(r.bytes_in - lastBytes.in, dt) + ' ↑' + fmtKbps(r.bytes_out - lastBytes.out, dt) + ' kbps';
  }
  lastBytes = { in: r.bytes_in, out: r.bytes_out, t: now };

  $('pubPanel').innerHTML =
    (r.publisher_connected ? '연결됨' : '끊김') +
    ' · PIN ' + (r.publisher_pin_set ? '설정됨' : '없음') +
    ' · v' + r.version + ' · up ' + Math.floor(r.uptime_sec / 60) + 'm' +
    ' · 마지막 프레임 ' + ago(r.last_publish_at);

  const wb = $('watcherTable').querySelector('tbody');
  wb.innerHTML = st.watchers.list.map((w) =>
    '<tr><td>' + w.id + '</td><td>' + w.ip + '</td><td>' + ago(w.since) + '</td></tr>').join('');

  const sb = $('streamTable').querySelector('tbody');
  sb.innerHTML = st.streams.map((s) =>
    '<tr><td>' + s.name + '</td><td>' + (s.codec || '—') + '</td><td>' + s.ws_watchers +
    '</td><td>' + (s.active ? '● live' : '○') + '</td></tr>').join('');
}

async function pollState() {
  try {
    const res = await fetch('/dashboard/api/state');
    if (res.status === 401) { stopStatusPolling(); stopLive(); showLogin(); return; }
    if (!res.ok) { $('banner').hidden = false; return; }
    renderState(await res.json());
  } catch (e) {
    $('banner').hidden = false; // relay 연결 끊김
  }
}

function startStatusPolling() {
  if (statusTimer) return;
  pollState();
  statusTimer = setInterval(() => { if (!document.hidden) pollState(); }, 2000);
}
function stopStatusPolling() { clearInterval(statusTimer); statusTimer = null; lastBytes = null; }
```

(Task 7의 `window.startStatusPolling = ... no-op` 4줄 중 status 관련 2줄 삭제.)

- [ ] **Step 2: 수동 확인**

relay 실행 + 에이전트/퍼블리셔 연결 상태에서 `/dashboard` 로그인 → 카드/테이블이 2초마다 갱신. relay 종료 → "연결 끊김" 배너.

- [ ] **Step 3: 커밋**

```bash
git add relay/dashboard_assets/app.js
git commit -m "feat(relay): dashboard status tab polling + render"
```

---

## Task 9: 라이브 탭 — 카메라 그리드 (WS 우선 + HLS 폴백)

**Files:**
- Modify: `relay/dashboard_assets/app.js`

> 플레이어 로직은 검증된 `web/viewer.js`의 sequence-mode MSE 플레이어를 이식한다.

- [ ] **Step 1: app.js에 라이브 그리드 추가**

```js
// --- 라이브 그리드 ---
let liveActive = false;
let liveWS = [];

function isIOS() {
  return /iPad|iPhone|iPod/.test(navigator.userAgent) ||
    (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
}
function wsUsable() { return ('MediaSource' in window) && !isIOS(); }
const hx2 = (n) => n.toString(16).padStart(2, '0');
function codecFromInit(d) {
  for (let i = 0; i + 8 < d.length; i++)
    if (d[i] === 0x61 && d[i + 1] === 0x76 && d[i + 2] === 0x63 && d[i + 3] === 0x43)
      return 'avc1.' + hx2(d[i + 5]) + hx2(d[i + 6]) + hx2(d[i + 7]);
  return null;
}
function playWS(video, wsUrl, onFail) {
  if (!wsUsable()) { onFail && onFail(); return null; }
  const ms = new MediaSource();
  let sb = null, ws = null, gotInit = false, failed = false;
  const q = [];
  const cleanup = () => { try { ws && ws.close(); } catch (e) {} try { if (ms.readyState === 'open') ms.endOfStream(); } catch (e) {} };
  const fail = () => { if (failed) return; failed = true; clearTimeout(timer); cleanup(); onFail && onFail(); };
  const flush = () => {
    if (!sb || sb.updating || !q.length) return;
    try { sb.appendBuffer(q.shift()); }
    catch (err) {
      if (err && err.name === 'QuotaExceededError') {
        try { if (sb.buffered.length) { const e = sb.buffered.end(sb.buffered.length - 1); if (e > 8) sb.remove(0, e - 4); } } catch (e) {}
      } else fail();
    }
  };
  video.src = URL.createObjectURL(ms);
  const timer = setTimeout(() => { if (!gotInit) fail(); }, 6000);
  ms.addEventListener('sourceopen', () => {
    try { ws = new WebSocket(wsUrl); } catch (e) { fail(); return; }
    ws.binaryType = 'arraybuffer';
    ws.onmessage = (e) => {
      if (failed) return;
      const data = new Uint8Array(e.data);
      if (!gotInit) {
        gotInit = true; clearTimeout(timer);
        const codec = codecFromInit(data);
        const mime = codec ? 'video/mp4; codecs="' + codec + '"' : '';
        if (!codec || !MediaSource.isTypeSupported(mime)) { fail(); return; }
        try { sb = ms.addSourceBuffer(mime); } catch (err) { fail(); return; }
        sb.mode = 'sequence';
        sb.addEventListener('updateend', () => { flush(); video.play().catch(() => {}); });
        sb.addEventListener('error', fail);
      }
      q.push(data); flush();
    };
    ws.onerror = fail;
    ws.onclose = () => { if (!gotInit) fail(); };
  });
  video.play().catch(() => {});
  return { close: cleanup };
}
function playHLS(video, hlsUrl) {
  if (video.canPlayType('application/vnd.apple.mpegurl')) { video.src = hlsUrl; video.play().catch(() => {}); }
  // (relay 대시보드는 hls.js 미번들 — 네이티브 HLS만. WS 미지원 브라우저용 폴백.)
}

async function startLive() {
  if (liveActive) return;
  liveActive = true;
  const res = await fetch('/dashboard/api/state');
  if (!res.ok) return;
  const st = await res.json();
  const grid = $('liveGrid');
  grid.innerHTML = '';
  st.streams.filter((s) => s.active).forEach((s) => {
    const cell = document.createElement('div'); cell.className = 'live-cell';
    const video = document.createElement('video'); video.muted = true; video.autoplay = true; video.playsInline = true;
    const label = document.createElement('div'); label.className = 'label'; label.textContent = s.name || s.id;
    cell.appendChild(video); cell.appendChild(label); grid.appendChild(cell);
    const wsUrl = (location.protocol === 'https:' ? 'wss' : 'ws') + '://' + location.host + '/surv/ws/' + s.id;
    const hlsUrl = location.origin + '/surv/' + s.id + '/index.m3u8';
    const p = playWS(video, wsUrl, () => playHLS(video, hlsUrl));
    if (p) liveWS.push(p);
  });
}
function stopLive() {
  liveActive = false;
  liveWS.forEach((p) => { try { p.close(); } catch (e) {} });
  liveWS = [];
  $('liveGrid').innerHTML = '';
}
```

(Task 7의 `window.startLive/stopLive = ... no-op` 2줄 삭제.)

- [ ] **Step 2: 수동 확인**

relay에 활성 CCTV 스트림이 있는 상태에서 `/dashboard` → 라이브 탭 → 전 채널 영상 재생. 탭을 상태로 전환 → 스트림 정리(`stopLive`). Safari/WKWebView·Chrome 모두 재생.

- [ ] **Step 3: 전체 빌드·테스트·커밋**

```bash
cd relay && go build ./... && go test ./
git add relay/dashboard_assets/app.js
git commit -m "feat(relay): dashboard live camera grid (WS + HLS fallback)"
```

---

## Self-Review (작성자 체크 완료)

- **스펙 커버리지**: 라우트/인증/state모델/상태탭/라이브탭/폴링/에러/테스트 — 각 스펙 항목이 Task 1~9에 매핑됨. 단, 스냅샷 썸네일(`/api/snapshot`)은 Task 8 pubPanel에서 텍스트로만 표시(이미지 삽입은 디자인 reskin 시 `<img src="/api/snapshot">` 추가 — 1줄, 별도 태스크 불필요).
- **Placeholder**: 없음(모든 코드 블록 실제 코드).
- **타입 일관성**: `dashboardState`/`relayInfo`/`StreamStat`/`WatcherInfo` 필드명과 JSON 태그가 프론트 렌더(`renderState`)의 접근 경로(`st.relay.bytes_in`, `s.ws_watchers` 등)와 일치.
- **proto 타입 확인 완료**: `DVRInfo.ID int64`, `ChannelInfo.DVRID int64` — Task 4 코드 반영됨.
