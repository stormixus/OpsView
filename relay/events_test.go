package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
