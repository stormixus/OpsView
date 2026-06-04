# ONVIF DVR Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add ONVIF as a third surveillance protocol (alongside Hikvision ISAPI and Dahua) so non-Hikvision/Dahua DVRs discover, stream (HLS via relay), and snapshot.

**Architecture:** The agent hand-rolls a minimal ONVIF SOAP client (no external dependency) and resolves per-channel RTSP + snapshot URIs at discovery time, storing them in the channels DB. The relay uses the stored RTSP URI directly when present, otherwise the existing per-protocol path template.

**Tech Stack:** Go stdlib (`net/http`, `crypto/sha1`, `crypto/rand`, `encoding/base64`, `encoding/xml`), `httptest` for tests. SQLite (`modernc.org/sqlite`). Existing relay/agent/proto packages.

Spec: `docs/superpowers/specs/2026-06-04-onvif-support-design.md`

---

## File Structure

- `agent/onvif.go` (new) — ONVIF SOAP client: WS-Security digest, SOAP call, probe, GetServices, GetProfiles, GetStreamUri, GetSnapshotUri, and the `onvifDiscover` orchestrator. One responsibility: talk ONVIF, return plain Go values.
- `agent/onvif_test.go` (new) — unit tests with `httptest` ONVIF fixtures.
- `agent/surveillance.go` (modify) — `channels` schema columns, `ChannelConfig` fields, `discoverFromDVROnvif`, protocol probe/dispatch, ONVIF snapshot.
- `agent/surveillance_onvif_test.go` (new) — discovery + schema tests.
- `proto/json_messages.go` (modify) — `ChannelInfo.RtspURI`.
- `agent/agent.go` (modify) — `sendSurvConfig` fills `RtspURI`.
- `relay/surv_proxy.go` (modify) — use per-channel `RtspURI` when set.
- `relay/surv_proxy_test.go` (modify) — RTSP URL selection test.
- `agent/web_ui.go` (modify) — "ONVIF" option in protocol dropdowns.

All `go` commands run from the package dir (e.g. `cd agent`).

---

### Task 1: WS-Security PasswordDigest helper

**Files:**
- Create: `agent/onvif.go`
- Test: `agent/onvif_test.go`

- [ ] **Step 1: Write the failing test**

`agent/onvif_test.go`:
```go
package main

import (
	"encoding/base64"
	"testing"
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./ -run TestOnvifPasswordDigest -v`
Expected: FAIL — `undefined: onvifPasswordDigest`

- [ ] **Step 3: Write minimal implementation**

`agent/onvif.go`:
```go
//go:build !ignore

package main

import (
	"crypto/sha1"
	"encoding/base64"
)

// onvifPasswordDigest computes the WS-Security UsernameToken PasswordDigest:
// Base64( SHA1( nonce + created + password ) ). nonce is the raw (pre-base64)
// nonce bytes; created is the UTC timestamp string used verbatim in the header.
func onvifPasswordDigest(nonce []byte, created, password string) string {
	h := sha1.New()
	h.Write(nonce)
	h.Write([]byte(created))
	h.Write([]byte(password))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
```
(The `//go:build !ignore` line is a no-op build tag placeholder; remove it — included only so the file has a clean first line. Actually omit it; start the file at `package main`.)

Final first lines of `agent/onvif.go`:
```go
package main

import (
	"crypto/sha1"
	"encoding/base64"
)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./ -run TestOnvifPasswordDigest -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/onvif.go agent/onvif_test.go
git commit -m "feat(agent): ONVIF WS-Security password digest"
```

---

### Task 2: SOAP envelope + authenticated SOAP call

**Files:**
- Modify: `agent/onvif.go`
- Test: `agent/onvif_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/onvif_test.go`:
```go
import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
)

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
	for _, want := range []string{"UsernameToken", "<Username>admin</Username>", "PasswordDigest", "Nonce", "Created", "GetDeviceInformation"} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body missing %q\n%s", want, gotBody)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./ -run TestOnvifSOAPCall -v`
Expected: FAIL — `undefined: onvifClient`

- [ ] **Step 3: Write minimal implementation**

