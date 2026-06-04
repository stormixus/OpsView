package main

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
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
