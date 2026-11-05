package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/opsview/opsview/proto"
)

// eventInterval is one paired event on the recording timeline (unix SECONDS, to
// match the segment timeline's units).
type eventInterval struct {
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	Kind  string `json:"kind"`
	Plate string `json:"plate,omitempty"`
}

type openKey struct{ stream, kind string }
type openInterval struct {
	startMs int64
	plate   string
}

type evDayCache struct {
	mtime time.Time
	size  int64
	ivals []eventInterval
}

// eventStore pairs SurvEvent edges into intervals, persists them per (stream,day)
// as JSONL under <recDir>/<stream>/.events/, and serves them (mtime-cached) to the
// marker API and the janitor. If recDir is "", it operates in-memory only.
type eventStore struct {
	recDir     string
	maxEventMs int64

	// ioMu serializes file reads/appends per stream without holding the global
	// state lock during disk I/O. Stripes bound lock memory for arbitrary streams.
	ioMu  [64]sync.Mutex
	mu    sync.Mutex
	open  map[openKey]openInterval
	cache map[string]evDayCache // "stream|day" -> intervals (mtime-keyed)
}

func newEventStore(recDir string) *eventStore {
	return &eventStore{
		recDir:     recDir,
		maxEventMs: 10 * 60 * 1000,
		open:       map[openKey]openInterval{},
		cache:      map[string]evDayCache{},
	}
}

func dayKeyFromMs(ms int64) string {
	return time.UnixMilli(ms).In(time.Local).Format("20060102")
}

func (e *eventStore) streamLock(stream string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(stream))
	return &e.ioMu[h.Sum32()%uint32(len(e.ioMu))]
}

// add ingests one edge. Active=true opens an interval (force-closing any stale
// open one past maxEventMs); Active=false closes the open interval.
func (e *eventStore) add(stream, kind string, active bool, tsMs int64) {
	if stream == "" || kind == "" || tsMs <= 0 {
		return
	}
	lock := e.streamLock(stream)
	lock.Lock()
	defer lock.Unlock()
	e.mu.Lock()
	k := openKey{stream, kind}
	if active {
		if cur, ok := e.open[k]; ok {
			// stale open: force-close at start+max before opening the new one
			e.open[k] = openInterval{startMs: tsMs}
			e.mu.Unlock()
			e.closeInterval(stream, kind, cur.startMs, cur.startMs+e.maxEventMs, cur.plate)
			return
		}
		e.open[k] = openInterval{startMs: tsMs}
		e.mu.Unlock()
		return
	}
	if cur, ok := e.open[k]; ok {
		delete(e.open, k)
		end := tsMs
		if end > cur.startMs+e.maxEventMs {
			end = cur.startMs + e.maxEventMs
		}
		e.mu.Unlock()
		e.closeInterval(stream, kind, cur.startMs, end, cur.plate)
		return
	}
	e.mu.Unlock()
}

// closeInterval appends a finished interval (caller holds the stream lock).
func (e *eventStore) closeInterval(stream, kind string, startMs, endMs int64, plate string) {
	iv := eventInterval{Start: startMs / 1000, End: endMs / 1000, Kind: kind, Plate: plate}
	startDay := dayKeyFromMs(startMs)
	e.fileInterval(stream, startDay, iv)
	// An event straddling local midnight must be findable on BOTH days, else the
	// next day's marker view misses it and the janitor could delete a post-midnight
	// segment it overlaps (overlaps() only queries the segment's own day).
	if endDay := dayKeyFromMs(endMs); endDay != startDay {
		e.fileInterval(stream, endDay, iv)
	}
}

