package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleRootRedirectsToDashboard(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handleRoot(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTemporaryRedirect)
	}
	if got := rec.Header().Get("Location"); got != "/dashboard" {
		t.Fatalf("Location = %q, want /dashboard", got)
	}
}

func TestHandleRootKeepsUnknownPathsNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	handleRoot(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
