package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOnvifPasswordDigest(t *testing.T) {
	nonce := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	created := "2026-06-04T00:00:00.000Z"
	got := onvifPasswordDigest(nonce, created, "test123")
	want := "f1hsgkxcZTTXJz1Cw+I7EKNskYY="
	if got != want {
		t.Fatalf("digest = %q, want %q", got, want)
	}
	// Sanity: base64 of a 20-byte SHA1.
	raw, err := base64.StdEncoding.DecodeString(got)
	if err != nil || len(raw) != 20 {
		t.Fatalf("digest not base64 sha1: err=%v len=%d", err, len(raw))
	}
}

func TestOnvifSOAPCallSendsSecurityHeader(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/soap+xml")
		w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><ok/></s:Body></s:Envelope>`))
	}))
	defer srv.Close()

	c := &onvifClient{http: srv.Client(), user: "admin", pass: "test123", timeout: 5 * time.Second}
	resp, err := c.call(srv.URL, "http://www.onvif.org/ver10/device/wsdl/GetDeviceInformation",
		`<tds:GetDeviceInformation xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/>`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(resp), "<ok/>") {
		t.Fatalf("unexpected resp: %s", resp)
	}
	for _, want := range []string{"UsernameToken", "admin", "PasswordDigest", "Nonce", "Created", "GetDeviceInformation"} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body missing %q\n%s", want, gotBody)
		}
	}
}

func TestOnvifParseProfiles(t *testing.T) {
	xmlData := []byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<trt:GetProfilesResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
  <trt:Profiles token="Profile_101">
    <tt:Name>mainStream</tt:Name>
    <tt:VideoSourceConfiguration><tt:SourceToken>VideoSource_1</tt:SourceToken></tt:VideoSourceConfiguration>
    <tt:VideoEncoderConfiguration><tt:Resolution><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:Resolution></tt:VideoEncoderConfiguration>
  </trt:Profiles>
  <trt:Profiles token="Profile_201">
    <tt:Name>mainStream</tt:Name>
    <tt:VideoSourceConfiguration><tt:SourceToken>VideoSource_2</tt:SourceToken></tt:VideoSourceConfiguration>
    <tt:VideoEncoderConfiguration><tt:Resolution><tt:Width>1280</tt:Width><tt:Height>720</tt:Height></tt:Resolution></tt:VideoEncoderConfiguration>
  </trt:Profiles>
</trt:GetProfilesResponse></s:Body></s:Envelope>`)
	profiles, err := parseOnvifProfiles(xmlData)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
	if profiles[0].Token != "Profile_101" || profiles[0].SourceToken != "VideoSource_1" ||
		profiles[0].Width != 1920 || profiles[0].Height != 1080 {
		t.Fatalf("profile0 = %+v", profiles[0])
	}
	if profiles[1].Token != "Profile_201" || profiles[1].Width != 1280 {
		t.Fatalf("profile1 = %+v", profiles[1])
	}
}

func TestOnvifParseUri(t *testing.T) {
	xmlData := []byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<trt:GetStreamUriResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
  <trt:MediaUri><tt:Uri>rtsp://192.168.0.46:554/Streaming/Channels/101</tt:Uri></trt:MediaUri>
</trt:GetStreamUriResponse></s:Body></s:Envelope>`)
	uri, err := parseOnvifMediaUri(xmlData)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if uri != "rtsp://192.168.0.46:554/Streaming/Channels/101" {
		t.Fatalf("uri = %q", uri)
	}
}

func TestOnvifProbeAndMediaXAddr(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/onvif/device_service", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		if strings.Contains(body, "GetDeviceInformation") {
			w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<tds:GetDeviceInformationResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
<tds:Manufacturer>HIKVISION</tds:Manufacturer></tds:GetDeviceInformationResponse></s:Body></s:Envelope>`))
			return
		}
		// GetServices
		w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>
