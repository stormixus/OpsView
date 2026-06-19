package main

import (
	"strings"
	"sync"
)

// Per-wall tile order + column count, keyed by the wall's stable uuid (the key is
// "<agentID>/<wallID>", where wallID is the viewer's uuid for that wall). Operators
// reorder by dragging in the viewer; the new order/columns are POSTed, persisted in
// SQLite (wall_layout table), and applied when the mosaic composes — so the GPU
// tiles in that order. Shared (every viewer of that wall sees the same order) and
// survives relay restarts. For group walls the order IS the membership; channels
// not listed fall to the end in default order.
//
// The in-memory maps are the hot read path (EnsureMosaic, no DB handle); writes
// go through to SQLite via wallLayoutDB. Loaded from the DB at startup.

var wallLayoutDB *agentStore

var wallOrders = struct {
	sync.Mutex
	m map[string][]string
}{m: map[string][]string{}}

var wallCols = struct {
	sync.Mutex
	m map[string]int
}{m: map[string]int{}}

// initWallLayoutStore wires the SQLite store and loads persisted wall layouts into
// the in-memory maps. Call once at startup after the store is open.
func initWallLayoutStore(s *agentStore) {
	if s == nil {
		return
	}
	orders, cols, err := s.loadWallLayouts()
	if err != nil {
		return
	}
	wallOrders.Lock()
	if orders != nil {
		wallOrders.m = orders
	}
	wallOrders.Unlock()
	wallCols.Lock()
	if cols != nil {
		wallCols.m = cols
	}
	wallCols.Unlock()
	wallLayoutDB = s
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
	return append([]string(nil), wallOrders.m[key]...)
}

func setWallOrder(key string, ids []string) {
	wallOrders.Lock()
	wallOrders.m[key] = append([]string(nil), ids...)
	order := append([]string(nil), wallOrders.m[key]...)
	wallOrders.Unlock()
	persistWallLayout(key, order)
}

func getWallCols(key string) int {
	wallCols.Lock()
	defer wallCols.Unlock()
	return wallCols.m[key]
}

func setWallCols(key string, cols int) {
	wallCols.Lock()
	if cols > 0 {
		wallCols.m[key] = cols
	} else {
		delete(wallCols.m, key)
	}
	wallCols.Unlock()
	persistWallLayout(key, getWallOrder(key))
}

// persistWallLayout writes a wall's current order + columns to SQLite (no-op when
// no DB is configured).
func persistWallLayout(key string, order []string) {
	if wallLayoutDB == nil {
		return
	}
	_ = wallLayoutDB.saveWallLayout(key, strings.Join(order, ","), getWallCols(key))
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
