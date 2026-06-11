package main

import (
	"encoding/xml"
	"testing"
)

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

func TestDeviceInfoParsesSerialAndMAC(t *testing.T) {
	body := []byte(`<?xml version="1.0"?><DeviceInfo><serialNumber>DS-7208-123</serialNumber><macAddress>a4:14:37:aa:bb:cc</macAddress><analogChannelNum>8</analogChannelNum></DeviceInfo>`)
	var info isAPIDeviceInfo
	if err := xml.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	if info.SerialNumber != "DS-7208-123" {
		t.Errorf("serial = %q", info.SerialNumber)
	}
	if info.MACAddress != "a4:14:37:aa:bb:cc" {
		t.Errorf("mac = %q", info.MACAddress)
	}
}

func TestAddDVRUsesStableKeyAsID(t *testing.T) {
	m := newTestSurvManager(t)
	// offline device (deviceInfo unreachable at port 1) -> keyed by addr. AddDVR
	// must still assign dvrs.id = the resolved stable key (here 1, first allocation).
	d, err := m.AddDVR("cam", "127.0.0.1", 1, "", 0, "u", "p", "isapi", 2000, "sub")
	if err != nil {
		t.Fatalf("AddDVR: %v", err)
	}
	var key int64
	m.db.QueryRow(`SELECT stable_key FROM device_keys WHERE addr=?`, "127.0.0.1").Scan(&key)
	if d.ID != key {
		t.Errorf("dvr id = %d, device_keys key = %d (must match)", d.ID, key)
	}
	var rowID int64
	m.db.QueryRow(`SELECT id FROM dvrs WHERE addr=?`, "127.0.0.1").Scan(&rowID)
	if rowID != key {
		t.Errorf("dvrs.id = %d, want %d", rowID, key)
	}
}
