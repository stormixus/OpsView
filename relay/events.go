package main

import (
	"bufio"
	"encoding/json"
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
}

type openKey struct{ stream, kind string }
type openInterval struct {
	startMs int64
}

type evDayCache struct {
	mtime time.Time
	ivals []eventInterval
}

// eventStore pairs SurvEvent edges into intervals, persists them per (stream,day)
// as JSONL under <recDir>/<stream>/.events/, and serves them (mtime-cached) to the
// marker API and the janitor. If recDir is "", it operates in-memory only.
type eventStore struct {
	recDir     string
	maxEventMs int64

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

// add ingests one edge. Active=true opens an interval (force-closing any stale
// open one past maxEventMs); Active=false closes the open interval.
func (e *eventStore) add(stream, kind string, active bool, tsMs int64) {
	if stream == "" || kind == "" || tsMs <= 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	k := openKey{stream, kind}
	if active {
		if cur, ok := e.open[k]; ok {
			// stale open: force-close at start+max before opening the new one
			e.closeLocked(stream, kind, cur.startMs, cur.startMs+e.maxEventMs)
		}
		e.open[k] = openInterval{startMs: tsMs}
		return
	}
	if cur, ok := e.open[k]; ok {
		delete(e.open, k)
		end := tsMs
		if end > cur.startMs+e.maxEventMs {
			end = cur.startMs + e.maxEventMs
		}
		e.closeLocked(stream, kind, cur.startMs, end)
	}
}

// closeLocked appends a finished interval (caller holds e.mu).
func (e *eventStore) closeLocked(stream, kind string, startMs, endMs int64) {
	iv := eventInterval{Start: startMs / 1000, End: endMs / 1000, Kind: kind}
	startDay := dayKeyFromMs(startMs)
	e.fileIntervalLocked(stream, startDay, iv)
	// An event straddling local midnight must be findable on BOTH days, else the
	// next day's marker view misses it and the janitor could delete a post-midnight
	// segment it overlaps (overlaps() only queries the segment's own day).
	if endDay := dayKeyFromMs(endMs); endDay != startDay {
		e.fileIntervalLocked(stream, endDay, iv)
	}
}

// fileIntervalLocked appends a closed interval to one day's JSONL (and the
// in-memory cache when recDir==""), invalidating that day's cache. Caller holds e.mu.
func (e *eventStore) fileIntervalLocked(stream, day string, iv eventInterval) {
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
	// invalidate the day cache so the next read re-loads
	delete(e.cache, stream+"|"+day)
	// also keep an in-memory copy for the recDir=="" case
	if e.recDir == "" {
		key := stream + "|" + day
		c := e.cache[key]
		c.ivals = append(c.ivals, iv)
		e.cache[key] = c
	}
}

// eventsForDay returns intervals for (stream,day), mtime-cached from the JSONL.
func (e *eventStore) eventsForDay(stream, day string) []eventInterval {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := stream + "|" + day
	if e.recDir == "" {
		return append([]eventInterval(nil), e.cache[key].ivals...)
	}
	p := filepath.Join(e.recDir, filepath.FromSlash(stream), ".events", day+".jsonl")
	info, err := os.Stat(p)
	if err != nil {
		return nil
	}
	if c, ok := e.cache[key]; ok && c.mtime.Equal(info.ModTime()) {
		return append([]eventInterval(nil), c.ivals...)
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
	e.cache[key] = evDayCache{mtime: info.ModTime(), ivals: ivals}
	return append([]eventInterval(nil), ivals...)
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
}

// HandleDashboardRecEventsList aggregates event intervals across all enabled
// channels in an agent session for a given day, sorted newest-first.
// Admin-gated. ?agent=<id>&day=YYYYMMDD.
func (h *Hub) HandleDashboardRecEventsList(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.events == nil {
		http.Error(w, "events disabled", http.StatusConflict)
		return
	}
	q := r.URL.Query()
	agentID := q.Get("agent")
	day := q.Get("day")

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
				if !ch.Enabled {
					continue
				}
				stream := streamPath(sess.id, streamIDFor(ch.DVRID, ch.ChNum))
				for _, iv := range h.events.eventsForDay(stream, day) {
					items = append(items, recEventItem{
						Stream: stream,
						Ch:     ch.ChNum,
						Name:   ch.Name,
						Kind:   iv.Kind,
						Start:  iv.Start,
						End:    iv.End,
					})
				}
			}
		}
	}

	// sort newest first
	sort.Slice(items, func(i, j int) bool {
		return items[i].Start > items[j].Start
	})

	w.Header().Set("Content-Type", "application/json")
	if day == todayKey() {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=60")
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"events": items})
}
