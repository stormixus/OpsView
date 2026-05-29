package main

import (
	"testing"

	"github.com/opsview/opsview/proto"
)

func TestResolveSurvHostUsesLANAddr(t *testing.T) {
	t.Parallel()
	lan := proto.DVRInfo{Addr: "192.168.1.64", ExtAddr: ""}
	if got := resolveSurvHost(lan); got != "192.168.1.64" {
		t.Fatalf("lan host = %q, want 192.168.1.64", got)
	}
	// ext_addr must not affect RTSP pull host
	withExt := proto.DVRInfo{Addr: "192.168.1.64", ExtAddr: "relay.public.example.com"}
	if got := resolveSurvHost(withExt); got != "192.168.1.64" {
		t.Fatalf("host = %q, want LAN 192.168.1.64", got)
	}
}

func TestBuildSurvRTSPURLUsesLANHost(t *testing.T) {
	t.Parallel()
	dvr := proto.DVRInfo{
		Addr: "10.0.0.5", ExtAddr: "public.example.com",
		Username: "admin", Password: "secret",
		Protocol: "isapi", StreamQuality: "sub",
	}
	u := buildSurvRTSPURL(dvr, 3)
	if want := "rtsp://admin:secret@10.0.0.5:554/Streaming/Channels/302"; u != want {
		t.Fatalf("url = %q, want %q", u, want)
	}
}
