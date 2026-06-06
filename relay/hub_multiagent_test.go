package main

import (
	"testing"

	"github.com/gorilla/websocket"
)

// cfgWithToken builds a Config with a default-only agent registry.
func cfgWithToken(token string) Config {
	reg, _ := parseAgentRegistry("", token)
	return Config{PublisherToken: token, MaxWatcherQueue: 4, Agents: reg}
}

func testConfig() Config { return cfgWithToken("tok") }

func TestHubSessionLifecycle(t *testing.T) {
	h := NewHub(testConfig())
	s := h.getOrCreateSession("gangnam", "강남점")
	if s == nil || s.id != "gangnam" {
		t.Fatal("getOrCreateSession failed")
	}
	if got := h.getOrCreateSession("gangnam", "강남점"); got != s {
		t.Fatal("must return the same session for same id")
	}
	// resolve by PIN (requires an online publisher)
	s.mu.Lock()
	s.publisher = &websocket.Conn{}
	s.pin = "481922"
	s.mu.Unlock()
	if found := h.sessionByPIN("481922"); found != s {
		t.Fatal("sessionByPIN must find the online session")
	}
	if h.sessionByPIN("000000") != nil {
		t.Fatal("unknown PIN must resolve to nil")
	}
}

func TestSessionByPINIsolation(t *testing.T) {
	h := NewHub(testConfig())
	a := h.getOrCreateSession("a", "A")
	b := h.getOrCreateSession("b", "B")
	a.mu.Lock()
	a.publisher = &websocket.Conn{}
	a.pin = "111111"
	a.mu.Unlock()
	b.mu.Lock()
	b.publisher = &websocket.Conn{}
	b.pin = "222222"
	b.mu.Unlock()
	if h.sessionByPIN("111111") != a {
		t.Fatal("PIN 111111 must resolve to session a")
	}
	if h.sessionByPIN("222222") != b {
		t.Fatal("PIN 222222 must resolve to session b")
	}
}

func TestSurvAgentSplit(t *testing.T) {
	h := NewHub(testConfig())
	h.getOrCreateSession("gangnam", "강남점")
	id, rest := h.splitSurvPath("gangnam/dvr1_ch1/index.m3u8")
	if id != "gangnam" || rest != "dvr1_ch1/index.m3u8" {
		t.Fatalf("split named => %q,%q", id, rest)
	}
	id2, rest2 := h.splitSurvPath("dvr1_ch1/index.m3u8")
	if id2 != "default" || rest2 != "dvr1_ch1/index.m3u8" {
		t.Fatalf("split flat => %q,%q", id2, rest2)
	}
}
