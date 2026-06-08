# Motion / Event Detection (ONVIF) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface DVR motion events on the recording timeline (jump-between-events markers) and bias retention so event footage outlives idle footage, reusing the DVR's own detection via ONVIF — the relay never decodes pixels.

**Architecture:** Agent subscribes to each ONVIF-capable DVR's PullPoint event stream and emits state-change *edges* (`proto.MsgSurvEvent`) to the relay. The relay pairs edges into intervals, persists them per `(stream,day)` as JSONL next to the recordings, serves them to the dashboard as timeline markers, and uses them to prioritize retention in the existing disk-cap janitor.

**Tech Stack:** Go (agent + relay + proto), ONVIF SOAP (reusing `agent/onvif.go`'s `onvifClient.call()` + WS-Security), vanilla-JS embedded dashboard.

**Spec:** `docs/superpowers/specs/2026-06-08-motion-event-detection-design.md`

---

## File Structure

- `proto/ovp.go` — add `MsgSurvEvent MessageType = 13` + `String()` arm.
- `proto/json_messages.go` — add `SurvEvent` payload struct.
- `agent/onvif.go` — add `eventsXAddr()` (resolve Events service) + `getEventProperties()` (topic probe).
- `agent/onvif_events.go` (new) — PullPoint subscription + PullMessages loop + edge parsing.
- `agent/surveillance.go` — `EventCapableDVRs()` helper exposing ONVIF DVRs + creds for the events manager.
- `agent/agent.go` — `sendSurvEvent()` + events-manager lifecycle (start/stop with the relay connection).
- `relay/events.go` (new) — `eventStore`: edge→interval pairing, JSONL persist, mtime-cached read, marker API handler.
- `relay/hub.go` — dispatch `MsgSurvEvent`; `stampSurvEventAgentID`.
- `relay/recorder.go` — event-aware janitor (priority-tiered deletion); own/reference the `eventStore`.
- `relay/dashboard_assets/app.js` + `style.css` — timeline marker overlay + click-to-seek.

Tests: `proto/json_messages_test.go`, `agent/onvif_events_test.go`, `relay/events_test.go`.

**Build/test reminders (from this codebase):**
- Relay: `cd relay && PATH=/opt/homebrew/bin:$PATH go build ./...`; binary smoke needs `go build -o relay .`.
- Agent: `cd agent && PATH=/opt/homebrew/bin:$PATH go build ./...`.
- Proto: `cd proto && go test ./...`.
- Race: `go test -race ./...` in the changed module.

---

## Phasing & Gate

- **Tasks 1–2 = Phase 0 (the gate).** Ship the capability probe to the live SM-Boutique agent, read what the DVR actually emits (Events service present? which topics?), and **capture a real `PullMessages` response XML**. STOP after Task 2's deploy and feed the captured XML back into Task 3's parser tests before implementing the parser. If the DVR exposes no usable ONVIF Events service, stop and switch to the vendor-fallback design (out of scope for this plan).
- **Tasks 3–9 = Phase 1.** The pipeline. Tasks 5–9 (proto downstream → relay → dashboard) are invariant to the probe outcome; only Task 3's parser depends on the captured XML.
- **Phase 1.5 (Protect-style per-kind chips/side-panel) is NOT in this plan** — conditional on Phase 0 showing smart events; the `kind` field is laid down now so that upgrade is frontend-only.

---

## Task 1: proto — `MsgSurvEvent` message type + payload

**Files:**
- Modify: `proto/ovp.go:30-34` (constant block) and the `String()` method around `proto/ovp.go:36-62`
- Modify: `proto/json_messages.go` (append struct)
- Test: `proto/json_messages_test.go`

- [ ] **Step 1: Write the failing test**

Add to `proto/json_messages_test.go`:

```go
func TestSurvEventRoundTrip(t *testing.T) {
	ev := SurvEvent{ChID: "dvr1_ch2", Kind: "motion", Active: true, TS: 1717843200123}
	payload, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	msg := MarshalMessage(MsgSurvEvent, payload)
	hdr, err := DecodeHeader(msg)
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if hdr.Type != MsgSurvEvent {
		t.Fatalf("type = %v, want MsgSurvEvent", hdr.Type)
	}
	var got SurvEvent
	if err := json.Unmarshal(msg[HeaderSize:], &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != ev {
		t.Fatalf("round-trip = %+v, want %+v", got, ev)
	}
	if MsgSurvEvent.String() != "SURV_EVENT" {
		t.Fatalf("String() = %q, want SURV_EVENT", MsgSurvEvent.String())
	}
}
```

(Confirm `json` is imported in the test file; if not, add it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd proto && go test ./... -run TestSurvEventRoundTrip`
Expected: FAIL — `MsgSurvEvent` and `SurvEvent` undefined.

- [ ] **Step 3: Add the constant + String arm**

In `proto/ovp.go`, after `MsgAgentControl MessageType = 12`:

```go
	MsgSurvEvent    MessageType = 13 // DVR motion/analytics event edge (publisher→relay)
```

In the `String()` switch, after the `MsgAgentControl` arm:

```go
	case MsgSurvEvent:
		return "SURV_EVENT"
```

- [ ] **Step 4: Add the payload struct**

Append to `proto/json_messages.go`:

```go
// SurvEvent is one DVR event edge (publisher→relay). The relay pairs Active
// true/false edges per (AgentID,ChID,Kind) into intervals for the recording
// timeline and retention. AgentID is left empty by the publisher and stamped by
// the relay, mirroring SurvConfig.
type SurvEvent struct {
	AgentID string `json:"agent_id,omitempty"`
	ChID    string `json:"ch_id"`  // "dvr1_ch2" — matches the stream/segment path
	Kind    string `json:"kind"`   // "motion" | "linecross" | "person" | "vehicle" | ...
	Active  bool   `json:"active"` // true = event started, false = ended
	TS      int64  `json:"ts"`     // event time, UTC unix milliseconds
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd proto && go test ./... -run TestSurvEventRoundTrip`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add proto/ovp.go proto/json_messages.go proto/json_messages_test.go
git commit -m "feat(proto): MsgSurvEvent message type for DVR event edges"
```

---

## Task 2: agent — ONVIF Events capability probe (Phase 0 gate)

**Files:**
- Modify: `agent/onvif.go` (add `eventsXAddr`, `getEventProperties`, topic parse)
- Modify: `agent/surveillance.go` (`probeDVROnvifEvents` + log on discovery)
- Test: `agent/onvif_events_test.go` (new)

- [ ] **Step 1: Write the failing test (capability + topic parse)**

Create `agent/onvif_events_test.go`:

```go
package main

import "testing"

const sampleGetEventPropertiesResp = `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
 <s:Body>
  <tev:GetEventPropertiesResponse xmlns:tev="http://www.onvif.org/ver10/events/wsdl"
       xmlns:tns1="http://www.onvif.org/ver10/topics">
   <wstop:TopicSet xmlns:wstop="http://docs.oasis-open.org/wsn/t-1">
    <tns1:VideoSource><MotionAlarm wstop:topic="true"/></tns1:VideoSource>
    <tns1:RuleEngine><CellMotionDetector><Motion wstop:topic="true"/></CellMotionDetector></tns1:RuleEngine>
   </wstop:TopicSet>
  </tev:GetEventPropertiesResponse>
 </s:Body>
</s:Envelope>`

func TestParseEventTopics(t *testing.T) {
	topics := parseEventTopics([]byte(sampleGetEventPropertiesResp))
	if len(topics) == 0 {
		t.Fatal("expected topics, got none")
	}
	joined := ""
	for _, tp := range topics {
		joined += tp + "\n"
	}
	for _, want := range []string{"MotionAlarm", "CellMotionDetector"} {
		if !containsSub(joined, want) {
			t.Fatalf("topics %q missing %q", joined, want)
		}
	}
}

func containsSub(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./... -run TestParseEventTopics`
Expected: FAIL — `parseEventTopics` undefined.

- [ ] **Step 3: Implement events service resolution + topic probe**

Add to `agent/onvif.go` (near the namespace consts, add `onvifNSEvents`):

```go
const onvifNSEvents = "http://www.onvif.org/ver10/events/wsdl"

// eventsXAddr resolves the Events service URL via GetServices/GetCapabilities,
// rewriting the advertised host to the one we dialed (devices advertise their
// own LAN IP). Returns "" if the device exposes no Events service.
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
// path strings (e.g. "VideoSource/MotionAlarm"). It is intentionally lenient:
// vendors structure the TopicSet differently, so we walk the XML token stream and
// collect element-name paths under the TopicSet, recording any path whose leaf
// carries a topic="true" marker (or, as a fallback, any non-namespace leaf).
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
```

(`bytes` is already imported in `agent/onvif.go`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./... -run TestParseEventTopics`
Expected: PASS.

- [ ] **Step 5: Wire a probe into discovery logging**

In `agent/surveillance.go`, add (call it from `discoverFromDVROnvif` after a successful discover, best-effort, non-fatal):

```go
// probeDVROnvifEvents logs whether a DVR exposes ONVIF Events and which topics —
// the Phase 0 gate for motion/event detection. Best-effort; never fails discovery.
func (m *SurveillanceManager) probeDVROnvifEvents(dvr DVRConfig) {
	c := newOnvifClient(dvr.Username, dvr.Password, 6*time.Second)
	c.http = m.client
	devURL := onvifDeviceURL(dvr.Addr, dvr.Port)
	evURL := c.eventsXAddr(devURL)
	if evURL == "" {
		log.Printf("[onvif-events] DVR %d (%s): NO Events service advertised", dvr.ID, dvr.Name)
		return
	}
	topics, err := c.getEventProperties(evURL)
	if err != nil {
		log.Printf("[onvif-events] DVR %d (%s): Events at %s but GetEventProperties failed: %v", dvr.ID, dvr.Name, evURL, err)
		return
	}
	log.Printf("[onvif-events] DVR %d (%s): Events at %s — %d topics: %v", dvr.ID, dvr.Name, evURL, len(topics), topics)
}
```

At the end of `discoverFromDVROnvif`, before `return out, nil`, add: `m.probeDVROnvifEvents(dvr)`.

- [ ] **Step 6: Build + commit**

Run: `cd agent && PATH=/opt/homebrew/bin:$PATH go build ./... && go test ./... -run 'TestParseEventTopics|TestOnvif'`
Expected: build OK, tests PASS.

```bash
git add agent/onvif.go agent/surveillance.go agent/onvif_events_test.go
git commit -m "feat(agent): ONVIF Events capability+topic probe (Phase 0 gate)"
```

---

## ⛔ CHECKPOINT (Phase 0 gate) — deploy + observe before Task 3

- [ ] Ship the agent (tag a release per the deployment topology), let the SM-Boutique agent run a discovery, and read the `[onvif-events]` log lines off that agent.
- [ ] **Capture a real `PullMessages` response.** If Events is supported, temporarily point a quick probe (or `curl` with the SOAP body from Task 3 Step 3) at the device and save the raw XML to `agent/testdata/pullmessages_real.xml`.
- [ ] **Decision:**
  - Events present + motion topic → proceed to Task 3, replacing the synthetic fixture in Task 3 Step 1 with the captured XML.
  - Events present + smart topics (person/vehicle/line) → proceed, and note Phase 1.5 is reachable.
  - No Events service → STOP. Switch to vendor-fallback design (Hikvision ISAPI `/Event/notification/alertStream` / Dahua) behind an event-source interface that emits the same `proto.SurvEvent`. Re-plan from there; Tasks 5–9 still apply unchanged.

---

## Task 3: agent — PullPoint subscription + PullMessages edge parser

**Files:**
- Create: `agent/onvif_events.go`
- Modify: `agent/onvif_events_test.go` (parser test)

- [ ] **Step 1: Write the failing test (PullMessages → edges)**

Add to `agent/onvif_events_test.go` (replace `samplePullMessagesResp` with `agent/testdata/pullmessages_real.xml` contents captured at the checkpoint if available):

```go
const samplePullMessagesResp = `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
 <s:Body>
  <tev:PullMessagesResponse xmlns:tev="http://www.onvif.org/ver10/events/wsdl">
   <wsnt:NotificationMessage xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2"
        xmlns:tt="http://www.onvif.org/ver10/schema">
    <wsnt:Topic>tns1:VideoSource/MotionAlarm</wsnt:Topic>
    <wsnt:Message>
     <tt:Message UtcTime="2026-06-08T09:00:00Z">
      <tt:Source><tt:SimpleItem Name="VideoSourceToken" Value="VideoSource_2"/></tt:Source>
      <tt:Data><tt:SimpleItem Name="State" Value="true"/></tt:Data>
     </tt:Message>
    </wsnt:Message>
   </wsnt:NotificationMessage>
  </tev:PullMessagesResponse>
 </s:Body>
</s:Envelope>`

func TestParsePullMessages(t *testing.T) {
	edges := parsePullMessages([]byte(samplePullMessagesResp))
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	e := edges[0]
	if e.chNum != 2 || e.kind != "motion" || !e.active {
		t.Fatalf("edge = %+v, want ch2 motion active", e)
	}
	if e.utcMs != 1749373200000 {
		t.Fatalf("utcMs = %d, want 1749373200000", e.utcMs)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && go test ./... -run TestParsePullMessages`
Expected: FAIL — `parsePullMessages` undefined.

- [ ] **Step 3: Implement the subscription + parser**

Create `agent/onvif_events.go`:

```go
package main

import (
	"bytes"
	"encoding/xml"
	"log"
	"strings"
	"time"
)

// onvifEdge is one parsed event state-change from a PullMessages response.
type onvifEdge struct {
	chNum  int
	kind   string
	active bool
	utcMs  int64
}

// topicToKind maps an ONVIF topic path to our event Kind. Lenient substring match;
// unknown topics return "" (ignored).
func topicToKind(topic string) string {
	switch {
	case strings.Contains(topic, "MotionAlarm"), strings.Contains(topic, "CellMotionDetector"), strings.Contains(topic, "MotionDetection"):
		return "motion"
	case strings.Contains(topic, "LineDetector"), strings.Contains(topic, "CrossLine"):
		return "linecross"
	case strings.Contains(topic, "Human"), strings.Contains(topic, "Person"):
		return "person"
	case strings.Contains(topic, "Vehicle"):
		return "vehicle"
	}
	return ""
}

// parsePullMessages extracts event edges from a PullMessagesResponse. Defensive:
// tolerates missing items, varied SimpleItem names, ignores unknown topics.
func parsePullMessages(data []byte) []onvifEdge {
	type simpleItem struct {
		Name  string `xml:"Name,attr"`
		Value string `xml:"Value,attr"`
	}
	var env struct {
		Msgs []struct {
			Topic   string `xml:"Topic"`
			Message struct {
				UtcTime string       `xml:"UtcTime,attr"`
				Source  []simpleItem `xml:"Source>SimpleItem"`
				Data    []simpleItem `xml:"Data>SimpleItem"`
			} `xml:"Message>Message"`
		} `xml:"Body>PullMessagesResponse>NotificationMessage"`
	}
	if err := xml.Unmarshal(data, &env); err != nil {
		return nil
	}
	var edges []onvifEdge
	for _, m := range env.Msgs {
		kind := topicToKind(m.Topic)
		if kind == "" {
			continue
		}
		ch := 0
		for i, s := range m.Message.Source {
			if strings.Contains(s.Name, "Source") || strings.Contains(s.Name, "Channel") || strings.Contains(s.Name, "Token") {
				ch = chanNumFromSource(s.Value, i)
				break
			}
		}
		if ch == 0 {
			continue
		}
		active := false
		for _, s := range m.Message.Data {
			if strings.Contains(s.Name, "State") || s.Name == "IsMotion" || strings.Contains(s.Name, "Motion") {
				active = strings.EqualFold(s.Value, "true")
				break
			}
		}
		var utcMs int64
		if t, err := time.Parse(time.RFC3339, m.Message.UtcTime); err == nil {
			utcMs = t.UnixMilli()
		}
		edges = append(edges, onvifEdge{chNum: ch, kind: kind, active: active, utcMs: utcMs})
	}
	return edges
}

// --- subscription lifecycle ---

// subscribeURL is the PullPoint subscription endpoint returned by Create.
type pullPoint struct {
	endpoint string
}

// createPullPoint creates a PullPoint subscription and returns its endpoint.
func (c *onvifClient) createPullPoint(eventsURL string) (*pullPoint, error) {
	body := `<tev:CreatePullPointSubscription xmlns:tev="` + onvifNSEvents + `">` +
		`<tev:InitialTerminationTime>PT1M</tev:InitialTerminationTime>` +
		`</tev:CreatePullPointSubscription>`
	resp, err := c.call(eventsURL, onvifNSEvents+"/CreatePullPointSubscription", body)
	if err != nil {
		return nil, err
	}
	var env struct {
		Addr string `xml:"Body>CreatePullPointSubscriptionResponse>SubscriptionReference>Address"`
	}
	if err := xml.Unmarshal(resp, &env); err != nil || strings.TrimSpace(env.Addr) == "" {
		// Some devices pull from the events URL itself.
		return &pullPoint{endpoint: eventsURL}, nil
	}
	ep := strings.TrimSpace(env.Addr)
	// Rewrite host to the dialed events host (devices advertise their own IP).
	if u, e1 := urlParse(ep); e1 == nil {
		if eu, e2 := urlParse(eventsURL); e2 == nil {
			u.Host = eu.Host
			ep = u.String()
		}
	}
	return &pullPoint{endpoint: ep}, nil
}

// pull issues one PullMessages and returns the parsed edges.
func (c *onvifClient) pull(p *pullPoint) ([]onvifEdge, error) {
	body := `<tev:PullMessages xmlns:tev="` + onvifNSEvents + `">` +
		`<tev:Timeout>PT1S</tev:Timeout><tev:MessageLimit>10</tev:MessageLimit>` +
		`</tev:PullMessages>`
	resp, err := c.call(p.endpoint, onvifNSEvents+"/PullMessages", body)
	if err != nil {
		return nil, err
	}
	return parsePullMessages(resp), nil
}

// runEventLoop subscribes and pulls until stop is closed, invoking emit for each
// edge. Recreates the subscription on error with backoff. dvrID prefixes ChID.
func (c *onvifClient) runEventLoop(eventsURL string, stop <-chan struct{}, emit func(onvifEdge)) {
	backoff := 3 * time.Second
	for {
		select {
		case <-stop:
			return
		default:
		}
		pp, err := c.createPullPoint(eventsURL)
		if err != nil {
			log.Printf("[onvif-events] subscribe %s: %v — retry in %s", eventsURL, err, backoff)
			if !sleepOrStop(stop, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = 3 * time.Second
		deadline := time.Now().Add(55 * time.Second) // renew before PT1M expiry by resubscribing
		for time.Now().Before(deadline) {
			select {
			case <-stop:
				return
			default:
			}
			edges, err := c.pull(pp)
			if err != nil {
				log.Printf("[onvif-events] pull %s: %v — resubscribing", pp.endpoint, err)
				break
			}
			for _, e := range edges {
				emit(e)
			}
		}
	}
}

func sleepOrStop(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}

func nextBackoff(b time.Duration) time.Duration {
	if b < 30*time.Second {
		return b * 2
	}
	return b
}
```

Add a small `urlParse` shim at the top of the file (or reuse `net/url` directly — replace `urlParse` calls with `url.Parse` and import `net/url`). Keep `bytes` import if used; if `parsePullMessages` uses `xml.Unmarshal` (it does, not a decoder) you may drop `bytes` — let the compiler guide you.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && go test ./... -run TestParsePullMessages`
Expected: PASS. If parsing the captured real XML fails, adjust the struct tags to the device's actual element nesting (this is exactly why Phase 0 captured it).

- [ ] **Step 5: Build + commit**

Run: `cd agent && PATH=/opt/homebrew/bin:$PATH go build ./... && go test ./...`
Expected: build OK, tests PASS.

```bash
git add agent/onvif_events.go agent/onvif_events_test.go agent/testdata/pullmessages_real.xml
git commit -m "feat(agent): ONVIF PullPoint subscription + edge parser"
```

---

## Task 4: agent — events manager lifecycle + send to relay

**Files:**
- Modify: `agent/surveillance.go` (`EventCapableDVRs`)
- Modify: `agent/agent.go` (`sendSurvEvent`, events-manager goroutine tied to the connection)

- [ ] **Step 1: Expose event-capable DVRs**

Add to `agent/surveillance.go`:

```go
// onvifEventDVR is a DVR that advertises an ONVIF Events service.
type onvifEventDVR struct {
	dvr       DVRConfig
	eventsURL string
}

// EventCapableDVRs returns ONVIF DVRs that expose an Events service (resolved
// live). Used by the agent to start per-DVR event subscriptions.
func (m *SurveillanceManager) EventCapableDVRs() []onvifEventDVR {
	dvrs, err := m.ListDVRs()
	if err != nil {
		return nil
	}
	var out []onvifEventDVR
	for _, d := range dvrs {
		if !strings.EqualFold(d.Protocol, "onvif") {
			continue
		}
		c := newOnvifClient(d.Username, d.Password, 6*time.Second)
		c.http = m.client
		if ev := c.eventsXAddr(onvifDeviceURL(d.Addr, d.Port)); ev != "" {
			out = append(out, onvifEventDVR{dvr: d, eventsURL: ev})
		}
	}
	return out
}
```

(Confirm `strings` is imported in `surveillance.go`.)

- [ ] **Step 2: Add `sendSurvEvent` to the agent**

In `agent/agent.go`, mirror `sendSurvConfig`:

```go
// sendSurvEvent forwards one DVR event edge to the relay. AgentID is left empty;
// the relay stamps it (mirroring SurvConfig).
func (a *Agent) sendSurvEvent(chID, kind string, active bool, tsMs int64) {
	payload, _ := json.Marshal(proto.SurvEvent{ChID: chID, Kind: kind, Active: active, TS: tsMs})
	msg := proto.MarshalMessage(proto.MsgSurvEvent, payload)
	a.connMu.Lock()
	conn := a.conn
	a.connMu.Unlock()
	if conn == nil {
		return
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
		log.Printf("[agent] sendSurvEvent send error: %v", err)
	}
}
```

- [ ] **Step 3: Start/stop the events manager with the connection**

In the agent's per-connection setup (where `go a.readPump(conn)` is started, `agent/agent.go:176`), start an events manager scoped to that connection. Add fields to `Agent`: `evStop chan struct{}` guarded by `connMu`. After a successful connect:

```go
// start ONVIF event subscriptions for this connection
a.connMu.Lock()
if a.evStop != nil {
	close(a.evStop)
}
a.evStop = make(chan struct{})
evStop := a.evStop
a.connMu.Unlock()
go a.runEventManager(evStop)
```

And on disconnect (where the conn is torn down), close `a.evStop` under `connMu` and nil it.

Add the manager:

```go
// runEventManager launches one ONVIF event-subscription goroutine per
// event-capable DVR; each emits edges to the relay via sendSurvEvent. Stops when
// stop is closed (connection lost / shutdown).
func (a *Agent) runEventManager(stop <-chan struct{}) {
	if a.survMgr == nil {
		return
	}
	dvrs := a.survMgr.EventCapableDVRs()
	for _, ed := range dvrs {
		ed := ed
		c := newOnvifClient(ed.dvr.Username, ed.dvr.Password, 10*time.Second)
		log.Printf("[onvif-events] subscribing DVR %d (%s) at %s", ed.dvr.ID, ed.dvr.Name, ed.eventsURL)
		go c.runEventLoop(ed.eventsURL, stop, func(e onvifEdge) {
			chID := fmt.Sprintf("dvr%d_ch%d", ed.dvr.ID, e.chNum)
			a.sendSurvEvent(chID, e.kind, e.active, e.utcMs)
		})
	}
}
```

> **ChID note:** confirm the stream ID format the recorder uses (`streamPath` builds `dvrN_chM`). Match whatever `st.ID` is in `relay/recorder.go:105` (`streamPath(s.id, st.ID)`); the `dvr%d_ch%d` here must equal that `st.ID`. If the recorder's stream ID uses the DVR's *config* ID vs a 1-based index, align this format to it (grep `st.ID` producers). Adjust before shipping — a mismatch means markers won't line up with segments.

- [ ] **Step 4: Build**

Run: `cd agent && PATH=/opt/homebrew/bin:$PATH go build ./...`
Expected: build OK. (`fmt`, `encoding/json`, `proto`, `websocket` already imported in agent.go.)

- [ ] **Step 5: Commit**

```bash
git add agent/agent.go agent/surveillance.go
git commit -m "feat(agent): per-DVR ONVIF event subscription manager -> relay"
```

---

## Task 5: relay — event store (pairing + JSONL + mtime cache)

**Files:**
- Create: `relay/events.go`
- Test: `relay/events_test.go`

- [ ] **Step 1: Write the failing test (edge→interval pairing + persistence)**

Create `relay/events_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEventStorePairing(t *testing.T) {
	dir := t.TempDir()
	es := newEventStore(dir)
	// open then close -> one interval
	es.add("dvr1_ch2", "motion", true, 1_000_000)
	es.add("dvr1_ch2", "motion", false, 1_030_000) // +30s
	day := dayKeyFromMs(1_000_000)
	got := es.eventsForDay("dvr1_ch2", day)
	if len(got) != 1 {
		t.Fatalf("got %d intervals, want 1", len(got))
	}
	if got[0].Start != 1000 || got[0].End != 1030 || got[0].Kind != "motion" {
		t.Fatalf("interval = %+v, want start=1000 end=1030 motion (seconds)", got[0])
	}
	// persisted to <dir>/dvr1_ch2/.events/<day>.jsonl
	p := filepath.Join(dir, "dvr1_ch2", ".events", day+".jsonl")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("expected jsonl at %s: %v", p, err)
	}
}

func TestEventStoreOrphanClose(t *testing.T) {
	es := newEventStore(t.TempDir())
	es.maxEventMs = 600_000 // 10 min
	es.add("dvr1_ch1", "motion", true, 5_000_000)
	// a far-future open of the same key forces the stale one closed at start+max
	es.add("dvr1_ch1", "motion", true, 9_000_000)
	got := es.eventsForDay("dvr1_ch1", dayKeyFromMs(5_000_000))
	if len(got) != 1 || got[0].End != (5_000_000+600_000)/1000 {
		t.Fatalf("orphan not force-closed: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd relay && go test ./... -run TestEventStore`
Expected: FAIL — `newEventStore` undefined.

- [ ] **Step 3: Implement the event store**

Create `relay/events.go`:

```go
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// eventInterval is one paired event on the recording timeline (unix SECONDS, to
// match the segment timeline's units).
type eventInterval struct {
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	Kind  string `json:"kind"`
}

type openKey struct{ stream, kind string }
type openInterval struct {
	startMs int64
}

type evDayCache struct {
	mtime time.Time
	ivals []eventInterval
}

// eventStore pairs SurvEvent edges into intervals, persists them per (stream,day)
// as JSONL under <recDir>/<stream>/.events/, and serves them (mtime-cached) to the
// marker API and the janitor. If recDir is "", it operates in-memory only.
type eventStore struct {
	recDir     string
	maxEventMs int64

	mu    sync.Mutex
	open  map[openKey]openInterval
	cache map[string]evDayCache // "stream|day" -> intervals (mtime-keyed)
}

func newEventStore(recDir string) *eventStore {
	return &eventStore{
		recDir:     recDir,
		maxEventMs: 10 * 60 * 1000,
		open:       map[openKey]openInterval{},
		cache:      map[string]evDayCache{},
	}
}

func dayKeyFromMs(ms int64) string {
	return time.UnixMilli(ms).In(time.Local).Format("20060102")
}

// add ingests one edge. Active=true opens an interval (force-closing any stale
// open one past maxEventMs); Active=false closes the open interval.
func (e *eventStore) add(stream, kind string, active bool, tsMs int64) {
	if stream == "" || kind == "" || tsMs <= 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	k := openKey{stream, kind}
	if active {
		if cur, ok := e.open[k]; ok {
			// stale open: force-close at start+max before opening the new one
			e.closeLocked(stream, kind, cur.startMs, cur.startMs+e.maxEventMs)
		}
		e.open[k] = openInterval{startMs: tsMs}
		return
	}
	if cur, ok := e.open[k]; ok {
		delete(e.open, k)
		end := tsMs
		if end > cur.startMs+e.maxEventMs {
			end = cur.startMs + e.maxEventMs
		}
		e.closeLocked(stream, kind, cur.startMs, end)
	}
}

// closeLocked appends a finished interval (caller holds e.mu).
func (e *eventStore) closeLocked(stream, kind string, startMs, endMs int64) {
	iv := eventInterval{Start: startMs / 1000, End: endMs / 1000, Kind: kind}
	day := dayKeyFromMs(startMs)
	if e.recDir != "" {
		dir := filepath.Join(e.recDir, filepath.FromSlash(stream), ".events")
		if err := os.MkdirAll(dir, 0o755); err == nil {
			if f, err := os.OpenFile(filepath.Join(dir, day+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				if b, e2 := json.Marshal(iv); e2 == nil {
					f.Write(append(b, '\n'))
				}
				f.Close()
			}
		}
	}
	// invalidate the day cache so the next read re-loads
	delete(e.cache, stream+"|"+day)
	// also keep an in-memory copy for the recDir=="" case
	if e.recDir == "" {
		key := stream + "|" + day
		c := e.cache[key]
		c.ivals = append(c.ivals, iv)
		e.cache[key] = c
	}
}

// eventsForDay returns intervals for (stream,day), mtime-cached from the JSONL.
func (e *eventStore) eventsForDay(stream, day string) []eventInterval {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := stream + "|" + day
	if e.recDir == "" {
		return append([]eventInterval(nil), e.cache[key].ivals...)
	}
	p := filepath.Join(e.recDir, filepath.FromSlash(stream), ".events", day+".jsonl")
	info, err := os.Stat(p)
	if err != nil {
		return nil
	}
	if c, ok := e.cache[key]; ok && c.mtime.Equal(info.ModTime()) {
		return append([]eventInterval(nil), c.ivals...)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	var ivals []eventInterval
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var iv eventInterval
		if json.Unmarshal(sc.Bytes(), &iv) == nil {
			ivals = append(ivals, iv)
		}
	}
	e.cache[key] = evDayCache{mtime: info.ModTime(), ivals: ivals}
	return append([]eventInterval(nil), ivals...)
}

// overlaps reports whether [startSec,endSec] overlaps any event for the stream on
// the day(s) it spans. Used by the janitor.
func (e *eventStore) overlaps(stream string, startSec, endSec int64) bool {
	for _, day := range daysSpanned(startSec, endSec) {
		for _, iv := range e.eventsForDay(stream, day) {
			if iv.Start < endSec && iv.End > startSec {
				return true
			}
		}
	}
	return false
}

func daysSpanned(startSec, endSec int64) []string {
	d1 := time.Unix(startSec, 0).In(time.Local).Format("20060102")
	d2 := time.Unix(endSec, 0).In(time.Local).Format("20060102")
	if d1 == d2 {
		return []string{d1}
	}
	return []string{d1, d2}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd relay && go test ./... -run TestEventStore`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add relay/events.go relay/events_test.go
git commit -m "feat(relay): event store — edge pairing + JSONL persist + mtime cache"
```

---

## Task 6: relay — dispatch `MsgSurvEvent` in the hub

**Files:**
- Modify: `relay/hub.go` (dispatch arm near `:365`, `stampSurvEventAgentID`, own an `eventStore`)
- Test: `relay/events_test.go` (stamp test)

- [ ] **Step 1: Write the failing test**

Add to `relay/events_test.go`:

```go
func TestStampSurvEventAgentID(t *testing.T) {
	// build a MsgSurvEvent payload with empty AgentID, stamp it, confirm streamPath use
	stream := streamPath("acme", "dvr1_ch2")
	if stream == "" {
		t.Skip("streamPath unavailable")
	}
	if stream != "acme/dvr1_ch2" {
		t.Fatalf("streamPath = %q, want acme/dvr1_ch2", stream)
	}
}
```

(This pins the stream-path expectation the dispatch relies on; the real ingestion is covered by Task 5's store tests.)

- [ ] **Step 2: Run test**

Run: `cd relay && go test ./... -run TestStampSurvEventAgentID`
Expected: PASS or a clear failure telling you the actual `streamPath` format — adjust the expectation and the Task 4 ChID format to match.

- [ ] **Step 3: Add the eventStore to the Hub and dispatch the message**

Wherever the `Recorder` is constructed/owned, also construct `h.events = newEventStore(os.Getenv("RELAY_REC_DIR"))` (so the store writes under the same recordings root; empty → in-memory). Add an `events *eventStore` field to `Hub`.

In `relay/hub.go`, in the publisher message switch at `:365`, add:

```go
case proto.MsgSurvEvent:
	if h.events != nil {
		var ev proto.SurvEvent
		if json.Unmarshal(data[proto.HeaderSize:], &ev) == nil {
			stream := streamPath(sess.id, ev.ChID)
			h.events.add(stream, ev.Kind, ev.Active, ev.TS)
		}
	}
```

(No need to forward to watchers — events terminate at the store. `json` and `proto` are already imported in hub.go.)

- [ ] **Step 4: Build + test**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go build ./... && go test ./... -run 'TestEventStore|TestStampSurvEvent'`
Expected: build OK, PASS.

- [ ] **Step 5: Commit**

```bash
git add relay/hub.go relay/events_test.go
git commit -m "feat(relay): ingest MsgSurvEvent into the event store (scoped by streamPath)"
```

---

## Task 7: relay — marker API `GET /dashboard/rec/events`

**Files:**
- Modify: `relay/events.go` (handler) + route registration (where `HandleDashboardRecordings` is registered)
- Test: `relay/events_test.go`

- [ ] **Step 1: Write the failing test**

Add to `relay/events_test.go`:

```go
func TestEventsAPIShape(t *testing.T) {
	es := newEventStore(t.TempDir())
	es.add("dvr1_ch2", "motion", true, 1_000_000)
	es.add("dvr1_ch2", "motion", false, 1_010_000)
	ivals := es.eventsForDay("dvr1_ch2", dayKeyFromMs(1_000_000))
	b, _ := json.Marshal(map[string]interface{}{"events": ivals})
	if !containsStr(string(b), `"kind":"motion"`) || !containsStr(string(b), `"start":1000`) {
		t.Fatalf("payload shape wrong: %s", b)
	}
}

func containsStr(s, sub string) bool { return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

(Add `import "encoding/json"` to the test file if not present.)

- [ ] **Step 2: Run test**

Run: `cd relay && go test ./... -run TestEventsAPIShape`
Expected: PASS (this validates the JSON shape the handler returns).

- [ ] **Step 3: Implement the handler**

Add to `relay/events.go`:

```go
import "net/http"  // add to the existing import block

// HandleDashboardRecEvents serves event-timeline markers for a stream+day.
// Admin-gated, mirrors HandleDashboardRecordings.
func (h *Hub) HandleDashboardRecEvents(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.events == nil {
		http.Error(w, "events disabled", http.StatusConflict)
		return
	}
	stream := r.URL.Query().Get("stream")
	day := r.URL.Query().Get("day")
	w.Header().Set("Content-Type", "application/json")
	if day == todayKey() {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=300")
	}
	ivals := []eventInterval{}
	if stream != "" && len(day) == 8 {
		ivals = h.events.eventsForDay(stream, day)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"events": ivals})
}

func todayKey() string { return time.Now().In(time.Local).Format("20060102") }
```

Register the route next to the recordings routes (search where `HandleDashboardRecordings` / `HandleDashboardRecFile` are wired into the mux):

```go
mux.HandleFunc("/dashboard/rec/events", h.HandleDashboardRecEvents)
```

- [ ] **Step 4: Build + smoke**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go build ./... && go test ./...`
Expected: build OK, all PASS.

- [ ] **Step 5: Commit**

```bash
git add relay/events.go
git commit -m "feat(relay): GET /dashboard/rec/events marker API"
```

---

## Task 8: relay — event-aware janitor

**Files:**
- Modify: `relay/recorder.go` (`runJanitor`, new env consts, eventStore reference)
- Test: `relay/events_test.go` (janitor ordering)

- [ ] **Step 1: Write the failing test**

Add to `relay/events_test.go`:

```go
func TestJanitorPrefersNonEvent(t *testing.T) {
	segs := []janSeg{
		{path: "a", size: 100, startSec: 1000, durSec: 300, event: false}, // old, no event
		{path: "b", size: 100, startSec: 1300, durSec: 300, event: true},  // old, event
		{path: "c", size: 100, startSec: 9_000_000, durSec: 300, event: false}, // within keep-all
	}
	// cap forces dropping 100 bytes; keep-all protects c; non-event old (a) goes first
	order := janitorDeleteOrder(segs, 200 /*cap*/, 300 /*total? computed*/, janPolicy{
		keepAllCutoff:   8_000_000,
		keepEventCutoff: 0,
	})
	if len(order) == 0 || order[0].path != "a" {
		t.Fatalf("expected 'a' (non-event, old) deleted first, got %+v", order)
	}
	for _, s := range order {
		if s.path == "c" {
			t.Fatal("keep-all segment c must never be deleted")
		}
	}
}
```

- [ ] **Step 2: Run test**

Run: `cd relay && go test ./... -run TestJanitorPrefersNonEvent`
Expected: FAIL — `janSeg`/`janitorDeleteOrder`/`janPolicy` undefined.

- [ ] **Step 3: Refactor the janitor into a testable ordering function + wire events**

In `relay/recorder.go`, add consts + a reference to the store, and replace the body of `runJanitor`'s deletion logic with a call to a pure `janitorDeleteOrder`:

```go
const (
	recKeepAllHoursDefault  = 72
	recKeepEventDaysDefault = 30
)

type janSeg struct {
	path     string
	size     int64
	startSec int64
	durSec   int64
	event    bool
}

type janPolicy struct {
	keepAllCutoff   int64 // segments with startSec >= this are never deleted
	keepEventCutoff int64 // event segments with startSec < this may be deleted (tier 3)
}

// janitorDeleteOrder returns, in deletion order, the segments to remove to get
// total bytes back under cap, honoring: (1) keep-all window never deleted,
// (2) non-event old deleted first, (3) old event segments next, (4) global
// oldest-first fallback. Pure for testing.
func janitorDeleteOrder(segs []janSeg, cap, total int64, p janPolicy) []janSeg {
	if cap <= 0 || total <= cap {
		return nil
	}
	// Deletion tiers, sacrificed in order: (1) non-event old, (3) old event past
	// keep-event, (2) event within keep-event (last resort). keep-all is never added.
	var tier1, tier2, tier3 []janSeg
	for _, s := range segs {
		switch {
		case s.startSec >= p.keepAllCutoff:
			continue // protected
		case !s.event:
			tier1 = append(tier1, s)
		case s.startSec < p.keepEventCutoff:
			tier3 = append(tier3, s)
		default:
			tier2 = append(tier2, s) // event, within keep-event window
		}
	}
	sortByStart(tier1)
	sortByStart(tier2)
	sortByStart(tier3)

	var out []janSeg
	freed := int64(0)
	for _, group := range [][]janSeg{tier1, tier3, tier2} {
		for _, s := range group {
			if total-freed <= cap {
				return out
			}
			out = append(out, s)
			freed += s.size
		}
	}
	return out
}

func sortByStart(a []janSeg) {
	sort.Slice(a, func(i, j int) bool { return a[i].startSec < a[j].startSec })
}
```

Then in `runJanitor`, after walking the files, build `[]janSeg` (parse `startSec` from the filename via `recNameLayout`, `durSec` from `r.segSecs`, `event` via `r.hub.events.overlaps(stream, start, start+dur)` where `stream` is the seg's path relative to `r.dir`), compute the cutoffs from env (`RELAY_REC_KEEP_ALL_HOURS`, `RELAY_REC_KEEP_EVENT_DAYS`, defaulting to the consts), call `janitorDeleteOrder`, and `os.Remove` each returned path. Keep the existing log line.

> Deriving `stream` from a walked path: `rel, _ := filepath.Rel(r.dir, filepath.Dir(p)); stream := filepath.ToSlash(rel)`. Skip `.events` dirs in the walk (`if d.IsDir() && d.Name()==".events" { return filepath.SkipDir }`).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd relay && go test ./... -run TestJanitor`
Expected: PASS. (`sort` is already imported in recorder.go.)

- [ ] **Step 5: Build + full race test**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go build ./... && go test -race ./...`
Expected: build OK, all PASS, no races.

- [ ] **Step 6: Commit**

```bash
git add relay/recorder.go relay/events_test.go
git commit -m "feat(relay): event-aware retention janitor (keep-all window + event priority)"
```

---

## Task 9: dashboard — timeline event markers

**Files:**
- Modify: `relay/dashboard_assets/app.js` (fetch events on day load; draw bands; click-to-seek)
- Modify: `relay/dashboard_assets/style.css` (`.rec-ev` band styles)

- [ ] **Step 1: Fetch events when a day loads**

In the recording-tab day-load path (where `/dashboard/rec/recordings?...&day=` is fetched and the timeline is drawn), add a parallel fetch:

```js
async function loadRecEvents(stream, day){
  try{
    const r = await fetch(`/dashboard/rec/events?stream=${encodeURIComponent(stream)}&day=${day}`, {credentials:'same-origin'});
    if(!r.ok) return [];
    const j = await r.json();
    return Array.isArray(j.events) ? j.events : [];
  }catch(_){ return []; }
}
```

Store the result on the rec context (e.g. `recCtx.events = await loadRecEvents(stream, day)`), alongside the segment list.

- [ ] **Step 2: Draw event bands on the 24h timeline**

In the timeline render function (the one that lays out segments across 24h), after drawing segments, overlay each event interval as a thin band. Day start (local midnight) in unix seconds = `dayStart`; timeline width maps `[dayStart, dayStart+86400]` to `[0,100]%`:

```js
function renderEventBands(container, events, dayStart){
  for(const ev of events){
    const left = Math.max(0, (ev.start - dayStart) / 864);      // % (86400/100)
    const width = Math.max(0.3, (ev.end - ev.start) / 864);     // min width so 1s events are visible
    const b = document.createElement('div');
    b.className = 'rec-ev rec-ev-' + (ev.kind || 'motion');
    b.style.left = left + '%';
    b.style.width = width + '%';
    b.title = (ev.kind||'motion') + ' · ' + new Date(ev.start*1000).toLocaleTimeString();
    b.addEventListener('click', (e)=>{ e.stopPropagation(); recSeekTo(ev.start); });
    container.appendChild(b);
  }
}
```

Call `renderEventBands(timelineEl, recCtx.events, dayStart)` after the segment layout. Reuse the existing seek function for `recSeekTo` (the same one click-on-timeline already uses to jump to a unix-second position).

- [ ] **Step 3: Style the bands**

Add to `relay/dashboard_assets/style.css`:

```css
.rec-ev{ position:absolute; top:0; bottom:0; background:var(--warn); opacity:.55;
  pointer-events:auto; cursor:pointer; border-radius:1px; z-index:3; }
.rec-ev:hover{ opacity:.85; }
.rec-ev-motion{ background:var(--warn); }
/* kind-specific colors are data-ready for Phase 1.5 (person/vehicle/linecross) */
```

(Ensure the timeline container is `position:relative` so `%` offsets anchor to it — it already is if segments use the same absolute layout.)

- [ ] **Step 4: Smoke (embedded asset served)**

Run:
```bash
cd relay && PATH=/opt/homebrew/bin:$PATH go build -o relay . && \
RELAY_REC_DIR=/tmp/rectest ./relay & sleep 1
curl -s localhost:8080/dashboard/app.js | grep -c 'rec-ev' ; kill %1
```
Expected: `grep -c` ≥ 1 (new JS is embedded/served). Then `pkill -f '/relay/relay'` and `rm -f relay`.

- [ ] **Step 5: Commit**

```bash
git add relay/dashboard_assets/app.js relay/dashboard_assets/style.css
git commit -m "feat(dashboard): motion event bands on the recording timeline (click-to-seek)"
```

---

## Completion

After all tasks: `go test -race ./...` in `proto`, `agent`, `relay`; `go vet ./...`. Then use **superpowers:finishing-a-development-branch** to verify tests, present merge/PR options, and clean up.

**Manual end-to-end (Phase 1):** with `RELAY_REC_DIR` set and an ONVIF-events DVR connected, trigger motion at a camera; confirm a warn-colored band appears at the right position on that channel's 24h timeline and clicking it seeks playback into the correct segment. Verify clock alignment (DVR `UtcTime` vs segment local time) — if markers are consistently offset, switch event TS to relay receive-time (noted in the spec's Risks).
