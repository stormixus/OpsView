package main

import (
	"testing"

	"github.com/opsview/opsview/proto"
)

func TestResolveSurvHost(t *testing.T) {
	t.Parallel()
	lan := proto.DVRInfo{Addr: "192.168.1.64", ExtAddr: ""}
	if got := resolveSurvHost(lan); got != "192.168.1.64" {
		t.Fatalf("lan host = %q, want 192.168.1.64", got)
	}
	pub := proto.DVRInfo{Addr: "192.168.1.64", ExtAddr: "cctv.example.com"}
	if got := resolveSurvHost(pub); got != "cctv.example.com" {
		t.Fatalf("public host = %q, want cctv.example.com", got)
	}
}

func TestBuildSurvRTSPURLUsesExtHost(t *testing.T) {
	t.Parallel()
	dvr := proto.DVRInfo{
		Addr: "10.0.0.5", ExtAddr: "public.example.com",
		Username: "admin", Password: "secret",
		Protocol: "isapi", StreamQuality: "sub",
	}
	u := buildSurvRTSPURL(dvr, 3)
	if want := "rtsp://admin:secret@public.example.com:554/Streaming/Channels/302"; u != want {
		t.Fatalf("url = %q, want %q", u, want)
	}
}
