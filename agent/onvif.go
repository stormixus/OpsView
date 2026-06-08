package main

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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
	ctype := fmt.Sprintf(`application/soap+xml; charset=utf-8; action="%s"`, action)
	doReq := func(authHeader string) (*http.Response, error) {
		req, err := http.NewRequest("POST", url, bytes.NewBufferString(env))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", ctype)
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		return c.http.Do(req)
	}

	resp, err := doReq("")
	if err != nil {
		return nil, err
	}
	// Many DVRs (e.g. Hikvision) gate the ONVIF endpoint with HTTP Digest auth on
	// top of the WS-Security UsernameToken. Answer the challenge and retry once.
	if resp.StatusCode == http.StatusUnauthorized {
		chal := resp.Header.Get("WWW-Authenticate")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if dh := c.digestAuth("POST", url, chal); dh != "" {
			if resp, err = doReq(dh); err != nil {
				return nil, err
			}
		}
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

var digestParamRe = regexp.MustCompile(`(\w+)=(?:"([^"]*)"|([^,\s]+))`)

func parseDigestChallenge(challenge string) map[string]string {
	out := map[string]string{}
	challenge = strings.TrimSpace(challenge)
	if i := strings.IndexAny(challenge, " \t"); i >= 0 {
		challenge = challenge[i+1:] // drop the "Digest" scheme word
	}
	for _, m := range digestParamRe.FindAllStringSubmatch(challenge, -1) {
		v := m[2]
		if v == "" {
			v = m[3]
		}
		out[strings.ToLower(m[1])] = v
	}
	return out
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// onvifHTTPGet GETs rawURL, answering an HTTP Digest challenge if the device
// demands one (Hikvision ONVIF snapshot endpoints require Digest), falling back
// to Basic auth otherwise.
func onvifHTTPGet(httpc *http.Client, rawURL, user, pass string) ([]byte, error) {
	c := &onvifClient{http: httpc, user: user, pass: pass}
	do := func(auth string, basic bool) (*http.Response, error) {
		req, err := http.NewRequest("GET", rawURL, nil)
		if err != nil {
			return nil, err
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		} else if basic {
			req.SetBasicAuth(user, pass)
		}
		return httpc.Do(req)
	}
	resp, err := do("", false)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		chal := resp.Header.Get("WWW-Authenticate")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if dh := c.digestAuth("GET", rawURL, chal); dh != "" {
			resp, err = do(dh, false)
		} else {
			resp, err = do("", true) // Basic fallback
		}
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("onvif snapshot: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

// digestAuth builds an HTTP Digest Authorization header answering a
// WWW-Authenticate challenge, or "" if it is not a Digest challenge.
func (c *onvifClient) digestAuth(method, endpoint, challenge string) string {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(challenge)), "digest") {
		return ""
	}
	p := parseDigestChallenge(challenge)
	realm, nonce := p["realm"], p["nonce"]
	if realm == "" || nonce == "" {
		return ""
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	uri := u.RequestURI()
	ha1 := md5hex(c.user + ":" + realm + ":" + c.pass)
	ha2 := md5hex(method + ":" + uri)
	cnonce := randomHex(8)
	nc := "00000001"
	qop := p["qop"]
	var response string
	if qop == "auth" {
		response = md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
	} else {
		response = md5hex(ha1 + ":" + nonce + ":" + ha2)
	}
	h := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
		c.user, realm, nonce, uri, response)
	if qop != "" {
		h += fmt.Sprintf(`, qop=%s, nc=%s, cnonce="%s"`, qop, nc, cnonce)
	}
	if p["opaque"] != "" {
		h += fmt.Sprintf(`, opaque="%s"`, p["opaque"])
	}
	if p["algorithm"] != "" {
		h += fmt.Sprintf(`, algorithm=%s`, p["algorithm"])
	}
	return h
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	xmlEscapeTo(&b, s)
	return b.String()
}

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
		Body struct {
			Response struct {
				MediaUri struct {
					Uri string `xml:"Uri"`
				} `xml:"MediaUri"`
			} `xml:",any"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(data, &env); err != nil {
		return "", err
	}
	if env.Body.Response.MediaUri.Uri == "" {
		return "", fmt.Errorf("onvif: no MediaUri/Uri in response")
	}
	return env.Body.Response.MediaUri.Uri, nil
}

const (
	onvifNSDevice = "http://www.onvif.org/ver10/device/wsdl"
	onvifNSMedia  = "http://www.onvif.org/ver10/media/wsdl"
	onvifNSEvents = "http://www.onvif.org/ver10/events/wsdl"
)

func onvifDeviceURL(addr string, port int) string {
	return fmt.Sprintf("http://%s:%d/onvif/device_service", addr, port)
}

// onvifFetchURLAllowed limits SSRF from a device-provided snapshot URL: it must
// be http(s) and must not target link-local (incl. the 169.254.169.254
// cloud-metadata endpoint), link-local-multicast, or the unspecified address.
// Loopback is allowed (the agent is a LAN desktop app; the cloud-metadata
// endpoint is the real threat). Private LAN addresses (the usual DVR) pass.
// Hostnames are allowed (DVRs use IP literals; classifying a hostname would need
// DNS we don't want in this path).
func onvifFetchURLAllowed(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	h := u.Hostname()
	if h == "" {
		return false
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return true // hostname literal
	}
	return !(ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified())
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

func (c *onvifClient) probeWith(httpc *http.Client, deviceURL string) bool {
	if httpc != nil {
		c.http = httpc
	}
	return c.probe(deviceURL)
}

// mediaXAddr resolves the Media service URL via GetServices, rewriting its host
// to the host we actually dialed (devices often advertise their LAN IP, which
// can differ from how we reach them).
func (c *onvifClient) mediaXAddr(deviceURL string) (string, error) {
	dialed, _ := url.Parse(deviceURL)
	rewrite := func(xaddr string) string {
		if u, err := url.Parse(xaddr); err == nil && dialed != nil {
			u.Host = dialed.Host // devices advertise their own IP; use the one we dialed
			return u.String()
		}
		return xaddr
	}

	// 1) GetServices (ONVIF 2.x). Some devices (e.g. some Hikvision firmwares)
	//    return a SOAP fault here, so treat any failure as "try the next method".
	if resp, err := c.call(deviceURL, onvifNSDevice+"/GetServices",
		`<tds:GetServices xmlns:tds="`+onvifNSDevice+`"><tds:IncludeCapability>false</tds:IncludeCapability></tds:GetServices>`); err == nil {
		var env struct {
			Services []struct {
				Namespace string `xml:"Namespace"`
				XAddr     string `xml:"XAddr"`
			} `xml:"Body>GetServicesResponse>Service"`
		}
		if xml.Unmarshal(resp, &env) == nil {
			for _, s := range env.Services {
				if strings.Contains(s.Namespace, "/media") && s.XAddr != "" {
					return rewrite(s.XAddr), nil
				}
			}
		}
	}

	// 2) GetCapabilities (ONVIF 1.x; widely supported on Hikvision DVRs).
	if resp, err := c.call(deviceURL, onvifNSDevice+"/GetCapabilities",
		`<tds:GetCapabilities xmlns:tds="`+onvifNSDevice+`"><tds:Category>All</tds:Category></tds:GetCapabilities>`); err == nil {
		var env struct {
			XAddr string `xml:"Body>GetCapabilitiesResponse>Capabilities>Media>XAddr"`
		}
		if xml.Unmarshal(resp, &env) == nil && env.XAddr != "" {
			return rewrite(env.XAddr), nil
		}
	}

	// 3) Fallback to the conventional media-service path.
	if dialed != nil {
		return "http://" + dialed.Host + "/onvif/media_service", nil
	}
	return "", fmt.Errorf("onvif: media service not found")
}

// eventsXAddr resolves the Events service URL via GetServices/GetCapabilities,
// rewriting the advertised host to the one we dialed. Returns "" if the device
// exposes no Events service.
func (c *onvifClient) eventsXAddr(deviceURL string) string {
	dialed, _ := url.Parse(deviceURL)
	rewrite := func(xaddr string) string {
		if u, err := url.Parse(xaddr); err == nil && dialed != nil {
			u.Host = dialed.Host
			return u.String()
		}
		return xaddr
	}
	if resp, err := c.call(deviceURL, onvifNSDevice+"/GetServices",
		`<tds:GetServices xmlns:tds="`+onvifNSDevice+`"><tds:IncludeCapability>false</tds:IncludeCapability></tds:GetServices>`); err == nil {
		var env struct {
			Services []struct {
				Namespace string `xml:"Namespace"`
				XAddr     string `xml:"XAddr"`
			} `xml:"Body>GetServicesResponse>Service"`
		}
		if xml.Unmarshal(resp, &env) == nil {
			for _, s := range env.Services {
				if strings.Contains(s.Namespace, "/events") && s.XAddr != "" {
					return rewrite(s.XAddr)
				}
			}
		}
	}
	if resp, err := c.call(deviceURL, onvifNSDevice+"/GetCapabilities",
		`<tds:GetCapabilities xmlns:tds="`+onvifNSDevice+`"><tds:Category>All</tds:Category></tds:GetCapabilities>`); err == nil {
		var env struct {
			XAddr string `xml:"Body>GetCapabilitiesResponse>Capabilities>Events>XAddr"`
		}
		if xml.Unmarshal(resp, &env) == nil && env.XAddr != "" {
			return rewrite(env.XAddr)
		}
	}
	return ""
}

// getEventProperties asks the Events service which topics it supports.
func (c *onvifClient) getEventProperties(eventsURL string) ([]string, error) {
	resp, err := c.call(eventsURL, onvifNSEvents+"/GetEventProperties",
		`<tev:GetEventProperties xmlns:tev="`+onvifNSEvents+`"/>`)
	if err != nil {
		return nil, err
	}
	return parseEventTopics(resp), nil
}

// parseEventTopics flattens a GetEventPropertiesResponse TopicSet into leaf topic
// path strings (e.g. "VideoSource/MotionAlarm"). Lenient: vendors structure the
// TopicSet differently, so walk the token stream and record paths whose leaf
// carries topic="true".
func parseEventTopics(data []byte) []string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var path []string
	inTopicSet := false
	var topics []string
	seen := map[string]bool{}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "TopicSet" {
				inTopicSet = true
				path = path[:0]
				continue
			}
			if inTopicSet {
				path = append(path, t.Name.Local)
				isTopic := false
				for _, a := range t.Attr {
					if a.Name.Local == "topic" && a.Value == "true" {
						isTopic = true
					}
				}
				if isTopic {
					p := strings.Join(path, "/")
					if !seen[p] {
						seen[p] = true
						topics = append(topics, p)
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "TopicSet" {
				inTopicSet = false
				continue
			}
			if inTopicSet && len(path) > 0 {
				path = path[:len(path)-1]
			}
		}
	}
	return topics
}

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
	for _, p := range profiles {
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
		snap, _ := c.getSnapshotURI(mediaURL, p.Token) // best-effort; snapshots optional
		chNum := chanNumFromSource(p.SourceToken, len(out))
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
