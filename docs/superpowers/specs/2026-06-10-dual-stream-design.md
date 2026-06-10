# Dual-Stream Recording — Design

**Status:** Approved design (brainstorm), ready for implementation plan.
**Date:** 2026-06-10

## Goal

Decouple the **live** stream from the **recording** stream so each channel can record the high-resolution main stream (forensic quality) while the live grid stays on the lightweight sub stream. Per-channel opt-in. A side benefit: high-res main becomes available as a future LPR source on plate-facing channels.

## Background — why

Today OpsView is **single-stream per channel**: one `streamEntry` (`relay/surv_proxy.go`) pulls one RTSP stream (main or sub, chosen by the per-DVR `StreamQuality`), and the recorder (`relay/recorder.go`) records that same stream via the relay's self-served HLS (`ffmpeg -i selfHLS -c copy`). Consequences:

- Raising record quality forces the **live grid** to the heavy main stream too → browser concurrent-decoder exhaustion ("허덕임"), confirmed live with high-res many-channel grids.
- So you cannot have "high-res recording + light live" at once.

Measured baseline (this deployment): recording is currently the **sub** stream — ~0.31 Mbps/ch, 121.9 GB/day for all channels, ~131 days on 16 TB. The main stream (per Hikvision config seen) is **1080p / H.264 / 15 fps / 2048 kbps CBR** — browser-playable (264), ~2 Mbps. Recording all channels at main ⇒ ~20 days/16 TB; hence per-channel opt-in, not global.

## Decisions (locked during brainstorm)

