package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSizeBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0}, {"abc", 0},
		{"2TB", 2 * (1 << 40)},
		{"500GB", 500 * (1 << 30)},
		{"100MB", 100 * (1 << 20)},
		{"1024", 1024},
	}
	for _, c := range cases {
		if got := parseSizeBytes(c.in); got != c.want {
			t.Errorf("%q: got %d want %d", c.in, got, c.want)
		}
	}
}

func TestRecorderJanitor(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "ch1")
	os.MkdirAll(sub, 0o755)
	mk := func(name string, age time.Duration) {
		p := filepath.Join(sub, name)
		os.WriteFile(p, make([]byte, 1000), 0o644)
		mt := time.Now().Add(-age)
		os.Chtimes(p, mt, mt)
	}
	mk("a.mp4", 3*time.Hour) // oldest
	mk("b.mp4", 2*time.Hour)
	mk("c.mp4", 1*time.Hour) // newest
	r := &Recorder{dir: dir, capBytes: 2500}
	r.runJanitor() // 3000 > 2500 -> delete oldest (a) -> 2000 <= 2500
	if _, err := os.Stat(filepath.Join(sub, "a.mp4")); !os.IsNotExist(err) {
		t.Fatal("oldest a.mp4 should be deleted")
	}
	for _, keep := range []string{"b.mp4", "c.mp4"} {
		if _, err := os.Stat(filepath.Join(sub, keep)); err != nil {
			t.Fatalf("%s should remain", keep)
		}
	}
}

func TestRecordingsListing(t *testing.T) {
	dir := t.TempDir()
	r := &Recorder{dir: dir, segSecs: 300}
	stream := "dvr4_ch1"
	sd := filepath.Join(dir, stream)
	os.MkdirAll(sd, 0o755)
	os.WriteFile(filepath.Join(sd, "20260607_120000.mp4"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(sd, "20260607_120500.mp4"), []byte("xx"), 0o644)
	os.WriteFile(filepath.Join(sd, "20260606_235500.mp4"), []byte("y"), 0o644)

	days := r.days(stream)
	if len(days) != 2 || days[0] != "20260607" {
		t.Fatalf("days: %v", days)
	}
	segs := r.segments(stream, "20260607")
	if len(segs) != 2 || segs[0].Dur != 300 {
		t.Fatalf("segs: %+v", segs)
	}
	// path safety
	if _, ok := r.safeStreamDir("../etc"); ok {
		t.Fatal("traversal dir allowed")
	}
	if _, ok := r.segmentFile(stream, "../x.mp4"); ok {
		t.Fatal("traversal file allowed")
	}
	if _, ok := r.segmentFile(stream, "20260607_120000.mp4"); !ok {
		t.Fatal("valid segment rejected")
	}
}

func TestSegmentsForExport(t *testing.T) {
	dir := t.TempDir()
	r := &Recorder{dir: dir, segSecs: 300}
	stream := "dvr4_ch1"
	sd := filepath.Join(dir, stream)
	os.MkdirAll(sd, 0o755)
	// three back-to-back 5-minute segments at 12:00, 12:05, 12:10 local time.
	for _, name := range []string{"20260607_120000.mp4", "20260607_120500.mp4", "20260607_121000.mp4"} {
		os.WriteFile(filepath.Join(sd, name), []byte("x"), 0o644)
	}
	base, err := time.ParseInLocation(recNameLayout, "20260607_120000", time.Local)
	if err != nil {
		t.Fatal(err)
	}
	start := base.Unix()

	// a window from 12:03 to 12:07 overlaps the first two segments only.
	got := r.segmentsForExport(stream, start+180, start+420)
	if len(got) != 2 || got[0].Name != "20260607_120000.mp4" || got[1].Name != "20260607_120500.mp4" {
		t.Fatalf("range overlap: %+v", got)
	}
	// a window entirely before any recording returns nothing.
	if got := r.segmentsForExport(stream, start-7200, start-3600); len(got) != 0 {
		t.Fatalf("expected empty, got %+v", got)
	}
	// invalid (end<=start) returns nothing.
	if got := r.segmentsForExport(stream, start+100, start+100); got != nil {
		t.Fatalf("expected nil for empty range, got %+v", got)
	}
}

func TestRecordTargetsExcludesWall(t *testing.T) {
	got := recordTargets([]string{"dvr1_ch1", "wall", "SM-Boutique/wall", "SM-Boutique/dvr1_ch2"})
	if _, ok := got["wall"]; ok {
		t.Error("wall (live-only mosaic) must not be a record target")
	}
	if _, ok := got["SM-Boutique/wall"]; ok {
		t.Error("agent-prefixed wall must not be a record target")
	}
	if got["dvr1_ch1"] != "dvr1_ch1" {
		t.Errorf("normal channel should record itself, got %q", got["dvr1_ch1"])
	}
	if got["SM-Boutique/dvr1_ch2"] != "SM-Boutique/dvr1_ch2" {
		t.Errorf("agent-prefixed channel should record itself, got %q", got["SM-Boutique/dvr1_ch2"])
	}
}
