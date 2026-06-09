package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDaysInRange(t *testing.T) {
	a, _ := time.ParseInLocation("20060102_150405", "20260607_230000", time.Local)
	b, _ := time.ParseInLocation("20060102_150405", "20260609_010000", time.Local)
	got := daysInRange(a.Unix(), b.Unix())
	want := []string{"20260607", "20260608", "20260609"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if d := daysInRange(a.Unix(), a.Unix()+60); len(d) != 1 || d[0] != "20260607" {
		t.Fatalf("single day: %v", d)
	}
	if d := daysInRange(b.Unix(), a.Unix()); len(d) != 0 {
		t.Fatalf("reversed: %v", d)
	}
}

func TestTimelineFor(t *testing.T) {
	dir := t.TempDir()
	rec := &Recorder{dir: dir, segSecs: 300}
	stream := "agentx/dvr0_ch1"
	sd := filepath.Join(dir, "agentx", "dvr0_ch1")
	os.MkdirAll(sd, 0o755)
	for _, name := range []string{"20260607_120000.mp4", "20260607_120500.mp4"} {
		os.WriteFile(filepath.Join(sd, name), []byte("x"), 0o644)
	}
	base, _ := time.ParseInLocation("20060102_150405", "20260607_120000", time.Local)
	start := base.Unix()

	ev := newEventStore(dir)
	ev.add("agentx/dvr0_ch1", "motion", true, (start+60)*1000)
	ev.add("agentx/dvr0_ch1", "motion", false, (start+70)*1000)
	ev.add("agentx/dvr0_ch1", "motion", true, (start+95)*1000)
	ev.add("agentx/dvr0_ch1", "motion", false, (start+100)*1000)

	h := &Hub{rec: rec, events: ev}
	tl := h.timelineFor(stream, start, start+600)

	if len(tl.Segments) != 2 {
		t.Fatalf("segments: %+v", tl.Segments)
	}
	if len(tl.Events) != 1 {
		t.Fatalf("expected clustered to 1 event, got %+v", tl.Events)
	}
	if tl.Events[0].Kind != "motion" {
		t.Fatalf("kind: %+v", tl.Events[0])
	}
}
