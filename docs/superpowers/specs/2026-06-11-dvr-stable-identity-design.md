# DVR Stable Identity — Design

**Status:** Approved design (brainstorm), ready for implementation plan.
**Date:** 2026-06-11

## Goal

Give each physical DVR/NVR a **stable identity** so that re-adding the same device (even after a full DB wipe / `ResetDB`) reuses the same key — keeping stream ids (`dvr<key>_ch<n>`), recording directories, channel config, and player deep-links correctly associated with the same physical camera.

## Background — the fragility

Today the channel/stream identity is **positional**: `dvr<DVRID>_ch<ChNum>` where `DVRID` is the agent SQLite `dvrs.id` **AUTOINCREMENT rowid** (`agent/surveillance.go` AddDVR → `res.LastInsertId()`). `ResetDB` deletes all rows AND resets the counter (`DELETE FROM sqlite_sequence`). So a wipe + re-add reuses `dvr1`, `dvr2`… for **different physical cameras**, scrambling everything keyed by that id:

- **Recording dirs** — `Records/<agent>/dvr<id>_ch<n>/` (`relay/recorder.go`).
- **Config** — channel names/order keyed by `(dvr_id, ch_num)`.
- **Player deep-links** — `/dashboard/agent/<id>/ch/dvr<id>_ch<n>` (added 2026-06-11).

As long as the DB is not wiped, ids are stable; the bug only triggers on full reset + re-add. See memory `dvr-stable-identity`.

## Key decisions (locked in brainstorm)

1. **Identity source (Q1):** fallback chain **serial → MAC → addr:port → generated UUID**. The agent already calls `/ISAPI/System/deviceInfo` during discovery, so `serialNumber`/`macAddress` are within reach.
2. **Approach (Q2): seeded stable-key table** — a persistent mapping that survives `ResetDB`, seeded from the *current* DVRs so existing ids/recordings DON'T change (zero recording migration).
3. **`dvrs.id` IS the stable key.** Making the rowid itself the stable key avoids any inbound/outbound translation: the agent already emits `dvr.id` / `ch.DVRID` to the relay, so the id becoming stable propagates automatically. **The relay and dashboard need NO changes.**

## Architecture & data flow

```
[agent SQLite]                                  [proto -> relay]        [relay]
device_keys(stable_key PK, serial,mac,addr)     DVRID = dvrs.id  ──▶  dvr<key>_ch<n>
   ^ survives ResetDB; resolves natural-id          (= stable_key)     (code UNCHANGED:
   |                                                                     stream id, rec dir,
dvrs.id = stable_key (explicit, not autoincrement)                       deep-link all stable)
channels.dvr_id -> dvrs.id (stable_key)
```

The agent keeps using `dvrs.id` for FKs and proto emission exactly as today; the only change is that `dvrs.id` is now a *resolved stable key* instead of an autoincrement rowid. No translation layer.

## Components (all agent-side; relay & dashboard unchanged)

### A. Device identity fetch
- Add `serialNumber` and `macAddress` to the `isAPIDeviceInfo` XML struct (`agent/surveillance.go` — the `/ISAPI/System/deviceInfo` response is already fetched and `xml.Unmarshal`-ed).
- `fetchDeviceIdentity(dvr) (serial, mac string)` — best-effort: GET deviceInfo, parse serial/mac. Returns empty strings on failure (offline / non-Hikvision). ONVIF/Dahua: serial may be unavailable → empty (chain falls through to addr).

### B. `device_keys` table + resolve
- New table:
  ```sql
  CREATE TABLE IF NOT EXISTS device_keys (
    stable_key INTEGER PRIMARY KEY,
    serial TEXT NOT NULL DEFAULT '',
    mac    TEXT NOT NULL DEFAULT '',
    addr   TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
  );
  ```
- `resolveStableKey(serial, mac, addr string) int64`:
  1. Match an existing row by **serial** (if serial != ""), else **mac** (if != ""), else **addr** (if != ""). On match, return its `stable_key` (and enrich empty facets — see F).
  2. No match → allocate `max(stable_key)+1` (0 → 1), INSERT a row with the known facets, return it. **Never reuse a key.**
- Matching is "any non-empty facet equals", in priority order; the first facet that yields a hit wins.

