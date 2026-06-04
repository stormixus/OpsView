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
