package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Recorder is a relay-side NVR: per active surveillance stream it supervises an
// ffmpeg that reads the relay's OWN HLS output (so the DVR is not pulled a second
// time) and copies it to segmented MP4 files on disk. A janitor enforces a disk
// cap by deleting the oldest segments. Recording is opt-in (RELAY_REC_DIR set).
type Recorder struct {
	dir      string // root recordings directory (RELAY_REC_DIR)
	selfHLS  string // base URL of the relay's own HLS, e.g. http://127.0.0.1:8080
	capBytes int64  // disk cap; 0 = unlimited
	segSecs  int    // segment length in seconds

	hub  *Hub
	mu   sync.Mutex
	recs map[string]*recProc // stream path -> running recorder
	stop chan struct{}

	idxMu    sync.Mutex               // guards the segment/day index caches
	segCache map[string]segCacheEntry // "stream|day" -> cached segments (keyed by dir mtime)
	dayCache map[string]dayCacheEntry // "stream" -> cached day list (keyed by dir mtime)
}

type recProc struct {
	cancel context.CancelFunc
	src    string
}

const (
	recReconcile            = 15 * time.Second
	recJanitorTick          = 2 * time.Minute
	recSegSeconds           = 300 // 5-minute segments
	recKeepAllHoursDefault  = 72
	recKeepEventDaysDefault = 30
)

type janSeg struct {
	path     string
	size     int64
	startSec int64
	durSec   int64
	event    bool
}

type janPolicy struct {
	keepAllCutoff   int64 // segments with startSec >= this are never deleted
	keepEventCutoff int64 // event segments with startSec < this may be deleted (tier 3)
}

// newRecorder builds a Recorder from env. Returns nil (recording disabled) when
// RELAY_REC_DIR is unset.
func newRecorder(h *Hub, relayPort string) *Recorder {
	dir := strings.TrimSpace(os.Getenv("RELAY_REC_DIR"))
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[rec] cannot use RELAY_REC_DIR %s: %v — recording disabled", dir, err)
		return nil
	}
	return &Recorder{
		dir:      dir,
		selfHLS:  "http://127.0.0.1:" + relayPort,
		capBytes: parseSizeBytes(os.Getenv("RELAY_REC_MAX")), // e.g. "2TB", "500GB"; 0 = unlimited
		segSecs:  recSegSeconds,
		hub:      h,
		recs:     make(map[string]*recProc),
		stop:     make(chan struct{}),
		segCache: make(map[string]segCacheEntry),
		dayCache: make(map[string]dayCacheEntry),
	}
}

// Run reconciles recorders against the set of active streams and runs the janitor
// until the relay stops.
func (r *Recorder) Run() {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		log.Printf("[rec] ffmpeg not found — recording disabled")
		return
	}
	log.Printf("[rec] recording to %s (cap=%s, %ds segments)", r.dir, fmtBytesCap(r.capBytes), r.segSecs)
	recTicker := time.NewTicker(recReconcile)
	janTicker := time.NewTicker(recJanitorTick)
	defer recTicker.Stop()
	defer janTicker.Stop()
	r.reconcile()
	for {
		select {
		case <-r.hub.done:
			r.stopAll()
			return
		case <-recTicker.C:
			r.reconcile()
		case <-janTicker.C:
			r.runJanitor()
			r.pruneEventThumbs()
		}
	}
}

// recordTargets maps each channel's recording OUTPUT path to the stream id to
// record FROM. A channel with a "<id>@main" stream records the main stream into
// its base dir and is NOT also recorded from its sub; other channels record their
// own (sub) stream. Operates on full streamPath()s (agent prefix preserved).
func recordTargets(active []string) map[string]string {
	has := map[string]bool{}
	for _, id := range active {
		has[id] = true
	}
	out := map[string]string{}
	for _, id := range active {
		seg := id
		if i := strings.LastIndex(id, "/"); i >= 0 {
			seg = id[i+1:]
		}
		if strings.HasPrefix(seg, "wall") {
			continue // live-only mosaic composite: no HLS to record from, and redundant
		}
		if isMainStreamID(id) {
			out[baseStreamID(id)] = id
			continue
		}
		if !has[mainStreamID(id)] {
			out[id] = id
		}
	}
	return out
}