Add to `agent/onvif.go` (extend imports):
```go
import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"time"
)

type onvifClient struct {
	http    *http.Client
	user    string
	pass    string
	timeout time.Duration
}

func newOnvifClient(user, pass string, timeout time.Duration) *onvifClient {
	return &onvifClient{http: &http.Client{Timeout: timeout}, user: user, pass: pass, timeout: timeout}
}

// wsseHeader builds a fresh WS-Security UsernameToken/PasswordDigest header.
func (c *onvifClient) wsseHeader() (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	created := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	digest := onvifPasswordDigest(nonce, created, c.pass)
	return fmt.Sprintf(`<wsse:Security s:mustUnderstand="1" `+
		`xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" `+
		`xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">`+
		`<wsse:UsernameToken><wsse:Username>%s</wsse:Username>`+
		`<wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">%s</wsse:Password>`+
		`<wsse:Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">%s</wsse:Nonce>`+
		`<wsu:Created>%s</wsu:Created></wsse:UsernameToken></wsse:Security>`,
		xmlEscape(c.user), digest, base64.StdEncoding.EncodeToString(nonce), created), nil
}

// call POSTs a SOAP 1.2 request (body is the inner Body content) and returns the
// raw response bytes. action is the SOAP action (set on the Content-Type).
func (c *onvifClient) call(url, action, innerBody string) ([]byte, error) {
	header, err := c.wsseHeader()
	if err != nil {
		return nil, err
	}
	env := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">` +
		`<s:Header>` + header + `</s:Header>` +
		`<s:Body>` + innerBody + `</s:Body></s:Envelope>`
	req, err := http.NewRequest("POST", url, bytes.NewBufferString(env))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", fmt.Sprintf(`application/soap+xml; charset=utf-8; action="%s"`, action))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return body, fmt.Errorf("onvif %s: HTTP %d", action, resp.StatusCode)
	}
	return body, nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xmlEscapeTo(&b, s)
	return b.String()
}
```

Add the small escaper (avoid pulling text/template); add `"strings"` is not needed. Add:
```go
func xmlEscapeTo(b *bytes.Buffer, s string) error {
	repl := map[rune]string{'&': "&amp;", '<': "&lt;", '>': "&gt;", '"': "&quot;", '\'': "&apos;"}
	for _, r := range s {
		if v, ok := repl[r]; ok {
			b.WriteString(v)
		} else {
			b.WriteRune(r)
		}
	}
	return nil
}
```
Remove the unused `sha1` import duplication (it's used by Task 1's function — keep one import block).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./ -run TestOnvif -v`
Expected: PASS (both Task 1 and Task 2 tests)

- [ ] **Step 5: Commit**

```bash
git add agent/onvif.go agent/onvif_test.go
git commit -m "feat(agent): ONVIF authenticated SOAP call"
```

---

### Task 3: Parse GetProfiles / GetStreamUri / GetSnapshotUri

**Files:**
- Modify: `agent/onvif.go`
- Test: `agent/onvif_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/onvif_test.go`:
```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./ -run "TestOnvifParse" -v`
Expected: FAIL — `undefined: parseOnvifProfiles`

- [ ] **Step 3: Write minimal implementation**

Add to `agent/onvif.go` (add `"encoding/xml"` to imports):
```go
type onvifProfile struct {
	Token       string
	Name        string
	SourceToken string
	Width       int
	Height      int
}

func parseOnvifProfiles(data []byte) ([]onvifProfile, error) {
	var env struct {
		Profiles []struct {
			Token  string `xml:"token,attr"`
			Name   string `xml:"Name"`
			Source struct {
				SourceToken string `xml:"SourceToken"`
			} `xml:"VideoSourceConfiguration"`
			Enc struct {
				Resolution struct {
					Width  int `xml:"Width"`
					Height int `xml:"Height"`
				} `xml:"Resolution"`
			} `xml:"VideoEncoderConfiguration"`
		} `xml:"Body>GetProfilesResponse>Profiles"`
	}
	if err := xml.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	out := make([]onvifProfile, 0, len(env.Profiles))
	for _, p := range env.Profiles {
		out = append(out, onvifProfile{
			Token:       p.Token,
			Name:        p.Name,
			SourceToken: p.Source.SourceToken,
			Width:       p.Enc.Resolution.Width,
			Height:      p.Enc.Resolution.Height,
		})
	}
	return out, nil
}

// parseOnvifMediaUri extracts the <Uri> from a GetStreamUri/GetSnapshotUri
// response (both wrap it in <MediaUri><Uri>).
func parseOnvifMediaUri(data []byte) (string, error) {
	var env struct {
		Uri string `xml:"Body>>MediaUri>Uri"`
	}
	if err := xml.Unmarshal(data, &env); err != nil {
		return "", err
	}
	if env.Uri == "" {
		return "", fmt.Errorf("onvif: no MediaUri/Uri in response")
	}
	return env.Uri, nil
}
```

