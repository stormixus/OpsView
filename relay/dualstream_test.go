package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/opsview/opsview/proto"
)

func TestBuildSurvRTSPURLMainVsSub(t *testing.T) {
	dvr := proto.DVRInfo{Addr: "1.2.3.4", Username: "u", Password: "p", Protocol: "isapi"}
	main := buildSurvRTSPURL(dvr, 1, true)
	sub := buildSurvRTSPURL(dvr, 1, false)
	if !strings.Contains(main, "/Streaming/Channels/101") {
		t.Errorf("main: got %q, want .../Channels/101", main)
	}
	if !strings.Contains(sub, "/Streaming/Channels/102") {
		t.Errorf("sub: got %q, want .../Channels/102", sub)
	}
}

func TestBuildSurvRTSPURLDahuaMainVsSub(t *testing.T) {
	dvr := proto.DVRInfo{Addr: "1.2.3.4", Username: "u", Password: "p", Protocol: "dahua"}
	if got := buildSurvRTSPURL(dvr, 2, true); !strings.Contains(got, "subtype=0") {
		t.Errorf("dahua main: got %q, want subtype=0", got)
	}
	if got := buildSurvRTSPURL(dvr, 2, false); !strings.Contains(got, "subtype=1") {
		t.Errorf("dahua sub: got %q, want subtype=1", got)
	}
}

func TestStreamIDHelpers(t *testing.T) {
	if mainStreamID("dvr3_ch1") != "dvr3_ch1@main" {
		t.Errorf("mainStreamID wrong: %q", mainStreamID("dvr3_ch1"))
	}
	if !isMainStreamID("dvr3_ch1@main") || isMainStreamID("dvr3_ch1") {
		t.Errorf("isMainStreamID wrong")
	}
	if baseStreamID("dvr3_ch1@main") != "dvr3_ch1" || baseStreamID("dvr3_ch1") != "dvr3_ch1" {
		t.Errorf("baseStreamID wrong")
	}
}

func TestDesiredStreamIDs(t *testing.T) {
	chans := []proto.ChannelInfo{
		{DVRID: 3, ChNum: 1, Enabled: true, RecordHighRes: true},
		{DVRID: 3, ChNum: 2, Enabled: true, RecordHighRes: false},
		{DVRID: 3, ChNum: 9, Enabled: false, RecordHighRes: true}, // disabled => no stream
	}
	got := desiredStreamIDs(chans)
	want := map[string]bool{"dvr3_ch1": true, "dvr3_ch1@main": true, "dvr3_ch2": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("desiredStreamIDs = %v, want %v", got, want)
	}
}

func TestRecordTargets(t *testing.T) {
	cases := []struct {
		name   string
		active []string
		want   map[string]string // outputBase -> sourceID
	}{
		{"sub only", []string{"dvr3_ch2"}, map[string]string{"dvr3_ch2": "dvr3_ch2"}},
		{"main present", []string{"dvr3_ch1", "dvr3_ch1@main"}, map[string]string{"dvr3_ch1": "dvr3_ch1@main"}},
		{"agent prefixed", []string{"a1/dvr3_ch1", "a1/dvr3_ch1@main"}, map[string]string{"a1/dvr3_ch1": "a1/dvr3_ch1@main"}},
		{"mixed", []string{"dvr3_ch1", "dvr3_ch1@main", "dvr3_ch2"},
			map[string]string{"dvr3_ch1": "dvr3_ch1@main", "dvr3_ch2": "dvr3_ch2"}},
	}
	for _, c := range cases {
		got := recordTargets(c.active)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: recordTargets(%v) = %v, want %v", c.name, c.active, got, c.want)
		}
	}
}
