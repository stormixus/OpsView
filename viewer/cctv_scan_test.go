package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestListScanSubnetsIPv4(t *testing.T) {
	t.Parallel()
	m := &CCTVManager{}
	subnets, err := m.ListScanSubnets()
	if err != nil {
		t.Fatalf("ListScanSubnets: %v", err)
	}
	for _, s := range subnets {
		_, _, err := net.ParseCIDR(s)
		if err != nil {
			t.Errorf("invalid CIDR %q: %v", s, err)
		}
	}
}

func TestProbeHostOnLANDetectsISAPI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ISAPI/System/deviceInfo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	host, port, err := splitTestHostPort(server.Listener.Addr())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}

	m := &CCTVManager{
		client:      server.Client(),
		shortClient: server.Client(),
	}
	hits := make(chan lanScanHit, 4)
	m.probeHostOnLAN(context.Background(), host, []int{port}, "admin", "", hits)
	close(hits)
	var found bool
	for h := range hits {
		if h.addr == host && h.port == port && h.protocol == "isapi" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected isapi hit for %s:%d", host, port)
	}
}

func TestProbeReachableISAPI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ISAPI/System/deviceInfo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	host, port, err := splitTestHostPort(server.Listener.Addr())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	m := &CCTVManager{
		client:      server.Client(),
		shortClient: server.Client(),
	}
	if !m.ProbeReachable(host, port, "admin", "") {
		t.Fatal("ProbeReachable expected true for mock ISAPI")
	}
}

func TestProbeReachableUnreachable(t *testing.T) {
	t.Parallel()
	m := &CCTVManager{
		shortClient: &http.Client{Timeout: 500 * time.Millisecond},
	}
	if m.ProbeReachable("192.0.2.1", 80, "admin", "") {
		t.Fatal("ProbeReachable expected false for TEST-NET-1")
	}
}
