package main

import (
	"testing"
)

func TestSurvWSClientCount(t *testing.T) {
	h := newSurvWSHub()
	if h.ClientCount() != 0 {
		t.Fatalf("empty hub count=%d want 0", h.ClientCount())
	}
	c := h.add()
	if h.ClientCount() != 1 {
		t.Fatalf("after add count=%d want 1", h.ClientCount())
	}
	h.remove(c)
	if h.ClientCount() != 0 {
		t.Fatalf("after remove count=%d want 0", h.ClientCount())
	}
}

func TestFragMuxerCodec(t *testing.T) {
	sps := []byte{0x67, 0x42, 0xc0, 0x28, 0xd9, 0x00, 0x78, 0x02, 0x27, 0xe5, 0x84, 0x00, 0x00, 0x03, 0x00, 0x04, 0x00, 0x00, 0x03, 0x00, 0xf0, 0x3c, 0x60, 0xc9, 0x20}
	pps := []byte{0x08}
	m := newFragMuxerH264(sps, pps)
	if got := m.Codec(); got != "h264" {
		t.Fatalf("Codec()=%q want h264", got)
	}
}

func TestSessionWatcherList(t *testing.T) {
	s := newAgentSession("a", "A")
	w := &Watcher{id: 7, ip: "1.2.3.4:55", send: make(chan []byte, 1)}
	s.watchers[w] = struct{}{}
	list := s.watcherList()
	if len(list) != 1 || list[0].ID != 7 || list[0].IP != "1.2.3.4:55" {
		t.Fatalf("watcherList=%+v", list)
	}
}
