package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/opsview/opsview/relay/lpr"
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

// TestLPRIntegration verifies that storeEventThumb triggers LPR asynchronously
// and updates the open event plate.
func TestLPRIntegration(t *testing.T) {
	recDir := t.TempDir()
	h := NewHub(cfgWithDash("dash-secret"))
	h.rec = &Recorder{dir: recDir}
	h.events = newEventStore(recDir)
	h.lpr = lpr.FuncRecognizer(func([]byte) (lpr.Result, error) {
		return lpr.Result{Plate: "8171", Confidence: 0.96}, nil
	})

	stream := "dvr1_ch2"
	tsMs := int64(1717843200123)

	// 4. Create an open event
	h.events.add(stream, "motion", true, tsMs)

	// Verify open event exists
	h.events.mu.Lock()
	if _, ok := h.events.open[openKey{stream, "motion"}]; !ok {
		h.events.mu.Unlock()
		t.Fatalf("expected open event to exist")
	}
	h.events.mu.Unlock()

	// 5. Trigger storeEventThumb (calls LPR in goroutine)
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0xFF, 0xD9}
	h.storeEventThumb(stream, tsMs, jpeg)

	// 6. Wait for goroutine to finish (with timeout)
	start := time.Now()
	for {
		h.events.mu.Lock()
		cur, ok := h.events.open[openKey{stream, "motion"}]
		h.events.mu.Unlock()

		if ok && cur.plate == "8171" {
			break // Success!
		}

		if time.Since(start) > 200*time.Millisecond {
			t.Fatalf("timed out waiting for LPR update. got plate: %q", cur.plate)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 7. Close the event and verify the plate is stored in the closed eventInterval
	h.events.add(stream, "motion", false, tsMs+5000)

	day := dayKeyFromMs(tsMs)
	closedEvents := h.events.eventsForDay(stream, day)
	if len(closedEvents) != 1 {
		t.Fatalf("expected 1 closed event, got %d", len(closedEvents))
	}
	if closedEvents[0].Plate != "8171" {
		t.Fatalf("expected plate '8171' in closed event, got %q", closedEvents[0].Plate)
	}
}

// TestNearestEvThumb verifies the windowed nearest-snapshot fallback used when an
// event has no exact thumb and no live segment (throttled capture + pruned video).
func TestNearestEvThumb(t *testing.T) {
	recDir := t.TempDir()
	stream := "agentX/dvr1_ch1"
	dir := filepath.Join(recDir, filepath.FromSlash(stream), evThumbDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, sec := range []int64{1000, 1050, 1200} {
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatInt(sec, 10)+".jpg"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// exact hit
	if got := nearestEvThumb(recDir, stream, 1050, 60); got != filepath.Join(dir, "1050.jpg") {
		t.Errorf("t=1050: got %q, want 1050.jpg", got)
	}
	// nearest within window: 1030 -> 1050 (20s) beats 1000 (30s)
	if got := nearestEvThumb(recDir, stream, 1030, 60); got != filepath.Join(dir, "1050.jpg") {
		t.Errorf("t=1030: got %q, want 1050.jpg", got)
	}
	// outside window (nearest is 1200, 100s away) -> none
	if got := nearestEvThumb(recDir, stream, 1300, 60); got != "" {
		t.Errorf("t=1300: got %q, want \"\" (outside window)", got)
	}
	// missing stream dir -> none, no panic
	if got := nearestEvThumb(recDir, "nope/dvr9_ch9", 1000, 60); got != "" {
		t.Errorf("missing dir: got %q, want \"\"", got)
	}
}

func thumbRequest(h *Hub, etag string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/dashboard/api/rec-thumb?stream=cam&t=1000", nil)
	req.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: signSession(h.effectiveDashToken(), time.Now().Add(time.Hour))})
	req.Header.Set("If-None-Match", etag)
	w := httptest.NewRecorder()
	h.HandleDashboardRecThumb(w, req)
	return w
}

func TestStoredThumbCacheReplacementIsolationAndRevalidation(t *testing.T) {
	h := NewHub(cfgWithDash("secret"))
	h.rec = &Recorder{dir: t.TempDir()}
	h.storeEventThumb("cam", 1_000_000, []byte("first"))
	w := thumbRequest(h, "")
	if w.Code != 200 || w.Body.String() != "first" {
		t.Fatal(w.Code, w.Body.String())
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	if w := thumbRequest(h, etag); w.Code != 304 || w.Body.Len() != 0 {
		t.Fatal("unchanged thumbnail must return 304", w.Code)
	}
	path := evThumbPath(h.rec.dir, "cam", 1000)
	info, _ := os.Stat(path)
	// Same length and timestamp: atomic file identity must invalidate the cache.
	tmp := path + ".replacement"
	os.WriteFile(tmp, []byte("other"), 0600)
	os.Chtimes(tmp, info.ModTime(), info.ModTime())
	os.Rename(tmp, path)
	w = thumbRequest(h, etag)
	if w.Code != 200 || w.Body.String() != "other" || w.Header().Get("ETag") == etag {
		t.Fatal("stale replacement", w.Code, w.Body.String())
	}
	other := NewHub(cfgWithDash("secret"))
	other.rec = &Recorder{dir: t.TempDir()}
	other.storeEventThumb("cam", 1_000_000, []byte("isolated"))
	if w := thumbRequest(other, ""); w.Body.String() != "isolated" {
		t.Fatal("cache crossed recording roots")
	}
	unauth := httptest.NewRecorder()
	h.HandleDashboardRecThumb(unauth, httptest.NewRequest("GET", "/dashboard/api/rec-thumb?stream=cam&t=1000", nil))
	if unauth.Code != 401 {
		t.Fatal("warm cache bypassed auth", unauth.Code)
	}
	os.Remove(path)
	if got := h.storedThumb(path); got != nil {
		t.Fatal("deleted file served from memory")
	}
}

func TestThumbnailCacheBudgetsAndLRU(t *testing.T) {
	var c thumbnailCache
	img := make([]byte, maxEventThumbBytes)
	for n := 0; n < 20; n++ {
		c.put(strconv.Itoa(n), img, nil)
	}
	if c.bytes > thumbCacheBytes || c.get("0", nil) != nil {
		t.Fatal("byte budget did not evict oldest")
	}
	if c.get("4", nil) == nil {
		t.Fatal("expected oldest remaining entry")
	}
	c.put("next", img, nil)
	if c.get("5", nil) != nil || c.get("4", nil) == nil {
		t.Fatal("cache did not respect LRU access")
	}
	var small thumbnailCache
	for n := 0; n < 600; n++ {
		small.put(strconv.Itoa(n), []byte("x"), nil)
	}
	if len(small.entries) != thumbCacheCap || small.bytes != thumbCacheCap {
		t.Fatal("entry cap exceeded")
	}
	small.put("oversized", make([]byte, maxEventThumbBytes+1), nil)
	if small.get("oversized", nil) != nil {
		t.Fatal("oversized cache entry accepted")
	}
}

func TestConcurrentStoredThumbReplace(t *testing.T) {
	h := NewHub(cfgWithDash("secret"))
	h.rec = &Recorder{dir: t.TempDir()}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				h.storeEventThumb("cam", 1_000_000, []byte(strconv.Itoa(n)))
				w := thumbRequest(h, "")
				if w.Code != 200 || w.Body.Len() != 1 {
					t.Errorf("partial thumb: %d %q", w.Code, w.Body.String())
					return
				}
			}
		}(n)
	}
	wg.Wait()
}
