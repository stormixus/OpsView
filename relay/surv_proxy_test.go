package main

import (
	"strings"
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

func TestBuildSurvRTSPURLUsesChannelURI(t *testing.T) {
	dvr := proto.DVRInfo{Addr: "10.0.0.9", Port: 80, Username: "admin", Password: "pw", Protocol: "onvif"}
	// Channel carries an explicit ONVIF RTSP URI without credentials.
	ch := proto.ChannelInfo{ChNum: 1, RtspURI: "rtsp://10.0.0.9:554/live/ch1"}
	got := survRTSPURLForChannel(dvr, ch)
	want := "rtsp://admin:pw@10.0.0.9:554/live/ch1"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// No per-channel URI -> falls back to the template (ISAPI path).
	dvr2 := proto.DVRInfo{Addr: "10.0.0.8", Port: 80, Username: "admin", Password: "pw", Protocol: "isapi", StreamQuality: "sub"}
	ch2 := proto.ChannelInfo{ChNum: 2}
	got2 := survRTSPURLForChannel(dvr2, ch2)
	if !strings.Contains(got2, "/Streaming/Channels/202") {
		t.Fatalf("template fallback wrong: %q", got2)
	}
}