### C. AddDVR assigns the stable key as `dvrs.id`
- `AddDVR` flow becomes: best-effort `fetchDeviceIdentity` → `resolveStableKey(serial, mac, addr)` → `INSERT INTO dvrs (id, name, addr, ...) VALUES (<stable_key>, ...)` (explicit id, not autoincrement). Return that id.
- SQLite allows explicit `INTEGER PRIMARY KEY` values; keep the column definition but stop relying on `LastInsertId()` for identity (use the resolved key).
- If a `dvrs` row with that id somehow already exists (re-add of a still-present device), treat as update/no-op rather than a duplicate insert.

### D. ResetDB preserves the mapping
- `ResetDB` continues to `DELETE FROM dvrs` / `channels` (and `sqlite_sequence`, now harmless) but **must NOT touch `device_keys`**. That's what makes re-add reuse the same key.

### E. Seed migration (one-time, idempotent)
- On first run after upgrade (guard: `device_keys` empty AND `dvrs` non-empty, or a one-shot marker), seed: for each existing `dvrs` row, `INSERT INTO device_keys (stable_key, serial, mac, addr) VALUES (<dvrs.id>, '', '', <dvrs.addr>)`. Serial/mac filled lazily by F when the device is next reachable.
- This keeps every existing `dvrs.id` exactly as-is → existing `Records/<agent>/dvr<id>_ch<n>/` stays valid → **no recording migration**.

### F. Facet enrichment
- When discovery (or any reachable-device path) learns a serial/mac for a DVR whose `device_keys` row has that facet empty, `UPDATE device_keys SET serial=?/mac=? WHERE stable_key=?`. **Never change `stable_key`.** This upgrades an addr-only key to be serial-matchable for future re-adds.

## Edge cases

1. **Offline at add (no serial/mac):** resolve by `addr` → allocate/match by addr. Enriched with serial later (F).
2. **IP change, serial known:** match by serial → same key; addr facet updated.
3. **Device replaced at same addr, both serials known:** different serials → different keys → recordings stay separate (correct).
4. **Replacement where the old key was addr-only (serial was never learned):** addr match reuses the old key → new device inherits the old recording dir. Accepted ambiguous case (no stable signal existed); documented, not engineered around.
5. **Duplicate/again-pathological serial (two devices report same serial):** they share a key. Pathological; not handled.

## Testing

- **Go (unit), agent:**
  - `resolveStableKey`: serial match reuses key; mac match when no serial; addr match when no serial/mac; no match allocates `max+1`; never reuses a freed key (allocate, "delete dvr", allocate again → new key).
  - Seed migration: existing `dvrs` ids appear in `device_keys` with matching `stable_key`; idempotent (re-run doesn't duplicate or renumber).
  - ResetDB preserves `device_keys`: add DVR (serial S → key K) → ResetDB → re-add same serial S → **same key K** (and a fresh device with serial T → K+1, not K).
  - AddDVR inserts `dvrs.id` == resolved stable key.
  - Facet enrichment: addr-only row gains serial without changing `stable_key`; subsequent serial match hits it.
- **Build:** `cd agent && go build ./... && go test -race ./...`; `cd relay && go build ./...` (must still compile — no relay change expected). `gofmt -l`.
- **Manual:** on a live agent, ResetDB + re-add the same DVR → confirm channels keep the same `dvr<key>_ch<n>` ids and existing recordings/deep-links still resolve.

## Non-goals

- No relay or dashboard code changes.
- No remapping/moving of existing recording directories (the seed makes it unnecessary).
- No handling of the pathological duplicate-serial case (edge 5).
- No retroactive serial backfill for DVRs that are offline at migration time (handled lazily by F when they next come online).

## Open items (verify during implementation)

1. **ONVIF / Dahua serial availability** — confirm whether non-Hikvision devices expose a serial via their discovery path; if not, they ride the addr fallback (acceptable, just less robust to IP change).
2. **AddDVR latency** — `fetchDeviceIdentity` adds one HTTP round-trip to add; confirm it's bounded by a short timeout and doesn't block the UI badly (it can fail fast to the addr fallback).
3. **`dvrs.id` explicit-insert vs existing AUTOINCREMENT schema** — verify SQLite accepts explicit ids on the existing table definition without dropping/recreating it; confirm nothing else relies on `LastInsertId()` semantics.
