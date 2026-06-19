package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Per-wall custom tile order. Operators reorder wall tiles by dragging in the
// viewer; the new order is POSTed here, stored keyed by "<agentID>/<wallID>", and
// applied when the mosaic is (re)composed — so the GPU tiles in that order. Shared
// (every viewer of that wall sees the same order) and persisted to disk so it
// survives relay restarts. Channels not listed fall to the end in default order.
var wallOrders = struct {
	sync.Mutex
	m      map[string][]string
	loaded bool
	path   string
}{m: map[string][]string{}}

// wallOrderPath is the persistence file on the relay's data volume, or "" when no
// persistent location is configured (then order is in-memory only for this run).
func wallOrderPath() string {
	if v := strings.TrimSpace(os.Getenv("RELAY_WALL_ORDER_FILE")); v != "" {
		return v
	}
	if db := strings.TrimSpace(os.Getenv("RELAY_DB")); db != "" {
		return filepath.Join(filepath.Dir(db), "wallorder.json")
	}
	if rec := strings.TrimSpace(os.Getenv("RELAY_REC_DIR")); rec != "" {
		return filepath.Join(rec, "wallorder.json")
	}
	return ""
}

func loadWallOrdersLocked() {
	if wallOrders.loaded {
		return
	}
	wallOrders.loaded = true
	wallOrders.path = wallOrderPath()
	if wallOrders.path == "" {
		return
	}
	b, err := os.ReadFile(wallOrders.path)
	if err != nil {
		return
	}
	var m map[string][]string
	if json.Unmarshal(b, &m) == nil && m != nil {
		wallOrders.m = m
	}
}

func wallOrderKey(agentID, wallID string) string {
	if agentID == "" {
		agentID = "default"
	}
	return agentID + "/" + wallID
}

func getWallOrder(key string) []string {
	wallOrders.Lock()
	defer wallOrders.Unlock()
	loadWallOrdersLocked()
	return append([]string(nil), wallOrders.m[key]...)
}

func setWallOrder(key string, ids []string) {
	wallOrders.Lock()
	defer wallOrders.Unlock()
	loadWallOrdersLocked()
	wallOrders.m[key] = append([]string(nil), ids...)
	if wallOrders.path == "" {
		return
	}
	b, err := json.MarshalIndent(wallOrders.m, "", "  ")
	if err != nil {
		return
	}
	tmp := wallOrders.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, wallOrders.path) // atomic replace
	}
}

// reorderByPreference returns ids reordered so the ones present in pref come first
// (in pref's order), then the rest in their incoming order. Pure — unit-tested.
func reorderByPreference(ids, pref []string) []string {
	if len(pref) == 0 {
		return ids
	}
	have := map[string]bool{}
	for _, id := range ids {
		have[id] = true
	}
	out := make([]string, 0, len(ids))
	placed := map[string]bool{}
	for _, id := range pref {
		if have[id] && !placed[id] {
			out = append(out, id)
			placed[id] = true
		}
	}
	for _, id := range ids {
		if !placed[id] {
			out = append(out, id)
		}
	}
	return out
}

// applyWallOrder reorders ids by the stored custom order for this wall.
func applyWallOrder(key string, ids []string) []string {
	return reorderByPreference(ids, getWallOrder(key))
}