Note: Go's `encoding/xml` matches element local names, ignoring namespace prefixes, so the `trt:`/`tt:` prefixes in fixtures are handled. The `Body>>MediaUri>Uri` path uses `>` wildcard for the response element (`GetStreamUriResponse` or `GetSnapshotUriResponse`).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./ -run "TestOnvifParse" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/onvif.go agent/onvif_test.go
git commit -m "feat(agent): parse ONVIF profiles and media URIs"
```

---

### Task 4: Resolve media service + probe (GetServices, GetDeviceInformation)

**Files:**
- Modify: `agent/onvif.go`
- Test: `agent/onvif_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/onvif_test.go`:
```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./ -run TestOnvifProbeAndMediaXAddr -v`
Expected: FAIL — `c.probe undefined`

- [ ] **Step 3: Write minimal implementation**

Add to `agent/onvif.go` (add `"net/url"`, `"strings"` to imports):
```go
const (
	onvifNSDevice = "http://www.onvif.org/ver10/device/wsdl"
	onvifNSMedia  = "http://www.onvif.org/ver10/media/wsdl"
)

func onvifDeviceURL(addr string, port int) string {
	return fmt.Sprintf("http://%s:%d/onvif/device_service", addr, port)
}

// probe returns true if the device answers GetDeviceInformation as ONVIF.
func (c *onvifClient) probe(deviceURL string) bool {
	resp, err := c.call(deviceURL, onvifNSDevice+"/GetDeviceInformation",
		`<tds:GetDeviceInformation xmlns:tds="`+onvifNSDevice+`"/>`)
	if err != nil {
		return false
	}
	return strings.Contains(string(resp), "GetDeviceInformationResponse")
}

// mediaXAddr resolves the Media service URL via GetServices, rewriting its host
// to the host we actually dialed (devices often advertise their LAN IP, which
// can differ from how we reach them).
func (c *onvifClient) mediaXAddr(deviceURL string) (string, error) {
	resp, err := c.call(deviceURL, onvifNSDevice+"/GetServices",
		`<tds:GetServices xmlns:tds="`+onvifNSDevice+`"><tds:IncludeCapability>false</tds:IncludeCapability></tds:GetServices>`)
	if err != nil {
		return "", err
	}
	var env struct {
		Services []struct {
			Namespace string `xml:"Namespace"`
			XAddr     string `xml:"XAddr"`
		} `xml:"Body>GetServicesResponse>Service"`
	}
	if err := xml.Unmarshal(resp, &env); err != nil {
		return "", err
	}
	dialed, _ := url.Parse(deviceURL)
	for _, s := range env.Services {
		if strings.Contains(s.Namespace, "/media/") && s.XAddr != "" {
			if u, err := url.Parse(s.XAddr); err == nil && dialed != nil {
				u.Host = dialed.Host
				return u.String(), nil
			}
			return s.XAddr, nil
		}
	}
	// Fallback to the conventional Hikvision media path.
	if dialed != nil {
		return "http://" + dialed.Host + "/onvif/Media", nil
	}
	return "", fmt.Errorf("onvif: media service not found")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./ -run TestOnvifProbeAndMediaXAddr -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/onvif.go agent/onvif_test.go
git commit -m "feat(agent): ONVIF probe + media service resolution"
```

---

### Task 5: Discovery orchestrator (`onvifDiscover`)

**Files:**
- Modify: `agent/onvif.go`
- Test: `agent/onvif_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/onvif_test.go`:
```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./ -run TestOnvifDiscover -v`
Expected: FAIL — `c.discover undefined`

- [ ] **Step 3: Write minimal implementation**

Add to `agent/onvif.go` (`"regexp"`, `"strconv"` to imports):
```go
type onvifChannel struct {
	ChNum       int
	Name        string
	Width       int
	Height      int
	RTSPURI     string
	SnapshotURI string
}

var onvifTrailingDigits = regexp.MustCompile(`(\d+)\s*$`)

// chanNumFromSource derives a channel number from a VideoSource token like
// "VideoSource_1" / "VideoSourceToken_2"; falls back to the 1-based index.
func chanNumFromSource(sourceToken string, idx int) int {
	if m := onvifTrailingDigits.FindStringSubmatch(sourceToken); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return n
		}
	}
	return idx + 1
}

