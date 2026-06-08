package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/opsview/opsview/proto"
)

// orderStreams must group by DVR then by channel display order (not Go map order).
func TestOrderStreamsByChannelOrder(t *testing.T) {
	streams := []streamState{
		{ID: "dvr1_ch3"}, {ID: "dvr1_ch1"}, {ID: "dvr2_ch1"}, {ID: "dvr1_ch2"},
	}
	channels := []proto.ChannelInfo{
		{DVRID: 1, ChNum: 1, Order: 2},
		{DVRID: 1, ChNum: 2, Order: 0},
		{DVRID: 1, ChNum: 3, Order: 1},
		{DVRID: 2, ChNum: 1, Order: 0},
	}
	orderStreams(streams, channels)
	got := []string{streams[0].ID, streams[1].ID, streams[2].ID, streams[3].ID}
	want := []string{"dvr1_ch2", "dvr1_ch3", "dvr1_ch1", "dvr2_ch1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// Streams with no matching channel sort last, by ID — fully deterministic.
func TestOrderStreamsDeterministicForUnknown(t *testing.T) {
	a := []streamState{{ID: "z_orphan"}, {ID: "dvr1_ch1"}, {ID: "a_orphan"}}
	chs := []proto.ChannelInfo{{DVRID: 1, ChNum: 1, Order: 0}}
	orderStreams(a, chs)
	got := []string{a[0].ID, a[1].ID, a[2].ID}
	want := []string{"dvr1_ch1", "a_orphan", "z_orphan"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// A rename in a re-sent config must update an already-running stream's name live.
func TestHandleSurvConfigUpdatesExistingStreamName(t *testing.T) {
	sp := NewSurvProxy()
	sp.streams["dvr1_ch2"] = &streamEntry{id: "dvr1_ch2", name: "201"}
	cfg := proto.SurvConfig{
		DVRs:     []proto.DVRInfo{{ID: 1, Addr: "10.0.0.9", Port: 80}},
		Channels: []proto.ChannelInfo{{DVRID: 1, ChNum: 2, Name: "B-2", Enabled: true}},
	}
	payload, _ := json.Marshal(cfg)
	sp.HandleSurvConfig(payload)
	if got := sp.streams["dvr1_ch2"].name; got != "B-2" {
		t.Fatalf("name=%q want B-2 (updated live)", got)
	}
}
