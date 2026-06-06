package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opsview/opsview/proto"
)

func cfgWithRegistry(t *testing.T, jsonStr, legacy string) Config {
	t.Helper()
	reg, err := parseAgentRegistry(jsonStr, legacy)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return Config{PublisherToken: legacy, MaxWatcherQueue: 4, Agents: reg}
}

func startRelay(t *testing.T, cfg Config) (*Hub, string) {
	t.Helper()
	hub := NewHub(cfg)
	go hub.Run()
	t.Cleanup(hub.Stop)
	mux := http.NewServeMux()
	mux.HandleFunc("/publish", hub.HandlePublish)
	mux.HandleFunc("/watch", hub.HandleWatch)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return hub, "ws" + strings.TrimPrefix(srv.URL, "http")
}

// connectPublisher dials /publish, performs the HELLO/AUTH handshake, and
// asserts the result message type.
func connectPublisher(t *testing.T, wsURL, agentID, token, pin string, want proto.MessageType) {
	t.Helper()
	c := dialWS(t, wsURL+"/publish")
	t.Cleanup(func() { c.Close() })
	writeMsg(t, c, proto.MsgHello, mustJSON(proto.Hello{Role: "publisher", Client: "test", AgentID: agentID}))
	writeMsg(t, c, proto.MsgAuth, mustJSON(proto.Auth{Token: token, PIN: pin}))
	if got := readMsgType(t, c); got != want {
		t.Fatalf("publisher agent=%q: got %s want %s", agentID, got, want)
	}
}

func watchWithPIN(t *testing.T, wsURL, pin string) proto.MessageType {
	t.Helper()
	w := dialWS(t, wsURL+"/watch")
	t.Cleanup(func() { w.Close() })
	writeMsg(t, w, proto.MsgHello, mustJSON(proto.Hello{Role: "watcher", Client: "test"}))
	writeMsg(t, w, proto.MsgAuth, mustJSON(proto.Auth{Token: pin}))
	return readMsgType(t, w)
}

// Backward compat: a publisher that omits agent_id lands in the default session,
// and a watcher with the advertised PIN attaches to it.
func TestLegacyPublisherDefaultSession(t *testing.T) {
	hub, wsURL := startRelay(t, cfgWithToken("legacy"))
	connectPublisher(t, wsURL, "", "legacy", "654321", proto.MsgReady)

	if s := hub.sessionByID("default"); s == nil || !s.online() {
		t.Fatal("legacy publisher must occupy the online default session")
	}
	if got := watchWithPIN(t, wsURL, "654321"); got != proto.MsgReady {
		t.Fatalf("watcher with default PIN: got %s want MsgReady", got)
	}
}

// Two tenants connect with distinct agent_ids + PINs. A watcher's PIN routes it
// to exactly one tenant; the other tenant's PIN must not resolve to it.
func TestTwoAgentsIsolationEndToEnd(t *testing.T) {
	cfg := cfgWithRegistry(t, `[{"id":"a","name":"A","token":"tA"},{"id":"b","name":"B","token":"tB"}]`, "legacy")
	hub, wsURL := startRelay(t, cfg)

	connectPublisher(t, wsURL, "a", "tA", "111111", proto.MsgReady)
	connectPublisher(t, wsURL, "b", "tB", "222222", proto.MsgReady)

	if got := watchWithPIN(t, wsURL, "111111"); got != proto.MsgReady {
		t.Fatalf("watcher PIN 111111: got %s want MsgReady", got)
	}
	// Isolation: PIN 111111 resolves to session a, never session b.
	sa := hub.sessionByID("a")
	sb := hub.sessionByID("b")
	if hub.sessionByPIN("111111") != sa {
		t.Fatal("PIN 111111 must resolve to tenant a")
	}
	if hub.sessionByPIN("111111") == sb {
		t.Fatal("isolation breach: a's PIN resolved to tenant b")
	}
	// A bogus PIN attaches to no tenant.
	if got := watchWithPIN(t, wsURL, "999999"); got != proto.MsgError {
		t.Fatalf("watcher bogus PIN: got %s want MsgError", got)
	}
}

// A second agent advertising a PIN already in use by another online agent is
// rejected (409), keeping the PIN namespace globally unique.
func TestPINConflictRejected(t *testing.T) {
	cfg := cfgWithRegistry(t, `[{"id":"a","token":"tA"},{"id":"c","token":"tC"}]`, "legacy")
	_, wsURL := startRelay(t, cfg)

	connectPublisher(t, wsURL, "a", "tA", "111111", proto.MsgReady)
	// agent c tries to reuse a's PIN -> error
	connectPublisher(t, wsURL, "c", "tC", "111111", proto.MsgError)
}
