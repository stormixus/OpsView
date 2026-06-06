package main

import (
	"encoding/json"
	"testing"

	"github.com/opsview/opsview/proto"
)

// A named agent's surv config must carry its tenant id so watchers build the
// scoped /surv/<id>/dvrN_chM path; the default/legacy agent stays flat.
func TestStampSurvConfigAgentID(t *testing.T) {
	cfg := proto.SurvConfig{
		DVRs:     []proto.DVRInfo{{ID: 1, Name: "DVR1", Addr: "192.168.0.69", Port: 80, Username: "admin", Password: "pw"}},
		Channels: []proto.ChannelInfo{{DVRID: 1, ChNum: 2, Name: "1층 복도", Enabled: true}},
	}
	payload, _ := json.Marshal(cfg)
	msg := proto.MarshalMessage(proto.MsgSurvConfig, payload)

	// named agent -> AgentID stamped, payload otherwise intact
	out := parseSurv(t, stampSurvConfigAgentID(msg, "SM-Boutique"))
	if out.AgentID != "SM-Boutique" {
		t.Fatalf("AgentID = %q, want SM-Boutique", out.AgentID)
	}
	if len(out.DVRs) != 1 || out.DVRs[0].Username != "admin" || len(out.Channels) != 1 || out.Channels[0].Name != "1층 복도" {
		t.Fatalf("stamp dropped config data: %+v", out)
	}

	// default + empty -> no scope stamped (flat path preserved)
	for _, id := range []string{"default", ""} {
		if got := parseSurv(t, stampSurvConfigAgentID(msg, id)).AgentID; got != "" {
			t.Fatalf("agent %q: AgentID = %q, want empty", id, got)
		}
	}
}

func parseSurv(t *testing.T, msg []byte) proto.SurvConfig {
	t.Helper()
	var c proto.SurvConfig
	if err := json.Unmarshal(msg[proto.HeaderSize:], &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return c
}