<tds:GetServicesResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
<tds:Service><tds:Namespace>http://www.onvif.org/ver10/media/wsdl</tds:Namespace>
<tds:XAddr>http://HOST/onvif/Media</tds:XAddr></tds:Service></tds:GetServicesResponse></s:Body></s:Envelope>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	c := newOnvifClient("admin", "test123", 5*time.Second)
	c.http = srv.Client()

	devURL := srv.URL + "/onvif/device_service"
	if !c.probe(devURL) {
		t.Fatalf("probe should succeed")
	}
	media, err := c.mediaXAddr(devURL)
	if err != nil {
		t.Fatalf("mediaXAddr: %v", err)
	}
	// XAddr host is rewritten to the device host the agent dialed.
	want := "http://" + host + "/onvif/Media"
	if media != want {
		t.Fatalf("media = %q, want %q", media, want)
	}
}

// onvifTestServer serves a minimal 2-channel ONVIF device.
func onvifTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	media := func(w http.ResponseWriter, body string) {
		w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body>` + body + `</s:Body></s:Envelope>`))
	}
	mux.HandleFunc("/onvif/device_service", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "GetDeviceInformation") {
			media(w, `<tds:GetDeviceInformationResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl"><tds:Manufacturer>X</tds:Manufacturer></tds:GetDeviceInformationResponse>`)
			return
		}
		media(w, `<tds:GetServicesResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl"><tds:Service><tds:Namespace>http://www.onvif.org/ver10/media/wsdl</tds:Namespace><tds:XAddr>http://HOST/onvif/Media</tds:XAddr></tds:Service></tds:GetServicesResponse>`)
	})
	mux.HandleFunc("/onvif/Media", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		switch {
		case strings.Contains(body, "GetProfiles"):
			media(w, `<trt:GetProfilesResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
<trt:Profiles token="Profile_1"><tt:Name>main</tt:Name><tt:VideoSourceConfiguration><tt:SourceToken>VideoSource_1</tt:SourceToken></tt:VideoSourceConfiguration><tt:VideoEncoderConfiguration><tt:Resolution><tt:Width>1920</tt:Width><tt:Height>1080</tt:Height></tt:Resolution></tt:VideoEncoderConfiguration></trt:Profiles>
<trt:Profiles token="Profile_2"><tt:Name>main</tt:Name><tt:VideoSourceConfiguration><tt:SourceToken>VideoSource_2</tt:SourceToken></tt:VideoSourceConfiguration><tt:VideoEncoderConfiguration><tt:Resolution><tt:Width>1280</tt:Width><tt:Height>720</tt:Height></tt:Resolution></tt:VideoEncoderConfiguration></trt:Profiles>
</trt:GetProfilesResponse>`)
		case strings.Contains(body, "GetStreamUri"):
			ch := "1"
			if strings.Contains(body, "Profile_2") {
				ch = "2"
			}
			media(w, `<trt:GetStreamUriResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema"><trt:MediaUri><tt:Uri>rtsp://HOST:554/live/ch`+ch+`</tt:Uri></trt:MediaUri></trt:GetStreamUriResponse>`)
		case strings.Contains(body, "GetSnapshotUri"):
			media(w, `<trt:GetSnapshotUriResponse xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema"><trt:MediaUri><tt:Uri>http://HOST/snap.jpg</tt:Uri></trt:MediaUri></trt:GetSnapshotUriResponse>`)
		}
	})
	return httptest.NewServer(mux)
}

