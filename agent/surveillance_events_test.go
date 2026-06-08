package main

import "testing"

func TestISAPIEventDVRs(t *testing.T) {
	m := NewSurveillanceManager()
	defer m.Shutdown()

	// Add ISAPI DVR
	isapiDVR, err := m.AddDVR("isapi-dvr", "10.0.0.1", 80, "", 0, "u", "p", "isapi", 30, "main")
	if err != nil {
		t.Fatalf("add isapi: %v", err)
	}

	// Add RTSP DVR (should be filtered out)
	_, err = m.AddDVR("rtsp-dvr", "10.0.0.2", 80, "", 0, "u", "p", "rtsp", 30, "main")
	if err != nil {
		t.Fatalf("add rtsp: %v", err)
	}

	// Give the ISAPI DVR a channel so it qualifies
	_, err = m.db.Exec(`INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height, rtsp_uri, snapshot_uri)
		VALUES (?, 3, 'c', 0, 1, 1920, 1080, '', '')`, isapiDVR.ID)
	if err != nil {
		t.Fatalf("insert channel: %v", err)
	}

	got := m.ISAPIEventDVRs()

	// Filter to only the DVR we just added
	var filtered []isapiEventDVR
	for _, ed := range got {
		if ed.dvr.ID == isapiDVR.ID {
			filtered = append(filtered, ed)
		}
	}

	if len(filtered) != 1 {
		t.Fatalf("ISAPIEventDVRs for our test DVR = %d, want 1", len(filtered))
	}
	if filtered[0].dvr.Protocol != "isapi" || len(filtered[0].chNums) != 1 || filtered[0].chNums[0] != 3 {
		t.Fatalf("unexpected: protocol=%s, chNums=%v", filtered[0].dvr.Protocol, filtered[0].chNums)
	}

	// Verify RTSP DVR was excluded (not in the full list)
	for _, ed := range got {
		if ed.dvr.Protocol != "isapi" {
			t.Fatalf("non-ISAPI DVR in results: protocol=%s", ed.dvr.Protocol)
		}
	}
}
