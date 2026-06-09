package proto

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestHelloAgentID(t *testing.T) {
	var h Hello
	if err := json.Unmarshal([]byte(`{"role":"publisher","agent_id":"gangnam"}`), &h); err != nil {
		t.Fatal(err)
	}
	if h.AgentID != "gangnam" {
		t.Fatalf("AgentID=%q want gangnam", h.AgentID)
	}
	// 하위호환: 없으면 빈 문자열
	var h2 Hello
	json.Unmarshal([]byte(`{"role":"publisher"}`), &h2)
	if h2.AgentID != "" {
		t.Fatalf("missing agent_id => %q want empty", h2.AgentID)
	}
}

func TestAgentControlRoundTrip(t *testing.T) {
	b, _ := json.Marshal(AgentControl{Action: "reconnect"})
	var out AgentControl
	if err := json.Unmarshal(b, &out); err != nil || out.Action != "reconnect" {
		t.Fatalf("roundtrip: %v %+v", err, out)
	}
	if MsgAgentControl.String() != "AGENT_CONTROL" {
		t.Fatalf("String: %s", MsgAgentControl.String())
	}
}

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

func TestSurvEventThumbRoundTrip(t *testing.T) {
	th := SurvEventThumb{ChID: "dvr1_ch2", TS: 1717843200123, Jpeg: []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}}
	payload, err := json.Marshal(th)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	msg := MarshalMessage(MsgSurvEventThumb, payload)
	hdr, err := DecodeHeader(msg)
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if hdr.Type != MsgSurvEventThumb {
		t.Fatalf("type = %v, want MsgSurvEventThumb", hdr.Type)
	}
	var got SurvEventThumb
	if err := json.Unmarshal(msg[HeaderSize:], &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ChID != th.ChID || got.TS != th.TS || !bytes.Equal(got.Jpeg, th.Jpeg) {
		t.Fatalf("round-trip = %+v, want %+v", got, th)
	}
	if MsgSurvEventThumb.String() != "SURV_EVENT_THUMB" {
		t.Fatalf("String() = %q, want SURV_EVENT_THUMB", MsgSurvEventThumb.String())
	}
}
