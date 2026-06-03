package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHVRProbe is an opt-in diagnostic against a REAL Hikvision/ISAPI DVR. It
// SKIPS unless HVR_ADDR is set, so it never runs in normal CI.
//
// It dumps the raw ISAPI discovery endpoints (so you can see the exact XML the
// device returns — flat vs nested resolution, Basic vs Digest auth) and then
// runs the production discoverFromDVRISAPI path, printing the channels it finds.
// Use it to debug "why aren't my channels showing up" against any real device.
//
// Credentials come from the environment only and are never printed.
//
//	HVR_ADDR=192.168.0.169 HVR_PORT=80 HVR_USER=admin HVR_PASS='***' \
//	  go test ./ -run TestHVRProbe -v -count=1
func TestHVRProbe(t *testing.T) {
	addr := os.Getenv("HVR_ADDR")
	if addr == "" {
		t.Skip("set HVR_ADDR/HVR_PORT/HVR_USER/HVR_PASS to probe a real DVR")
	}
	port, _ := strconv.Atoi(os.Getenv("HVR_PORT"))
	if port == 0 {
		port = 80
	}
	user := os.Getenv("HVR_USER")
	pass := os.Getenv("HVR_PASS")
	proto := os.Getenv("HVR_PROTOCOL")
	if proto == "" {
		proto = "isapi"
	}

	mgr := &SurveillanceManager{
		client:      &http.Client{Timeout: 10 * time.Second},
		shortClient: &http.Client{Timeout: 5 * time.Second},
	}
	dvr := DVRConfig{ID: 1, Addr: addr, Port: port, Username: user, Password: pass, Protocol: proto}

	t.Logf("=== probing %s:%d (user=%q, pass set=%v, protocol=%q) ===", addr, port, user, pass != "", proto)

	// 1) Raw ISAPI endpoint dump (Basic auth, exactly like production). Reveals
	//    the real resolution XML shape (flat vs nested <Video>) and whether the
	//    device demands Digest (401 + WWW-Authenticate).
	for _, ep := range []string{
		"/ISAPI/System/deviceInfo",
		"/ISAPI/System/Video/inputs/channels",
		"/ISAPI/Streaming/channels",
		"/ISAPI/Streaming/channels/101", // sample channel-1 main-stream resolution
	} {
		u := fmt.Sprintf("http://%s:%d%s", addr, port, ep)
		req, _ := http.NewRequest("GET", u, nil)
		req.SetBasicAuth(user, pass)
		resp, err := mgr.client.Do(req)
		if err != nil {
			t.Logf("\nGET %s\n  -> ERROR: %v", ep, err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 6000))
		resp.Body.Close()
		authNote := ""
		if strings.HasPrefix(strings.ToLower(resp.Header.Get("WWW-Authenticate")), "digest") {
			authNote = "  [DIGEST AUTH REQUIRED]"
		}
		t.Logf("\nGET %s\n  -> HTTP %d%s\n%s", ep, resp.StatusCode, authNote, strings.TrimSpace(string(body)))
	}

	// 2) Production discovery path — exactly what the agent runs.
	t.Logf("\n=== running production discoverFromDVRISAPI ===")
	chs, err := mgr.discoverFromDVRISAPI(dvr)
	t.Logf("RESULT: %d channels, err=%v", len(chs), err)
	for _, c := range chs {
		t.Logf("  ch %-3d %-22q %dx%d", c.ChNum, c.Name, c.Width, c.Height)
	}
	if len(chs) == 0 {
		t.Logf("⚠️  no channels discovered — inspect the raw dump above for the cause")
	}
}
