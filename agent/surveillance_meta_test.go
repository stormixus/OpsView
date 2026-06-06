package main

import "testing"

func TestReorderAndRenameChannels(t *testing.T) {
	m := newTestSurvManager(t)
	dvr, err := m.AddDVR("cam", "10.0.0.1", 80, "", 0, "admin", "pw", "isapi", 2000, "sub")
	if err != nil {
		t.Fatalf("AddDVR: %v", err)
	}
	for _, c := range []struct {
		ch   int
		name string
	}{{1, "101"}, {2, "201"}, {3, "301"}} {
		if _, err := m.db.Exec(
			`INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height) VALUES (?,?,?,?,1,0,0)`,
			dvr.ID, c.ch, c.name, c.ch-1); err != nil {
			t.Fatalf("insert ch %d: %v", c.ch, err)
		}
	}

	// reorder to 3, 1, 2
	if err := m.ReorderChannels(dvr.ID, []int{3, 1, 2}); err != nil {
		t.Fatalf("ReorderChannels: %v", err)
	}
	chs, err := m.ListChannels(dvr.ID) // ordered by display_order
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(chs) != 3 || chs[0].ChNum != 3 || chs[1].ChNum != 1 || chs[2].ChNum != 2 {
		t.Fatalf("order wrong: %+v", chs)
	}

	// rename ch 1 -> lobby
	if err := m.RenameChannel(dvr.ID, 1, "lobby"); err != nil {
		t.Fatalf("RenameChannel: %v", err)
	}
	chs, _ = m.ListChannels(dvr.ID)
	var ok bool
	for _, c := range chs {
		if c.ChNum == 1 && c.Name == "lobby" {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("rename not applied: %+v", chs)
	}
}

func TestChannelMetaTriggersOnChange(t *testing.T) {
	m := newTestSurvManager(t)
	dvr, _ := m.AddDVR("cam", "10.0.0.1", 80, "", 0, "admin", "pw", "isapi", 2000, "sub")
	m.db.Exec(`INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height) VALUES (?,1,'101',0,1,0,0)`, dvr.ID)
	var fired int
	m.onChange = func() { fired++ }
	m.RenameChannel(dvr.ID, 1, "x")
	m.ReorderChannels(dvr.ID, []int{1})
	if fired != 2 {
		t.Fatalf("onChange fired %d times, want 2", fired)
	}
}
