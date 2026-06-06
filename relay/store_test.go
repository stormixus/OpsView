package main

import (
	"path/filepath"
	"testing"
)

func TestAgentStoreCRUDAndRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	store, err := openAgentStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.close() })

	reg, _ := parseAgentRegistry(`[{"id":"seed","name":"Seed","token":"t0"}]`, "leg")
	if err := reg.useStore(store); err != nil {
		t.Fatalf("useStore: %v", err)
	}
	if !reg.editable() {
		t.Fatal("registry should be editable with a store")
	}
	// env entries are seeded into the store
	if e, ok := reg.lookup("seed"); !ok || e.Token != "t0" {
		t.Fatalf("seed missing: %+v ok=%v", e, ok)
	}
	// add a named agent
	if err := reg.upsertNamed(agentEntry{ID: "gangnam", Name: "강남", Token: "tA"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if e, ok := reg.lookup("gangnam"); !ok || e.Token != "tA" {
		t.Fatal("upsert not visible")
	}
	// persists across reopen
	store2, _ := openAgentStore(path)
	t.Cleanup(func() { store2.close() })
	reg2, _ := parseAgentRegistry("", "leg")
	if err := reg2.useStore(store2); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg2.lookup("gangnam"); !ok {
		t.Fatal("not persisted across reopen")
	}
	if e, _ := reg2.lookup(""); e.Token != "leg" {
		t.Fatalf("default token lost: %q", e.Token)
	}
	// remove
	if err := reg.removeNamed("gangnam"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := reg.lookup("gangnam"); ok {
		t.Fatal("remove not applied")
	}
	// validation
	if err := reg.upsertNamed(agentEntry{ID: "x", Token: ""}); err == nil {
		t.Fatal("empty token must be rejected")
	}
	if err := reg.upsertNamed(agentEntry{ID: "default", Token: "y"}); err == nil {
		t.Fatal("'default' id must be rejected")
	}
}

func TestDashboardPasswordDBOverridesEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	store, err := openAgentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.close() })

	cfg := cfgWithDash("env-pass")
	cfg.Store = store
	h := NewHub(cfg)
	if h.effectiveDashToken() != "env-pass" {
		t.Fatalf("no DB token yet => should use env, got %q", h.effectiveDashToken())
	}
	if err := h.setDashToken("db-pass"); err != nil {
		t.Fatalf("setDashToken: %v", err)
	}
	if h.effectiveDashToken() != "db-pass" {
		t.Fatal("DB token must override env")
	}
	// persists across restart (new hub from same store path)
	store2, _ := openAgentStore(path)
	t.Cleanup(func() { store2.close() })
	cfg2 := cfgWithDash("env-pass")
	cfg2.Store = store2
	h2 := NewHub(cfg2)
	if h2.effectiveDashToken() != "db-pass" {
		t.Fatal("DB password not loaded on restart")
	}
}

func TestDashboardPasswordReadOnlyWithoutStore(t *testing.T) {
	h := NewHub(cfgWithDash("env-pass"))
	if err := h.setDashToken("x"); err == nil {
		t.Fatal("setDashToken must fail without a store")
	}
	if h.effectiveDashToken() != "env-pass" {
		t.Fatal("should still use env")
	}
}

func TestRegistryReadOnlyWithoutStore(t *testing.T) {
	reg, _ := parseAgentRegistry("", "leg")
	if reg.editable() {
		t.Fatal("env-only registry must not be editable")
	}
	if err := reg.upsertNamed(agentEntry{ID: "a", Token: "t"}); err == nil {
		t.Fatal("upsert must fail without a store")
	}
}

func TestIPLabels(t *testing.T) {
	store, err := openAgentStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.close() })
	if err := store.setIPLabel("1.2.3.4", "프론트"); err != nil {
		t.Fatal(err)
	}
	if m, _ := store.ipLabels(); m["1.2.3.4"] != "프론트" {
		t.Fatalf("set: %v", m)
	}
	store.setIPLabel("1.2.3.4", "사장님") // update
	if m, _ := store.ipLabels(); m["1.2.3.4"] != "사장님" {
		t.Fatal("update failed")
	}
	store.setIPLabel("1.2.3.4", "") // empty removes
	if m, _ := store.ipLabels(); len(m) != 0 {
		t.Fatalf("remove failed: %v", m)
	}
}
