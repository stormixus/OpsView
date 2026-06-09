package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestStoredEventThumbServedDirectly verifies that storeEventThumb writes a JPEG
// to <recDir>/<stream>/.evthumbs/<t>.jpg and that HandleDashboardRecThumb serves
// it directly (no ffmpeg, no recording segment lookup). ChID/TS match the
// SurvEvent edge so rec-thumb?stream=&t=<event.start> finds the stored thumb.
func TestStoredEventThumbServedDirectly(t *testing.T) {
	recDir := t.TempDir()
	h := NewHub(cfgWithDash("dash-secret"))
	h.rec = &Recorder{dir: recDir}

	stream := "dvr1_ch2"
	// A SurvEvent edge fires at this unix-ms; the agent ships the thumb with the
	// SAME ts, and the dashboard later requests rec-thumb?t=<event.start (sec)>.
	tsMs := int64(1717843200123)
	tSec := tsMs / 1000
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0xFF, 0xD9}

	h.storeEventThumb(stream, tsMs, jpeg)

	// Stored at the exact path rec-thumb reads.
	want := filepath.Join(recDir, stream, ".evthumbs", "1717843200.jpg")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected stored thumb at %s: %v", want, err)
	}

	mux := http.NewServeMux()
	h.registerDashboard(mux)

	req := httptest.NewRequest("GET", "/dashboard/api/rec-thumb?stream="+stream+"&t="+strconv.FormatInt(tSec, 10), nil)
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: signSession(h.effectiveDashToken(), time.Now().Add(time.Hour))})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("rec-thumb => %d want 200 (stored thumb should serve without ffmpeg)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content-type = %q want image/jpeg", ct)
	}
	if got := rec.Body.Bytes(); string(got) != string(jpeg) {
		t.Fatalf("served body did not match stored JPEG (%d vs %d bytes)", len(got), len(jpeg))
	}
}

// TestStoreEventThumbRejectsOversized confirms an oversized payload is dropped
// (not written to disk).
func TestStoreEventThumbRejectsOversized(t *testing.T) {
	recDir := t.TempDir()
	h := NewHub(cfgWithDash("dash-secret"))
	h.rec = &Recorder{dir: recDir}

	big := make([]byte, maxEventThumbBytes+1)
	h.storeEventThumb("dvr1_ch1", 1717843200000, big)

	if _, err := os.Stat(filepath.Join(recDir, "dvr1_ch1", ".evthumbs", "1717843200.jpg")); !os.IsNotExist(err) {
		t.Fatalf("oversized thumb should not be written; stat err=%v", err)
	}
}
