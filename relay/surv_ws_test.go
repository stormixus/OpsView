package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
)

// A valid H264 SPS/PPS (from mediacommon's fmp4 tests) so Init.Marshal can parse
// codec parameters.
var testWSSPS = []byte{
	0x67, 0x42, 0xc0, 0x28, 0xd9, 0x00, 0x78, 0x02,
	0x27, 0xe5, 0x84, 0x00, 0x00, 0x03, 0x00, 0x04,
	0x00, 0x00, 0x03, 0x00, 0xf0, 0x3c, 0x60, 0xc9,
	0x20,
}
var testWSPPS = []byte{0x08}

func TestAnnexBToAVCC(t *testing.T) {
	au := [][]byte{{0xAA, 0xBB}, {0xCC}}
	got := annexBToAVCC(au)
	want := []byte{0, 0, 0, 2, 0xAA, 0xBB, 0, 0, 0, 1, 0xCC}
	if !bytes.Equal(got, want) {
		t.Fatalf("avcc = % x, want % x", got, want)
	}
	// Lengths must round-trip.
	if binary.BigEndian.Uint32(got[0:4]) != 2 || binary.BigEndian.Uint32(got[6:10]) != 1 {
		t.Fatalf("length prefixes wrong: % x", got)
	}
}

func TestFragMuxerInitIsValidFMP4(t *testing.T) {
	fm := newFragMuxerH264(testWSSPS, testWSPPS)
	if len(fm.initSeg) == 0 {
		t.Fatal("empty init segment")
	}
	// The init segment must parse back as a single-track fMP4 init.
	var parsed fmp4.Init
	if err := parsed.Unmarshal(bytes.NewReader(fm.initSeg)); err != nil {
		t.Fatalf("init does not parse as fMP4: %v", err)
	}
	if len(parsed.Tracks) != 1 || parsed.Tracks[0].TimeScale != survWSTimescale {
		t.Fatalf("parsed init tracks = %d, timescale = %d", len(parsed.Tracks),
			func() uint32 {
				if len(parsed.Tracks) > 0 {
					return parsed.Tracks[0].TimeScale
				}
				return 0
			}())
	}
}

func TestFragMuxerWriteAUKeyframeFlag(t *testing.T) {
	fm := newFragMuxerH264(testWSSPS, testWSPPS)

	// IDR access unit (NAL type 5) -> keyframe.
	idr := [][]byte{{0x65, 0x01, 0x02, 0x03}}
	frag, kf, _ := fm.writeAU(0, idr)
	if len(frag) == 0 {
		t.Fatal("empty fragment for IDR")
	}
	if !kf {
		t.Fatal("IDR au should be a keyframe")
	}

	// Non-IDR slice (NAL type 1) -> not a keyframe.
	nonIDR := [][]byte{{0x41, 0x09, 0x08}}
	frag2, kf2, _ := fm.writeAU(3000, nonIDR)
	if len(frag2) == 0 {
		t.Fatal("empty fragment for non-IDR")
	}
	if kf2 {
		t.Fatal("non-IDR au should not be a keyframe")
	}
}

func TestSurvWSHubGatesUntilKeyframe(t *testing.T) {
	h := newSurvWSHub()
	c := h.add()

	// A non-keyframe fragment before any keyframe must be withheld.
	h.broadcast([]byte("p"), false)
	select {
	case <-c.send:
		t.Fatal("client received a fragment before its first keyframe")
	default:
	}

	// The first keyframe starts the client; subsequent fragments flow.
	h.broadcast([]byte("K"), true)
	h.broadcast([]byte("p2"), false)
	if got := <-c.send; string(got) != "K" {
		t.Fatalf("first delivered = %q, want K", got)
	}
	if got := <-c.send; string(got) != "p2" {
		t.Fatalf("second delivered = %q, want p2", got)
	}
}

// TestSurvWSLive drives the full relay pipeline against a real RTSP stream and
// checks that valid fMP4 init + media fragments come out. Opt-in:
//
//	RELAY_RTSP='rtsp://user:pass@ip:554/Streaming/Channels/101' go test ./ -run TestSurvWSLive -v
func TestSurvWSLive(t *testing.T) {
	rtsp := os.Getenv("RELAY_RTSP")
	if rtsp == "" {
		t.Skip("set RELAY_RTSP to a reachable rtsp:// URL to run")
	}
	sp := NewSurvProxy()
	defer sp.StopAll()
	if err := sp.StartChannel("test", "test", rtsp); err != nil {
		t.Fatalf("StartChannel: %v", err)
	}
	sp.mu.RLock()
	entry := sp.streams["test"]
	sp.mu.RUnlock()
	if entry == nil || entry.wsHub == nil {
		t.Fatal("no wsHub on entry")
	}
	client := entry.wsHub.add()
	defer entry.wsHub.remove(client)

	// The first delivered message is the fMP4 init segment, then fragments.
	deadline := time.After(12 * time.Second)
	var initSeg []byte
	frags := 0
	for frags < 3 {
		select {
		case msg := <-client.send:
			if initSeg == nil {
				initSeg = msg
				var pi fmp4.Init
				if err := pi.Unmarshal(bytes.NewReader(initSeg)); err != nil {
					t.Fatalf("first message is not a valid fMP4 init: %v", err)
				}
				t.Logf("init %d bytes, %d track(s)", len(initSeg), len(pi.Tracks))
			} else if len(msg) == 0 {
				t.Fatal("empty fragment")
			} else {
				frags++
			}
		case <-deadline:
			t.Fatalf("timeout: gotInit=%v frags=%d", initSeg != nil, frags)
		}
	}
	t.Logf("OK: received fMP4 init + %d fragments from real RTSP", frags)
}
