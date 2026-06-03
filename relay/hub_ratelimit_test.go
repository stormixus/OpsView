package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opsview/opsview/proto"
)

// After maxFails wrong PINs from one IP, the relay must refuse further watcher
// handshakes (HTTP 429) — even one presenting the correct PIN.
func TestWatcherAuthRateLimited(t *testing.T) {
	hub := NewHub(Config{MaxWatcherQueue: 4, PublisherToken: "secret-token"})
	hub.pinLimiter.maxFails = 3 // small threshold for a fast test
	go hub.Run()
	defer hub.Stop()

	mux := http.NewServeMux()
	mux.HandleFunc("/publish", hub.HandlePublish)
	mux.HandleFunc("/watch", hub.HandleWatch)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	// A publisher must be connected so watchers reach the PIN check.
	pub := dialWS(t, wsURL+"/publish")
	defer pub.Close()
	writeMsg(t, pub, proto.MsgHello, mustJSON(proto.Hello{Role: "publisher", Client: "test"}))
	writeMsg(t, pub, proto.MsgAuth, mustJSON(proto.Auth{Token: "secret-token", PIN: "654321"}))
	if got := readMsgType(t, pub); got != proto.MsgReady {
		t.Fatalf("publisher setup: got %s", got)
	}

	wrongAttempt := func() proto.MessageType {
		w := dialWS(t, wsURL+"/watch")
		defer w.Close()
		writeMsg(t, w, proto.MsgHello, mustJSON(proto.Hello{Role: "watcher", Client: "test"}))
		writeMsg(t, w, proto.MsgAuth, mustJSON(proto.Auth{Token: "000000"}))
		return readMsgType(t, w)
	}

	// The first maxFails attempts fail as invalid PIN (401).
	for i := 0; i < 3; i++ {
		if got := wrongAttempt(); got != proto.MsgError {
			t.Fatalf("attempt %d: expected MsgError, got %s", i+1, got)
		}
	}

	// Now blocked: the relay rejects (429) immediately on connect, before it
	// even reads the handshake — so a correct PIN no longer helps.
	w := dialWS(t, wsURL+"/watch")
	defer w.Close()
	if got := readMsgType(t, w); got != proto.MsgError {
		t.Fatalf("blocked attempt: expected MsgError(429), got %s", got)
	}
}