func TestOnvifDiscover(t *testing.T) {
	srv := onvifTestServer(t)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	parts := strings.Split(host, ":")
	addr := parts[0]
	port := 0
	if len(parts) == 2 {
		fmt.Sscanf(parts[1], "%d", &port)
	}
	c := newOnvifClient("admin", "test123", 5*time.Second)
	c.http = srv.Client()

	chans, err := c.discover(addr, port)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(chans) != 2 {
		t.Fatalf("got %d channels, want 2", len(chans))
	}
	if chans[0].ChNum != 1 || chans[0].Width != 1920 || !strings.HasSuffix(chans[0].RTSPURI, "/live/ch1") {
		t.Fatalf("chan0 = %+v", chans[0])
	}
	if chans[1].ChNum != 2 || chans[1].Height != 720 {
		t.Fatalf("chan1 = %+v", chans[1])
	}
}

func TestOnvifFetchURLAllowed(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"http://192.168.0.46/onvif/snap.jpg", true},  // private LAN DVR
		{"https://10.0.0.5:8000/snap", true},          // private + https
		{"http://camera.local/snap.jpg", true},        // hostname literal
		{"http://169.254.169.254/latest/meta", false}, // cloud-metadata (link-local)
		{"http://127.0.0.1:9000/snap.jpg", true},      // loopback allowed (LAN desktop agent)
		{"http://0.0.0.0/snap.jpg", false},            // unspecified
		{"ftp://192.168.0.46/snap.jpg", false},        // non-http scheme
		{"file:///etc/passwd", false},                 // non-http scheme
		{"", false},
	}
	for _, c := range cases {
		if got := onvifFetchURLAllowed(c.url); got != c.want {
			t.Errorf("onvifFetchURLAllowed(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestOnvifDigestRetry(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Digest realm="test", qop="auth", nonce="abc123", opaque="op1", algorithm="MD5"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		sawAuth = r.Header.Get("Authorization")
		w.Write([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><ok/></s:Body></s:Envelope>`))
	}))
	defer srv.Close()
	c := newOnvifClient("admin", "test123", 5*time.Second)
	c.http = srv.Client()
	resp, err := c.call(srv.URL+"/onvif/device_service", "act", `<x/>`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(resp), "<ok/>") {
		t.Fatalf("resp: %s", resp)
	}
	for _, want := range []string{"Digest ", `username="admin"`, `realm="test"`, `nonce="abc123"`, "response=", "qop=auth", "cnonce=", `opaque="op1"`} {
		if !strings.Contains(sawAuth, want) {
			t.Fatalf("Authorization missing %q: %s", want, sawAuth)
		}
	}
}

// TestOnvifLive probes a real ONVIF device. Opt-in:
// ONVIF_ADDR=192.168.0.46 ONVIF_USER=x ONVIF_PASS=y go test ./ -run TestOnvifLive -v
func TestOnvifLive(t *testing.T) {
	addr := os.Getenv("ONVIF_ADDR")
	if addr == "" {
		t.Skip("set ONVIF_ADDR/ONVIF_USER/ONVIF_PASS (and optional ONVIF_PORT) to run")
	}
	port := 80
	if p := os.Getenv("ONVIF_PORT"); p != "" {
		fmt.Sscanf(p, "%d", &port)
	}
	c := newOnvifClient(os.Getenv("ONVIF_USER"), os.Getenv("ONVIF_PASS"), 8*time.Second)
	chans, err := c.discover(addr, port)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(chans) == 0 {
		t.Fatal("no channels discovered")
	}
	for _, ch := range chans {
		t.Logf("ch%d name=%q %dx%d rtsp=%s snap=%s", ch.ChNum, ch.Name, ch.Width, ch.Height, ch.RTSPURI, ch.SnapshotURI)
	}
	// Verify a snapshot fetch (Digest auth) returns a JPEG.
	if su := chans[0].SnapshotURI; su != "" {
		img, err := onvifHTTPGet(c.http, su, os.Getenv("ONVIF_USER"), os.Getenv("ONVIF_PASS"))
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if len(img) < 3 || img[0] != 0xFF || img[1] != 0xD8 {
			t.Fatalf("snapshot not JPEG (%d bytes)", len(img))
		}
		t.Logf("snapshot ch1: %d bytes JPEG OK", len(img))
	}
}
