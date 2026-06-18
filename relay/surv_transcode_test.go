package main

import (
	"os"
	"reflect"
	"testing"
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

// The transcode-live opt-in is per-DVR via RELAY_TRANSCODE_DVR. A numeric value
// is the DVR id and matches that DVR's "dvr<id>_ch<n>" stream ids; unset disables
// it entirely (zero behavior change).
func TestTranscodeEnabledForChannel(t *testing.T) {
	t.Setenv("RELAY_TRANSCODE_DVR", "3")
	if !transcodeEnabledForChannel("dvr3_ch1") {
		t.Fatal("dvr3_ch1 should be transcode-enabled when RELAY_TRANSCODE_DVR=3")
	}
	if transcodeEnabledForChannel("dvr30_ch1") {
		t.Fatal("dvr30_ch1 must not match prefix dvr3_ (boundary)")
	}
	if transcodeEnabledForChannel("dvr2_ch1") {
		t.Fatal("dvr2_ch1 should not be enabled")
	}

	os.Unsetenv("RELAY_TRANSCODE_DVR")
	if transcodeEnabledForChannel("dvr3_ch1") {
		t.Fatal("unset RELAY_TRANSCODE_DVR must disable transcode-live")
	}
}
