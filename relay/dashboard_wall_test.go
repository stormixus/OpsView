package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWallPageRedirectsUnauthedWithNext(t *testing.T) {
	h := NewHub(cfgWithDash("dash-secret"))
	rec := httptest.NewRecorder()
	h.HandleWallPage(rec, httptest.NewRequest("GET", "/dashboard/wall", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("unauthed: want 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard?next=%2Fdashboard%2Fwall" {
		t.Fatalf("unauthed Location = %q, want /dashboard?next=%%2Fdashboard%%2Fwall", loc)
	}
}

func TestWallPageServesWhenAuthed(t *testing.T) {
	h := NewHub(cfgWithDash("dash-secret"))
	req := httptest.NewRequest("GET", "/dashboard/wall", nil)
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: signSession(h.effectiveDashToken(), time.Now().Add(time.Hour))})
	rec := httptest.NewRecorder()
	h.HandleWallPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed: want 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("authed Content-Type = %q", ct)
	}
}
