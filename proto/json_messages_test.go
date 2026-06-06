package proto

import (
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