// reconcile starts a recorder for every active stream and stops recorders for
// streams that have gone away.
func (r *Recorder) reconcile() {
	var active []string
	for _, s := range r.hub.allSessions() {
		for _, st := range s.survProxy.StreamStats() {
			// The live-wall mosaics ("wall", "walldvr<N>") are WS-only composites with
			// no HLS muxer, so they must never be recorded (recording reads a stream's
			// self-HLS, which 503s for a wall and loops ffmpeg forever). They're also
			// redundant — every channel they tile is already recorded on its own.
			if st.Active && !strings.HasPrefix(st.ID, "wall") {
				active = append(active, streamPath(s.id, st.ID))
			}
		}
	}
	want := recordTargets(active) // outputBase -> sourceID
	r.mu.Lock()
	defer r.mu.Unlock()
	for out, src := range want {
		if cur, ok := r.recs[out]; !ok {
			r.startLocked(out, src)
		} else if cur.src != src {
			cur.cancel() // source flipped (sub<->main); restart against the new one
			r.startLocked(out, src)
		}
	}
	for out, p := range r.recs {
		if _, ok := want[out]; !ok {
			p.cancel()
			delete(r.recs, out)
		}
	}
}

// startLocked launches a supervised ffmpeg recorder: records FROM sourcePath's
// self-HLS INTO outputPath's directory (so a main stream lands in the channel dir).
func (r *Recorder) startLocked(outputPath, sourcePath string) {
	ctx, cancel := context.WithCancel(context.Background())
	r.recs[outputPath] = &recProc{cancel: cancel, src: sourcePath}
	outDir := filepath.Join(r.dir, filepath.FromSlash(outputPath))
	hlsURL := r.selfHLS + "/surv/" + sourcePath + "/index.m3u8"
	go r.supervise(ctx, outputPath, hlsURL, outDir)
	log.Printf("[rec] recording %s (source %s) -> %s", outputPath, sourcePath, outDir)
}

// supervise runs ffmpeg and restarts it (with backoff) until ctx is cancelled.
func (r *Recorder) supervise(ctx context.Context, path, hlsURL, outDir string) {
	backoff := 3 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			log.Printf("[rec] %s: mkdir: %v", path, err)
		}
		// %Y%m%d_%H%M%S in the filename keeps segments time-sortable per channel.
		pattern := filepath.Join(outDir, "%Y%m%d_%H%M%S.mp4")
		cmd := exec.CommandContext(ctx, "ffmpeg",
			"-hide_banner", "-loglevel", "error",
			"-rw_timeout", "15000000", // 15s I/O timeout so a dead HLS doesn't hang
			"-i", hlsURL,
			"-c", "copy", "-an",
			"-f", "segment",
			"-segment_time", fmt.Sprintf("%d", r.segSecs),
			"-segment_format", "mp4",
			"-segment_format_options", "movflags=+faststart",
			"-reset_timestamps", "1",
			"-strftime", "1",
			pattern,
		)
		err := cmd.Run()
		if ctx.Err() != nil {
			return // cancelled
		}
		log.Printf("[rec] %s: ffmpeg exited (%v) — restarting in %s", path, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (r *Recorder) stopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for path, p := range r.recs {
		p.cancel()
		delete(r.recs, path)
	}
}

// janitorDeleteOrder returns, in deletion order, the segments to remove to get
// total bytes back under cap, honoring: (1) keep-all window never deleted,
// (2) non-event old deleted first, (3) old event segments next, (4) global
// oldest-first fallback. Pure for testing.
func janitorDeleteOrder(segs []janSeg, cap, total int64, p janPolicy) []janSeg {
	if cap <= 0 || total <= cap {
		return nil
	}
	// Deletion tiers, sacrificed in order: (1) non-event old, (3) old event past
	// keep-event, (2) event within keep-event, then (last resort) keep-all recent
	// footage. The disk cap is a HARD ceiling — we prefer to keep recent and
	// event-tagged footage, but if sacrificing everything else still leaves us over
	// cap we evict even keep-all segments (oldest-first) so the cap is honored.
	var tier1, tier2, tier3, keepAll []janSeg
	for _, s := range segs {
		switch {
		case s.startSec >= p.keepAllCutoff:
			keepAll = append(keepAll, s) // prefer to keep; deletable only as last resort
		case !s.event:
			tier1 = append(tier1, s)
		case s.startSec < p.keepEventCutoff:
			tier3 = append(tier3, s)
		default:
			tier2 = append(tier2, s) // event, within keep-event window
		}
	}
	sortByStart(tier1)
	sortByStart(tier2)
	sortByStart(tier3)
	sortByStart(keepAll)

	var out []janSeg
	freed := int64(0)
	for _, group := range [][]janSeg{tier1, tier3, tier2, keepAll} {
		for _, s := range group {
			if total-freed <= cap {
				return out
			}
			out = append(out, s)
			freed += s.size
		}
	}
	return out
}

