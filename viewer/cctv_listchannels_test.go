package main

import "testing"

// ListChannels must surface stored rows (ordered) and not silently drop them on
// a Scan error.
func TestListChannelsReturnsOrderedRows(t *testing.T) {
	m := newTestCCTVManager(t)
	if _, err := m.db.Exec(`INSERT INTO dvrs (id, addr) VALUES (1, '10.0.0.1')`); err != nil {
		t.Fatal(err)
	}
	// Insert out of display_order to verify ordering.
	rows := []struct {
		ch, order int
		name      string
	}{
		{2, 1, "second"},
		{1, 0, "first"},
	}
	for _, r := range rows {
		if _, err := m.db.Exec(`INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height) VALUES (1,?,?,?,1,0,0)`, r.ch, r.name, r.order); err != nil {
			t.Fatal(err)
		}
	}

	chs, err := m.ListChannels(1)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(chs) != 2 {
		t.Fatalf("got %d channels, want 2", len(chs))
	}
	if chs[0].Name != "first" || chs[1].Name != "second" {
		t.Fatalf("wrong order: %q, %q", chs[0].Name, chs[1].Name)
	}
	if !chs[0].Enabled {
		t.Errorf("channel should be enabled")
	}
}
