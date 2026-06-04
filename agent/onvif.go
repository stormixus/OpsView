package main

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