func sortByStart(a []janSeg) {
	sort.Slice(a, func(i, j int) bool { return a[i].startSec < a[j].startSec })
}

// runJanitor enforces the disk cap by deleting segments, preferring to keep recent
// footage and event-tagged segments while still honoring the cap.
func (r *Recorder) runJanitor() {
	if r.capBytes <= 0 {
		return
	}
	// Build janSeg list with event tags
	var segs []janSeg
	var total int64
	filepath.WalkDir(r.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".events" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".mp4") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		// Parse startSec from filename via recNameLayout; files that don't match
		// (e.g., test files) default to epoch 0 so they're deleted first (oldest)
		name := strings.TrimSuffix(filepath.Base(p), ".mp4")
		startSec := int64(0)
		if t, err := time.ParseInLocation(recNameLayout, name, time.Local); err == nil {
			startSec = t.Unix()
		}
		// Derive stream from path relative to r.dir
		rel, _ := filepath.Rel(r.dir, filepath.Dir(p))
		stream := filepath.ToSlash(rel)
		// Compute durSec
		durSec := int64(r.segSecs)
		if durSec <= 0 {
			durSec = int64(recSegSeconds)
		}
		// Check if this segment overlaps any event
		event := false
		if r.hub != nil && r.hub.events != nil {
			event = r.hub.events.overlaps(stream, startSec, startSec+durSec)
		}
		segs = append(segs, janSeg{
			path:     p,
			size:     info.Size(),
			startSec: startSec,
			durSec:   durSec,
			event:    event,
		})
		total += info.Size()
		return nil
	})
	if total <= r.capBytes {
		return
	}
	// Compute cutoffs from env
	now := time.Now().Unix()
	keepAllHours := parseEnvInt("RELAY_REC_KEEP_ALL_HOURS", recKeepAllHoursDefault)
	keepEventDays := parseEnvInt("RELAY_REC_KEEP_EVENT_DAYS", recKeepEventDaysDefault)
	keepAllCutoff := now - int64(keepAllHours)*3600
	keepEventCutoff := now - int64(keepEventDays)*86400
	policy := janPolicy{
		keepAllCutoff:   keepAllCutoff,
		keepEventCutoff: keepEventCutoff,
	}
	// Get deletion order
	toDelete := janitorDeleteOrder(segs, r.capBytes, total, policy)
	// Delete
	freed := int64(0)
	deleted := 0
	for _, s := range toDelete {
		if err := os.Remove(s.path); err == nil {
			freed += s.size
			deleted++
		}
	}
	if deleted > 0 {
		log.Printf("[rec] janitor: freed %s (%d segments) — over cap %s", fmtBytesCap(freed), deleted, fmtBytesCap(r.capBytes))
	}
}

// pruneEventThumbs deletes pre-stored event-thumb JPEGs older than the recordings
// event-retention window (RELAY_REC_KEEP_EVENT_DAYS, default 30d) so .evthumbs/
// dirs don't grow unbounded. Walks <recDir>/*/.evthumbs/. Best-effort and bounded.
func (r *Recorder) pruneEventThumbs() {
	keepEventDays := parseEnvInt("RELAY_REC_KEEP_EVENT_DAYS", recKeepEventDaysDefault)
	cutoff := time.Now().Add(-time.Duration(keepEventDays) * 24 * time.Hour)
	pruned := 0
	filepath.WalkDir(r.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Base(filepath.Dir(p)) != evThumbDir || !strings.HasSuffix(p, ".jpg") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			if os.Remove(p) == nil {
				pruned++
			}
		}
		return nil
	})
	if pruned > 0 {
		log.Printf("[rec] janitor: pruned %d event thumbs older than %dd", pruned, keepEventDays)
	}
}

func parseEnvInt(key string, def int) int {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n < 0 {
		return def
	}
	return n
}

// parseSizeBytes parses "2TB", "500GB", "100MB", or a raw byte count. 0 on empty/invalid.
func parseSizeBytes(s string) int64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "TB"):
		mult, s = 1<<40, strings.TrimSuffix(s, "TB")
	case strings.HasSuffix(s, "GB"):
		mult, s = 1<<30, strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "MB"):
		mult, s = 1<<20, strings.TrimSuffix(s, "MB")
	}
	var n float64
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%g", &n); err != nil || n < 0 {
		return 0
	}
	return int64(n * float64(mult))
}

func fmtBytesCap(b int64) string {
	if b <= 0 {
		return "unlimited"
	}
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.1fTB", float64(b)/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.0fGB", float64(b)/(1<<30))
	default:
		return fmt.Sprintf("%.0fMB", float64(b)/(1<<20))
	}
}
