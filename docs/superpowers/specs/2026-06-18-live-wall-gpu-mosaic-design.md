# Live Wall (GPU mosaic) — Design

**Goal:** A live-only surveillance "wall" where the relay composites all of an
agent's channels into ONE GPU-encoded mosaic stream, so the viewer decodes a
single video instead of N — eliminating the browser's concurrent-decoder cap
(rotation, black cells, freeze-frames) entirely.

**Status:** Design approved 2026-06-18. Successor work to the live-grid rotation
(commit 88c45a9) and the NVENC transcode-live experiment (relay/surv_transcode.go).

---

## Problem & framing

The live grid decodes one `<video>` per channel. Browsers cap concurrent H.264
decoders (~16–25 desktop, fewer on weak clients like the N100), so a large grid
can't play every cell live — losers go black, and the current mitigation rotates
a capped set and freezes the rest. The user wants every camera live at once with
no stills, and is willing to drop browser dependence to get it.

A native viewer was considered and **rejected**: it still must decode N streams
(the N100 has its own limits) and means rewriting the whole UI for no structural
win. The leverage point is making the **client decode exactly one stream**. With
an RTX 4080 already on the relay (NVENC/NVDEC confirmed), the relay composites the
channels into a single mosaic and ships one stream. The client (browser, VLC, TV,
anything) decodes one video — the cap problem disappears, and it's a bandwidth win
over the Cloudflare tunnel too (1 stream vs N).

## Scope

- **In:** live-only wall, ≤16 channels (≤4×4), per agent (지점). GPU mosaic on the
  relay, a lean `/wall` page, click-a-cell → that channel's single live stream.
- **Out:** recording playback, timeline, events, sprites — the existing full
  dashboard keeps those. The wall is a separate, minimal live surface.
- **Reuse (~60%):** `surv_transcode.go`'s Annex-B ingest, ffmpeg supervise loop,
  `survWSHub`/`fragMuxer` fan-out, and the client `playWS`. The wall is, in effect,
  a transcode stream with N inputs + an xstack compose stage and one output.

## Architecture

```
DVR ──RTSP──▶ relay (already pulls each channel → self-HLS)
                 │  inputs = each enabled channel's OWN self-HLS
                 │  (http://127.0.0.1:<port>/surv/<id>/index.m3u8) — NO extra DVR pull
                 ▼
   ffmpeg (one process, supervised):
     -hwaccel cuda  (NVDEC decode each input)
     scale each to cell size, xstack into rows×cols grid
     h264_nvenc  →  one <WALL_RES>/<WALL_FPS> stream
     -bsf:v h264_metadata=aud=insert  -f h264 pipe:1
                 │  Annex-B
                 ▼
   ingestAnnexB → fragMuxer → survWSHub, registered as stream id "wall"
   in the SELECTED AGENT's SurvProxy
                 │
   browser: ONE <video> playing /surv/ws/<agent>/wall via playWS
```

- **Inputs are self-HLS, not the DVR.** The relay already pulls every channel's
  RTSP and muxes self-HLS (the recorder and transcode-live both source from it).
  The mosaic reuses those, so it adds **zero** DVR connections. Self-HLS also
  survives RTSP reconnects (the muxer persists), which matters for robustness.
- **One output stream** registered under id `wall` in the agent's `SurvProxy`,
  served by the existing `/surv/ws/...` route and consumed by the existing
  `playWS` MSE path. No new client streaming code.

## Components (units)

1. **`relay/surv_mosaic.go`** — the mosaic pipeline.
   - `mosaicLayout(n int) (rows, cols int, cells []cellRect)` — pure: channel count
     → grid shape + per-cell (x,y,w,h) rectangles within the output canvas. ≤16 ⇒
     ceil(sqrt) grid (matches the dashboard's `niceCols`). **Unit-tested.**
   - `mosaicArgs(inputs []string, layout, res, fps) []string` — pure: builds the
     ffmpeg invocation (inputs, per-input `scale`, `xstack` layout string, nvenc
     output, AUD bsf, Annex-B pipe). **Unit-tested** (string assertions).
   - `StartMosaic(agentSP *SurvProxy, id string, inputs []string, layout, res, fps)`
     — registers a `streamEntry{wsHub, frag}` under `id`, supervises ffmpeg
     (reusing the `superviseTranscode`/`ingestAnnexB` machinery, generalized), and
     restarts on exit with backoff.
   - Lazy lifecycle: start on first WS client for `wall`, stop after a linger with
     zero clients (free the GPU when nobody watches).
2. **Layout endpoint** — `GET /surv/wall/<agent>/layout` → JSON
   `{res, fps, rows, cols, cells:[{i, id, name, x, y, w, h}]}` so the client can
   place transparent per-cell labels + click targets exactly over the video grid.
3. **`relay/dashboard_assets/wall.html` + `wall.js`** — the lean page:
   - one full-bleed `<video>` (the mosaic) via `playWS` (zombie-reconnect applies);
   - a transparent CSS-grid overlay built from the layout JSON (channel name + a
     click target per cell);
   - click a cell → open that channel's **single** live stream fullscreen (1
     decode, reuse playWS); ESC/back → return to the wall;
   - gated by the existing dashboard token; `?agent=<id>` selects the venue.
