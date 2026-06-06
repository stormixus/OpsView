package main

import (
	"encoding/json"
	"fmt"
	"sync"
)

// agentEntry is one configured agent's publish-time identity.
type agentEntry struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token"`
}

// agentRegistry maps agent_id -> entry. The empty key "" is the default
// (legacy) agent, authenticated by RELAY_PUBLISHER_TOKEN. When a store is
// attached, named agents are persisted in SQLite and managed at runtime.
type agentRegistry struct {
	mu    sync.RWMutex
	byID  map[string]agentEntry
	store *agentStore // nil = env-only (immutable)
}

// parseAgentRegistry builds the registry from the RELAY_AGENTS JSON array plus
// the legacy publisher token (the default agent). jsonStr may be empty. The
// result is in-memory until useStore attaches persistence.
func parseAgentRegistry(jsonStr, legacyToken string) (*agentRegistry, error) {
	byID := map[string]agentEntry{}
	// default agent (agent_id "")
	byID[""] = agentEntry{ID: "default", Name: "default", Token: legacyToken}

	if jsonStr != "" {
		var entries []agentEntry
		if err := json.Unmarshal([]byte(jsonStr), &entries); err != nil {
			return nil, fmt.Errorf("RELAY_AGENTS parse: %w", err)
		}
		for _, e := range entries {
			if err := validateAgentEntry(e); err != nil {
				return nil, err
			}
			if e.Name == "" {
				e.Name = e.ID
			}
			if _, dup := byID[e.ID]; dup {
				return nil, fmt.Errorf("duplicate agent id %q", e.ID)
			}
			byID[e.ID] = e
		}
	}
	return &agentRegistry{byID: byID}, nil
}

// validateAgentEntry rejects ids/tokens that would break auth or collide with default.
func validateAgentEntry(e agentEntry) error {
	if e.ID == "" || e.ID == "default" {
		return fmt.Errorf("agent id must be non-empty and not 'default'")
	}
	if e.Token == "" {
		return fmt.Errorf("agent %q: token required", e.ID)
	}
	return nil
}

// useStore attaches SQLite persistence: seeds the store from the current
// in-memory named agents if it is empty, then loads the registry from it.
func (r *agentRegistry) useStore(s *agentStore) error {
	n, err := s.count()
	if err != nil {
		return err
	}
	if n == 0 {
		r.mu.RLock()
		seed := make([]agentEntry, 0)
		for id, e := range r.byID {
			if id != "" {
				seed = append(seed, e)
			}
		}
		r.mu.RUnlock()
		for _, e := range seed {
			if err := s.upsert(e); err != nil {
				return err
			}
		}
	}
	r.store = s
	return r.reload()
}

// reload refreshes the named agents from the store (default agent preserved).
func (r *agentRegistry) reload() error {
	if r.store == nil {
		return nil
	}
	entries, err := r.store.list()
	if err != nil {
		return err
	}
	r.mu.Lock()
	def := r.byID[""]
	m := map[string]agentEntry{"": def}
	for _, e := range entries {
		m[e.ID] = e
	}
	r.byID = m
	r.mu.Unlock()
	return nil
}

// lookup resolves an agent_id (possibly "") to its entry.
func (r *agentRegistry) lookup(agentID string) (agentEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.byID[agentID]
	return e, ok
}

// ids returns all configured agent ids (including "default").
func (r *agentRegistry) ids() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byID))
	for id := range r.byID {
		out = append(out, id)
	}
	return out
}

// listNamed returns the persisted (non-default) agents.
func (r *agentRegistry) listNamed() []agentEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]agentEntry, 0)
	for id, e := range r.byID {
		if id != "" {
			out = append(out, e)
		}
	}
	return out
}

// editable reports whether the registry is backed by a store (dashboard CRUD).
func (r *agentRegistry) editable() bool { return r.store != nil }

// upsertNamed adds or updates a named agent and persists it.
func (r *agentRegistry) upsertNamed(e agentEntry) error {
	if r.store == nil {
		return fmt.Errorf("agent registry is read-only (no RELAY_DB configured)")
	}
	if e.Name == "" {
		e.Name = e.ID
	}
	if err := validateAgentEntry(e); err != nil {
		return err
	}
	if err := r.store.upsert(e); err != nil {
		return err
	}
	return r.reload()
}

// removeNamed deletes a named agent and persists the change.
func (r *agentRegistry) removeNamed(id string) error {
	if r.store == nil {
		return fmt.Errorf("agent registry is read-only (no RELAY_DB configured)")
	}
	if err := r.store.remove(id); err != nil {
		return err
	}
	return r.reload()
}
