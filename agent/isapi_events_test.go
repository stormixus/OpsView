package main

import (
	"bytes"
	"os"
	"testing"
)

func TestAlertKind(t *testing.T) {
	cases := []struct{ et, tt, want string }{
		{"VMD", "human", "person"},
		{"VMD", "vehicle", "vehicle"},
		{"VMD", "", "motion"},
		{"linedetection", "", "linecross"},
		{"videoloss", "", ""},
		{"tamperdetection", "", ""},
	}
	for _, c := range cases {
		if got := alertKind(c.et, c.tt); got != c.want {
			t.Fatalf("alertKind(%q,%q)=%q want %q", c.et, c.tt, got, c.want)
		}
	}
}

func TestParseAlertEvent(t *testing.T) {
	vmd := []byte(`<EventNotificationAlert><channelID>8</channelID><dateTime>2026-06-08T15:05:33</dateTime><eventType>VMD</eventType><eventState>active</eventState><targetType>human</targetType></EventNotificationAlert>`)
	ev, ok := parseAlertEvent(vmd)
	if !ok || ev.chNum != 8 || ev.kind != "person" || !ev.active {
		t.Fatalf("vmd parse = %+v ok=%v, want ch8 person active", ev, ok)
	}
	if ev.tsMs <= 0 {
		t.Fatalf("tsMs not parsed: %d", ev.tsMs)
	}
	// device-level heartbeat must be ignored
	hb := []byte(`<EventNotificationAlert><channelID>0</channelID><eventType>videoloss</eventType><eventState>inactive</eventState></EventNotificationAlert>`)
	if _, ok := parseAlertEvent(hb); ok {
		t.Fatal("videoloss/channel0 must be ignored")
	}
}

func TestScanAlertStream(t *testing.T) {
	data, err := os.ReadFile("testdata/alertstream_sample.txt")
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	var got []alertEvent
	scanAlertStream(bytes.NewReader(data), func(e alertEvent) { got = append(got, e) })
	// 2 VMD edges (active+inactive on ch8); videoloss skipped
	if len(got) != 2 {
		t.Fatalf("got %d edges, want 2: %+v", len(got), got)
	}
	if got[0].chNum != 8 || got[0].kind != "person" || !got[0].active {
		t.Fatalf("edge0=%+v want ch8 person active", got[0])
	}
	if got[1].active {
		t.Fatalf("edge1 should be inactive: %+v", got[1])
	}
}
