package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsLoopbackHost(t *testing.T) {
	ok := []string{"127.0.0.1", "127.0.0.1:54321", "localhost", "localhost:8080", "::1", "[::1]:8080"}
	for _, h := range ok {
		if !isLoopbackHost(h) {
			t.Errorf("host %q should be loopback", h)
		}
	}
	bad := []string{"evil.com", "evil.com:80", "10.0.0.5", "192.168.1.1:8080", "8.8.8.8"}
	for _, h := range bad {
		if isLoopbackHost(h) {
			t.Errorf("host %q should be rejected", h)
		}
	}
}

func TestLocalOnlyRejectsNonLoopbackHost(t *testing.T) {
	h := localOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Loopback Host -> passes through.
	r := httptest.NewRequest(http.MethodPost, "/api/surv/reset-db", nil)
	r.Host = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback host: got %d, want 200", rec.Code)
	}

	// Rebinding Host -> 403.
	r = httptest.NewRequest(http.MethodPost, "/api/surv/reset-db", nil)
	r.Host = "attacker.example.com"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("rebinding host: got %d, want 403", rec.Code)
	}
}

func TestAgentStopIsIdempotent(t *testing.T) {
	a := NewAgent(AgentConfig{Profile: 1080})
	a.Stop()
	a.Stop() // must not panic (double close of stopped channel)
}
