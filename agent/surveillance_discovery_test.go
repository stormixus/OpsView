package main

import (
	"strings"
	"testing"
)

// M-26: after an exhaustive probe builds foundSet, the channel list must include
// every found channel — a gap must NOT truncate higher channels.
func TestChannelsFromFoundSetDoesNotTruncateAtGaps(t *testing.T) {
	found := map[int]bool{1: true, 2: true, 3: true, 7: true, 8: true} // gap at 4-6
	chs := channelsFromFoundSet(42, found, 32)

	got := make([]int, len(chs))
	for i, c := range chs {
		got[i] = c.ChNum
	}
	want := []int{1, 2, 3, 7, 8}
	if len(got) != len(want) {
		t.Fatalf("channels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("channels = %v, want %v", got, want)
		}
	}
	if chs[0].DVRID != 42 {
		t.Fatalf("DVRID not propagated: %d", chs[0].DVRID)
	}
}

// L-32: channel names come from DVR devices; strip control chars and cap length.
func TestSanitizeChannelName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Lobby", "Lobby"},
		{"Cam\x001\x07", "Cam1"},
		{"  spaced  ", "spaced"},
		{"line\nbreak", "linebreak"},
	}
	for _, c := range cases {
		if got := sanitizeChannelName(c.in); got != c.want {
			t.Errorf("sanitizeChannelName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := sanitizeChannelName(strings.Repeat("a", 200)); len([]rune(got)) > 64 {
		t.Errorf("name not capped: %d runes", len([]rune(got)))
	}
}

// L-33: ListChannels must surface stored rows (and not silently drop on errors).
func TestListChannelsReturnsInsertedRows(t *testing.T) {
	m := newTestSurvManager(t)
	d, err := m.AddDVR("cam", "10.0.0.1", 80, "", 0, "admin", "pw", "isapi", 2000, "sub")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, m, `INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height) VALUES (?,?,?,?,?,?,?)`, d.ID, 1, "ch1", 0, 1, 1920, 1080)
	mustExec(t, m, `INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height) VALUES (?,?,?,?,?,?,?)`, d.ID, 2, "ch2", 1, 0, 0, 0)

	chs, err := m.ListChannels(d.ID)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(chs) != 2 {
		t.Fatalf("got %d channels, want 2", len(chs))
	}
	if chs[0].Name != "ch1" || !chs[0].Enabled {
		t.Errorf("ch1 wrong: %+v", chs[0])
	}
	if chs[1].Enabled {
		t.Errorf("ch2 should be disabled: %+v", chs[1])
	}
}

// Hybrid DVRs (analog + IP) exceed 16 channels. normalizeChannelDiscovery must
// not chop them to 16 (which dropped real cameras and, via the old discovery
// cleanup, destroyed their operator names/order). Keep up to 32; only cap beyond.
func TestNormalizeChannelDiscoveryKeepsHybridChannels(t *testing.T) {
	in := make([]ChannelConfig, 18)
	for i := range in {
		in[i] = ChannelConfig{ChNum: i + 1, Name: "ch"}
	}
	if got := normalizeChannelDiscovery(in); len(got) != 18 {
		t.Fatalf("18-channel hybrid DVR capped to %d, want 18", len(got))
	}

	big := make([]ChannelConfig, 40)
	for i := range big {
		big[i] = ChannelConfig{ChNum: i + 1, Name: "ch"}
	}
	if got := normalizeChannelDiscovery(big); len(got) != 32 {
		t.Fatalf("over-cap kept %d, want 32 ceiling", len(got))
	}
}

func mustExec(t *testing.T, m *SurveillanceManager, q string, args ...any) {
	t.Helper()
	if _, err := m.db.Exec(q, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}
