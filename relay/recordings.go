package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// recSegment is one recorded MP4 file in the review timeline.
type recSegment struct {
	Name  string `json:"name"`  // e.g. "20260607_153000.mp4"
	Start int64  `json:"start"` // unix seconds (local time parsed from the name)
	Dur   int    `json:"dur"`   // seconds (gap to next segment, capped at nominal)
	Size  int64  `json:"size"`
}

const recNameLayout = "20060102_150405"

// recFinalizeQuiesce: a segment file untouched (mtime not advancing) for at least
// this long is treated as finalized and safe to cache immutably. ffmpeg writes the
// active segment continuously, so its mtime stays fresh; once it rolls to the next
// segment the previous file's mtime freezes. Comfortably larger than any write gap.
const recFinalizeQuiesce = 90 * time.Second

// recSegIdxCap bounds the in-memory segment-index cache (one entry per browsed
// (stream,day)); past it the cache is reset so it can't grow without limit over a
// long uptime as an operator browses historical days.
const recSegIdxCap = 1024

// segCacheEntry / dayCacheEntry memoize directory scans keyed by the stream dir's
// mtime — a new segment bumps the dir mtime and invalidates the entry, so past
// days (whose dirs never change) are scanned once and the timeline + per-second
// seek lookups resolve from memory instead of re-reading the disk every time.
type segCacheEntry struct {
	mtime time.Time
	segs  []recSegment
}
type dayCacheEntry struct {
	mtime time.Time
	days  []string
}

// dirMtime returns a directory's modification time, or the zero time if it can't
// be stat'd (which disables caching for that path).
func dirMtime(dir string) time.Time {
	if info, err := os.Stat(dir); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

// safeStreamDir validates a stream path (allows one "agent/stream" slash, blocks
// traversal) and returns its on-disk directory.
func (r *Recorder) safeStreamDir(stream string) (string, bool) {
	stream = strings.Trim(stream, "/")
	if stream == "" || strings.Contains(stream, "..") {
		return "", false
	}
	for _, c := range stream {
		ok := c == '/' || c == '_' || c == '-' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !ok {
			return "", false
		}
	}
	dir := filepath.Join(r.dir, filepath.FromSlash(stream))
	if rel, err := filepath.Rel(r.dir, dir); err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return dir, true
}

// days returns the YYYYMMDD dates with recordings for a stream (newest first).
func (r *Recorder) days(stream string) []string {
	dir, ok := r.safeStreamDir(stream)
	if !ok {
		return nil
	}
	mtime := dirMtime(dir)
	if !mtime.IsZero() {
		r.idxMu.Lock()
		if e, ok := r.dayCache[stream]; ok && e.mtime.Equal(mtime) {
			days := e.days
			r.idxMu.Unlock()
			return days
		}
		r.idxMu.Unlock()
	}
	set := map[string]bool{}
	for _, e := range readDirSafe(dir) {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".mp4") && len(n) >= 8 {
			set[n[:8]] = true
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	if !mtime.IsZero() {
		r.idxMu.Lock()
		if r.dayCache == nil {
			r.dayCache = make(map[string]dayCacheEntry)
		}
		r.dayCache[stream] = dayCacheEntry{mtime: mtime, days: out}
		r.idxMu.Unlock()
	}
	return out
}

// segments returns the recordings for a stream on a YYYYMMDD day, time-sorted,
// with durations computed from the gap to the next segment.
func (r *Recorder) segments(stream, day string) []recSegment {
	dir, ok := r.safeStreamDir(stream)
	if !ok || len(day) != 8 {
		return nil
	}
	mtime := dirMtime(dir)
	key := stream + "|" + day
	if !mtime.IsZero() {
		r.idxMu.Lock()
		if e, ok := r.segCache[key]; ok && e.mtime.Equal(mtime) {
			segs := e.segs
			r.idxMu.Unlock()
			return segs
		}
		r.idxMu.Unlock()
	}
	var segs []recSegment
	for _, e := range readDirSafe(dir) {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, day) || !strings.HasSuffix(n, ".mp4") {
			continue
		}
		t, err := time.ParseInLocation(recNameLayout, strings.TrimSuffix(n, ".mp4"), time.Local)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		segs = append(segs, recSegment{Name: n, Start: t.Unix(), Size: info.Size()})
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].Start < segs[j].Start })
	nominal := r.segSecs
	if nominal <= 0 {
		nominal = recSegSeconds
	}
	for i := range segs {
		dur := nominal
		if i+1 < len(segs) {
			if gap := int(segs[i+1].Start - segs[i].Start); gap > 0 && gap < nominal {
				dur = gap
			}
		}
		segs[i].Dur = dur
	}
	if !mtime.IsZero() {
		r.idxMu.Lock()
		if r.segCache == nil || len(r.segCache) >= recSegIdxCap {
			r.segCache = make(map[string]segCacheEntry) // bounded: reset past the cap
		}
		r.segCache[key] = segCacheEntry{mtime: mtime, segs: segs}
		r.idxMu.Unlock()
	}
	return segs
}

