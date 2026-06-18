package main

import (
	"os"
	"reflect"
	"testing"

	"github.com/opsview/opsview/proto"
)

// splitNALUnits must split an Annex-B byte run into complete NAL units (start
// codes stripped) and return the trailing, not-yet-terminated NAL as rest so a
// streaming reader can resume on the next chunk.
func TestSplitNALUnits_MixedStartCodes(t *testing.T) {
	// [00 00 01] SPS(67 AA) [00 00 00 01] PPS(68 BB) [00 00 01] IDR(65 CC ...)
	data := []byte{0, 0, 1, 0x67, 0xAA, 0, 0, 0, 1, 0x68, 0xBB, 0, 0, 1, 0x65, 0xCC}
	nals, rest := splitNALUnits(data)
	want := [][]byte{{0x67, 0xAA}, {0x68, 0xBB}}
	if !reflect.DeepEqual(nals, want) {
		t.Fatalf("nals = %v, want %v", nals, want)
	}
	// The final IDR is not yet terminated by a following start code -> it stays in rest.
	if !reflect.DeepEqual(rest, []byte{0, 0, 1, 0x65, 0xCC}) {
		t.Fatalf("rest = %v, want trailing start code + IDR", rest)
	}
}

func TestSplitNALUnits_NoStartCode(t *testing.T) {
	data := []byte{0x67, 0xAA, 0xBB}
	nals, rest := splitNALUnits(data)
	if len(nals) != 0 {
		t.Fatalf("nals = %v, want none", nals)
	}
	if !reflect.DeepEqual(rest, data) {
		t.Fatalf("rest = %v, want all bytes back", rest)
	}
}

// groupNALsIntoAUs groups a NAL sequence into access units, starting a new AU at
// each Access Unit Delimiter (NAL type 9) and dropping the AUD itself.
func TestGroupNALsIntoAUs_SplitsOnAUD(t *testing.T) {
	aud := []byte{0x09, 0x10}
	sps := []byte{0x67, 0xAA}
	pps := []byte{0x68, 0xBB}
	idr := []byte{0x65, 0xCC}
	non := []byte{0x41, 0xDD}
	got := groupNALsIntoAUs([][]byte{aud, sps, pps, idr, aud, non})
	want := [][][]byte{{sps, pps, idr}, {non}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("aus = %v, want %v", got, want)
	}
}

func TestNalType(t *testing.T) {
	cases := map[byte]byte{0x67: 7, 0x68: 8, 0x65: 5, 0x41: 1, 0x09: 9, 0x06: 6}
	for first, want := range cases {
		if got := nalType([]byte{first, 0x00}); got != want {
			t.Fatalf("nalType(%#x) = %d, want %d", first, got, want)
		}
	}
}

// The transcode-live opt-in is per-DVR via RELAY_TRANSCODE_DVR: either the numeric
// DVR id (matched against "dvr<id>_ch<n>" stream ids) or the DVR's host/IP (matched
// against its LAN addr). Unset disables it entirely (zero behavior change).
func TestTranscodeEnabledFor(t *testing.T) {
	dvr := proto.DVRInfo{Addr: "192.168.0.169:554"}
	other := proto.DVRInfo{Addr: "192.168.0.200"}

	t.Run("numeric id matches stream-id prefix", func(t *testing.T) {
		t.Setenv("RELAY_TRANSCODE_DVR", "3")
		if !transcodeEnabledFor("dvr3_ch1", dvr) {
			t.Fatal("dvr3_ch1 should match RELAY_TRANSCODE_DVR=3")
		}
		if transcodeEnabledFor("dvr30_ch1", dvr) {
			t.Fatal("dvr30_ch1 must not match prefix dvr3_ (boundary)")
		}
		if transcodeEnabledFor("dvr2_ch1", dvr) {
			t.Fatal("dvr2_ch1 should not match")
		}
	})

	t.Run("IP matches the DVR addr regardless of port", func(t *testing.T) {
		t.Setenv("RELAY_TRANSCODE_DVR", "192.168.0.169")
		if !transcodeEnabledFor("dvr7_ch1", dvr) {
			t.Fatal("channel on 192.168.0.169 should match by IP")
		}
		if transcodeEnabledFor("dvr8_ch1", other) {
			t.Fatal("channel on a different DVR must not match")
		}
	})

	t.Run("unset disables", func(t *testing.T) {
		os.Unsetenv("RELAY_TRANSCODE_DVR")
		if transcodeEnabledFor("dvr3_ch1", dvr) {
			t.Fatal("unset RELAY_TRANSCODE_DVR must disable transcode-live")
		}
	})
}
