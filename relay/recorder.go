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
}

type recProc struct {
	cancel context.CancelFunc
}

const (
	recReconcile   = 15 * time.Second
	recJanitorTick = 2 * time.Minute
	recSegSeconds  = 300 // 5-minute segments
)

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
		}
	}
}

// reconcile starts a recorder for every active stream and stops recorders for
// streams that have gone away.
func (r *Recorder) reconcile() {
	want := map[string]string{} // path -> stream id (label)
	for _, s := range r.hub.allSessions() {
		for _, st := range s.survProxy.StreamStats() {
			if !st.Active {
				continue
			}
			want[streamPath(s.id, st.ID)] = st.ID
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for path := range want {
		if _, ok := r.recs[path]; !ok {
			r.startLocked(path)
		}
	}
	for path, p := range r.recs {
		if _, ok := want[path]; !ok {
			p.cancel()
			delete(r.recs, path)
		}
	}
}

// startLocked launches a supervised ffmpeg recorder for one stream path.
func (r *Recorder) startLocked(path string) {
	ctx, cancel := context.WithCancel(context.Background())
	r.recs[path] = &recProc{cancel: cancel}
	outDir := filepath.Join(r.dir, filepath.FromSlash(path))
	hlsURL := r.selfHLS + "/surv/" + path + "/index.m3u8"
	go r.supervise(ctx, path, hlsURL, outDir)
	log.Printf("[rec] recording %s -> %s", path, outDir)
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

// runJanitor enforces the disk cap by deleting the oldest segment files until the
// recordings directory is back under the cap.
func (r *Recorder) runJanitor() {
	if r.capBytes <= 0 {
		return
	}
	type seg struct {
		path string
		size int64
		mod  time.Time
	}
	var segs []seg
	var total int64
	filepath.WalkDir(r.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".mp4") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		segs = append(segs, seg{p, info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	})
	if total <= r.capBytes {
		return
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].mod.Before(segs[j].mod) })
	freed := int64(0)
	deleted := 0
	for _, s := range segs {
		if total-freed <= r.capBytes {
			break
		}
		if err := os.Remove(s.path); err == nil {
			freed += s.size
			deleted++
		}
	}
	if deleted > 0 {
		log.Printf("[rec] janitor: freed %s (%d segments) — over cap %s", fmtBytesCap(freed), deleted, fmtBytesCap(r.capBytes))
	}
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
