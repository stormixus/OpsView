package main

import "testing"

func TestRecordHiresMigrationBackfillsMainDVRs(t *testing.T) {
	// Create DB + base tables but without the record_hires column yet
	m := newTestSurvManager(t)

	// Drop the record_hires column to simulate pre-migration state
	m.db.Exec(`ALTER TABLE channels DROP COLUMN record_hires`)

	// Add DVRs and channels in the "old" schema (no record_hires column)
	dvrMain, _ := m.AddDVR("mainDVR", "1.2.3.4", 80, "", 0, "u", "p", "isapi", 5, "main")
	_, err := m.db.Exec(`INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height) VALUES (?,1,'101',0,1,1920,1080)`, dvrMain.ID)
	if err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	dvrSub, _ := m.AddDVR("subDVR", "1.2.3.5", 80, "", 0, "u", "p", "isapi", 5, "sub")
	_, err = m.db.Exec(`INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height) VALUES (?,1,'201',0,1,1920,1080)`, dvrSub.ID)
	if err != nil {
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

	// Re-run migration again to prove idempotency (doesn't re-backfill)
	m.migrate()

	if got := m.channelHiRes(dvrMain.ID, 1); !got {
		t.Errorf("after second migration: main-DVR channel: record_hires = false, want true (idempotency check)")
	}
	if got := m.channelHiRes(dvrSub.ID, 1); got {
		t.Errorf("after second migration: sub-DVR channel: record_hires = true, want false (idempotency check)")
	}
}

func TestSetChannelHiResRoundTrips(t *testing.T) {
	m := newTestSurvManager(t)
	dvr, _ := m.AddDVR("d", "1.2.3.4", 80, "", 0, "u", "p", "isapi", 5, "sub")
	_, err := m.db.Exec(`INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height) VALUES (?,1,'101',0,1,1920,1080)`, dvr.ID)
	if err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	if err := m.SetChannelHiRes(dvr.ID, 1, true); err != nil {
		t.Fatalf("SetChannelHiRes: %v", err)
	}
	if !m.channelHiRes(dvr.ID, 1) {
		t.Errorf("after SetChannelHiRes(true): got false")
	}
}