// updateOpenPlate updates the plate number for an active open event or recently cached event.
func (e *eventStore) updateOpenPlate(stream string, tsMs int64, plate string) {
	lock := e.streamLock(stream)
	lock.Lock()
	defer lock.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()

	// 1. Check open events
	for k, cur := range e.open {
		if k.stream == stream && cur.startMs == tsMs {
			cur.plate = plate
			e.open[k] = cur
			return
		}
	}

	// 2. Check recently cached events (if closed quickly)
	day := dayKeyFromMs(tsMs)
	key := stream + "|" + day
	if c, ok := e.cache[key]; ok {
		tSec := tsMs / 1000
		for i := len(c.ivals) - 1; i >= 0; i-- {
			if c.ivals[i].Start == tSec {
				c.ivals[i].Plate = plate
				e.cache[key] = c
				break
			}
		}
	}
}

// fileInterval appends a closed interval to one day's JSONL (and the
// in-memory cache when recDir==""), invalidating that day's cache. Caller holds the stream lock.
func (e *eventStore) fileInterval(stream, day string, iv eventInterval) {
	if e.recDir != "" {
		dir := filepath.Join(e.recDir, filepath.FromSlash(stream), ".events")
		if err := os.MkdirAll(dir, 0o755); err == nil {
			if f, err := os.OpenFile(filepath.Join(dir, day+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				if b, e2 := json.Marshal(iv); e2 == nil {
					f.Write(append(b, '\n'))
				}
				f.Close()
			}
		}
	}
	// Invalidate only after the append, under the same stream lock as readers.
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.recDir != "" {
		delete(e.cache, stream+"|"+day)
	}
	// also keep an in-memory copy for the recDir=="" case
	if e.recDir == "" {
		key := stream + "|" + day
		c := e.cache[key]
		c.ivals = append(c.ivals, iv)
		e.cache[key] = c
	}
}

// Request-local diagnostics expose aggregate durations only, never file paths.
type eventReadTiming struct{ wait, stat, cache, load time.Duration }

// eventsForDay returns intervals for (stream,day), mtime-cached from the JSONL.
func (e *eventStore) eventsForDay(stream, day string) []eventInterval {
	return e.eventsForDayMeasured(stream, day, nil)
}
func (e *eventStore) eventsForDayMeasured(stream, day string, timing *eventReadTiming) []eventInterval {
	started := time.Now()
	lock := e.streamLock(stream)
	lock.Lock()
	if timing != nil {
		timing.wait += time.Since(started)
	}
	defer lock.Unlock()
	e.mu.Lock()
	key := stream + "|" + day
	if e.recDir == "" {
		ivals := append([]eventInterval(nil), e.cache[key].ivals...)
		e.mu.Unlock()
		return ivals
	}
	e.mu.Unlock()
	p := filepath.Join(e.recDir, filepath.FromSlash(stream), ".events", day+".jsonl")
	started = time.Now()
	info, err := os.Stat(p)
	if timing != nil {
		timing.stat += time.Since(started)
	}
	if err != nil {
		return nil
	}
	started = time.Now()
	e.mu.Lock()
	c, ok := e.cache[key]
	if ok && c.mtime.Equal(info.ModTime()) && c.size == info.Size() {
		ivals := append([]eventInterval(nil), c.ivals...)
		e.mu.Unlock()
		if timing != nil {
			timing.cache += time.Since(started)
		}
		return ivals
	}
	e.mu.Unlock()
	if timing != nil {
		timing.cache += time.Since(started)
	}
	started = time.Now()
	if timing != nil {
		defer func() { timing.load += time.Since(started) }()
	}
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	var ivals []eventInterval
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var iv eventInterval
		if json.Unmarshal(sc.Bytes(), &iv) == nil {
			ivals = append(ivals, iv)
		}
	}
	if sc.Err() != nil {
		// Preserve valid intervals for retention decisions, but never cache a partial scan.
		return ivals
	}
	e.mu.Lock()
	e.cache[key] = evDayCache{mtime: info.ModTime(), size: info.Size(), ivals: ivals}
	result := append([]eventInterval(nil), ivals...)
	e.mu.Unlock()
	return result
}

// overlaps reports whether [startSec,endSec] overlaps any event for the stream on
// the day(s) it spans. Used by the janitor.
func (e *eventStore) overlaps(stream string, startSec, endSec int64) bool {
	for _, day := range daysSpanned(startSec, endSec) {
		for _, iv := range e.eventsForDay(stream, day) {
			if iv.Start < endSec && iv.End > startSec {
				return true
			}
		}
	}
	return false
}