func (c *onvifClient) getProfiles(mediaURL string) ([]onvifProfile, error) {
	resp, err := c.call(mediaURL, onvifNSMedia+"/GetProfiles",
		`<trt:GetProfiles xmlns:trt="`+onvifNSMedia+`"/>`)
	if err != nil {
		return nil, err
	}
	return parseOnvifProfiles(resp)
}

func (c *onvifClient) getStreamURI(mediaURL, profileToken string) (string, error) {
	body := `<trt:GetStreamUri xmlns:trt="` + onvifNSMedia + `" xmlns:tt="http://www.onvif.org/ver10/schema">` +
		`<trt:StreamSetup><tt:Stream>RTP-Unicast</tt:Stream><tt:Transport><tt:Protocol>RTSP</tt:Protocol></tt:Transport></trt:StreamSetup>` +
		`<trt:ProfileToken>` + xmlEscape(profileToken) + `</trt:ProfileToken></trt:GetStreamUri>`
	resp, err := c.call(mediaURL, onvifNSMedia+"/GetStreamUri", body)
	if err != nil {
		return "", err
	}
	return parseOnvifMediaUri(resp)
}

func (c *onvifClient) getSnapshotURI(mediaURL, profileToken string) (string, error) {
	body := `<trt:GetSnapshotUri xmlns:trt="` + onvifNSMedia + `"><trt:ProfileToken>` + xmlEscape(profileToken) + `</trt:ProfileToken></trt:GetSnapshotUri>`
	resp, err := c.call(mediaURL, onvifNSMedia+"/GetSnapshotUri", body)
	if err != nil {
		return "", err
	}
	return parseOnvifMediaUri(resp)
}

