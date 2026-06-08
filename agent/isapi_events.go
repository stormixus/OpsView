package main

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// alertEvent is one parsed Hikvision ISAPI event edge.
type alertEvent struct {
	chNum  int
	kind   string // "motion" | "person" | "vehicle" | "linecross" | "intrusion"
	active bool
	tsMs   int64
}

// alertKind maps a Hikvision eventType (+ optional AcuSense targetType) to our
// event kind, or "" for events we ignore.
func alertKind(eventType, targetType string) string {
	et := strings.ToLower(strings.TrimSpace(eventType))
	tt := strings.ToLower(strings.TrimSpace(targetType))
	switch et {
	case "vmd", "motiondetection":
		switch tt {
		case "human":
			return "person"
		case "vehicle":
			return "vehicle"
		default:
			return "motion"
		}
	case "linedetection":
		return "linecross"
	case "fielddetection", "regionentrance", "regionexiting", "intrusion":
		return "intrusion"
	}
	return "" // videoloss, tamper, heartbeats, etc. -> ignored
}

// isapiAlert mirrors the fields we read from <EventNotificationAlert>.
type isapiAlert struct {
	ChannelID  int    `xml:"channelID"`
	DateTime   string `xml:"dateTime"`
	EventType  string `xml:"eventType"`
	EventState string `xml:"eventState"`
	TargetType string `xml:"targetType"`
}

// parseAlertEvent parses one <EventNotificationAlert> block into an edge. ok=false
// for blocks we ignore (channelID<=0, or an eventType we don't map).
func parseAlertEvent(block []byte) (alertEvent, bool) {
	var a isapiAlert
	if err := xml.Unmarshal(block, &a); err != nil {
		return alertEvent{}, false
	}
	if a.ChannelID <= 0 {
		return alertEvent{}, false
	}
	kind := alertKind(a.EventType, a.TargetType)
	if kind == "" {
		return alertEvent{}, false
	}
	var tsMs int64
	// Hikvision dateTime is local time, no timezone (e.g. 2026-06-08T15:05:33).
	if t, err := time.ParseInLocation("2006-01-02T15:04:05", strings.TrimSpace(a.DateTime), time.Local); err == nil {
		tsMs = t.UnixMilli()
	}
	return alertEvent{
		chNum:  a.ChannelID,
		kind:   kind,
		active: strings.EqualFold(strings.TrimSpace(a.EventState), "active"),
		tsMs:   tsMs,
	}, true
}

// scanAlertStream reads the multipart alertStream body, extracts each
// <EventNotificationAlert>...</EventNotificationAlert> block (ignoring multipart
// boundaries / part headers), parses it, and calls emit for relevant edges. It
// returns when r is exhausted or errors.
func scanAlertStream(r io.Reader, emit func(alertEvent)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var buf bytes.Buffer
	inBlock := false
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "<EventNotificationAlert") {
			inBlock = true
			buf.Reset()
		}
		if inBlock {
			buf.WriteString(line)
			buf.WriteByte('\n')
			if strings.Contains(line, "</EventNotificationAlert>") {
				inBlock = false
				if ev, ok := parseAlertEvent(buf.Bytes()); ok {
					emit(ev)
				}
			}
		}
	}
}

