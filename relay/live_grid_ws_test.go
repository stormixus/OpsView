package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func gridTestServer(t *testing.T) (*Hub, string) {
	t.Helper()
	h := NewHub(cfgWithDash("grid-test-secret"))
	h.getOrCreateSession("site-a", "A")
	h.getOrCreateSession("site-b", "B")
	mux := http.NewServeMux()
	h.registerDashboard(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(func() { h.Stop(); server.Close() })
	return h, "ws" + strings.TrimPrefix(server.URL, "http") + "/dashboard/api/live-grid-ws?agent="
}

func gridTestDial(t *testing.T, h *Hub, url string) *websocket.Conn {
	t.Helper()
	header := http.Header{}
	header.Set("Cookie", (&http.Cookie{Name: dashboardCookieName, Value: signSession(h.effectiveDashToken(), time.Now().Add(time.Hour))}).String())
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	conn, response, err := dialer.Dial(url, header)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		t.Fatalf("dial grid: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func seedGridTestStream(t *testing.T, h *Hub, agent, stream string, tag byte) (*survWSHub, [][]byte) {
	t.Helper()
	mux := newFragMuxerH264(testWSSPS, testWSPPS)
	hub := newSurvWSHub()
	hub.setInit(mux.initSeg)
	key, isKey, _ := mux.writeAU(90000, [][]byte{{0x65, tag}})
	part, _, _ := mux.writeAU(93000, [][]byte{{0x41, tag + 1}})
	if len(mux.initSeg) == 0 || len(key) == 0 || len(part) == 0 || !isKey {
		t.Fatal("invalid fMP4 fixture")
	}
	hub.broadcast(key, true)
	hub.broadcast(part, false)
	session := h.sessionByID(agent)
	session.survProxy.mu.Lock()
	session.survProxy.streams[stream] = &streamEntry{id: stream, wsHub: hub}
	session.survProxy.mu.Unlock()
	return hub, [][]byte{mux.initSeg, key, part}
}

func gridTestRead(t *testing.T, conn *websocket.Conn) (uint16, []byte) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	kind, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read grid packet: %v", err)
	}
	if kind != websocket.BinaryMessage || len(data) < 2 {
		t.Fatalf("invalid grid packet: type=%d bytes=%x", kind, data)
	}
	return binary.BigEndian.Uint16(data[:2]), data[2:]
}

func awaitGridCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-deadline.C:
			t.Fatal("grid subscriptions did not reach expected state")
		case <-ticker.C:
		}
	}
}

func TestLiveGridWSAuthenticationAndAgentLookup(t *testing.T) {
	h, base := gridTestServer(t)
	for _, sample := range []struct {
		agent, cookie string
		status        int
	}{{"site-a", "", 401}, {"site-a", "invalid", 401}, {"missing", signSession(h.effectiveDashToken(), time.Now().Add(time.Hour)), 404}} {
		header := http.Header{}
		if sample.cookie != "" {
			header.Set("Cookie", (&http.Cookie{Name: dashboardCookieName, Value: sample.cookie}).String())
		}
		conn, response, err := websocket.DefaultDialer.Dial(base+sample.agent, header)
		if conn != nil {
			conn.Close()
		}
		if err == nil || response == nil || response.StatusCode != sample.status {
			t.Fatalf("authentication/lookup: response=%v error=%v", response, err)
		}
		response.Body.Close()
	}
}