1. **Live = sub, always** (grid + full-screen default). Fixes grid decoder load as a bonus.
2. **Per-channel record toggle**, two static modes this release: **Off** (record sub = today) / **Always** (record main = high-res). *(On-event mode is v2.)*
3. **Phased (B):** this spec is the static dual-stream MVP. On-event dynamic switching is a separate later spec.
4. **D-lite:** the full-screen player's manual "고화질(HD)" live toggle is exposed **only on Always channels** (their main pipeline is already running → zero extra cost). On-demand main start for Off channels is deferred to v1.5.
5. **265 is not involved** — everything stays H.264 (see `codec-streaming-strategy` memory). Main is native-resolution; never upscale (a 720p camera's main is 720p — UI labels this, see E).

## Architecture & data flow

```
                       ┌─ sub pipeline (always) ──→ WS/HLS ──→ live grid (light)
  DVR ch ──RTSP──┤                                         ↘ full-screen default
   sub (…02)      │
   main (…01)     └─ Always channels only:
                        main pipeline ──→ WS  ──→ player "HD" toggle (free)
                                       └→ recorder records main → channel rec dir
  Off channels: recorder records the sub stream (unchanged)
```

| Mode | Live | Record | DVR RTSP conns |
|------|------|--------|----------------|
| **Off** (default, = today) | sub | sub | 1 (sub) |
| **Always** (high-res) | sub | **main** | 2 (sub + main) |

The 2× DVR connection cost is incurred **only on Always channels**.

## Components

### A. Per-channel record toggle (config flow)

- New field `RecordHighRes bool` (json `record_hires`):
  - `agent/surveillance.go` — `ChannelConfig` struct + SQLite column (migration: `ALTER TABLE … ADD COLUMN record_hires INTEGER DEFAULT 0`) + `AddChannel`/`UpdateChannel` scans/writes.
  - `proto/json_messages.go` — `ChannelInfo` field.
  - Relay reads it from `SurvConfig`.
- **Migration to preserve current behavior:** on first run after upgrade, for any DVR whose `StreamQuality == "main"`, set its channels' `record_hires = true` (they record main today; keep them high-res). DVRs on sub stay `false`. This deployment is on sub → no-op, owner opts in per channel afterward.
- **UI:** in the live-grid **edit mode** (existing per-cell rename inputs, `relay/dashboard_assets`), add a per-cell **"HD 녹화"** toggle. Persists via the existing channel-update path.

### B. Live = sub, always (relay)

- The **base** `streamEntry` per channel pulls the **sub** stream regardless of `StreamQuality`. For the Hikvision/Dahua template path, force the sub stream-id in `buildSurvRTSPURL`. The per-DVR `StreamQuality` no longer controls live (its meaning is retired — live is always sub; record quality is now per-channel).
- **Edge — ONVIF `RtspURI` channels:** `survRTSPURLForChannel` prefers a discovered `ch.RtspURI`, which may be a *main* profile URI. To guarantee a sub live stream we need the channel's **sub** profile URI. Open item (see below) — primary deployments use the ISAPI template path, which is fine.

### C. Main pipeline + recorder (relay core)

- For `RecordHighRes == true` channels, start a **second `streamEntry`** keyed `<path>@main` (e.g. `dvr3_ch1@main`), pulling the **main** RTSP (`buildSurvRTSPURL` with main forced). It produces HLS + WS like any stream.
- **Recorder source/output split** (`recorder.go`):
  - Today `startLocked(path)` uses both `path`→source HLS and `path`→output dir.
  - New: **record source** = `<path>@main` self-HLS when the channel is Always, else `<path>` (sub) — but **record output dir stays the channel's normal path** (`Records/<agent>/dvr3_ch1/`). Unified player unchanged; it just finds 1080p segments.
  - An Always channel records **only** its main (not also the sub) — no double recording.
- `reconcile` must include `@main` streams in the active set and map their recording to the **base** channel dir. Recommended: derive the record set per *channel* (pick source by toggle) rather than blindly per active stream, so the `@main` stream feeds recording while the base sub feeds live without being separately recorded.
- **Bitrate/segment note:** main = 1080p 264 ~2 Mbps; existing janitor (space/age prune) handles the shorter retention. No new retention logic this release.

### D. Full-screen player manual HD toggle (D-lite)

- The full-screen player (`relay/dashboard_assets/app.js`, `openPlayer`/live path) gets a **"고화질"** button **shown only when the channel is Always** (its `<path>@main` pipeline is live).
- Toggling HD swaps the player's live WS from `/surv/ws/<path>` (sub) to `/surv/ws/<path>@main` (main). One stream at a time — no decoder concern. Toggling back returns to sub.
- Relay WS routing must accept the `<path>@main` key (it already routes by stream id; the `@main` entry exists for Always channels).
- **Off channels:** no HD button (no main pipeline). On-demand main start = v1.5.

### E. 720p guard

- `Width/Height` already exist on `ChannelInfo`. In the edit-mode toggle UI, label channels whose height ≤ 720 with a muted **"720p"** tag, signalling that HD record won't yield 1080p (camera native res is the ceiling — DVR upscaling adds no detail). The toggle is still allowed (recording native main is still better than sub), just honestly labelled.

## Error handling & constraints

- **DVR concurrent-stream limit:** Always channels double their RTSP sessions. If the DVR refuses the extra main connection, the main `streamEntry` fails to start; recorder falls back to recording the sub (so recording never stops — it just isn't high-res) and logs it. Surface per-channel main-pipeline health in stream stats.
- **Main pipeline crash/reconnect:** reuse the existing `streamEntry` reconnect machinery; recorder's `supervise` already restarts ffmpeg with backoff.
- **SSRF guard / creds:** main URL is built via the same `buildSurvRTSPURL` + `isBlockedRTSPHost` path as today — no new trust surface.

## Testing

- **Go (unit):**
  - `buildSurvRTSPURL` returns the correct main (`…01`) vs sub (`…02`) path per `StreamQuality` override.
  - Record source/output resolver: given a channel + toggle, returns `(source = @main|base, outputDir = channel dir)`; Always → main source, channel dir; Off → sub source, channel dir. (Extract as a pure function for testability.)
  - `reconcile` includes `@main` active streams and does not double-record an Always channel.
  - Migration: `StreamQuality=="main"` DVR ⇒ channels get `record_hires=true`.
- **JS (node --test):**
  - Edit-mode toggle state serialize/deserialize.
  - Player live-source selector: pure function `(channel, hdOn) → wsPath` returns `<path>@main` only when channel is Always and hdOn.
- **Manual verification checklist:**
  - Always channel: recording saved as 1080p, plays back in desktop Chrome via unified player.
  - Live grid stays light (sub) even with many Always channels; no black cells.
  - HD button appears only on Always channels; toggling shows crisp main, toggling back returns to sub.
  - 720p channel shows the "720p" label.
  - DVR shows 2 sessions for an Always channel (sub + main).

## Non-goals (this release)

- **On-event dynamic main switching** (record sub idle, main during events) — v2, separate spec. Note Hikvision's native "Main Stream (Event)" profile will complement it (relay decides *when*, camera profile decides *how good*).
- **Grid auto-switch to high-res on event** — rejected (small cells can't show high-res; adds load).
- **On-demand main start for Off channels** (D-full) — v1.5.
- **LPR rewired to the main stream** — separate, and blocked on a Korean OCR model regardless (see `anpr-mechanical-parking` memory). This release merely *makes the high-res main available* for that later work on plate-facing channels.
- **H.265 / GPU transcode** — explicitly out (see `codec-streaming-strategy` memory).

## Open items (verify during implementation, non-blocking)

1. **Hikvision "Main Stream (Event)" via RTSP pull:** confirm whether a continuous third-party RTSP pull of the main stream actually switches to the Event encoding profile during alarms, or whether that profile only applies to the DVR's own recording. Affects v2 (On-event), not this MVP. 1-minute test: trigger an event, watch main RTSP bitrate.
2. **ONVIF-only DVRs (B edge):** if a DVR provides only one discovered `RtspURI` profile, forcing live=sub / record=main needs both profile URIs. Template (ISAPI/Dahua) DVRs are unaffected. Decide fallback (e.g. discover both profiles, or skip dual-stream for ONVIF-only).
