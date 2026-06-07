package main

import "testing"

// drain reads all currently-buffered messages from a client without blocking.
func drain(c *survWSClient) [][]byte {
	var out [][]byte
	for {
		select {
		case m := <-c.send:
			out = append(out, m)
		default:
			return out
		}
	}
}

// A client joining mid-GOP must be fast-started with init + the whole current GOP
// (keyframe + the P-frames since it) so it decodes immediately, never init-less.
func TestSurvWSGOPFastStart(t *testing.T) {
	h := newSurvWSHub()
	init := []byte("INIT")
	kf := []byte("KEYFRAME")
	p1, p2 := []byte("P1"), []byte("P2")
	h.setInit(init)
	h.broadcast(kf, true)
	h.broadcast(p1, false)
	h.broadcast(p2, false)

	c := h.add()
	if !c.started {
		t.Fatal("client joining mid-GOP should be started immediately")
	}
	got := drain(c)
	want := [][]byte{init, kf, p1, p2}
	if len(got) != len(want) {
		t.Fatalf("seeded %d msgs, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if string(got[i]) != string(want[i]) {
			t.Fatalf("msg %d = %q, want %q", i, got[i], want[i])
		}
	}
	if string(got[0]) != "INIT" || string(got[1]) != "KEYFRAME" {
		t.Fatalf("must lead with init then keyframe, got %q,%q", got[0], got[1])
	}
}

// A client joining before any keyframe (empty GOP) is NOT fast-started — it waits
// for the next keyframe via broadcast's gate (never gets frags without a keyframe).
func TestSurvWSNoFastStartBeforeKeyframe(t *testing.T) {
	h := newSurvWSHub()
	h.setInit([]byte("INIT"))
	c := h.add()
	if c.started {
		t.Fatal("client joining before any keyframe must not be started")
	}
	if msgs := drain(c); len(msgs) != 0 {
		t.Fatalf("unstarted client should have no seeded msgs, got %q", msgs)
	}
	// a non-keyframe frag must not start it...
	h.broadcast([]byte("P"), false)
	if c.started || len(drain(c)) != 0 {
		t.Fatal("non-keyframe must not start a waiting client")
	}
	// ...but the next keyframe seeds init + keyframe.
	h.broadcast([]byte("KF"), true)
	if !c.started {
		t.Fatal("keyframe should start the waiting client")
	}
	got := drain(c)
	if len(got) != 2 || string(got[0]) != "INIT" || string(got[1]) != "KF" {
		t.Fatalf("want [INIT KF], got %q", got)
	}
}

// A GOP exceeding the cap disables fast-start (cache cleared) so a stream with no
// keyframes / a pathological GOP can't grow the cache unbounded or seed a client
// with a leading non-keyframe.
func TestSurvWSGOPCapDisablesFastStart(t *testing.T) {
	h := newSurvWSHub()
	h.setInit([]byte("INIT"))
	h.broadcast([]byte("KF"), true)
	for i := 0; i < gopMaxFrags+5; i++ {
		h.broadcast([]byte("P"), false)
	}
	if h.gop != nil {
		t.Fatalf("GOP past cap should be cleared, len=%d", len(h.gop))
	}
	c := h.add()
	if c.started {
		t.Fatal("with cleared GOP the client must not be fast-started")
	}
}
