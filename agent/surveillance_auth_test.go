package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A Hikvision DVR that rejects credentials (401 — wrong password, or an IP lock
// from repeated failed logins) must be classified ISAPI and report a clear auth
// error — NOT fall through to an RTSP probe and report "no RTSP channels found".
func TestISAPIAuthFailureReportsAuthNotRTSP(t *testing.T) {
	mux := http.NewServeMux()
	// Every ISAPI endpoint demands auth and rejects it.
	mux.HandleFunc("/ISAPI/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	host, port, err := splitTestHostPort(server.Listener.Addr())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	mgr := &SurveillanceManager{client: server.Client(), shortClient: server.Client()}
	dvr := DVRConfig{ID: 1, Addr: host, Port: port, Username: "admin", Password: "wrong"}

	// Probe must classify a 401 ISAPI endpoint as Hikvision (isapi), not rtsp.
	if proto := mgr.probeDVRProtocol(dvr); proto != "isapi" {
		t.Fatalf("probeDVRProtocol on 401 = %q, want \"isapi\"", proto)
	}

	// Discovery must surface the auth error, not a generic/RTSP failure.
	_, derr := mgr.discoverFromDVRISAPI(dvr)
	if !errors.Is(derr, errDVRAuth) {
		t.Fatalf("discoverFromDVRISAPI on 401 = %v, want errDVRAuth", derr)
	}
}
