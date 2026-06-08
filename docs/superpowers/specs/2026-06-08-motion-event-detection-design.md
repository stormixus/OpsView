# Motion / Event Detection (ONVIF) — Design Spec

**Date:** 2026-06-08
**Status:** Approved (brainstorming complete)
**Author:** OpsView

## Goal

Surface DVR motion (and, when available, smart) events in OpsView: mark them on the
recording timeline so an operator can jump between events, and bias recording
retention so event footage outlives idle footage. Reuse the DVR's own motion
detection via ONVIF — the relay never decodes pixels.

## Non-Goals

- **Live push notifications / alerts** — explicitly out of scope for this spec.
- **Server-side object detection ML** (person/vehicle/plate when the DVR can't
  emit them) — same family as the deferred ANPR/LPR work; out of scope.
- **Gated (motion-only) recording** — rejected during brainstorming in favor of
  continuous capture + event-differentiated retention (keeps pre-roll, no ffmpeg
  gating, no lost frames if VMD misses).

## Background / Current State

- `agent/onvif.go` already speaks ONVIF for **discovery only**: `GetDeviceInformation`,
  `GetServices`/`GetCapabilities`, `GetProfiles`, `GetStreamUri`, `GetSnapshotUri`.
  The SOAP transport (`onvifClient.call()`) + WS-Security UsernameToken digest +
  HTTP Digest fallback are built and tested. The **Events** service
  (`CreatePullPointSubscription` / `PullMessages`) is NOT implemented.
- Recording (`relay/recorder.go`) is opt-in (`RELAY_REC_DIR`). Per active stream a
  supervised `ffmpeg -c copy` reads the relay's OWN HLS and writes 5-min segmented
  MP4s named `%Y%m%d_%H%M%S.mp4`. The janitor enforces a disk cap
  (`RELAY_REC_MAX`) by deleting **oldest-first** when over cap. There is no
  age-based or event-aware retention today.
- The recording dashboard (`relay/dashboard_assets/app.js`) already renders a 24h
  timeline, multi-cam synced playback, and hover-scrub thumbnails.
- Agent→relay messages use the OVP envelope (`proto/ovp.go`): 12-byte header +
  JSON payload, dispatched in `relay/hub.go` by `hdr.Type`. The relay stamps the
  tenant `AgentID` onto forwarded surveillance messages (`stampSurvConfigAgentID`).

## Design North-Star

UniFi Protect's recording UX is the reference: a 24h timeline with motion shown as
colored bands, jump-between-events, event-type filtering, and a hybrid recording
mode (full-time capture with event footage prioritized for retention). ~80% of
that falls out of this design directly. The remaining 20% — **smart detection**
(person/vehicle/line-cross as distinct types) — depends entirely on whether the
DVR emits smart events over ONVIF. We do NOT pre-build that UI; we lay a data
model that carries event types from day one and upgrade the UI only after the
Phase 0 probe confirms the DVR actually emits them.

## Data Flow

```
DVR (VMD / analytics)
   │  ONVIF PullPoint  (CreatePullPointSubscription → PullMessages loop)
   ▼
agent  (onvif_events.go)
   │  proto.MsgSurvEvent  (edge: Active true/false, per channel, UTC ms)
   ▼
relay
   ├─► event store      <recdir>/<stream>/.events/YYYYMMDD.jsonl   (paired intervals)
   ├─► marker API        GET /dashboard/rec/events?stream=&day=
   │      ▼
   │   dashboard         timeline overlay (bands) + jump-to-event
   └─► event-aware janitor   (retention: keep-all window → drop non-event → drop old events)
```

## Components

### 1. Agent — ONVIF Events subscription

**Files:** `agent/onvif_events.go` (new), `agent/onvif.go` (capability probe),
`agent/surveillance.go` (lifecycle wiring), `agent/onvif_events_test.go` (new).

**Phase 0 — capability probe (the gate).**
Extend discovery to detect the Events service. During the existing
`GetServices`/`GetCapabilities` flow, capture the **Events** service XAddr (if
present) and whether the device advertises `WSPullPointSupport`. Record per
device whether ONVIF Events is available and which topics it advertises
(`GetEventProperties` → topic set). Log it and surface it (via the next
`SurvConfig` or a one-shot log the operator can read off the relay). This ships
first so we learn what the real SM-Boutique DVR emits **before** building the
pipeline. If the DVR exposes no usable Events service, this gates a fallback
design (see Risks → vendor fallback).

**Phase 1 — subscription + pull loop.**
For each DVR that advertises Events:
- `CreatePullPointSubscription` (broad — no topic filter, or a coarse
  `tns1:VideoSource//.` filter). Capture the returned `SubscriptionReference`
  address (the pull endpoint) and `wsnt:TerminationTime`.