// discover resolves all channels (one per video source; first profile per source
// wins). Channel mapping by source token is verified against real hardware.
func (c *onvifClient) discover(addr string, port int) ([]onvifChannel, error) {
	devURL := onvifDeviceURL(addr, port)
	mediaURL, err := c.mediaXAddr(devURL)
	if err != nil {
		return nil, err
	}
	profiles, err := c.getProfiles(mediaURL)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []onvifChannel
	for i, p := range profiles {
		key := p.SourceToken
		if key == "" {
			key = p.Token
		}
		if seen[key] {
			continue // one channel per source; first (main) profile wins
		}
		seen[key] = true
		stream, err := c.getStreamURI(mediaURL, p.Token)
		if err != nil {
			log.Printf("[onvif] GetStreamUri %s: %v", p.Token, err)
			continue
		}
		snap, _ := c.getSnapshotURI(mediaURL, p.Token)
		chNum := chanNumFromSource(p.SourceToken, len(out))
		_ = i
		out = append(out, onvifChannel{
			ChNum: chNum, Name: p.Name, Width: p.Width, Height: p.Height,
			RTSPURI: stream, SnapshotURI: snap,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("onvif: no channels discovered")
	}
	return out, nil
}
```
Add `"log"` to imports.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./ -run TestOnvifDiscover -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/onvif.go agent/onvif_test.go
git commit -m "feat(agent): ONVIF discovery orchestrator"
```

---

### Task 6: DB columns + ChannelConfig fields

**Files:**
- Modify: `agent/surveillance.go`
- Test: `agent/surveillance_onvif_test.go` (new)

- [ ] **Step 1: Write the failing test**

`agent/surveillance_onvif_test.go`:
```go
package main

import "testing"

func TestChannelStoresURIs(t *testing.T) {
	m := newTestSurvManager(t)
	id, err := m.AddDVR("cam", "10.0.0.9", 80, "", 0, "admin", "pw", "onvif", 2000, "sub")
	if err != nil {
		t.Fatalf("AddDVR: %v", err)
	}
	_, err = m.db.Exec(`INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height, rtsp_uri, snapshot_uri)
		VALUES (?, 1, 'ch1', 0, 1, 1920, 1080, 'rtsp://x/live/ch1', 'http://x/snap.jpg')`, id.ID)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	chs, err := m.ListChannels(id.ID)
	if err != nil || len(chs) != 1 {
		t.Fatalf("ListChannels = %d (%v)", len(chs), err)
	}
	if chs[0].RtspURI != "rtsp://x/live/ch1" || chs[0].SnapshotURI != "http://x/snap.jpg" {
		t.Fatalf("uris not loaded: %+v", chs[0])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./ -run TestChannelStoresURIs -v`
Expected: FAIL — no such column `rtsp_uri` / `ChannelConfig` has no `RtspURI`.

- [ ] **Step 3: Write minimal implementation**

In `agent/surveillance.go`, add columns in `migrate()` alongside the existing `ALTER TABLE dvrs` block — add a channels ALTER list. Find the loop:
```go
	for _, stmt := range []string{
		`ALTER TABLE dvrs ADD COLUMN protocol TEXT NOT NULL DEFAULT 'isapi'`,
		`ALTER TABLE dvrs ADD COLUMN ext_addr TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE dvrs ADD COLUMN ext_port INTEGER NOT NULL DEFAULT 0`,
	} {
```
Add two entries to that slice:
```go
		`ALTER TABLE channels ADD COLUMN rtsp_uri TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE channels ADD COLUMN snapshot_uri TEXT NOT NULL DEFAULT ''`,
```

Add fields to `ChannelConfig` struct:
```go
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	RtspURI     string `json:"rtsp_uri,omitempty"`
	SnapshotURI string `json:"snapshot_uri,omitempty"`
```

Update `ListChannels` SELECT + Scan to include the new columns. Find the query in `ListChannels` (selects `id, dvr_id, ch_num, name, display_order, enabled, width, height`) and add `, rtsp_uri, snapshot_uri`; add `&c.RtspURI, &c.SnapshotURI` to the `rows.Scan(...)`.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./ -run TestChannelStoresURIs -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/surveillance.go agent/surveillance_onvif_test.go
git commit -m "feat(agent): channel rtsp_uri/snapshot_uri columns"
```

---

### Task 7: `discoverFromDVROnvif` + protocol dispatch + probe chain

**Files:**
- Modify: `agent/surveillance.go`
- Test: `agent/surveillance_onvif_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/surveillance_onvif_test.go`:
```go
import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscoverFromDVROnvif(t *testing.T) {
	srv := onvifTestServer(t) // reuse helper from onvif_test.go (same package)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	parts := strings.Split(host, ":")
	port := 0
	if len(parts) == 2 {
		_, _ = fmtSscan(parts[1], &port)
	}
	m := newTestSurvManager(t)
	m.client = srv.Client()
	m.shortClient = srv.Client()
	dvr := DVRConfig{ID: 5, Addr: parts[0], Port: port, Username: "admin", Password: "test123", Protocol: "onvif"}

	chans, err := m.discoverFromDVROnvif(dvr)
	if err != nil {
		t.Fatalf("discoverFromDVROnvif: %v", err)
	}
	if len(chans) != 2 {
		t.Fatalf("got %d channels, want 2", len(chans))
	}
	if chans[0].RtspURI == "" || chans[0].ChNum != 1 {
		t.Fatalf("chan0 = %+v", chans[0])
	}
}

func fmtSscan(s string, p *int) (int, error) { return fmt.Sscanf(s, "%d", p) }
```
(Add `import "fmt"` to the file's import block.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./ -run TestDiscoverFromDVROnvif -v`
Expected: FAIL — `m.discoverFromDVROnvif undefined`

- [ ] **Step 3: Write minimal implementation**

In `agent/surveillance.go`:

Add the discovery method (place near `discoverFromDVRRTSP`):
```go
func (m *SurveillanceManager) discoverFromDVROnvif(dvr DVRConfig) ([]ChannelConfig, error) {
	c := newOnvifClient(dvr.Username, dvr.Password, 6*time.Second)
	c.http = m.client
	chans, err := c.discover(dvr.Addr, dvr.Port)
	if err != nil {
		return nil, err
	}
	var out []ChannelConfig
	for _, ch := range chans {
		out = append(out, ChannelConfig{
			DVRID: dvr.ID, ChNum: ch.ChNum, Name: ch.Name, Order: ch.ChNum - 1,
			Width: ch.Width, Height: ch.Height, RtspURI: ch.RTSPURI, SnapshotURI: ch.SnapshotURI,
		})
	}
	return out, nil
}
```

Add ONVIF to `discoverWithProtocol`:
```go
	switch dvr.Protocol {
	case "rtsp":
		return m.discoverFromDVRRTSP(dvr)
	case "dahua":
		return m.discoverFromDVRDahua(dvr)
	case "onvif":
		return m.discoverFromDVROnvif(dvr)
	default:
		return m.discoverFromDVRISAPI(dvr)
	}
```

Add ONVIF to `probeDVRProtocol`, between the Dahua block and the final RTSP fallback (before `log.Printf("[surv] Probe fallback to RTSP ...`):
```go
	if newOnvifClient(dvr.Username, dvr.Password, 3*time.Second).probeWith(m.shortClient, onvifDeviceURL(dvr.Addr, dvr.Port)) {
		log.Printf("[surv] Probed ONVIF for %s:%d", dvr.Addr, dvr.Port)
		return "onvif"
	}
```

Add a `probeWith` helper to `agent/onvif.go` so the probe can use the manager's short client:
```go
func (c *onvifClient) probeWith(httpc *http.Client, deviceURL string) bool {
	if httpc != nil {
		c.http = httpc
	}
	return c.probe(deviceURL)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./ -run "TestDiscoverFromDVROnvif|TestOnvif" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/surveillance.go agent/onvif.go agent/surveillance_onvif_test.go
git commit -m "feat(agent): ONVIF discovery + protocol probe/dispatch"
```

---

### Task 8: ONVIF snapshot in `FetchSnapshot`

**Files:**
- Modify: `agent/surveillance.go`
- Test: `agent/surveillance_onvif_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/surveillance_onvif_test.go`:
```go
func TestFetchSnapshotOnvif(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3}) // fake jpeg
	}))
	defer srv.Close()
	m := newTestSurvManager(t)
	m.client = srv.Client()
	id, _ := m.AddDVR("cam", "10.0.0.9", 80, "", 0, "admin", "pw", "onvif", 2000, "sub")
	_, _ = m.db.Exec(`INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height, rtsp_uri, snapshot_uri)
		VALUES (?, 1, 'ch1', 0, 1, 1920, 1080, 'rtsp://x/ch1', ?)`, id.ID, srv.URL+"/snap.jpg")

	data, err := m.FetchSnapshot(id.ID, 1)
	if err != nil {
		t.Fatalf("FetchSnapshot: %v", err)
	}
	if len(data) < 3 || data[0] != 0xFF || data[1] != 0xD8 {
		t.Fatalf("not jpeg: %v", data)
	}
}
```
(Add `"net/http"` import.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./ -run TestFetchSnapshotOnvif -v`
Expected: FAIL — ONVIF case returns an error / wrong path.

- [ ] **Step 3: Write minimal implementation**

In `agent/surveillance.go`, add a `case "onvif"` to `FetchSnapshot`'s switch and a helper:
```go
	switch dvr.Protocol {
	case "rtsp":
		data, err = m.fetchSnapshotISAPIOnPort(dvr, chNum, 80)
		if err != nil {
			data, err = m.fetchSnapshotRTSP(dvr, chNum)
		}
	case "dahua":
		data, err = m.fetchSnapshotDahua(dvr, chNum)
	case "onvif":
		data, err = m.fetchSnapshotOnvif(dvr, chNum)
	default:
		data, err = m.fetchSnapshotISAPI(dvr, chNum)
	}
```
Add the helper:
```go
func (m *SurveillanceManager) fetchSnapshotOnvif(dvr DVRConfig, chNum int) ([]byte, error) {
	var snapURI string
	err := m.db.QueryRow(`SELECT snapshot_uri FROM channels WHERE dvr_id=? AND ch_num=?`, dvr.ID, chNum).Scan(&snapURI)
	if err != nil {
		return nil, err
	}
	if snapURI == "" {
		return nil, fmt.Errorf("onvif: no snapshot URI for ch %d", chNum)
	}
	req, _ := http.NewRequest("GET", snapURI, nil)
	req.SetBasicAuth(dvr.Username, dvr.Password)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("onvif snapshot returned %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./ -run TestFetchSnapshotOnvif -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/surveillance.go agent/surveillance_onvif_test.go
git commit -m "feat(agent): ONVIF snapshot fetch"
```

---

### Task 9: Pass RtspURI to the relay (proto + agent sendSurvConfig)

**Files:**
- Modify: `proto/json_messages.go`, `agent/agent.go`
- Test: covered by relay test in Task 10 (proto is a plain field).

- [ ] **Step 1: Add the proto field**

In `proto/json_messages.go`, `ChannelInfo` struct — add after `Height`:
```go
	RtspURI string `json:"rtsp_uri,omitempty"`
```

- [ ] **Step 2: Fill it in the agent**

In `agent/agent.go`, `sendSurvConfig`, where it appends `proto.ChannelInfo{...}` for each channel, add:
```go
			cfg.Channels = append(cfg.Channels, proto.ChannelInfo{
				ID: ch.ID, DVRID: ch.DVRID, ChNum: ch.ChNum,
				Name: ch.Name, Order: ch.Order, Enabled: ch.Enabled,
				Width: ch.Width, Height: ch.Height,
				RtspURI: ch.RtspURI,
			})
```

- [ ] **Step 3: Build to verify it compiles**

Run: `cd proto && go build ./... && cd ../agent && go build ./...`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add proto/json_messages.go agent/agent.go
git commit -m "feat(proto,agent): carry per-channel RtspURI to relay"
```

---

### Task 10: Relay uses per-channel RtspURI

**Files:**
- Modify: `relay/surv_proxy.go`
- Test: `relay/surv_proxy_test.go`

- [ ] **Step 1: Write the failing test**

Append to `relay/surv_proxy_test.go`:
```go
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
```
(Ensure `strings` and `proto` are imported in the test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd relay && go test ./ -run TestBuildSurvRTSPURLUsesChannelURI -v`
Expected: FAIL — `survRTSPURLForChannel undefined`

- [ ] **Step 3: Write minimal implementation**

In `relay/surv_proxy.go`, add a channel-aware wrapper and credential injector:
```go
// survRTSPURLForChannel uses the channel's ONVIF-provided RtspURI when present
// (injecting DVR credentials if the URI has none), otherwise the per-protocol
// template via buildSurvRTSPURL.
func survRTSPURLForChannel(dvr proto.DVRInfo, ch proto.ChannelInfo) string {
	if ch.RtspURI != "" {
		u, err := url.Parse(ch.RtspURI)
		if err == nil {
			if u.User == nil && dvr.Username != "" {
				u.User = url.UserPassword(dvr.Username, dvr.Password)
			}
			return u.String()
		}
	}
	return buildSurvRTSPURL(dvr, ch.ChNum)
}
```

Then update the caller in `ServeHLS` (where it currently calls `buildSurvRTSPURL(dvr, chNum)`) to look up the channel and call `survRTSPURLForChannel`. Locate the cached config + channel:
```go
	// existing code resolves `dvr` (proto.DVRInfo) and `chNum`.
	var chInfo proto.ChannelInfo
	chInfo.ChNum = chNum
	for _, ch := range sp.survChannelsFor(dvr.ID) { // helper below
		if ch.ChNum == chNum {
			chInfo = ch
			break
		}
	}
	rtspURL := survRTSPURLForChannel(dvr, chInfo)
```
Add a helper that reads channels from the cached surv config (mirror how `ServeHLS` already finds the DVR). If `ServeHLS` already decodes the cached `proto.SurvConfig`, reuse that slice; otherwise add:
```go
func (sp *SurvProxy) survChannelsFor(dvrID int64) []proto.ChannelInfo {
	cfg := sp.hub.currentSurvConfig() // existing accessor used to find the DVR
	var out []proto.ChannelInfo
	for _, ch := range cfg.Channels {
		if ch.DVRID == dvrID {
			out = append(out, ch)
		}
	}
	return out
}
```
**Note for implementer:** `ServeHLS` already obtains the DVR from the cached config — reuse that exact accessor (do not invent `currentSurvConfig` if a different name exists; match the code). Replace the `buildSurvRTSPURL(dvr, chNum)` call site with `survRTSPURLForChannel(dvr, chInfo)`.

- [ ] **Step 4: Run test + full relay tests**

Run: `cd relay && go test ./ -run TestBuildSurvRTSPURLUsesChannelURI -v && go test ./`
Expected: PASS, and no existing relay test regresses.

- [ ] **Step 5: Commit**

```bash
git add relay/surv_proxy.go relay/surv_proxy_test.go
git commit -m "feat(relay): use ONVIF per-channel RTSP URI when present"
```

---

### Task 11: ONVIF option in the DVR protocol dropdowns

**Files:**
- Modify: `agent/web_ui.go`

- [ ] **Step 1: Add the option (add-DVR form)**

In `agent/web_ui.go` `htmlTemplate`, the add-DVR `<select id="dvr-protocol">` currently has options `auto / isapi / dahua / rtsp`. Add before `</select>`:
```html
<option value="onvif">ONVIF (범용)</option>
```

- [ ] **Step 2: Add the option (edit-DVR modal)**

Same addition in the edit modal `<select id="edit-dvr-protocol">`:
```html
<option value="onvif">ONVIF (범용)</option>
```

- [ ] **Step 3: Build to verify**

Run: `cd agent && go build ./...`
Expected: no errors (HTML is a Go string literal; just confirm it still compiles).

- [ ] **Step 4: Commit**

```bash
git add agent/web_ui.go
git commit -m "feat(agent): ONVIF option in DVR protocol dropdowns"
```

---

### Task 12: Full verification (build, vet, tests, cross-build)

**Files:** none (verification only)

- [ ] **Step 1: Agent + proto + relay tests**

Run:
```bash
cd proto && go test ./... && cd ../agent && go test ./ && cd ../relay && go test ./
```
Expected: all PASS.

- [ ] **Step 2: Vet**

Run: `cd agent && go vet . && cd ../relay && go vet . && cd ../proto && go vet ./...`
Expected: no output.

- [ ] **Step 3: Windows cross-build of the agent**

Run: `cd agent && GOOS=windows GOARCH=amd64 go build -o /dev/null .`
Expected: no errors.

- [ ] **Step 4: Commit (if any fixups were needed)**

```bash
git add -A && git commit -m "chore: ONVIF support verification fixups" || echo "nothing to commit"
```

---

### Task 13: Live verification against a real Hikvision ONVIF device (manual)

**Not a code task — manual acceptance once ONVIF is enabled on a Hikvision DVR.**

- [ ] Enable ONVIF on the Hikvision DVR (Configuration → Network → Advanced → Integration Protocol → enable ONVIF; add an ONVIF user with a password).
- [ ] From the dev machine, probe it directly to capture real responses (adjust IP/creds):
```bash
curl -s -u 'admin:PASS' -X POST "http://192.168.0.46/onvif/device_service" \
  -H 'Content-Type: application/soap+xml' \
  --data '<?xml version="1.0"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><GetSystemDateAndTime xmlns="http://www.onvif.org/ver10/device/wsdl"/></s:Body></s:Envelope>'
```
  (GetSystemDateAndTime needs no auth and confirms ONVIF is reachable; then verify the WS-Security UsernameToken path works for GetProfiles.)
- [ ] In the agent UI, add the DVR with protocol **ONVIF**, run channel discovery, confirm channels appear with resolutions.
- [ ] Confirm HLS streams play in the viewer and snapshots work.
- [ ] If the channel↔profile mapping is wrong for this device, adjust `chanNumFromSource` / the per-source selection in `agent/onvif.go:discover` and re-test. Capture the real `GetProfiles` XML into a new fixture test if a quirk is found.

---

## Self-Review Notes

- **Spec coverage:** client (T1–T5), DB/discovery/dispatch/probe (T6–T7), snapshot (T8), proto+agent (T9), relay (T10), UI (T11), verification (T12), live test (T13). All spec sections covered.
- **Type consistency:** `onvifChannel` fields (`ChNum/Name/Width/Height/RTSPURI/SnapshotURI`) → `ChannelConfig` (`RtspURI/SnapshotURI`) → `proto.ChannelInfo.RtspURI`. `discover`/`probe`/`mediaXAddr`/`getProfiles`/`getStreamURI`/`getSnapshotURI` names consistent across tasks.
- **Known device-specific risk:** channel↔profile mapping (`chanNumFromSource`) and media XAddr path — both validated in T13; the `getProfiles`/`parseOnvifMediaUri` parsers rely on local-name XML matching so namespace prefixes don't matter.
- **Relay caller note (T10):** the exact accessor for the cached surv config must match existing `ServeHLS` code; the plan flags this explicitly rather than guessing a name.
