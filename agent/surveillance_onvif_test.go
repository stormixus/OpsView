package main

import "testing"

func TestChannelStoresURIs(t *testing.T) {
	m := newTestSurvManager(t)
	id, err := m.AddDVR("cam", "10.0.0.9", 80, "", 0, "admin", "pw", "onvif", 2000, "sub")
	if err != nil {
		t.Fatalf("AddDVR: %v", err)
	}
	_, err = m.db.Exec(`INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height, rtsp_uri, snapshot_uri)
		VALUES (?, 1, 'ch1', 0, 1, 1920, 1080, 'rtsp://x/live/ch1', 'http://x/snap.jpg')`, id.ID)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	chs, err := m.ListChannels(id.ID)
	if err != nil || len(chs) != 1 {
		t.Fatalf("ListChannels = %d (%v)", len(chs), err)
	}
	if chs[0].RtspURI != "rtsp://x/live/ch1" || chs[0].SnapshotURI != "http://x/snap.jpg" {
		t.Fatalf("uris not loaded: %+v", chs[0])
	}
}
