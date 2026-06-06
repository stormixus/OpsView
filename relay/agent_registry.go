package main

import (
	"encoding/json"
	"fmt"
)

// agentEntry is one configured agent's publish-time identity.
type agentEntry struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token"`
}

// agentRegistry maps agent_id -> entry. The empty key "" is the default
// (legacy) agent, authenticated by RELAY_PUBLISHER_TOKEN.
type agentRegistry struct {
	byID map[string]agentEntry
}

// parseAgentRegistry builds the registry from the RELAY_AGENTS JSON array plus
// the legacy publisher token (the default agent). jsonStr may be empty.
func parseAgentRegistry(jsonStr, legacyToken string) (*agentRegistry, error) {
	reg := &agentRegistry{byID: map[string]agentEntry{}}
	// default agent (agent_id "")
	reg.byID[""] = agentEntry{ID: "default", Name: "default", Token: legacyToken}

	if jsonStr != "" {
		var entries []agentEntry
		if err := json.Unmarshal([]byte(jsonStr), &entries); err != nil {
			return nil, fmt.Errorf("RELAY_AGENTS parse: %w", err)
		}
		for _, e := range entries {
			if e.ID == "" || e.ID == "default" {
				return nil, fmt.Errorf("agent id must be non-empty and not 'default'")
			}
			if e.Token == "" {
				return nil, fmt.Errorf("agent %q: token required", e.ID)
			}
			if _, dup := reg.byID[e.ID]; dup {
				return nil, fmt.Errorf("duplicate agent id %q", e.ID)
			}
			if e.Name == "" {
				e.Name = e.ID
			}
			reg.byID[e.ID] = e
		}
	}
	return reg, nil
}

// lookup resolves an agent_id (possibly "") to its entry.
func (r *agentRegistry) lookup(agentID string) (agentEntry, bool) {
	e, ok := r.byID[agentID]
	return e, ok
}

// ids returns all configured agent ids (including "default").
func (r *agentRegistry) ids() []string {
	out := make([]string, 0, len(r.byID))
	for id := range r.byID {
		out = append(out, id)
	}
	return out
}