// segmentsForExport returns the segments (time-ordered, with durations) for a
// stream that overlap [start,end] unix seconds, scanning across day boundaries
// so a clip can span midnight.
func (r *Recorder) segmentsForExport(stream string, start, end int64) []recSegment {
	dir, ok := r.safeStreamDir(stream)
	if !ok || end <= start {
		return nil
	}
	var segs []recSegment
	for _, e := range readDirSafe(dir) {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".mp4") {
			continue
		}
		t, err := time.ParseInLocation(recNameLayout, strings.TrimSuffix(n, ".mp4"), time.Local)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		segs = append(segs, recSegment{Name: n, Start: t.Unix(), Size: info.Size()})
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].Start < segs[j].Start })
	nominal := r.segSecs
	if nominal <= 0 {
		nominal = recSegSeconds
	}
	for i := range segs {
		dur := nominal
		if i+1 < len(segs) {
			if gap := int(segs[i+1].Start - segs[i].Start); gap > 0 && gap < nominal {
				dur = gap
			}
		}
		segs[i].Dur = dur
	}
	out := segs[:0]
	for _, s := range segs {
		if s.Start < end && s.Start+int64(s.Dur) > start {
			out = append(out, s)
		}
	}
	return out
}

// segmentFile resolves + validates one segment file path for serving.
func (r *Recorder) segmentFile(stream, name string) (string, bool) {
	dir, ok := r.safeStreamDir(stream)
	if !ok {
		return "", false
	}
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || !strings.HasSuffix(name, ".mp4") {
		return "", false
	}
	return filepath.Join(dir, name), true
}

func readDirSafe(dir string) []os.DirEntry {
	e, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	return e
}

// HandleDashboardRecordings lists recording days (no day param) or the segments
// for a given day. Admin-gated. ?stream=<path>[&day=YYYYMMDD].
func (h *Hub) HandleDashboardRecordings(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.rec == nil {
		http.Error(w, "recording disabled", http.StatusConflict)
		return
	}
	stream := r.URL.Query().Get("stream")
	day := r.URL.Query().Get("day")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store") // today's listing changes as segments land
	if day != "" {
		json.NewEncoder(w).Encode(map[string]interface{}{"segments": h.rec.segments(stream, day)})
	} else {
		json.NewEncoder(w).Encode(map[string]interface{}{"days": h.rec.days(stream)})
	}
}

// HandleDashboardRecFile serves a recorded segment MP4 (Range-enabled, so the
// browser can seek), optionally as a download (?dl=1). Admin-gated.
func (h *Hub) HandleDashboardRecFile(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.rec == nil {
		http.Error(w, "recording disabled", http.StatusConflict)
		return
	}
	path, ok := h.rec.segmentFile(r.URL.Query().Get("stream"), r.URL.Query().Get("name"))
	if !ok {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	if r.URL.Query().Get("dl") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(path)+`"`)
	}
	// A finalized segment is immutable: let the browser cache it so scrub seeks, the
	// hover-scrub preview, replays, and multi-cam re-seeks resolve from cache instead
	// of re-fetching byte ranges (incl. the moov) every time. CRITICAL: ffmpeg cuts
	// segments on input media PTS, not wall-clock — and the recorder's input is the
	// relay's own HLS, which can stall — so a name/clock check can mis-classify a
	// still-growing file as "past". The file's mtime instead stops advancing the
	// moment ffmpeg closes the segment (after the +faststart moov relocation), so a
	// file untouched for the quiescence window is guaranteed complete; only then is
	// it safe to cache long-term. The currently-recording segment stays no-cache.
	// http.ServeContent honors If-None-Match/If-Range against the Etag below.
	if time.Since(info.ModTime()) > recFinalizeQuiesce {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		w.Header().Set("Etag", fmt.Sprintf(`"%x-%x"`, info.Size(), info.ModTime().UnixNano()))
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