4. **Reuse, unchanged:** `survWSHub`, `fragMuxer`, `ingestAnnexB`, `splitNALUnits`,
   `playWS`.

## Configuration (env)

- `RELAY_WALL=1` — enable the wall feature/route (GPU required). Off ⇒ `/wall`
  404s; the normal dashboard grid is unaffected.
- `RELAY_WALL_RES=1080p|720p` (default `1080p`) — output canvas. 1080p ⇒ 480×270
  cells at 4×4 (crisper, LAN); 720p ⇒ 320×180 (half the bandwidth, better remote).
  Detail comes from click-to-enlarge regardless, so this is a glance-quality knob.
- `RELAY_WALL_FPS` (default `15`) — output frame rate; a wall doesn't need 30.
- Requires the GPU wiring already added for transcode-live (`RELAY_RUNTIME=nvidia`
  + `NVIDIA_VISIBLE_DEVICES`).

## Data flow (request → pixels)

1. Browser opens `/wall?agent=<id>` (token-gated) → loads wall.html/js.
2. JS `GET /surv/wall/<id>/layout` → builds the overlay grid (names, positions).
3. JS plays `/surv/ws/<id>/wall` via `playWS` → single mosaic `<video>`.
4. Relay **lazily** starts the mosaic ffmpeg on that first WS client: inputs =
   the agent's enabled channels' self-HLS, composed per `mosaicLayout`.
5. Click a cell → JS opens `/surv/ws/<id>/<channelId>` fullscreen (1 decode);
   close → back to the wall.
6. Zero WS clients for the linger window → relay stops the mosaic ffmpeg.

## Robustness (the main risk)

A dead/stalled input must not freeze the whole mosaic.

- **Inputs = self-HLS**, which persists across RTSP reconnects, so inputs rarely
  truly end; a brief stall just delays that tile.
- **Hold last frame on a gap:** compose so an input that stalls/EOFs shows its
  last frame (or black) instead of blocking the grid (ffmpeg framesync
  `eof_action`/`repeatlast`, or a black base + per-input overlay).
- **Supervise + restart** the wall ffmpeg on exit with backoff (same pattern as
  the recorder / transcode-live).
- **Channel-set change** (config re-sent: add/remove/enable/reorder) → restart the
  mosaic with the new layout; client re-fetches layout on reconnect.
- **v1 = single ffmpeg + xstack.** If single-process robustness proves
  insufficient (one input wedging the grid), escalate to a **two-stage** design:
  each input → its own always-on holder that emits CFR frames (last-frame on
  stall) → a compositor stacks those. Noted as the explicit fallback, not v1.

## Error handling

- GPU/nvenc unavailable → mosaic ffmpeg fails; `/wall` surfaces an error. The wall
  is a GPU feature; the normal per-cell dashboard grid remains the non-GPU path.
- ffmpeg crash → supervised restart; the frozen `<video>` is rebuilt by the
  existing zombie-reconnect watchdog.
- No channels / agent offline → layout is empty; the page shows an offline state.

## Multi-tenant

The relay is multi-agent. The wall is **per agent**: stream id `wall` lives in that
agent's `SurvProxy`, addressed by the agent-scoped `/surv/...` path like any
channel. One mosaic per watched agent; lazy-start means only watched agents spend
GPU.

## Testing

- **Pure, TDD (no DOM/ffmpeg):**
  - `mosaicLayout(n)` — 1,4,9,16 channels → correct rows×cols and non-overlapping
    cell rects tiling the canvas; ≤16 boundary; 1-channel degenerate.
  - `mosaicArgs(...)` — generated ffmpeg args for a known layout (input list,
    scale filters, xstack layout string, nvenc output, AUD bsf, `-f h264 pipe:1`).
- **Reused & already covered:** `splitNALUnits`, `groupNALsIntoAUs`, ingest.
- **Manual:** visual on the relay with the agent's real channels — verify all
  tiles live, a pulled cable shows last-frame (grid keeps running), click-enlarge,
  and `nvidia-smi` shows one NVENC session.

## Out of scope / future

- Per-cell audio, PTZ, motion highlighting on the wall.
- A second lower-res mosaic variant for remote vs LAN (start with the res knob).
- Two-stage robust compositor (only if v1 xstack proves fragile).
