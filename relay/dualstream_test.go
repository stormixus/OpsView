package main

import (
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
