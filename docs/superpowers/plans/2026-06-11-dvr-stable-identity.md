# DVR Stable Identity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `dvrs.id` a stable, serial-derived key (surviving `ResetDB`) so re-adding the same physical DVR reuses the same id — keeping stream ids, recordings, and deep-links associated with the same camera.

**Architecture:** All changes are agent-side (`agent/surveillance.go`). A new `device_keys` table maps a device's natural identity (serial → mac → addr) to a small integer that is never reused and is preserved across `ResetDB`. `AddDVR` resolves that key and inserts it as the explicit `dvrs.id`. The relay/dashboard are unchanged — they already use whatever id the agent emits, so the id becoming stable propagates automatically.

**Tech Stack:** Go, SQLite (`database/sql`), Hikvision ISAPI deviceInfo (XML).

**Build/test commands:**
- Agent: `cd agent && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -race ./...`
- Relay (must still compile — NO relay changes expected): `cd relay && PATH=/opt/homebrew/bin:$PATH go build ./...`
- Format: `PATH=/opt/homebrew/bin:$PATH gofmt -l agent`

**Conventions:** Test manager helper is `newTestSurvManager(t)` (see `agent/surveillance_meta_test.go`). The HTTP client is `m.client`. `columnExists(db, table, col)` already exists. Spec: `docs/superpowers/specs/2026-06-11-dvr-stable-identity-design.md`.

---

## File Structure

- `agent/surveillance.go` — ALL production changes:
  - `migrate()`: add `device_keys` CREATE TABLE + one-time seed from existing `dvrs`.
  - new `resolveStableKey(serial, mac, addr)` + `enrichDeviceKey(...)`.
  - `isAPIDeviceInfo` struct: add `SerialNumber`, `MACAddress`.
  - new `fetchDeviceIdentity(addr, port, username, password, protocol)`.
  - `AddDVR`: resolve key, insert explicit `dvrs.id`.
  - `ResetDB`: unchanged behaviour, but a comment noting it must NOT delete `device_keys`.
- `agent/surveillance_identity_test.go` — NEW test file for all the above.

---

## Task 1: `device_keys` table + key allocator (`resolveStableKey` / `enrichDeviceKey`)

**Files:**
- Modify: `agent/surveillance.go` (`migrate` CREATE TABLE; add `resolveStableKey`, `enrichDeviceKey`)
- Test: `agent/surveillance_identity_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `agent/surveillance_identity_test.go`:
```go
package main

import "testing"

func TestResolveStableKeyAllocatesAndMatches(t *testing.T) {
	m := newTestSurvManager(t)

	// first device: no match -> allocate 1
	k1, err := m.resolveStableKey("SER-A", "mac-a", "1.2.3.4")
	if err != nil {
		t.Fatalf("resolve A: %v", err)
	}
	if k1 != 1 {
		t.Errorf("first key = %d, want 1", k1)
	}
	// second distinct device -> 2
	k2, _ := m.resolveStableKey("SER-B", "mac-b", "1.2.3.5")
	if k2 != 2 {
		t.Errorf("second key = %d, want 2", k2)
	}
	// re-resolve A by serial -> same key 1
	if k, _ := m.resolveStableKey("SER-A", "", ""); k != 1 {
		t.Errorf("serial re-match = %d, want 1", k)
	}
	// match by mac when serial empty
	if k, _ := m.resolveStableKey("", "mac-b", ""); k != 2 {
		t.Errorf("mac match = %d, want 2", k)
	}
	// match by addr when serial+mac empty
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
	// device_keys rows are never deleted, so a brand-new device always gets max+1
	k3, _ := m.resolveStableKey("SER-C", "", "1.1.1.3")
	if k3 != 3 {
		t.Errorf("third key = %d, want 3 (no reuse)", k3)
	}
}

