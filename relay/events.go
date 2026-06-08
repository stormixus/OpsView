package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
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
	day := dayKeyFromMs(startMs)
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
