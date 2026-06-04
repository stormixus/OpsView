package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