func TestEnrichDeviceKeyFillsEmptyFacetsKeepsKey(t *testing.T) {
	m := newTestSurvManager(t)
	// added offline: only addr known
	k, _ := m.resolveStableKey("", "", "9.9.9.9")
	if k != 1 {
		t.Fatalf("addr-only key = %d, want 1", k)
	}
	// later the serial is learned -> resolve by addr enriches serial, key stays 1
	k2, _ := m.resolveStableKey("SER-Z", "", "9.9.9.9")
	if k2 != 1 {
		t.Errorf("after enrich, key = %d, want 1 (unchanged)", k2)
	}
	// now matchable by the learned serial
	if k3, _ := m.resolveStableKey("SER-Z", "", ""); k3 != 1 {
		t.Errorf("serial match after enrich = %d, want 1", k3)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd agent && PATH=/opt/homebrew/bin:$PATH go test -race -run 'ResolveStableKey|EnrichDeviceKey' ./...`
Expected: FAIL — `resolveStableKey` undefined.

- [ ] **Step 3: Create the `device_keys` table**

In `agent/surveillance.go` `migrate()`, add this CREATE TABLE to the `stmts` slice (alongside the `dvrs` / `channels` CREATE TABLE statements):
```go
		`CREATE TABLE IF NOT EXISTS device_keys (
			stable_key INTEGER PRIMARY KEY,
			serial TEXT NOT NULL DEFAULT '',
			mac    TEXT NOT NULL DEFAULT '',
			addr   TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
```

- [ ] **Step 4: Implement `resolveStableKey` + `enrichDeviceKey`**

Add to `agent/surveillance.go`:
```go
// resolveStableKey returns the stable device key for a DVR's natural identity,
// matching an existing key by serial -> mac -> addr (first non-empty facet that
// hits wins) and enriching that row's empty facets; on no match it allocates
// max(stable_key)+1 and inserts a new row. Keys are never reused, and the table
// is preserved across ResetDB, so the same physical device always maps to the
// same key.
func (m *SurveillanceManager) resolveStableKey(serial, mac, addr string) (int64, error) {
	match := func(col, val string) (int64, bool) {
		if val == "" {
			return 0, false
		}
		var k int64
		// col is a fixed identifier ("serial"/"mac"/"addr"), never user input.
		if err := m.db.QueryRow(`SELECT stable_key FROM device_keys WHERE `+col+`=? LIMIT 1`, val).Scan(&k); err == nil {
			return k, true
		}
		return 0, false
	}
	for _, f := range []struct{ col, val string }{{"serial", serial}, {"mac", mac}, {"addr", addr}} {
		if k, ok := match(f.col, f.val); ok {
			m.enrichDeviceKey(k, serial, mac, addr)
			return k, nil
		}
	}
	var maxKey sql.NullInt64
	m.db.QueryRow(`SELECT MAX(stable_key) FROM device_keys`).Scan(&maxKey)
	key := int64(1)
	if maxKey.Valid {
		key = maxKey.Int64 + 1
	}
	if _, err := m.db.Exec(`INSERT INTO device_keys (stable_key, serial, mac, addr) VALUES (?,?,?,?)`, key, serial, mac, addr); err != nil {
		return 0, err
	}
	return key, nil
}

// enrichDeviceKey fills any empty facet of an existing device_keys row from the
// supplied values without ever changing the stable_key. Empty supplied values
// leave the stored facet untouched.
func (m *SurveillanceManager) enrichDeviceKey(key int64, serial, mac, addr string) {
	m.db.Exec(`UPDATE device_keys SET
		serial = CASE WHEN serial='' THEN ? ELSE serial END,
		mac    = CASE WHEN mac='' THEN ? ELSE mac END,
		addr   = CASE WHEN addr='' THEN ? ELSE addr END
		WHERE stable_key=?`, serial, mac, addr, key)
}
```
(`database/sql` is already imported.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd agent && PATH=/opt/homebrew/bin:$PATH go test -race -run 'ResolveStableKey|EnrichDeviceKey' ./...`
Expected: PASS.

- [ ] **Step 6: Build + gofmt + commit**

Run:
```
cd agent && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH gofmt -l surveillance.go surveillance_identity_test.go
```
Expected: build OK, no gofmt output.
```bash
git add agent/surveillance.go agent/surveillance_identity_test.go
git commit -m "feat(agent): device_keys table + stable-key allocator (serial/mac/addr)"
```

---

## Task 2: Seed migration + ResetDB preservation

**Files:**
- Modify: `agent/surveillance.go` (`migrate` seed block; `ResetDB` comment only)
- Test: `agent/surveillance_identity_test.go` (extend)

- [ ] **Step 1: Write the failing test**

Append to `agent/surveillance_identity_test.go`:
```go
func TestSeedFromExistingDVRsIdempotent(t *testing.T) {
	m := newTestSurvManager(t)
	// simulate a pre-upgrade DB: dvrs rows with autoincrement ids, no device_keys.
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
	// device with serial S -> key K, plus a dvrs row at that id.
	k, _ := m.resolveStableKey("SER-S", "", "1.2.3.4")
	if _, err := m.db.Exec(`INSERT INTO dvrs (id, name, addr, port) VALUES (?,?,?,?)`, k, "x", "1.2.3.4", 80); err != nil {
		t.Fatal(err)
	}

	if err := m.ResetDB(); err != nil {
		t.Fatalf("ResetDB: %v", err)
	}

	// dvrs wiped...
	var nd int
	m.db.QueryRow(`SELECT COUNT(*) FROM dvrs`).Scan(&nd)
	if nd != 0 {
		t.Errorf("dvrs after reset = %d, want 0", nd)
	}
	// ...but device_keys preserved -> same serial resolves to the SAME key.
	if k2, _ := m.resolveStableKey("SER-S", "", "9.9.9.9"); k2 != k {
		t.Errorf("re-resolve after reset = %d, want %d (same key)", k2, k)
	}
	// a genuinely new device gets the next key, not the freed one.
	if k3, _ := m.resolveStableKey("SER-NEW", "", "8.8.8.8"); k3 != k+1 {
		t.Errorf("new device key = %d, want %d", k3, k+1)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd agent && PATH=/opt/homebrew/bin:$PATH go test -race -run 'SeedFromExisting|ResetDBPreserves' ./...`
Expected: FAIL — `seedDeviceKeys` undefined.

- [ ] **Step 3: Implement `seedDeviceKeys` + call it from `migrate`**

Add to `agent/surveillance.go`:
```go
// seedDeviceKeys gives every existing DVR a stable key equal to its current id,
// once, so existing stream ids (dvr<id>_ch<n>) and on-disk recordings keep the
// same path after this feature lands (zero recording migration). Idempotent:
// only runs while device_keys is empty.
func (m *SurveillanceManager) seedDeviceKeys() {
	var n int
	m.db.QueryRow(`SELECT COUNT(*) FROM device_keys`).Scan(&n)
	if n > 0 {
		return
	}
	if _, err := m.db.Exec(`INSERT INTO device_keys (stable_key, addr) SELECT id, addr FROM dvrs`); err != nil {
		log.Printf("[surv] seed device_keys: %v", err)
	}
}
```
At the END of `migrate()` (after the `record_hires` block), call it:
```go
	m.seedDeviceKeys()
```

- [ ] **Step 4: Confirm ResetDB does not touch device_keys**

In `ResetDB`, the transaction deletes `channels`, `dvrs`, and `sqlite_sequence` only. Leave it as-is and add a comment above the `DELETE FROM dvrs` line so the invariant is explicit:
```go
	// NOTE: device_keys is intentionally NOT cleared here — it is the persistent
	// serial->stable_key memory that lets a re-added DVR reuse its id.
	if _, err := tx.Exec(`DELETE FROM dvrs`); err != nil {
		return err
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd agent && PATH=/opt/homebrew/bin:$PATH go test -race -run 'SeedFromExisting|ResetDBPreserves' ./...`
Expected: PASS.

- [ ] **Step 6: Build + gofmt + commit**

Run: `cd agent && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH gofmt -l surveillance.go`
Expected: build OK, no gofmt output.
```bash
git add agent/surveillance.go agent/surveillance_identity_test.go
git commit -m "feat(agent): seed device_keys from existing DVRs; ResetDB preserves mapping"
```

---

## Task 3: Device identity fetch (serial / MAC from deviceInfo)

**Files:**
- Modify: `agent/surveillance.go` (`isAPIDeviceInfo` struct; add `fetchDeviceIdentity`)
- Test: manual/build (network fetch; the unit-tested logic is the parse, covered via the struct)

- [ ] **Step 1: Add serial/mac to the deviceInfo struct**

In `agent/surveillance.go`, extend `isAPIDeviceInfo`:
```go
type isAPIDeviceInfo struct {
	XMLName           xml.Name `xml:"DeviceInfo"`
	AnalogChannelNum  int      `xml:"analogChannelNum"`
	DigitalChannelNum int      `xml:"digitalChannelNum"`
	SerialNumber      string   `xml:"serialNumber"`
	MACAddress        string   `xml:"macAddress"`
}
```

- [ ] **Step 2: Add a parse test**

Append to `agent/surveillance_identity_test.go`:
```go
import "encoding/xml" // add to the test file's import block

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
```
Run: `cd agent && PATH=/opt/homebrew/bin:$PATH go test -race -run DeviceInfoParses ./...` — Expected: PASS (struct fields exist).

- [ ] **Step 3: Implement `fetchDeviceIdentity`**

Add to `agent/surveillance.go`:
```go
// fetchDeviceIdentity best-effort fetches a DVR's hardware serial + MAC from
// ISAPI /System/deviceInfo. Returns empty strings on any failure (device
// offline, or non-ISAPI like Dahua/ONVIF) so the caller falls back to addr.
func (m *SurveillanceManager) fetchDeviceIdentity(addr string, port int, username, password, protocol string) (serial, mac string) {
	if protocol != "" && protocol != "isapi" {
		return "", "" // serial source is ISAPI-specific; others ride the addr fallback
	}
	u := fmt.Sprintf("http://%s:%d/ISAPI/System/deviceInfo", addr, port)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return "", ""
	}
	req.SetBasicAuth(username, password)
	resp, err := m.client.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", ""
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", ""
	}
	var info isAPIDeviceInfo
	if xml.Unmarshal(body, &info) != nil {
		return "", ""
	}
	return info.SerialNumber, info.MACAddress
}
```
(`fmt`, `net/http`, `io`, `encoding/xml` are already imported in this file.)

- [ ] **Step 4: Build + gofmt + commit**

Run: `cd agent && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -race -run DeviceInfoParses ./... && PATH=/opt/homebrew/bin:$PATH gofmt -l surveillance.go surveillance_identity_test.go`
Expected: build OK, test PASS, no gofmt output.
```bash
git add agent/surveillance.go agent/surveillance_identity_test.go
git commit -m "feat(agent): parse + fetch device serial/MAC from ISAPI deviceInfo"
```

---

## Task 4: AddDVR assigns the stable key as `dvrs.id`

**Files:**
- Modify: `agent/surveillance.go` (`AddDVR`)
- Test: `agent/surveillance_identity_test.go` (extend)

- [ ] **Step 1: Write the failing test**

Append to `agent/surveillance_identity_test.go`:
```go
func TestAddDVRUsesStableKeyAsID(t *testing.T) {
	m := newTestSurvManager(t)
	// offline device (deviceInfo unreachable) -> keyed by addr. AddDVR must still
	// assign dvrs.id = the resolved stable key (here 1, the first allocation).
	d, err := m.AddDVR("cam", "127.0.0.1", 1, "", 0, "u", "p", "isapi", 2000, "sub")
	if err != nil {
		t.Fatalf("AddDVR: %v", err)
	}
	var key int64
	m.db.QueryRow(`SELECT stable_key FROM device_keys WHERE addr=?`, "127.0.0.1").Scan(&key)
	if d.ID != key {
		t.Errorf("dvr id = %d, device_keys key = %d (must match)", d.ID, key)
	}
	// the dvrs row's id must equal that key
	var rowID int64
	m.db.QueryRow(`SELECT id FROM dvrs WHERE addr=?`, "127.0.0.1").Scan(&rowID)
	if rowID != key {
		t.Errorf("dvrs.id = %d, want %d", rowID, key)
	}
}
```
(`127.0.0.1:1` is an unreachable port so `fetchDeviceIdentity` fails fast → addr fallback, keeping the test offline/hermetic.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd agent && PATH=/opt/homebrew/bin:$PATH go test -race -run AddDVRUsesStableKey ./...`
Expected: FAIL — current AddDVR uses autoincrement, so `dvrs.id` won't equal the device_keys key (and the device_keys row may not exist).

- [ ] **Step 3: Rewrite AddDVR to resolve + insert the explicit id**

In `agent/surveillance.go`, replace the INSERT-and-LastInsertId block of `AddDVR` (the `res, err := m.db.Exec(`INSERT INTO dvrs (name, addr, ...)` ...)` through `id, _ := res.LastInsertId()`) with:
```go
	serial, mac := m.fetchDeviceIdentity(addr, port, username, password, protocol)
	id, err := m.resolveStableKey(serial, mac, addr)
	if err != nil {
		return DVRConfig{}, err
	}
	// Insert with the explicit stable id. ON CONFLICT handles re-adding a device
	// whose row still exists (same id) — update its connection fields, don't dup.
	_, err = m.db.Exec(`INSERT INTO dvrs (id, name, addr, port, ext_addr, ext_port, username, password, protocol, refresh_rate, stream_quality)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, addr=excluded.addr, port=excluded.port,
			ext_addr=excluded.ext_addr, ext_port=excluded.ext_port, username=excluded.username,
			password=excluded.password, protocol=excluded.protocol, refresh_rate=excluded.refresh_rate,
			stream_quality=excluded.stream_quality`,
		id, name, addr, port, extAddr, extPort, username, password, protocol, refreshRate, streamQuality)
	if err != nil {
		return DVRConfig{}, err
	}
```
The existing `if m.onChange != nil { m.onChange() }` and the `return DVRConfig{ID: id, ...}` below stay — `id` is now the resolved stable key.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd agent && PATH=/opt/homebrew/bin:$PATH go test -race -run AddDVRUsesStableKey ./...`
Expected: PASS.

- [ ] **Step 5: Full agent test + relay build + gofmt**

Run:
```
cd agent && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -race ./...
cd ../relay && PATH=/opt/homebrew/bin:$PATH go build ./...
PATH=/opt/homebrew/bin:$PATH gofmt -l agent
```
Expected: agent builds + ALL tests pass; relay builds (no relay changes); no gofmt output (besides any pre-existing unrelated file).

- [ ] **Step 6: Commit**
```bash
git add agent/surveillance.go agent/surveillance_identity_test.go
git commit -m "feat(agent): AddDVR assigns resolved stable key as dvrs.id (serial-stable identity)"
```

---

## Final verification (after all tasks)

- [ ] **Both modules + format:**
```
cd agent && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -race ./...
cd ../relay && PATH=/opt/homebrew/bin:$PATH go build ./...
PATH=/opt/homebrew/bin:$PATH gofmt -l agent relay proto
```
Expected: green; relay unchanged still compiles.

- [ ] **Grep for other `dvrs` inserts** that might bypass the stable-key path: `grep -rn "INSERT INTO dvrs" agent/`. Confirm `AddDVR` is the only one; if a restore/import path exists, route it through `resolveStableKey` too (note as a concern if found).

- [ ] **Manual (live agent, post-release):** ResetDB, then re-add the same DVR → confirm its channels keep the same `dvr<key>_ch<n>` ids and existing recordings/deep-links still resolve. Add a brand-new DVR → confirm it gets a fresh key (max+1), not a reused one.

- [ ] **Update memory:** mark `dvr-stable-identity` as built (pending release), note the device_keys mechanism is live.
