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

func TestSeedFromExistingDVRsIdempotent(t *testing.T) {
	m := newTestSurvManager(t)
	if _, err := m.db.Exec(`DELETE FROM device_keys`); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.Exec(`INSERT INTO dvrs (id, name, addr, port) VALUES (1,'a','10.0.0.1',80),(2,'b','10.0.0.2',80)`); err != nil {
		t.Fatal(err)
	}

	m.seedDeviceKeys() // first run seeds
	m.seedDeviceKeys() // second run must be a no-op (idempotent)

	rows := map[int64]string{}
	r, err := m.db.Query(`SELECT stable_key, addr FROM device_keys ORDER BY stable_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for r.Next() {
		var k int64
		var addr string
		r.Scan(&k, &addr)
		rows[k] = addr
	}
	if len(rows) != 2 || rows[1] != "10.0.0.1" || rows[2] != "10.0.0.2" {
		t.Errorf("seeded device_keys = %v, want {1:10.0.0.1, 2:10.0.0.2}", rows)
	}
}

func TestResetDBPreservesDeviceKeysAndReusesKey(t *testing.T) {
	m := newTestSurvManager(t)
	k, _ := m.resolveStableKey("SER-S", "", "1.2.3.4")
	if _, err := m.db.Exec(`INSERT INTO dvrs (id, name, addr, port) VALUES (?,?,?,?)`, k, "x", "1.2.3.4", 80); err != nil {
		t.Fatal(err)
	}

	if err := m.ResetDB(); err != nil {
		t.Fatalf("ResetDB: %v", err)
	}

	var nd int
	m.db.QueryRow(`SELECT COUNT(*) FROM dvrs`).Scan(&nd)
	if nd != 0 {
		t.Errorf("dvrs after reset = %d, want 0", nd)
	}
	if k2, _ := m.resolveStableKey("SER-S", "", "9.9.9.9"); k2 != k {
		t.Errorf("re-resolve after reset = %d, want %d (same key)", k2, k)
	}
	if k3, _ := m.resolveStableKey("SER-NEW", "", "8.8.8.8"); k3 != k+1 {
		t.Errorf("new device key = %d, want %d", k3, k+1)
	}
}