func TestLiveGridWSAgentIsolationAndOrderedGOP(t *testing.T) {
	h, base := gridTestServer(t)
	a, wantA := seedGridTestStream(t, h, "site-a", "dvr1_ch1", 10)
	b, wantB := seedGridTestStream(t, h, "site-a", "dvr1_ch2", 20)
	foreign, _ := seedGridTestStream(t, h, "site-b", "dvr1_ch1", 90)
	foreignOnly, _ := seedGridTestStream(t, h, "site-b", "dvr9_ch9", 100)
	conn := gridTestDial(t, h, base+"site-a")
	for _, control := range []liveGridControl{{Op: "sub", Slot: 3, Stream: "dvr9_ch9"}, {Op: "sub", Slot: 1, Stream: "dvr1_ch1"}, {Op: "sub", Slot: 65535, Stream: "dvr1_ch2"}} {
		if err := conn.WriteJSON(control); err != nil {
			t.Fatal(err)
		}
	}
	got := map[uint16][][]byte{}
	for i := 0; i < 6; i++ {
		slot, data := gridTestRead(t, conn)
		got[slot] = append(got[slot], data)
	}
	for slot, want := range map[uint16][][]byte{1: wantA, 65535: wantB} {
		if len(got[slot]) != len(want) {
			t.Fatalf("slot %d lost/reordered GOP: %d packets", slot, len(got[slot]))
		}
		for i := range want {
			if !bytes.Equal(got[slot][i], want[i]) {
				t.Fatalf("slot %d packet %d changed bytes/order", slot, i)
			}
		}
	}
	if foreign.ClientCount() != 0 || foreignOnly.ClientCount() != 0 || a.ClientCount() != 1 || b.ClientCount() != 1 {
		t.Fatal("subscription escaped agent scope")
	}
	liveA, liveB := []byte{0, 0, 0, 12, 'm', 'o', 'o', 'f', 31}, []byte{0, 0, 0, 12, 'm', 'o', 'o', 'f', 41}
	a.broadcast(liveA, false)
	b.broadcast(liveB, false)
	for i := 0; i < 2; i++ {
		slot, data := gridTestRead(t, conn)
		want := map[uint16][]byte{1: liveA, 65535: liveB}[slot]
		if !bytes.Equal(data, want) {
			t.Fatalf("live packet changed channel or payload: slot=%d payload=%x", slot, data)
		}
	}
	conn.Close()
	awaitGridCondition(t, func() bool { return a.ClientCount()+b.ClientCount() == 0 })
}

