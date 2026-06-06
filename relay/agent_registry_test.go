package main

import "testing"

func TestRegistryParse(t *testing.T) {
	reg, err := parseAgentRegistry(`[{"id":"gangnam","name":"강남점","token":"tA"},{"id":"hongdae","name":"홍대점","token":"tB"}]`, "legacyTok")
	if err != nil {
		t.Fatal(err)
	}
	// default agent (agent_id "") authenticated by legacy token
	if e, ok := reg.lookup(""); !ok || e.Token != "legacyTok" || e.ID != "default" {
		t.Fatalf("default lookup=%+v ok=%v", e, ok)
	}
	if e, ok := reg.lookup("gangnam"); !ok || e.Token != "tA" || e.Name != "강남점" {
		t.Fatalf("gangnam lookup=%+v ok=%v", e, ok)
	}
	if _, ok := reg.lookup("unknown"); ok {
		t.Fatal("unknown agent must not resolve")
	}
}

func TestRegistryEmptyJSONJustDefault(t *testing.T) {
	reg, err := parseAgentRegistry("", "legacyTok")
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := reg.lookup(""); !ok || e.Token != "legacyTok" {
		t.Fatalf("default-only lookup=%+v ok=%v", e, ok)
	}
}

func TestRegistryDuplicateIDRejected(t *testing.T) {
	if _, err := parseAgentRegistry(`[{"id":"x","token":"1"},{"id":"x","token":"2"}]`, "leg"); err == nil {
		t.Fatal("duplicate agent id must error")
	}
}
