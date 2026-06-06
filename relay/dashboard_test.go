package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStateRequiresAuth(t *testing.T) {
	h := NewHub(cfgWithDash("dash-secret"))
	req := httptest.NewRequest("GET", "/dashboard/api/state", nil)
	rec := httptest.NewRecorder()
	h.HandleDashboardState(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie => %d want 401", rec.Code)
	}
}

func TestLoginThenState(t *testing.T) {
	h := NewHub(cfgWithDash("dash-secret"))

	bad := httptest.NewRequest("POST", "/dashboard/api/login", strings.NewReader(`{"password":"nope"}`))
	badRec := httptest.NewRecorder()
	h.HandleDashboardLogin(badRec, bad)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login => %d want 401", badRec.Code)
	}

	ok := httptest.NewRequest("POST", "/dashboard/api/login", strings.NewReader(`{"password":"dash-secret"}`))
	okRec := httptest.NewRecorder()
	h.HandleDashboardLogin(okRec, ok)
	if okRec.Code != http.StatusOK {
		t.Fatalf("good login => %d want 200", okRec.Code)
	}
	cookies := okRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login must set a cookie")
	}

	req := httptest.NewRequest("GET", "/dashboard/api/state", nil)
	req.AddCookie(cookies[0])
	rec := httptest.NewRecorder()
	h.HandleDashboardState(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("state with cookie => %d want 200", rec.Code)
	}
}

func TestDashboardDisabledWhenNoToken(t *testing.T) {
	h := NewHub(testConfig()) // no DashboardToken
	mux := http.NewServeMux()
	h.registerDashboard(mux)
	req := httptest.NewRequest("GET", "/dashboard", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled dashboard => %d want 404", rec.Code)
	}
}

// cfgWithDash returns a Config with a default registry and a dashboard token.
func cfgWithDash(dashToken string) Config {
	c := cfgWithToken("tok")
	c.DashboardToken = dashToken
	return c
}

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
	v := signSession("dash-secret", now.Add(-time.Second))
	if verifySession("dash-secret", v, now) {
		t.Fatal("expired session must not verify")
	}
}

func TestSessionTampered(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	v := signSession("dash-secret", now.Add(time.Hour))
	if verifySession("dash-secret", v+"x", now) {
		t.Fatal("tampered signature must not verify")
	}
	if verifySession("other-token", v, now) {
		t.Fatal("wrong token must not verify")
	}
}

func TestBuildDashboardState(t *testing.T) {
	h := NewHub(testConfig())
	st := h.buildDashboardState()
	if st.Relay.Version != relayVersion {
		t.Fatalf("version=%q want %q", st.Relay.Version, relayVersion)
	}
	// default session exists but is offline
	if st.Relay.AgentsOnline != 0 {
		t.Fatalf("agents_online=%d want 0", st.Relay.AgentsOnline)
	}
	if st.Agents == nil {
		t.Fatal("agents must be non-nil (JSON [])")
	}
	// default session is included
	var hasDefault bool
	for _, a := range st.Agents {
		if a.ID == "default" {
			hasDefault = true
			if a.Connected {
				t.Fatal("default agent should be offline")
			}
			if a.Streams == nil || a.Watchers == nil {
				t.Fatal("agent streams/watchers must be non-nil slices")
			}
		}
	}
	if !hasDefault {
		t.Fatal("default agent missing from state")
	}
}

func TestStreamPathNamespacing(t *testing.T) {
	if got := streamPath("default", "dvr1_ch1"); got != "dvr1_ch1" {
		t.Fatalf("default path=%q want dvr1_ch1", got)
	}
	if got := streamPath("gangnam", "dvr1_ch1"); got != "gangnam/dvr1_ch1" {
		t.Fatalf("named path=%q want gangnam/dvr1_ch1", got)
	}
}

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

func TestSessionWatcherList(t *testing.T) {
	s := newAgentSession("a", "A")
	w := &Watcher{id: 7, ip: "1.2.3.4:55", send: make(chan []byte, 1)}
	s.watchers[w] = struct{}{}
	list := s.watcherList()
	if len(list) != 1 || list[0].ID != 7 || list[0].IP != "1.2.3.4:55" {
		t.Fatalf("watcherList=%+v", list)
	}
}

func TestOpsSnapshotAuthAndOffline(t *testing.T) {
	h := NewHub(cfgWithDash("dash-secret"))
	// no cookie -> 401
	rec := httptest.NewRecorder()
	h.HandleDashboardOpsSnapshot(rec, httptest.NewRequest("GET", "/dashboard/api/ops-snapshot", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: want 401, got %d", rec.Code)
	}
	// authed but no online agent -> 204 (dashboard keeps its placeholder)
	req := httptest.NewRequest("GET", "/dashboard/api/ops-snapshot", nil)
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: signSession(h.effectiveDashToken(), time.Now().Add(time.Hour))})
	rec2 := httptest.NewRecorder()
	h.HandleDashboardOpsSnapshot(rec2, req)
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("offline: want 204, got %d", rec2.Code)
	}
}

func TestAgentControlAuthAndOffline(t *testing.T) {
	h := NewHub(cfgWithDash("dash-secret"))
	rec := httptest.NewRecorder()
	h.HandleDashboardAgentControl(rec, httptest.NewRequest("POST", "/dashboard/api/agent-control", strings.NewReader(`{"action":"reconnect"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: want 401, got %d", rec.Code)
	}
	req := httptest.NewRequest("POST", "/dashboard/api/agent-control", strings.NewReader(`{"action":"reconnect"}`))
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: signSession(h.effectiveDashToken(), time.Now().Add(time.Hour))})
	rec2 := httptest.NewRecorder()
	h.HandleDashboardAgentControl(rec2, req)
	if rec2.Code != http.StatusConflict { // authed but no online agent
		t.Fatalf("offline: want 409, got %d", rec2.Code)
	}
}

func TestIPLabelHandler(t *testing.T) {
	store, _ := openAgentStore(t.TempDir() + "/relay.db")
	t.Cleanup(func() { store.close() })
	cfg := cfgWithDash("dash-secret")
	cfg.Store = store
	h := NewHub(cfg)
	// unauthorized
	rec := httptest.NewRecorder()
	h.HandleDashboardIPLabel(rec, httptest.NewRequest("POST", "/dashboard/api/ip-label", strings.NewReader(`{"ip":"1.2.3.4","label":"x"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: want 401, got %d", rec.Code)
	}
	// authed -> stores
	req := httptest.NewRequest("POST", "/dashboard/api/ip-label", strings.NewReader(`{"ip":"1.2.3.4","label":"프론트"}`))
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: signSession(h.effectiveDashToken(), time.Now().Add(time.Hour))})
	rec2 := httptest.NewRecorder()
	h.HandleDashboardIPLabel(rec2, req)
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("authed: want 204, got %d", rec2.Code)
	}
	if h.getIPLabel("1.2.3.4") != "프론트" {
		t.Fatalf("label not stored: %q", h.getIPLabel("1.2.3.4"))
	}
}
