package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

const (
	thumbTimeout  = 10 * time.Second
	thumbCacheCap = 512
)

var (
	thumbCache   = make(map[string][]byte)
	thumbCacheMu sync.Mutex
)

// HandleDashboardRecThumb extracts a JPEG thumbnail from a recording at a given
// unix timestamp. Admin-gated. ?stream=<path>&t=<unix_sec>.
func (h *Hub) HandleDashboardRecThumb(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.rec == nil {
		http.Error(w, "recording disabled", http.StatusConflict)
		return
	}
	q := r.URL.Query()
	stream := q.Get("stream")
	t, _ := strconv.ParseInt(q.Get("t"), 10, 64)
	if t <= 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// check cache
	cacheKey := fmt.Sprintf("%s|%d", stream, t)
	thumbCacheMu.Lock()
	if img, ok := thumbCache[cacheKey]; ok {
		thumbCacheMu.Unlock()
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		w.Write(img)
		return
	}
	thumbCacheMu.Unlock()

	// find the segment containing t
	segs := h.rec.segmentsForExport(stream, t, t+1)
	var s recSegment
	var found bool
	for _, seg := range segs {
		if seg.Start <= t && t < seg.Start+int64(seg.Dur) {
			s = seg
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	path, ok := h.rec.segmentFile(stream, s.Name)
	if !ok {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// extract frame via ffmpeg
	offset := t - s.Start
	ctx, cancel := context.WithTimeout(r.Context(), thumbTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-ss", strconv.FormatInt(offset, 10), "-i", path,
		"-frames:v", "1", "-vf", "scale=320:-1", "-q:v", "6", "-f", "mjpeg", "pipe:1")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil || out.Len() == 0 {
		// The currently-recording segment has no finalized moov yet, so ffmpeg can't
		// read it — expected for events in the active (last ≤ segment-length) window.
		// Report "no thumbnail" (not an error) so the dashboard shows a clean
		// placeholder; it resolves once the segment closes and the day is reloaded.
		log.Printf("[rec-thumb] %s @%d: extract failed (likely active segment): %v", stream, t, err)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	img := out.Bytes()

	// store in cache (bounded: reset map when it grows past the cap)
	thumbCacheMu.Lock()
	if len(thumbCache) >= thumbCacheCap {
		thumbCache = make(map[string][]byte)
	}
	thumbCache[cacheKey] = img
	thumbCacheMu.Unlock()

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Write(img)
}
