package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	thumbTimeout  = 10 * time.Second
	thumbCacheCap = 512

	// evThumbNearWindowSec bounds how far rec-thumb will reach for a substitute
	// event snapshot when the requested second has no exact thumb and no live
	// segment (capture is throttled, and old events' video is pruned while the
	// thumbs are kept). Tighter than the event cluster gap so we don't grab a
	// neighbouring event's snapshot.
	evThumbNearWindowSec = 60

	// evThumbDir is the hidden per-stream subdir holding pre-stored event-start
	// JPEG snapshots. Hidden so the .mp4-only recordings listing ignores it.
	evThumbDir = ".evthumbs"

	// maxEventThumbBytes caps an accepted event-thumb JPEG so a malformed/oversized
	// payload can't fill the disk.
	maxEventThumbBytes = 2 << 20 // 2 MiB
)

// recRootDir returns the recordings root directory the event store / recorder use
// (RELAY_REC_DIR), or "" if recording is unconfigured.
func (h *Hub) recRootDir() string {
	if h.rec != nil && h.rec.dir != "" {
		return h.rec.dir
	}
	if h.events != nil && h.events.recDir != "" {
		return h.events.recDir
	}
	return os.Getenv("RELAY_REC_DIR")
}

// evThumbPath is <recDir>/<stream>/.evthumbs/<unixSec>.jpg — the pre-stored
// thumbnail for an event whose edge fired at unixSec. tSec is the SurvEvent edge
// TS (ms) divided by 1000, so rec-thumb?stream=&t=<event.start> finds it.
func evThumbPath(recDir, stream string, tSec int64) string {
	return filepath.Join(recDir, filepath.FromSlash(stream), evThumbDir, strconv.FormatInt(tSec, 10)+".jpg")
}

// nearestEvThumb returns the .evthumbs file whose unix-second name is closest to t
// within +/- windowSec, or "" if none. Lets rec-thumb still render a timeline
// event whose exact edge had no stored thumb (throttled capture) and whose video
// was pruned (thumbs outlive segments under event-differentiated retention).
func nearestEvThumb(recDir, stream string, t, windowSec int64) string {
	dir := filepath.Join(recDir, filepath.FromSlash(stream), evThumbDir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var best string
	bestDiff := windowSec + 1
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".jpg") {
			continue
		}
		sec, err := strconv.ParseInt(strings.TrimSuffix(name, ".jpg"), 10, 64)
		if err != nil {
			continue
		}
		d := sec - t
		if d < 0 {
			d = -d
		}
		if d <= windowSec && d < bestDiff {
			bestDiff = d
			best = filepath.Join(dir, name)
		}
	}
	return best
}

// storeEventThumb writes a pre-captured event JPEG to disk at the path
// rec-thumb later serves. tsMs is the SurvEvent edge timestamp (unix ms); the
// file is keyed by tsMs/1000 to match rec-thumb's ?t=<unix_sec>. Best-effort:
// no-ops if recording is unconfigured or the JPEG is empty/oversized.
func (h *Hub) storeEventThumb(stream string, tsMs int64, jpeg []byte) {
	if stream == "" || tsMs <= 0 || len(jpeg) == 0 {
		return
	}
	if len(jpeg) > maxEventThumbBytes {
		log.Printf("[rec-thumb] %s @%d: dropped oversized event thumb (%d bytes)", stream, tsMs/1000, len(jpeg))
		return
	}

	// Trigger License Plate Recognition asynchronously
	go h.runLPR(stream, tsMs, jpeg)

	recDir := h.recRootDir()
	if recDir == "" {
		return
	}
	dst := evThumbPath(recDir, stream, tsMs/1000)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		log.Printf("[rec-thumb] %s: mkdir evthumbs: %v", stream, err)
		return
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, jpeg, 0o644); err != nil {
		log.Printf("[rec-thumb] %s: write event thumb: %v", stream, err)
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		log.Printf("[rec-thumb] %s: rename event thumb: %v", stream, err)
	}
}

// runLPR runs in-process ONNX plate recognition when configured (RELAY_LPR=1).
func (h *Hub) runLPR(stream string, tsMs int64, jpeg []byte) {
	rec := h.lpr
	if rec == nil {
		return
	}
	res, err := rec.Recognize(jpeg)
	if err != nil {
		log.Printf("[lpr] recognize: %v", err)
		return
	}
	if res.Plate == "" {
		return
	}

	// Keep only the last 4 digits of the recognized plate number
	plate4 := extractLast4Digits(res.Plate)
	if plate4 == "" {
		log.Printf("[lpr] recognized plate %q, but could not extract last 4 digits", res.Plate)
		return
	}

	log.Printf("[lpr] recognized plate: %s (last 4: %s, confidence: %.2f) for stream: %s", res.Plate, plate4, res.Confidence, stream)
	if h.events != nil {
		h.events.updateOpenPlate(stream, tsMs, plate4)
	}
}

// extractLast4Digits filters non-digits and returns the last 4 digits of the string.
func extractLast4Digits(s string) string {
	var digits []rune
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
		}
	}
	if len(digits) < 4 {
		return ""
	}
	return string(digits[len(digits)-4:])
}

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

	// Prefer a pre-stored event-start snapshot (instant, no ffmpeg, no recording
	// dependency). The agent ships these on event edges; rec-thumb?t=<event.start>
	// finds <recDir>/<stream>/.evthumbs/<t>.jpg. Fall through to ffmpeg only when
	// there's no stored thumb (scrub/other times).
	if recDir := h.recRootDir(); recDir != "" {
		if img, err := os.ReadFile(evThumbPath(recDir, stream, t)); err == nil && len(img) > 0 {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
			w.Write(img)
			return
		}
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
		// No live segment at t (old event, video pruned). Serve the nearest stored
		// event snapshot so the timeline thumbnail still renders.
		if recDir := h.recRootDir(); recDir != "" {
			if p := nearestEvThumb(recDir, stream, t, evThumbNearWindowSec); p != "" {
				if img, err := os.ReadFile(p); err == nil && len(img) > 0 {
					w.Header().Set("Content-Type", "image/jpeg")
					w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
					w.Write(img)
					return
				}
			}
		}
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