func TestLiveGridWSCapReplacementUnsubscribeAndHubStop(t *testing.T) {
	h, base := gridTestServer(t)
	hubs := make([]*survWSHub, 17)
	for i := range hubs {
		hubs[i], _ = seedGridTestStream(t, h, "site-a", fmt.Sprintf("dvr1_ch%d", i+1), byte(i+1))
	}
	conn := gridTestDial(t, h, base+"site-a")
	for i := range hubs {
		if err := conn.WriteJSON(liveGridControl{Op: "sub", Slot: uint16(i + 1), Stream: fmt.Sprintf("dvr1_ch%d", i+1)}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 16*3; i++ {
		slot, _ := gridTestRead(t, conn)
		if slot > 16 {
			t.Fatal("seventeenth subscription exceeded cap")
		}
	}
	for i := 0; i < 16; i++ {
		if hubs[i].ClientCount() != 1 {
			t.Fatalf("expected first sixteen subscriptions, channel %d", i+1)
		}
	}
	if hubs[16].ClientCount() != 0 {
		t.Fatal("seventeenth subscription admitted")
	}
	conn.WriteJSON(liveGridControl{Op: "sub", Slot: 1, Stream: "dvr1_ch17"})
	awaitGridCondition(t, func() bool { return hubs[0].ClientCount() == 0 && hubs[16].ClientCount() == 1 })
	for i := 0; i < 3; i++ {
		if slot, _ := gridTestRead(t, conn); slot != 1 {
			t.Fatal("replacement packet used old slot")
		}
	}
	conn.WriteJSON(liveGridControl{Op: "unsub", Slot: 1})
	awaitGridCondition(t, func() bool { return hubs[16].ClientCount() == 0 })
	hubs[16].broadcast([]byte("must not arrive"), false)
	hubs[1].broadcast([]byte("still subscribed"), false)
	if slot, data := gridTestRead(t, conn); slot != 2 || string(data) != "still subscribed" {
		t.Fatalf("unsubscribe leaked media or stopped unrelated slot: %d %q", slot, data)
	}
	h.Stop()
	awaitGridCondition(t, func() bool {
		for _, hub := range hubs {
			if hub.ClientCount() != 0 {
				return false
			}
		}
		return true
	})
}

func TestLiveGridWSMalformedControlsCloseAndReleaseSubscriptions(t *testing.T) {
	h, base := gridTestServer(t)
	hub, _ := seedGridTestStream(t, h, "site-a", "dvr1_ch1", 10)
	for _, invalid := range []string{
		`not JSON`, `[]`, `{"op":"other","slot":1}`, `{"op":"sub","slot":0,"stream":"dvr1_ch1"}`,
		`{"op":"sub","slot":65536,"stream":"dvr1_ch1"}`, `{"op":"sub","slot":-1,"stream":"dvr1_ch1"}`,
		`{"op":"sub","slot":1.5,"stream":"dvr1_ch1"}`, `{"op":"sub","slot":2,"stream":"site-b/dvr1_ch1"}`,
		`{"op":"sub","slot":2,"stream":"dvr0_ch1"}`, `{"op":"sub","slot":2,"stream":"../dvr1_ch1"}`,
		`{"op":"sub","slot":2,"stream":"` + strings.Repeat("x", 5000) + `"}`,
	} {
		t.Run(fmt.Sprintf("%q", invalid[:min(len(invalid), 60)]), func(t *testing.T) {
			conn := gridTestDial(t, h, base+"site-a")
			conn.WriteJSON(liveGridControl{Op: "sub", Slot: 1, Stream: "dvr1_ch1"})
			for i := 0; i < 3; i++ {
				gridTestRead(t, conn)
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(invalid)); err != nil {
				t.Fatal(err)
			}
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, _, err := conn.ReadMessage(); err == nil {
				t.Fatal("malformed control did not close connection")
			} else {
				var networkError net.Error
				if errors.As(err, &networkError) && networkError.Timeout() {
					t.Fatal("malformed control left the connection open until read deadline")
				}
			}
			conn.Close()
			awaitGridCondition(t, func() bool { return hub.ClientCount() == 0 })
		})
	}
}

// Queue all controls behind the first hub's subscription lock. A websocket
// ping/pong then proves the reader has accepted those controls before releasing
// the writer; the assertion does not depend on TCP timing or future requests.
func TestLiveGridWSInitialMetadataPrecedesAlreadyQueuedMedia(t *testing.T) {
	h, base := gridTestServer(t)
	const subscriptions = 16
	hubs := make([]*survWSHub, subscriptions)
	initial := make(map[uint16][]byte)
	for i := range hubs {
		var seed [][]byte
		hubs[i], seed = seedGridTestStream(t, h, "site-a", fmt.Sprintf("dvr1_ch%d", i+1), byte(i+1))
		initial[uint16(i+1)] = seed[0]
	}
	// A ready channel has substantial queued GOP media as well as its init.
	for i := 0; i < 64; i++ {
		hubs[0].broadcast(bytes.Repeat([]byte{byte(i + 1)}, 1024), false)
	}
	hubs[0].mu.Lock()
	var unlocked sync.Once
	unlock := func() { unlocked.Do(func() { hubs[0].mu.Unlock() }) }
	defer unlock()
	conn := gridTestDial(t, h, base+"site-a")
	pong := make(chan struct{})
	var pongOnce sync.Once
	conn.SetPongHandler(func(payload string) error {
		if payload == "queued-controls" {
			pongOnce.Do(func() { close(pong) })
		}
		return nil
	})
	type received struct {
		slot uint16
		data []byte
		err  error
	}
	packets := make(chan received, subscriptions)
	readerDone := make(chan struct{})
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	go func() {
		defer close(readerDone)
		for i := 0; i < subscriptions; i++ {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				packets <- received{err: err}
				return
			}
			if kind != websocket.BinaryMessage || len(data) < 2 {
				packets <- received{err: fmt.Errorf("unexpected grid packet type %d length %d", kind, len(data))}
				return
			}
			packets <- received{slot: binary.BigEndian.Uint16(data[:2]), data: data[2:]}
		}
	}()
	defer func() { conn.Close(); <-readerDone }()
	for i := 0; i < subscriptions; i++ {
		if err := conn.WriteJSON(liveGridControl{Op: "sub", Slot: uint16(i + 1), Stream: fmt.Sprintf("dvr1_ch%d", i+1)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.WriteControl(websocket.PingMessage, []byte("queued-controls"), time.Now().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pong:
	case <-time.After(2 * time.Second):
		t.Fatal("reader did not accept queued controls behind blocked writer")
	}
	unlock()
	seen := make(map[uint16]bool)
	for i := 0; i < subscriptions; i++ {
		select {
		case packet := <-packets:
			if packet.err != nil {
				t.Fatal(packet.err)
			}
			if seen[packet.slot] || !bytes.Equal(packet.data, initial[packet.slot]) {
				t.Fatalf("queued GOP media overtook another ready subscription's initialization: packet=%d slot=%d bytes=%d", i, packet.slot, len(packet.data))
			}
			seen[packet.slot] = true
		case <-time.After(3 * time.Second):
			t.Fatal("initial metadata was not delivered")
		}
	}
	conn.Close()
	awaitGridCondition(t, func() bool {
		for _, hub := range hubs {
			if hub.ClientCount() != 0 {
				return false
			}
		}
		return true
	})
}
