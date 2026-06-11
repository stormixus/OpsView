package main

import "testing"

func TestResolveStableKeyAllocatesAndMatches(t *testing.T) {
	m := newTestSurvManager(t)

	k1, err := m.resolveStableKey("SER-A", "mac-a", "1.2.3.4")
	if err != nil {
		t.Fatalf("resolve A: %v", err)
	}
	if k1 != 1 {
		t.Errorf("first key = %d, want 1", k1)
	}
	k2, _ := m.resolveStableKey("SER-B", "mac-b", "1.2.3.5")
	if k2 != 2 {
		t.Errorf("second key = %d, want 2", k2)
	}
	if k, _ := m.resolveStableKey("SER-A", "", ""); k != 1 {
		t.Errorf("serial re-match = %d, want 1", k)
	}
	if k, _ := m.resolveStableKey("", "mac-b", ""); k != 2 {
		t.Errorf("mac match = %d, want 2", k)
	}
	if k, _ := m.resolveStableKey("", "", "1.2.3.4"); k != 1 {
		t.Errorf("addr match = %d, want 1", k)
	}
}

func TestResolveStableKeyNeverReusesFreedKey(t *testing.T) {
	m := newTestSurvManager(t)
	k1, _ := m.resolveStableKey("SER-A", "", "1.1.1.1")
	k2, _ := m.resolveStableKey("SER-B", "", "1.1.1.2")
	if k1 != 1 || k2 != 2 {
		t.Fatalf("keys = %d,%d want 1,2", k1, k2)
	}
	k3, _ := m.resolveStableKey("SER-C", "", "1.1.1.3")
	if k3 != 3 {
		t.Errorf("third key = %d, want 3 (no reuse)", k3)
	}
}

func TestEnrichDeviceKeyFillsEmptyFacetsKeepsKey(t *testing.T) {
	m := newTestSurvManager(t)
	k, _ := m.resolveStableKey("", "", "9.9.9.9")
	if k != 1 {
		t.Fatalf("addr-only key = %d, want 1", k)
	}
	k2, _ := m.resolveStableKey("SER-Z", "", "9.9.9.9")
	if k2 != 1 {
		t.Errorf("after enrich, key = %d, want 1 (unchanged)", k2)
	}
	if k3, _ := m.resolveStableKey("SER-Z", "", ""); k3 != 1 {
		t.Errorf("serial match after enrich = %d, want 1", k3)
	}
}
