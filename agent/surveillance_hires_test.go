package main

import "testing"

func TestRecordHiresMigrationBackfillsMainDVRs(t *testing.T) {
	// Create DB + base tables but without the record_hires column yet
	m := newTestSurvManager(t)

	// Drop the record_hires column to simulate pre-migration state
	if _, err := m.db.Exec(`ALTER TABLE channels DROP COLUMN record_hires`); err != nil {
		t.Fatalf("drop record_hires column: %v", err)
	}

	// Add DVRs and channels in the "old" schema (no record_hires column)
	dvrMain, err := m.AddDVR("mainDVR", "1.2.3.4", 80, "", 0, "u", "p", "isapi", 5, "main")
	if err != nil {
		t.Fatalf("AddDVR mainDVR: %v", err)
	}
	if _, err := m.db.Exec(`INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height) VALUES (?,1,'101',0,1,1920,1080)`, dvrMain.ID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	dvrSub, err := m.AddDVR("subDVR", "1.2.3.5", 80, "", 0, "u", "p", "isapi", 5, "sub")
	if err != nil {
		t.Fatalf("AddDVR subDVR: %v", err)
	}
	if _, err := m.db.Exec(`INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height) VALUES (?,1,'201',0,1,1920,1080)`, dvrSub.ID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	// Re-run migration to trigger the backfill
	m.migrate()

	// Now check the backfill worked
	if got := m.channelHiRes(dvrMain.ID, 1); !got {
		t.Errorf("main-DVR channel: record_hires = false, want true (backfill)")
	}
	if got := m.channelHiRes(dvrSub.ID, 1); got {
		t.Errorf("sub-DVR channel: record_hires = true, want false")
	}

	// Core invariant: a user toggle-off must survive a migration re-run. The
	// columnExists gate exists precisely so the backfill never re-fires and
	// clobbers an operator's deliberate toggle-off on restart.
	if err := m.SetChannelHiRes(dvrMain.ID, 1, false); err != nil {
		t.Fatalf("SetChannelHiRes(false): %v", err)
	}
	if m.channelHiRes(dvrMain.ID, 1) {
		t.Fatalf("pre-condition: record_hires = true after toggle-off, want false")
	}

	// Re-run migration: must NOT re-backfill and clobber the user's toggle-off.
	m.migrate()

	if m.channelHiRes(dvrMain.ID, 1) {
		t.Errorf("after migration re-run: main-DVR record_hires = true, want false (backfill clobbered user toggle-off)")
	}
	if got := m.channelHiRes(dvrSub.ID, 1); got {
		t.Errorf("after migration re-run: sub-DVR record_hires = true, want false")
	}
}

func TestSetChannelHiResRoundTrips(t *testing.T) {
	m := newTestSurvManager(t)
	dvr, err := m.AddDVR("d", "1.2.3.4", 80, "", 0, "u", "p", "isapi", 5, "sub")
	if err != nil {
		t.Fatalf("AddDVR: %v", err)
	}
	if _, err := m.db.Exec(`INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height) VALUES (?,1,'101',0,1,1920,1080)`, dvr.ID); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if err := m.SetChannelHiRes(dvr.ID, 1, true); err != nil {
		t.Fatalf("SetChannelHiRes: %v", err)
	}
	if !m.channelHiRes(dvr.ID, 1) {
		t.Errorf("after SetChannelHiRes(true): got false")
	}
}
