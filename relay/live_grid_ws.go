package main

import (
	"encoding/binary"
	"net/http"
	"reflect"
	"time"

	"github.com/gorilla/websocket"
)

const liveGridMaxSubscriptions = 16

// The dashboard grid shares a single handshake/connection. JSON controls add or
// remove a uint16 slot; binary replies prefix that slot to an unchanged fMP4
// message. The existing per-channel GOP caches and bounded client queues remain
// authoritative. Fullscreen and older clients retain the single-stream route.
type liveGridControl struct {
	Op     string `json:"op"`
	Slot   uint16 `json:"slot"`
	Stream string `json:"stream,omitempty"`
}

type liveGridSubscription struct {
	hub     *survWSHub
	client  *survWSClient
	initial bool
}

func (h *Hub) HandleDashboardLiveGridWS(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	session := h.sessionByID(r.URL.Query().Get("agent"))
	if session == nil || session.survProxy == nil {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	controls := make(chan liveGridControl, liveGridMaxSubscriptions)
	done, writerDone := make(chan struct{}), make(chan struct{})
	defer func() {
		close(done)
		conn.Close()
		<-writerDone
	}()
	go func() {
		defer close(writerDone)
		defer conn.Close()
		subs := make(map[uint16]liveGridSubscription)
		remove := func(slot uint16) {
			if sub, ok := subs[slot]; ok {
				sub.hub.remove(sub.client)
				delete(subs, slot)
			}
		}
		defer func() {
			for slot := range subs {
				remove(slot)
			}
		}()
		apply := func(control liveGridControl) {
			if control.Op == "unsub" {
				remove(control.Slot)
				return
			}
			if _, exists := subs[control.Slot]; !exists && len(subs) >= liveGridMaxSubscriptions {
				return
			}
			remove(control.Slot)
			session.survProxy.mu.RLock()
			entry := session.survProxy.streams[control.Stream]
			var hub *survWSHub
			if entry != nil {
				hub = entry.wsHub
			}
			session.survProxy.mu.RUnlock()
			if hub != nil {
				subs[control.Slot] = liveGridSubscription{hub: hub, client: hub.add(), initial: true}
			}
		}
		write := func(slot uint16, fragment []byte) error {
			packet := make([]byte, 2+len(fragment))
			binary.BigEndian.PutUint16(packet, slot)
			copy(packet[2:], fragment)
			conn.SetWriteDeadline(time.Now().Add(survWSWriteWait))
			return conn.WriteMessage(websocket.BinaryMessage, packet)
		}
		ping := time.NewTicker(survWSPingPeriod)
		defer ping.Stop()
		for {
			// Apply pending viewport changes before sending old media. Each stream
			// is then selected fairly instead of draining one whole seeded GOP.
			select {
			case control := <-controls:
				apply(control)
				continue
			default:
			}
			// Initialization is small but required before MSE can accept media.
			// Deliver it before an established channel's larger GOP backlog can
			// consume the shared connection's initial congestion window.
			initialized := false
			for slot, sub := range subs {
				if !sub.initial {
					continue
				}
				select {
				case fragment, ok := <-sub.client.send:
					if !ok {
						remove(slot)
						continue
					}
					sub.initial = false
					subs[slot] = sub
					if err := write(slot, fragment); err != nil {
						return
					}
					initialized = true
				default:
				}
			}
			if initialized {
				continue
			}
			cases := []reflect.SelectCase{
				{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(controls)},
				{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(done)},
				{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(h.done)},
				{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ping.C)},
			}
			slots := make([]uint16, 0, len(subs))
			for slot, sub := range subs {
				slots = append(slots, slot)
				cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(sub.client.send)})
			}
			index, value, ok := reflect.Select(cases)
			switch index {
			case 0:
				apply(value.Interface().(liveGridControl))
			case 1, 2:
				return
			case 3:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(survWSWriteWait)); err != nil {
					return
				}
			default:
				slot := slots[index-4]
				if !ok {
					remove(slot)
					continue
				}
				fragment := value.Interface().([]byte)
				if sub := subs[slot]; sub.initial {
					sub.initial = false
					subs[slot] = sub
				}
				if err := write(slot, fragment); err != nil {
					return
				}
			}
		}
	}()
	conn.SetReadLimit(4096)
	conn.SetReadDeadline(time.Now().Add(survWSPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(survWSPongWait))
	})
	for {
		var control liveGridControl
		if err := conn.ReadJSON(&control); err != nil {
			return
		}
		if control.Slot == 0 || (control.Op != "sub" && control.Op != "unsub") {
			return
		}
		if control.Op == "sub" {
			dvr, ch, ok := dvrChOf(control.Stream)
			if !ok || dvr <= 0 || ch <= 0 {
				return
			}
		}
		select {
		case controls <- control:
		case <-writerDone:
			return
		case <-h.done:
			return
		}
	}
}
