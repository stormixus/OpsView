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
