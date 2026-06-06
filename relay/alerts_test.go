package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebhookURLAllowed(t *testing.T) {
	cases := map[string]bool{
		"https://hooks.slack.com/services/x": true,
		"http://192.168.0.5/hook":            true,  // LAN ok
		"http://127.0.0.1/x":                 false, // loopback blocked
		"http://169.254.169.254/latest":      false, // cloud metadata blocked
		"ftp://example.com/y":                false, // non-http
		"notaurl":                            false,
		"":                                   false,
	}
	for u, want := range cases {
		if got := webhookURLAllowed(u); got != want {
			t.Errorf("%q: got %v want %v", u, got, want)
		}
	}
}

func TestAlertConfigActive(t *testing.T) {
	if (alertConfig{Enabled: true}).active() {
		t.Fatal("no channel => not active")
	}
	if !(alertConfig{Enabled: true, WebhookURL: "https://x.io/y"}).active() {
		t.Fatal("webhook => active")
	}
	if (alertConfig{Enabled: false, WebhookURL: "https://x.io/y"}).active() {
		t.Fatal("disabled => not active")
	}
	if !(alertConfig{Enabled: true, TelegramToken: "t", TelegramChat: "c"}).active() {
		t.Fatal("telegram => active")
	}
}

func TestAlertConfigPersists(t *testing.T) {
	path := t.TempDir() + "/relay.db"
	store, _ := openAgentStore(path)
	cfg := cfgWithDash("x")
	cfg.Store = store
	h := NewHub(cfg)
	if h.getAlertConfig().Enabled {
		t.Fatal("default should be disabled")
	}
	if err := h.setAlertConfig(alertConfig{Enabled: true, WebhookURL: "https://hooks.slack.com/a"}); err != nil {
		t.Fatal(err)
	}
	if !h.getAlertConfig().active() {
		t.Fatal("should be active after set")
	}
	store.close()
	// reopen from same path -> config survives
	store2, _ := openAgentStore(path)
	t.Cleanup(func() { store2.close() })
	cfg2 := cfgWithDash("x")
	cfg2.Store = store2
	if !NewHub(cfg2).getAlertConfig().active() {
		t.Fatal("alert config not persisted")
	}
}

func TestAlertHandlersAuth(t *testing.T) {
	h := NewHub(cfgWithDash("x"))
	rec := httptest.NewRecorder()
	h.HandleDashboardAlertConfig(rec, httptest.NewRequest("GET", "/dashboard/api/alert-config", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("config GET no cookie: want 401, got %d", rec.Code)
	}
	rec2 := httptest.NewRecorder()
	h.HandleDashboardAlertTest(rec2, httptest.NewRequest("POST", "/dashboard/api/alert-test", nil))
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("test no cookie: want 401, got %d", rec2.Code)
	}
	// authed but no channel configured -> 409
	req := httptest.NewRequest("POST", "/dashboard/api/alert-test", nil)
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: signSession(h.effectiveDashToken(), time.Now().Add(time.Hour))})
	rec3 := httptest.NewRecorder()
	h.HandleDashboardAlertTest(rec3, req)
	if rec3.Code != http.StatusConflict {
		t.Fatalf("test no channel: want 409, got %d", rec3.Code)
	}
}