- Loop: `PullMessages` with `Timeout=PT1S`, `MessageLimit=10` against the pull
  endpoint. Parse each `wsnt:NotificationMessage`:
  - **Topic** → event `Kind`: `tns1:VideoSource/MotionAlarm` and
    `tns1:RuleEngine/CellMotionDetector` → `motion`; smart topics
    (`tns1:RuleEngine/...FieldDetector`, `LineDetector`, person/vehicle
    object-detection topics) → `linecross` / `person` / `vehicle` as advertised.
    Unknown topics → ignore.
  - **Source** (`tt:SimpleItem Name="Source"/VideoSourceToken/...`) →
    channel number via the existing `chanNumFromSource` mapping → `dvrN_chM`.
  - **State** (`tt:SimpleItem Name="State"/"IsMotion"` = `true`/`false`) →
    `Active bool`. A change in state is an **edge**.
  - **UtcTime** (message `@UtcTime`) → event timestamp.
- Renew the subscription (`Renew`) before `TerminationTime`, or recreate on
  expiry / on a pull error after backoff.
- Emit only **edges** (state changes) to the relay; the agent does NOT pair
  intervals (keeps the agent stateless across restarts).

**Lifecycle.** One subscription worker per Events-capable DVR, started/stopped
alongside the existing surveillance discovery lifecycle, cancelable on shutdown,
with reconnect/backoff identical in spirit to the recorder's supervise loop.

### 2. Proto — event message

**Files:** `proto/ovp.go` (new message type), `proto/json_messages.go` (payload),
`proto/json_messages_test.go` (round-trip test).

- New `MsgSurvEvent MessageType = 13` (+ `String()` arm).
- Payload:
  ```go
  // SurvEvent is one DVR event edge (agent→relay). The relay pairs Active
  // true/false edges per (AgentID,ChID,Kind) into intervals for the timeline
  // and retention. AgentID is left empty by the agent and stamped by the relay,
  // mirroring SurvConfig.
  type SurvEvent struct {
      AgentID string `json:"agent_id,omitempty"`
      ChID    string `json:"ch_id"`   // "dvr1_ch2" (matches stream/segment path)
      Kind    string `json:"kind"`    // "motion" | "linecross" | "person" | "vehicle" | ...
      Active  bool   `json:"active"`  // true = event started, false = ended
      TS      int64  `json:"ts"`      // UTC unix milliseconds (from the DVR's UtcTime)
  }
  ```
- Direction: agent→relay only. NOT forwarded to watchers (live alerts are out of
  scope); the relay terminates it into the event store.

### 3. Relay — event store, marker API, event-aware retention

**Files:** `relay/events.go` (new: store + pairing + API handler),
`relay/hub.go` (dispatch `MsgSurvEvent` + stamp AgentID),
`relay/recorder.go` (event-aware janitor), `relay/events_test.go` (new).

**Event store.**
- An `eventStore` owned by the relay (alongside / referenced by the `Recorder`).
  When recording is disabled (`RELAY_REC_DIR` unset) the store still functions for
  markers but has no segments to retain — acceptable; markers are useful alone.
  (If `RELAY_REC_DIR` is unset there is no `<recdir>`; in that case the store is
  in-memory only and markers cover the current session. Persistence requires
  `RELAY_REC_DIR`.)
- **Pairing.** On each `SurvEvent` edge, keyed by `(stream, kind)`:
  - `Active=true` → open an interval at `TS` (ignore if one is already open for
    that key).
  - `Active=false` → close the open interval `[start, TS]`, append it to the
    day's log.
  - **Orphan guard:** an interval open longer than `maxEventLen` (default 10 min)
    is force-closed at `start+maxEventLen` (DVR missed the inactive edge, or the
    agent restarted mid-event).
- **Persistence.** Append closed intervals as JSON lines to
  `<recdir>/<stream>/.events/YYYYMMDD.jsonl` (day = local date of interval start).
  `.events/` is a directory, so the existing `.mp4`-only `days()`/`segments()`
  scans never pick it up. One line per interval:
  `{"start":<unixMs>,"end":<unixMs>,"kind":"motion"}`.
- **In-memory index** keyed by `(stream,day)`, populated lazily from the jsonl,
  invalidated by the file's mtime — reuse the exact cache pattern already in
  `recordings.go` (`idxMu`, mtime-keyed entries). Both the marker API and the
  janitor read intervals from this index.

