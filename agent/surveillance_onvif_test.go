package main

import (
	"fmt"
	"strings"
	"testing"
)

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

func TestDiscoverFromDVROnvif(t *testing.T) {
	srv := onvifTestServer(t) // reuse helper from onvif_test.go (same package)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	parts := strings.Split(host, ":")
	port := 0
	if len(parts) == 2 {
		_, _ = fmtSscan(parts[1], &port)
	}
	m := newTestSurvManager(t)
	m.client = srv.Client()
	m.shortClient = srv.Client()
	dvr := DVRConfig{ID: 5, Addr: parts[0], Port: port, Username: "admin", Password: "test123", Protocol: "onvif"}

	chans, err := m.discoverFromDVROnvif(dvr)
	if err != nil {
		t.Fatalf("discoverFromDVROnvif: %v", err)
	}
	if len(chans) != 2 {
		t.Fatalf("got %d channels, want 2", len(chans))
	}
	if chans[0].RtspURI == "" || chans[0].ChNum != 1 {
		t.Fatalf("chan0 = %+v", chans[0])
	}
}

func fmtSscan(s string, p *int) (int, error) { return fmt.Sscanf(s, "%d", p) }