// digestStreamGET opens rawURL with HTTP Digest auth and returns the OPEN response
// for streaming (caller closes resp.Body). Mirrors onvifHTTPGet's handshake but
// keeps the body open.
func digestStreamGET(httpc *http.Client, rawURL, user, pass string) (*http.Response, error) {
	c := &onvifClient{user: user, pass: pass}
	mk := func(auth string) (*http.Response, error) {
		req, err := http.NewRequest("GET", rawURL, nil)
		if err != nil {
			return nil, err
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		return httpc.Do(req)
	}
	resp, err := mk("")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		chal := resp.Header.Get("WWW-Authenticate")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		dh := c.digestAuth("GET", rawURL, chal)
		if dh == "" {
			return nil, fmt.Errorf("alertStream: not a digest challenge")
		}
		resp, err = mk(dh)
		if err != nil {
			return nil, err
		}
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("alertStream: HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

// isapiEnsureCenterLinkage adds the `center` (Notify Surveillance Center) linkage
// to each channel's VMD trigger so motion is pushed to alertStream, preserving the
// existing record linkage. Idempotent (skips a channel already configured).
// Best-effort: logs and continues on per-channel errors.
func isapiEnsureCenterLinkage(httpc *http.Client, dvr DVRConfig, chNums []int) {
	base := fmt.Sprintf("http://%s:%d/ISAPI/Event/triggers/VMD-", dvr.Addr, dvr.Port)
	for _, ch := range chNums {
		url := fmt.Sprintf("%s%d", base, ch)
		body, err := onvifHTTPGet(httpc, url, dvr.Username, dvr.Password)
		if err != nil {
			log.Printf("[isapi-events] DVR %d ch %d: get trigger: %v", dvr.ID, ch, err)
			continue
		}
		if bytes.Contains(bytes.ToLower(body), []byte("<notificationmethod>center</notificationmethod>")) {
			continue // already enabled
		}
		// insert a center notification before the closing list tag
		ins := "<EventTriggerNotification><id>center</id><notificationMethod>center</notificationMethod></EventTriggerNotification></EventTriggerNotificationList>"
		out := bytes.Replace(body, []byte("</EventTriggerNotificationList>"), []byte(ins), 1)
		if bytes.Equal(out, body) {
			log.Printf("[isapi-events] DVR %d ch %d: no linkage list to amend", dvr.ID, ch)
			continue
		}
		if err := isapiPUT(httpc, url, dvr.Username, dvr.Password, out); err != nil {
			log.Printf("[isapi-events] DVR %d ch %d: enable center: %v", dvr.ID, ch, err)
		}
	}
}

// isapiPUT does a Digest-auth PUT of body. Best-effort.
func isapiPUT(httpc *http.Client, rawURL, user, pass string, body []byte) error {
	c := &onvifClient{user: user, pass: pass}
	mk := func(auth string) (*http.Response, error) {
		req, err := http.NewRequest("PUT", rawURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/xml")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		return httpc.Do(req)
	}
	resp, err := mk("")
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		chal := resp.Header.Get("WWW-Authenticate")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		dh := c.digestAuth("PUT", rawURL, chal)
		if resp, err = mk(dh); err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("PUT HTTP %d", resp.StatusCode)
	}
	return nil
}

// isapiAlertLoop enables center linkage then streams alertStream, emitting edges,
// reconnecting with backoff until stop is closed. chNums are the DVR's channels.
func isapiAlertLoop(httpc *http.Client, dvr DVRConfig, chNums []int, stop <-chan struct{}, emit func(alertEvent)) {
	isapiEnsureCenterLinkage(httpc, dvr, chNums)
	url := fmt.Sprintf("http://%s:%d/ISAPI/Event/notification/alertStream", dvr.Addr, dvr.Port)
	backoff := 3 * time.Second
	for {
		select {
		case <-stop:
			return
		default:
		}
		resp, err := digestStreamGET(httpc, url, dvr.Username, dvr.Password)
		if err != nil {
			log.Printf("[isapi-events] DVR %d alertStream connect: %v — retry in %s", dvr.ID, err, backoff)
			if !sleepOrStopISAPI(stop, backoff) {
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 3 * time.Second
		log.Printf("[isapi-events] DVR %d alertStream connected", dvr.ID)
		done := make(chan struct{})
		go func() {
			select {
			case <-stop:
				resp.Body.Close()
			case <-done:
			}
		}()
		scanAlertStream(resp.Body, emit)
		close(done)
		resp.Body.Close()
	}
}

func sleepOrStopISAPI(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}