**Marker API.**
- `GET /dashboard/rec/events?stream=<path>&day=YYYYMMDD` → `{"events":[{"start","end","kind"}]}`
  (unix **seconds**, matching the segment timeline's units). Admin-gated via
  `authedDashboard`, same as `HandleDashboardRecordings`.
- Caching mirrors recordings: `no-store` for today (events still landing);
  past days are cacheable (mtime/Etag) since their `.events/` file is frozen.

**Event-aware janitor.**
Replace the pure oldest-first deletion order in `runJanitor` with a
priority-tiered order, **preserving the existing "get back under cap" guarantee**:
1. **Keep-all window:** segments whose start is within `RELAY_REC_KEEP_ALL_HOURS`
   (default 72) are never deleted by the event logic.
2. Over cap → delete **non-event** segments older than the keep-all window,
   oldest-first. A segment is "event" if its `[start, start+dur]` overlaps any
   interval in that stream's event index.
3. Still over cap → delete **event** segments older than
   `RELAY_REC_KEEP_EVENT_DAYS` (default 30), oldest-first.
4. Still over cap → fall back to global oldest-first across whatever remains
   (matches today's behavior; guarantees the cap is honored even if everything is
   recent or event-tagged).
- Streams with **no events** recorded degrade to exactly today's behavior
  (everything is "non-event" → oldest-first).
- New env: `RELAY_REC_KEEP_ALL_HOURS` (default 72), `RELAY_REC_KEEP_EVENT_DAYS`
  (default 30). Existing `RELAY_REC_MAX` still the hard cap.

### 4. Dashboard — timeline markers

**Files:** `relay/dashboard_assets/app.js`, `relay/dashboard_assets/style.css`.

- On loading a day in the recording tab, fetch
  `/dashboard/rec/events?stream=&day=` alongside the segment list.
- Overlay event intervals as colored bands on the existing 24h timeline (a single
  motion color for Phase 1; `kind`→color is data-ready for later).
- Click a band → seek playback to the event start. Hover → tooltip with kind +
  local time. Per-channel in multi-cam mode.
- **Phase 1.5 (conditional — only if Phase 0 shows the DVR emits smart events):**
  upgrade to per-kind colored bands + filter chips (motion / person / vehicle /
  line-cross) + an event-list side panel (UniFi Protect style). Backend already
  carries `kind`, so this is frontend-only and throws nothing away.

## Phasing

| Phase | Scope | Gate |
|-------|-------|------|
| **0** | Agent ONVIF Events capability + topic probe; ship; read what SM-Boutique's DVR emits | Determines Phase 1 vs vendor fallback, and whether 1.5 is reachable |
| **1** | Subscription + pull loop → `MsgSurvEvent` → relay store + marker API + dashboard motion markers + event-aware janitor | Visible value; works for any motion-emitting DVR |
| **1.5** | (conditional) Protect-style per-kind filter chips + event side panel | Only if Phase 0 confirms smart events |
| **fallback** | Vendor event stream (Hikvision ISAPI `/Event/notification/alertStream`, Dahua HTTP) behind an event-source interface feeding the same `MsgSurvEvent` | Only if Phase 0 shows no usable ONVIF Events |

## Key Decisions (rationale)

1. **Edges from agent, intervals in relay.** Agent stays stateless — survives
   restarts without losing/duplicating intervals; relay is the single pairing
   authority and already persists recordings.
2. **Continuous capture + event-differentiated retention** (not gated recording).
   Preserves pre-roll, no ffmpeg start/stop complexity, no lost frames on VMD
   misses. Disk savings come from retention, not from not-recording.
3. **Janitor keeps cap logic, changes only deletion order.** The "stay under
   `RELAY_REC_MAX`" guarantee is untouched; event footage simply survives longer.
4. **Data model carries `kind` from day one; rich UI deferred.** No throwaway work
   if the DVR turns out to emit only basic motion; cheap upgrade if it emits more.
5. **Phase 0 probe gates everything.** We don't build the pipeline (or the Protect
   UI) on an assumption about the DVR's ONVIF Events support.

## Risks / Open Questions

- **DVR may not support ONVIF Events / PullPoint.** Mitigation: Phase 0 probe
  ships first. If unsupported, vendor fallback (ISAPI/Dahua) behind an
  event-source interface — same `MsgSurvEvent` downstream, so Phases 1+ unchanged.
- **Topic/source schema varies by vendor.** Parsing must be defensive: match on
  topic substrings, tolerate missing `SimpleItem`s, ignore unknown topics rather
  than erroring.
- **Clock skew** between DVR `UtcTime` and segment filenames (which use the
  recorder host's local clock via `-strftime`). Markers and segments could be
  offset if the DVR clock drifts. Note for Phase 1 verification: compare a known
  event against the segment it should fall in; if skew is material, consider
  stamping event TS from relay receive-time as an option.
- **Event volume.** A busy scene can emit many short motion edges. The orphan
  guard + interval pairing bound storage; the jsonl is append-only and small
  (tens of bytes per interval). Optional later: coalesce intervals closer than N
  seconds.
- **No `RELAY_REC_DIR`.** Without it there is no disk path for the event store;
  markers degrade to in-memory/session-only. Persistent markers require recording
  to be enabled (acceptable — markers are about recordings).

## Testing

- **Agent:** unit-test ONVIF Events XML parsing (PullMessages response → edges)
  with captured/synthetic Hikvision + generic ONVIF payloads; capability-probe
  parsing; subscription renew/expiry handling. (`agent/onvif_events_test.go`)
- **Proto:** `MsgSurvEvent` marshal/round-trip + `String()`.
- **Relay:** edge→interval pairing (open/close, duplicate-open, orphan force-close);
  jsonl persist + mtime-cached read; marker API output + auth gating + cache
  headers; event-aware janitor tier ordering (non-event dropped before event,
  keep-all window honored, cap still reached). (`relay/events_test.go`)
- **Race:** `go test -race ./...` for the new store + janitor concurrency.
- **Manual (Phase 0):** read the probe output off the live SM-Boutique DVR.
- **Manual (Phase 1):** trigger motion at the camera, confirm a band appears at
  the right spot on the timeline and click-to-seek lands in the right segment.
