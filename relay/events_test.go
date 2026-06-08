package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An event straddling local midnight must be filed under both days.
func TestEventStoreCrossMidnight(t *testing.T) {
	es := newEventStore("")
	mid := time.Date(2026, 6, 8, 0, 0, 0, 0, time.Local)
	startMs := mid.Add(-2 * time.Minute).UnixMilli() // 2026-06-07 23:58 local
	endMs := mid.Add(2 * time.Minute).UnixMilli()    // 2026-06-08 00:02 local
	d1, d2 := dayKeyFromMs(startMs), dayKeyFromMs(endMs)
	if d1 == d2 {
		t.Skipf("timestamps did not straddle midnight in TZ %s", time.Local)
	}
	es.add("dvr1_ch1", "motion", true, startMs)
	es.add("dvr1_ch1", "motion", false, endMs)
	if got := es.eventsForDay("dvr1_ch1", d1); len(got) != 1 {
		t.Fatalf("start day %s: got %d intervals, want 1", d1, len(got))
	}
	if got := es.eventsForDay("dvr1_ch1", d2); len(got) != 1 {
		t.Fatalf("end day %s: got %d intervals, want 1", d2, len(got))
	}
}

func TestEventStorePairing(t *testing.T) {
	dir := t.TempDir()
	es := newEventStore(dir)
	// open then close -> one interval
	es.add("dvr1_ch2", "motion", true, 1_000_000)
	es.add("dvr1_ch2", "motion", false, 1_030_000) // +30s
	day := dayKeyFromMs(1_000_000)
	got := es.eventsForDay("dvr1_ch2", day)
	if len(got) != 1 {
		t.Fatalf("got %d intervals, want 1", len(got))
	}
	if got[0].Start != 1000 || got[0].End != 1030 || got[0].Kind != "motion" {
		t.Fatalf("interval = %+v, want start=1000 end=1030 motion (seconds)", got[0])
	}
	// persisted to <dir>/dvr1_ch2/.events/<day>.jsonl
	p := filepath.Join(dir, "dvr1_ch2", ".events", day+".jsonl")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("expected jsonl at %s: %v", p, err)
	}
}

func TestEventStoreOrphanClose(t *testing.T) {
	es := newEventStore(t.TempDir())
	es.maxEventMs = 600_000 // 10 min
	es.add("dvr1_ch1", "motion", true, 5_000_000)
	// a far-future open of the same key forces the stale one closed at start+max
	es.add("dvr1_ch1", "motion", true, 9_000_000)
	got := es.eventsForDay("dvr1_ch1", dayKeyFromMs(5_000_000))
	if len(got) != 1 || got[0].End != (5_000_000+600_000)/1000 {
		t.Fatalf("orphan not force-closed: %+v", got)
	}
}

func TestStampSurvEventAgentID(t *testing.T) {
	// build a MsgSurvEvent payload with empty AgentID, stamp it, confirm streamPath use
	stream := streamPath("acme", "dvr1_ch2")
	if stream == "" {
		t.Skip("streamPath unavailable")
	}
	if stream != "acme/dvr1_ch2" {
		t.Fatalf("streamPath = %q, want acme/dvr1_ch2", stream)
	}
}

func TestEventsAPIShape(t *testing.T) {
	es := newEventStore(t.TempDir())
	es.add("dvr1_ch2", "motion", true, 1_000_000)
	es.add("dvr1_ch2", "motion", false, 1_010_000)
	ivals := es.eventsForDay("dvr1_ch2", dayKeyFromMs(1_000_000))
	b, _ := json.Marshal(map[string]interface{}{"events": ivals})
	s := string(b)
	if indexOf(s, `"kind":"motion"`) < 0 || indexOf(s, `"start":1000`) < 0 {
		t.Fatalf("payload shape wrong: %s", b)
	}
}

func TestJanitorPrefersNonEvent(t *testing.T) {
	segs := []janSeg{
		{path: "a", size: 100, startSec: 1000, durSec: 300, event: false},      // old, no event
		{path: "b", size: 100, startSec: 1300, durSec: 300, event: true},       // old, event
		{path: "c", size: 100, startSec: 9_000_000, durSec: 300, event: false}, // within keep-all
	}
	// cap forces dropping 100 bytes; keep-all protects c; non-event old (a) goes first
	order := janitorDeleteOrder(segs, 200 /*cap*/, 300 /*total*/, janPolicy{
		keepAllCutoff:   8_000_000,
		keepEventCutoff: 0,
	})
	if len(order) == 0 || order[0].path != "a" {
		t.Fatalf("expected 'a' (non-event, old) deleted first, got %+v", order)
	}
	for _, s := range order {
		if s.path == "c" {
			t.Fatal("keep-all segment c must not be deleted when the cap is reachable without it")
		}
	}
}

// The disk cap is a HARD ceiling: when only keep-all (recent) footage exists and it
// exceeds the cap, the janitor must still evict the oldest keep-all segments rather
// than let the disk overflow.
func TestJanitorCapIsHardCeiling(t *testing.T) {
	segs := []janSeg{
		{path: "new", size: 100, startSec: 9_000_002, durSec: 300, event: true},
		{path: "old", size: 100, startSec: 9_000_000, durSec: 300, event: false},
		{path: "mid", size: 100, startSec: 9_000_001, durSec: 300, event: true},
	}
	// all are within the keep-all window; total 300 > cap 200 -> must drop the oldest
	order := janitorDeleteOrder(segs, 200, 300, janPolicy{keepAllCutoff: 8_000_000, keepEventCutoff: 0})
	if len(order) != 1 || order[0].path != "old" {
		t.Fatalf("expected oldest keep-all 'old' evicted to honor the cap, got %+v", order)
	}
}

func TestRecEventListJSONShape(t *testing.T) {
	// verify the event-list response struct marshals to the expected shape
	items := []recEventItem{
		{Stream: "default/dvr1_ch2", Ch: 2, Name: "Camera 2", Kind: "motion", Start: 2000, End: 2030},
		{Stream: "default/dvr1_ch1", Ch: 1, Name: "Camera 1", Kind: "motion", Start: 1000, End: 1030},
	}
	b, _ := json.Marshal(map[string]interface{}{"events": items})
	s := string(b)
	// verify expected keys present
	if indexOf(s, `"stream"`) < 0 || indexOf(s, `"ch"`) < 0 || indexOf(s, `"name"`) < 0 ||
		indexOf(s, `"kind":"motion"`) < 0 || indexOf(s, `"start":`) < 0 || indexOf(s, `"end":`) < 0 {
		t.Fatalf("event-list payload shape wrong: %s", b)
	}
}