func daysSpanned(startSec, endSec int64) []string {
	d1 := time.Unix(startSec, 0).In(time.Local).Format("20060102")
	d2 := time.Unix(endSec, 0).In(time.Local).Format("20060102")
	if d1 == d2 {
		return []string{d1}
	}
	return []string{d1, d2}
}

// HandleDashboardRecEvents serves event-timeline markers for a stream+day.
// Admin-gated, mirrors HandleDashboardRecordings.
func (h *Hub) HandleDashboardRecEvents(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.events == nil {
		http.Error(w, "events disabled", http.StatusConflict)
		return
	}
	stream := r.URL.Query().Get("stream")
	day := r.URL.Query().Get("day")
	w.Header().Set("Content-Type", "application/json")
	if day == todayKey() {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=300")
	}
	ivals := []eventInterval{}
	if stream != "" && len(day) == 8 {
		ivals = h.events.eventsForDay(stream, day)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"events": ivals})
}

func todayKey() string { return time.Now().In(time.Local).Format("20060102") }

// recEventItem is a single event entry in the aggregated event-list API response.
type recEventItem struct {
	Stream string `json:"stream"`
	Ch     int    `json:"ch"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Start  int64  `json:"start"` // unix seconds
	End    int64  `json:"end"`   // unix seconds
	Plate  string `json:"plate,omitempty"`
}

// recEventMergeGapSec is the cooldown used to coalesce a channel's fragmented
// events: same-channel, same-kind intervals separated by at or under this gap are
// merged. A raw motion event that overlaps a more-specific smart event
// (person/vehicle/…) on the same channel is dropped, since AcuSense fires both for
// a single happening. The event-grid uses this (90s) to keep one channel from
// spraying dozens of cards; the player timeline uses a finer gap so distinct
// events stay visible (see recTimelineMergeGapSec).
const recEventMergeGapSec = 90

// recTimelineMergeGapSec is the finer cooldown used by the unified-player timeline:
// marks are spread out vertically by time, so showing more distinct events is
// useful (unlike the grid, where over-fragmentation is noise).
const recTimelineMergeGapSec = 25

// clusterEventItems coalesces fragmented per-channel events, merging same-(stream,
// kind) intervals separated by <= gapSec. Order-independent; the caller re-sorts.
func clusterEventItems(items []recEventItem, gapSec int64) []recEventItem {
	if len(items) <= 1 {
		return items
	}
	// 1) Merge same-(stream,kind) intervals separated by <= the cooldown.
	sort.Slice(items, func(i, j int) bool {
		if items[i].Stream != items[j].Stream {
			return items[i].Stream < items[j].Stream
		}
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].Start < items[j].Start
	})
	merged := make([]recEventItem, 0, len(items))
	for _, it := range items {
		if n := len(merged); n > 0 {
			last := &merged[n-1]
			if last.Stream == it.Stream && last.Kind == it.Kind && it.Start-last.End <= gapSec {
				if it.End > last.End {
					last.End = it.End
				}
				if last.Plate == "" && it.Plate != "" {
					last.Plate = it.Plate
				}
				continue
			}
		}
		merged = append(merged, it)
	}
	// 2) Per stream, drop a motion event that coincides with a more-specific one.
	specific := map[string][]recEventItem{}
	for _, it := range merged {
		if it.Kind != "motion" {
			specific[it.Stream] = append(specific[it.Stream], it)
		}
	}
	out := make([]recEventItem, 0, len(merged))
	for _, it := range merged {
		if it.Kind == "motion" {
			drop := false
			for _, sp := range specific[it.Stream] {
				if it.Start <= sp.End+gapSec && sp.Start <= it.End+gapSec {
					drop = true
					break
				}
			}
			if drop {
				continue
			}
		}
		out = append(out, it)
	}
	return out
}

// recListDays returns the YYYYMMDD days to aggregate: a from..to range (date
// presets) if both are valid, else the single day (back-compat). Newest-first
// caller re-sorts events anyway, so day order here doesn't matter.
func recListDays(day, from, to string) []string {
	if len(from) == 8 && len(to) == 8 {
		ft, e1 := time.ParseInLocation("20060102", from, time.Local)
		tt, e2 := time.ParseInLocation("20060102", to, time.Local)
		if e1 == nil && e2 == nil && !tt.Before(ft) {
			return daysInRange(ft.Unix(), tt.Add(12*time.Hour).Unix())
		}
	}
	if len(day) == 8 {
		return []string{day}
	}
	return nil
}

// HandleDashboardRecEventsList aggregates event intervals across all enabled
// channels in an agent session for a day (?day=YYYYMMDD) or a range
// (?from=YYYYMMDD&to=YYYYMMDD, for date presets), sorted newest-first. Admin-gated.
func (h *Hub) HandleDashboardRecEventsList(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.events == nil {
		http.Error(w, "events disabled", http.StatusConflict)
		return
	}
	started := time.Now()
	var timings eventReadTiming
	defer func() {
		if elapsed := time.Since(started); elapsed > time.Second {
			ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
			log.Printf("[rec-events] app_ms=%.0f wait_ms=%.0f stat_ms=%.0f cache_ms=%.0f load_ms=%.0f cancelled=%t", ms(elapsed), ms(timings.wait), ms(timings.stat), ms(timings.cache), ms(timings.load), r.Context().Err() != nil)
		}
	}()
	q := r.URL.Query()
	agentID := q.Get("agent")
	day := q.Get("day")
	days := recListDays(day, q.Get("from"), q.Get("to"))

	sess := h.sessionByID(agentID)
	if sess == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"events": []recEventItem{}})
		return
	}

	sess.survConfigMu.RLock()
	raw := sess.survConfig
	sess.survConfigMu.RUnlock()

	var items []recEventItem
	if len(raw) > proto.HeaderSize {
		var cfg proto.SurvConfig
		if json.Unmarshal(raw[proto.HeaderSize:], &cfg) == nil {
			for _, ch := range cfg.Channels {
				if r.Context().Err() != nil {
					return
				}
				if !ch.Enabled {
					continue
				}
				stream := streamPath(sess.id, streamIDFor(ch.DVRID, ch.ChNum))
				for _, d := range days {
					if r.Context().Err() != nil {
						return
					}
					for _, iv := range h.events.eventsForDayMeasured(stream, d, &timings) {
						items = append(items, recEventItem{
							Stream: stream,
							Ch:     ch.ChNum,
							Name:   ch.Name,
							Kind:   iv.Kind,
							Start:  iv.Start,
							End:    iv.End,
							Plate:  iv.Plate,
						})
					}
				}
			}
		}
	}

	// coalesce fragmented per-channel events (grid: consolidate aggressively), sort newest first
	mergeStart := time.Now()
	items = clusterEventItems(items, recEventMergeGapSec)
	sort.Slice(items, func(i, j int) bool {
		return items[i].Start > items[j].Start
	})

	mergeTime := time.Since(mergeStart)
	encodeStart := time.Now()
	body, err := json.Marshal(map[string]interface{}{"events": items})
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	encodeTime := time.Since(encodeStart)
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	w.Header().Set("Server-Timing", fmt.Sprintf("app;dur=%.3f, wait;dur=%.3f, stat;dur=%.3f, cache;dur=%.3f, load;dur=%.3f, merge;dur=%.3f, encode;dur=%.3f", ms(time.Since(started)), ms(timings.wait), ms(timings.stat), ms(timings.cache), ms(timings.load), ms(mergeTime), ms(encodeTime)))
	w.Header().Set("Content-Type", "application/json")
	today := todayKey()
	live := false
	for _, d := range days {
		if d == today {
			live = true
			break
		}
	}
	if live {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=60")
	}
	w.Write(append(body, '\n'))
}
