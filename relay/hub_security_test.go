package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/opsview/opsview/proto"
)

// --- C-1: publisher token validation -------------------------------------

func TestValidPublisherToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                 string
		provided, configured string
		want                 bool
	}{
		{"matching", "s3cret-token", "s3cret-token", true},
		{"mismatch", "wrong", "s3cret-token", false},
		{"empty provided", "", "s3cret-token", false},
		{"empty configured fails closed", "anything", "", false},
		{"both empty fails closed", "", "", false},
	}
	for _, c := range cases {
		if got := validPublisherToken(c.provided, c.configured); got != c.want {
			t.Errorf("%s: validPublisherToken(%q,%q)=%v want %v", c.name, c.provided, c.configured, got, c.want)
		}
	}
}

// End-to-end: a publisher must present the configured RELAY_PUBLISHER_TOKEN
// (not just any non-empty string), and the watcher PIN is the separate value
// the publisher advertises — not the publisher's auth token.
func TestPublisherWatcherAuthEndToEnd(t *testing.T) {
	hub := NewHub(Config{MaxWatcherQueue: 4, PublisherToken: "secret-token"})
	go hub.Run()
	defer hub.Stop()

	mux := http.NewServeMux()
	mux.HandleFunc("/publish", hub.HandlePublish)
	mux.HandleFunc("/watch", hub.HandleWatch)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	// 1) Wrong publisher token is rejected.
	c := dialWS(t, wsURL+"/publish")
	writeMsg(t, c, proto.MsgHello, mustJSON(proto.Hello{Role: "publisher", Client: "test"}))
	writeMsg(t, c, proto.MsgAuth, mustJSON(proto.Auth{Token: "not-the-secret", PIN: "654321"}))
	if got := readMsgType(t, c); got != proto.MsgError {
		t.Fatalf("wrong token: expected MsgError, got %s", got)
	}
	c.Close()

	// 2) Correct token + advertised PIN is accepted.
	pub := dialWS(t, wsURL+"/publish")
	defer pub.Close()
	writeMsg(t, pub, proto.MsgHello, mustJSON(proto.Hello{Role: "publisher", Client: "test"}))
	writeMsg(t, pub, proto.MsgAuth, mustJSON(proto.Auth{Token: "secret-token", PIN: "654321"}))
	if got := readMsgType(t, pub); got != proto.MsgReady {
		t.Fatalf("correct token: expected MsgReady, got %s", got)
	}

	// 3) Watcher presenting the advertised PIN is accepted.
	w1 := dialWS(t, wsURL+"/watch")
	writeMsg(t, w1, proto.MsgHello, mustJSON(proto.Hello{Role: "watcher", Client: "test"}))
	writeMsg(t, w1, proto.MsgAuth, mustJSON(proto.Auth{Token: "654321"}))
	if got := readMsgType(t, w1); got != proto.MsgReady {
		t.Fatalf("watcher correct PIN: expected MsgReady, got %s", got)
	}
	w1.Close()

	// 4) Watcher presenting the publisher's auth token (not the PIN) is rejected,
	//    proving the token and the PIN are decoupled.
	w2 := dialWS(t, wsURL+"/watch")
	writeMsg(t, w2, proto.MsgHello, mustJSON(proto.Hello{Role: "watcher", Client: "test"}))
	writeMsg(t, w2, proto.MsgAuth, mustJSON(proto.Auth{Token: "secret-token"}))
	if got := readMsgType(t, w2); got != proto.MsgError {
		t.Fatalf("watcher using publisher token: expected MsgError, got %s", got)
	}
	w2.Close()
}

// --- C-2: DVR credential redaction ---------------------------------------

func TestRedactSurvConfigPayload(t *testing.T) {
	t.Parallel()
	cfg := proto.SurvConfig{
		DVRs: []proto.DVRInfo{{
			ID: 1, Name: "lobby-cam", Addr: "10.0.0.5", Port: 80,
			Username: "rootuser", Password: "s3cr3t-pw",
		}},
		Channels: []proto.ChannelInfo{{ID: 1, DVRID: 1, ChNum: 1, Name: "ch1"}},
	}
	payload := mustJSON(cfg)

	out, err := redactSurvConfigPayload(payload)
	if err != nil {
		t.Fatalf("redact: %v", err)
	}
	var got proto.SurvConfig
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal redacted: %v", err)
	}
	if got.DVRs[0].Username != "" || got.DVRs[0].Password != "" {
		t.Fatalf("credentials not stripped: %+v", got.DVRs[0])
	}
	// Non-secret fields must survive so clients still render and play streams.
	if got.DVRs[0].Addr != "10.0.0.5" || got.DVRs[0].Name != "lobby-cam" {
		t.Fatalf("non-secret fields lost: %+v", got.DVRs[0])
	}
	if len(got.Channels) != 1 || got.Channels[0].Name != "ch1" {
		t.Fatalf("channels lost in redaction: %+v", got.Channels)
	}
}

// The unauthenticated /api/surv endpoint must (a) require the viewer PIN and
// (b) never return DVR credentials even to an authenticated caller.
func TestHandleSurvConfigRedactsAndRequiresPIN(t *testing.T) {
	hub := NewHub(Config{MaxWatcherQueue: 4, PublisherToken: "x"})
	hub.publisher = &websocket.Conn{} // non-nil marker: a publisher is "connected"
	hub.publisherPIN = "123456"

	cfg := proto.SurvConfig{
		DVRs: []proto.DVRInfo{{
			ID: 1, Name: "lobby-cam", Addr: "192.168.1.64", Port: 80,
			Username: "rootuser", Password: "s3cr3t-pw",
		}},
	}
	hub.survConfig = proto.MarshalMessage(proto.MsgSurvConfig, mustJSON(cfg))

	// No PIN -> 401
	if rec := callSurv(hub, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no pin: expected 401, got %d", rec.Code)
	}
	// Wrong PIN -> 401
	if rec := callSurv(hub, "999999"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong pin: expected 401, got %d", rec.Code)
	}
	// Correct PIN -> 200, redacted
	rec := callSurv(hub, "123456")
	if rec.Code != http.StatusOK {
		t.Fatalf("correct pin: expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "s3cr3t-pw") || strings.Contains(body, "rootuser") {
		t.Fatalf("response leaked DVR credentials: %s", body)
	}
	if !strings.Contains(body, "192.168.1.64") {
		t.Fatalf("response missing non-secret fields: %s", body)
	}
}

// --- test helpers --------------------------------------------------------

func callSurv(hub *Hub, pin string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	url := "/api/surv"
	if pin != "" {
		url += "?pin=" + pin
	}
	hub.HandleSurvConfig(rec, httptest.NewRequest(http.MethodGet, url, nil))
	return rec
}

func dialWS(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	return c
}

func writeMsg(t *testing.T, c *websocket.Conn, typ proto.MessageType, payload []byte) {
	t.Helper()
	if err := c.WriteMessage(websocket.BinaryMessage, proto.MarshalMessage(typ, payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readMsgType(t *testing.T, c *websocket.Conn) proto.MessageType {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	hdr, err := proto.DecodeHeader(data)
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	return hdr.Type
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
