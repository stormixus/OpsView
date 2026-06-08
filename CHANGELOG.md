# Changelog

All notable changes to OpsView are documented here.

## [Unreleased]

## [0.8.5] - 2026-06-08

### Fixed

- **Live grid channel order is now deterministic and reflects reorder edits.** The
  dashboard built its live grid straight from `StreamStats()`, which ranges a Go
  map — so the order was randomized on every poll/reload and a reorder edit never
  "stuck" (it reshuffled on the next refresh). `buildDashboardState` now sorts
  streams by DVR, then configured display order, then channel number. Relay-only.
- **Channel renames now appear live on the dashboard (no relay restart).** A
  stream's display name was frozen at stream creation; the reconcile loop skipped
  already-running streams entirely, so a rename in a re-sent config only showed
  after a relay restart recreated the stream. The reconcile now refreshes an
  existing stream's name when the config changes. Relay-only.
- **The selected DVR filter survives a page reload.** The device-filter chip
  selection lived only in JS state with no route, so a reload snapped back to 전체.
  It is now encoded in the URL (`?dvr=<id>`) and restored on load (and cleaned up
  if the device is gone). Relay-only.

### Changed

- **Reordering channels animates.** Dragging a cell in the live-grid editor now
  slides the other cells to their new slots (FLIP), instead of snapping — the live
  video streams are preserved (nodes are moved, not rebuilt). Relay-only.

## [0.8.4] - 2026-06-08

### Added

- **(internal/diagnostic) ONVIF Events capability probe — Phase 0 of motion/event
  detection.** During DVR discovery, agents now resolve each ONVIF DVR's Events
  service and log whether it is present and which event topics it advertises
  (`[onvif-events]` log lines). This is diagnostic only — no behavior change — and
  exists to confirm, on real hardware, whether the DVR exposes ONVIF Events
  (motion / smart events) before the detection pipeline is built. The wire message
  type `MsgSurvEvent` is reserved in the protocol but not yet emitted or consumed.

## [0.8.3] - 2026-06-07

### Changed

- **CCTV live video loads near-instantly (GOP fast-start).** A freshly-joined
  WebSocket viewer used to wait for the camera's *next* keyframe before any frame
  decoded — seconds of black on long-GOP DVRs. The relay now keeps the current GOP
  (the fragments since the last keyframe) per channel and flushes init + that GOP
  to a new client the moment it connects, so it decodes immediately and sits at
  most ~1 GOP behind live. Bounded + all-or-nothing so a pathological/keyframeless
  stream can't grow the cache or seed a client a broken GOP. Benefits both the
  dashboard and desktop viewer (server-side; no viewer update needed). Relay-only.
- **녹화 playback is much snappier.** (1) Finalized segments are served immutable
  (`Cache-Control: immutable` + ETag), so scrubbing, the hover preview, replays,
  and multi-cam re-seeks resolve from the browser cache instead of re-fetching byte
  ranges every time — the currently-recording segment stays no-cache (gated on
  mtime quiescence so a still-growing file is never cached). (2) Per-(stream,day)
  segment indexes are cached in memory keyed by the directory mtime, removing
  repeated disk scans from every day-switch/seek/grid-cell load. (3) The player
  pre-warms the next segment near the boundary and uses `preload=auto`, so
  auto-advance and click-to-play stop cold-opening. (4) Hover-scrub is throttled to
  animation frames and multi-cam drift correction is tighter (grid-size-aware).
- **Settings 관리 page polish.** Branch cards now refresh live (online/시청자/
  마지막 접속 stop freezing while the page is open) via a non-destructive patch that
  doesn't clobber an in-flight rename or a revealed token; the 관리 topbar button
  shows an active state and the sidebar no longer double-highlights while in manage;
  a loading skeleton + retry replaces the blank-until-loaded gap; branch tokens are
  kept out of the DOM (in a JS map) and auto-re-mask after reveal; the 알림/보안
  forms use design tokens (follow the accent + light theme); tighter mobile layout.

## [0.8.2] - 2026-06-07

### Fixed

- **CCTV black in the desktop viewer for named (multi-tenant) agents.** The relay
  namespaces a named agent's streams as `/surv/<agentId>/dvrN_chM`, but the viewer
  built the path without the scope (`/surv/dvrN_chM`), so the request fell to the
  default session, found no stream, and every cell went black. (Single "default"
  agents were unaffected — they fall back to direct RTSP.) The relay now stamps the
  session id into the surv config (`agent_id`) it forwards to watchers, and the
  viewer prefixes the scope onto the `/surv` (HLS) and `/surv/ws` (fMP4) paths.
  Needs **both** a relay image pull and a viewer update. proto: `SurvConfig.AgentID`.

## [0.8.1] - 2026-06-07

### Added

- **Dashboard: dedicated 관리 page.** Settings split out of the cramped ⚙ drawer:
  the quick-drawer now holds only display prefs (테마/액센트/정보밀도), while 지점·
  알림·보안 move to a full-page **관리** view at `/dashboard/manage` (deep-linkable
  and refresh-safe via the History API) with a left sub-tab rail. 지점 is now a
  **card grid** joining the agent registry with live state — each card shows
  online/last-seen, DVR·채널 (active/total), 시청자, and a masked token (reveal +
  copy), with 이름변경 (inline), 토큰 재발급, 재접속, 숨기기, 삭제 actions, an
  add-card, and a 숨긴 지점 section. Read-only when `RELAY_DB` is unset; the legacy
  "default" agent renders as a limited card. Relay-only.

### Fixed

- **Update toast showed a doubled "v" (`vv0.8.0`).** `main.Version` is the git tag
  verbatim (already `v`-prefixed), but the viewer's update UI prepended another
  "v". A `vlabel()` normalizer now renders exactly one. Viewer-only.

## [0.8.0] - 2026-06-07

### Added

- **Relay NVR recording (opt-in).** Set `RELAY_REC_DIR` and the relay records
  every active stream to segmented MP4 on disk by supervising one ffmpeg per
  channel that copies the relay's *own* HLS — so the DVR is not pulled a second
  time and nothing is re-encoded. `RELAY_REC_MAX` (e.g. "2TB") caps disk use; a
  janitor deletes the oldest segments past the cap. Records whatever quality the
  relay streams (substream here). Relay-only.
- **Dashboard: 녹화 review tab.** A new 녹화 tab per agent: pick a channel + day,
  scrub a 24-hour timeline of recorded segments, click any time to play it
  instantly (with seek + auto-advance to the next segment), hover for the time
  readout, and download the current segment. Backed by
  `/dashboard/api/rec` (list) + `/dashboard/api/rec-file` (Range-served MP4),
  admin-gated with path-traversal guards.
- **Dashboard: multi-channel synced playback.** The 녹화 tab now has a layout
  toggle (단일 / 2×2 / 3×3 / 4×4): pick a grid and the first N channels play
  back in lockstep off a shared master clock, so scrubbing the timeline seeks
  every channel to the same wall-clock instant. A 1s drift corrector keeps the
  tiles aligned and advances each across segment boundaries independently.
- **Dashboard: hover-scrub thumbnail.** Hovering the 녹화 timeline now shows a
  live preview of that moment (a muted `<video>` seeked to the hovered time)
  floating above the cursor, alongside the time readout — so you can find the
  right moment before clicking. No extra disk: it reads the same Range-served
  segments. Relay-only.
- **Dashboard: clip range export.** Drag across the 녹화 timeline to select a
  start–end window (up to 1h), then 구간 내보내기 downloads just that window as
  a single MP4. The relay concats the overlapping segments and copies the
  window with no re-encode (fragmented-MP4 streamed straight to the download),
  spanning segment and midnight boundaries. Backed by
  `/dashboard/api/rec-export`, admin-gated with the same path-traversal guards.

- **Dashboard: hide unused agents (지점 관리).** Hover an agent card → 숨기기 to
  hide it from the dashboard (e.g. the leftover "default" agent after moving to
  named tenants). Hidden agents drop from the overview, cards, and totals;
  restore them from the settings drawer's 지점 관리 → 숨긴 지점 list. Persisted in
  the relay DB (`hidden_agents`, requires `RELAY_DB`). Relay-only.
- **Fault alerts (Telegram + webhook).** The relay watches each agent (지점) and,
  when one goes offline for >30s, pushes an alert — and again on recovery — to
  Telegram and/or a webhook (Slack/Discord/etc.). Configure in the dashboard
  settings drawer (enable + bot token/chat_id + webhook URL, with a 테스트
  button); settings persist in the relay DB (`RELAY_DB`) or come from
  `RELAY_TELEGRAM_TOKEN` / `RELAY_TELEGRAM_CHAT` / `RELAY_ALERT_WEBHOOK` /
  `RELAY_ALERTS_ENABLED`. Webhook URLs to loopback/cloud-metadata are blocked
  (SSRF). Relay-only change.
- **Dashboard: name watchers by IP.** In the Watchers detail (click the Watchers
  stat) the operator can assign a display name to each client IP; the name is
  stored on the relay (`ip_labels`, requires `RELAY_DB`) and shown next to that
  IP everywhere afterward — so the list reads "프론트 192.168.0.50" instead of a
  bare address. Relay-only change (no agent/viewer update needed).

### Fixed

- **CCTV live grid went black after a few seconds (viewer watchdog loop).** The
  new grid watchdog treated a cell whose `<video>.currentTime` hadn't advanced as
  dead and reconnected it — but a reconnect resets `currentTime` to 0 and the
  fresh fMP4-over-WS stream only resumes on the relay's next keyframe (a full GOP,
  which on low-fps CCTV can exceed the 12s tick). So once it fired, the cell was
  torn down every tick before a keyframe ever arrived → permanent black, with the
  abandoned WebSocket also clobbering the new source via the HLS fallback. The
  watchdog now debounces (two consecutive stalled ticks), waits out a grace period
  after each reconnect, and cleanly tears down the prior player first. Viewer-only.
- **화면 소스 panel stayed over the live stream after reconnect.** The relay setup
  panel was only dismissed by the connect button's polling loop, so a connection
  that came up via the auto-reconnect path (`onclose` → retry) left the panel
  stuck over the frames. The panel now hides on the actual `onopen` event, so any
  successful connect — initial, auto-reconnect, or restore-on-launch — reveals the
  stream. Viewer-only.
- **Custom CCTV channel names/order vanished on hybrid DVRs.** Channel discovery
  capped to 16 channels and then deleted any DB channel not in that capped set. On
  a hybrid DVR (analog + IP) with more than 16 channels — or whose analog probe
  transiently returned a smaller set — this dropped real cameras and destroyed the
  operator's custom names and display order, which were recreated as bare defaults
  when the channels reappeared (the 16↔18 flap). Discovery now keeps up to 32
  channels (what the probe scans) and is additive: it upserts what it finds but
  never deletes a channel merely absent from one round, so operator labels/order
  survive. Agent update required.

## [0.7.0] - 2026-06-06

### Fixed

- **녹화 timeline was shifted off-screen (timezone).** The relay container ran in
  UTC (no `tzdata`/`TZ`), so ffmpeg named recording segments in UTC while the
  dashboard positioned them against the operator's local (KST) midnight — pushing
  the segment bars past the edge of the 24h track (empty timeline) and putting
  every scrub time ~9h off. The image now ships `tzdata` and defaults `TZ` to
  `Asia/Seoul` (override per-site), so recordings are named and positioned in the
  venue's local time. Relay-only.
- **녹화 tab ignored the DVR filter (전체 / DVR별).** Switching the DVR chips at the
  top did nothing in the 녹화 tab — the channel list and grid stayed on all
  channels. The chips now re-scope the recording view, and `openRec` filters its
  channels by the selected DVR.
- **Viewer: live grid auto-recovers dropped channels (이빨 빠짐).** When one or
  two channels failed to connect on a busy grid they stayed blank until a manual
  refresh. A watchdog now checks each cell every ~12s and re-attempts any whose
  video has stalled (no playback progress), using the same path the grid used
  (relay HLS or local RTSP). ISAPI snapshot cells are skipped.

### Added

- **Agent self-heals dropped DVRs + dashboard "재연결" button.** When a DVR
  reboots (or otherwise drops), its streams used to disappear from the relay
  until the agent was manually restarted. Now a background loop on the agent
  re-discovers any DVR that is reachable but has lost its channels (every 90s),
  and the dashboard's agent view gains a **재연결** button that sends a
  `MsgAgentControl{reconnect}` down to the agent (via `POST
  /dashboard/api/agent-control`, admin-gated) to force a full DVR
  re-discovery + config re-publish on demand. (Requires updating the agent.)

## [0.6.1] - 2026-06-06

### Fixed

- **Dashboard: enlarging a CCTV channel now shows the video.** The modal video
  element was only sized by a `.cell`-scoped rule, so in the fullscreen modal it
  had no dimensions and the stream (though attached) stayed invisible. Added a
  `.modal-cell .cellvid` rule (object-fit: contain).
- **Dashboard: assets are no longer served stale.** Static assets now send
  `Cache-Control: no-cache`, so a new relay build's CSS/JS/logo is picked up on
  the next load instead of after a manual hard refresh.

## [0.6.0] - 2026-06-06

### Added

- **Dashboard: change the password from settings.** The login password is now
  stored in the relay DB when set (editable in the settings drawer, requires
  `RELAY_DB`) and falls back to the docker env `RELAY_DASHBOARD_TOKEN` when the
  DB has none. Changing it persists on the relay and logs out other sessions.
- **Dashboard: 지점(tenant) 관리.** The settings drawer gains an agent manager —
  list locations (online dot + copyable token), add one (with a token
  generator), and delete one — backed by a SQLite registry on the relay. Set
  `RELAY_DB` (a path on the new `relay-data` volume) to enable editing; it is
  seeded once from `RELAY_AGENTS` and managed live afterward. Without `RELAY_DB`
  the registry stays env-only (read-only in the dashboard).
- **Dashboard: inline channel editing in the live view.** A 편집 toggle on the
  live tab turns the camera tiles into draggable, inline-renamable cells — drag
  to reorder, edit the name in place — replacing the separate modal editor. Each
  change round-trips to the agent per device.
- **Dashboard: live Ops snapshot.** The "Publisher · Ops 화면" panel now shows the
  real publisher screen (rendered on demand from the relay frame buffer via
  `GET /dashboard/api/ops-snapshot`, admin-gated) instead of a placeholder,
  refreshing while online and falling back to the placeholder when offline.
  Click the panel to enlarge it (faster-refreshing full view).
- **Dashboard: watcher detail.** Clicking the Watchers stat opens the full
  watcher list (id, client IP, connected duration).
- **Dashboard: branding.** The logo is now the OpsView app icon (background
  removed → transparent) used as the header logo and favicon; clicking it
  returns to the home overview.

### Fixed

- **Dashboard: enlarging a CCTV channel now plays live.** Clicking a channel
  rendered a demo placeholder instead of attaching the stream; the modal now
  uses the same live WS→HLS player as the grid cells.
- **Dashboard: show the watcher's real IP behind a tunnel.** Watcher IPs are now
  taken from `CF-Connecting-IP` / `X-Forwarded-For` (the real client) instead of
  the Cloudflare Tunnel's socket address. Rate limiting still keys on the
  unspoofable socket peer.
- **Viewer: Hikvision DVRs no longer get stuck on 2-second snapshots.** A DVR
  that answers ISAPI was probed as snapshot-only even when it also served live
  RTSP, downgrading every channel to 2s snapshot polling. Discovery now prefers
  live RTSP when its port is reachable (ISAPI stays the snapshot fallback), and
  RTSP targets port 554 even when the DVR is stored on an HTTP port (80/8000).

- **Viewer: connecting via a domain (Cloudflare Tunnel) just works.** Entering a
  domain host (e.g. `ops.example.net`) now auto-uses `wss`/`https` on 443 for
  both the Ops `/watch` socket and CCTV HLS, regardless of the port box (which
  becomes optional for domains). Previously only an explicit port `443` switched
  to TLS, so a tunnel domain silently tried plain `ws` and failed to connect.

## [0.5.0] - 2026-06-06

### Added

- **Channel order/label sync.** Set channel order and names once and have them
  flow to every viewer + the dashboard. The agent's settings screen gains a
  channel editor (a grid of channel snapshot thumbnails — drag to reorder, click
  the name to relabel); changes save to the agent DB and re-broadcast the config.
  The dashboard can edit too (drag-reorder + rename per device), round-tripping
  the edit to the agent over its publisher connection (new `MsgSurvMeta`) so both
  surfaces stay in sync.
- **Agent: optional 지점 ID (`agent_id`) for multi-tenant relays.** Settings gains
  an Agent ID field; when set, the agent claims a named tenant session on the
  relay (`RELAY_AGENTS`) with its own streams/PIN/isolation. Empty keeps the
  legacy single "default" agent behavior, so existing installs are unchanged.
- **Dashboard: group CCTV by DVR/NVR device.** A device chip selector
  (전체 + one chip per DVR) filters the status table and live grid to one device,
  so a 36-channel agent isn't a flat wall (and fewer live WS connections). The
  agent-provided channel name is shown with a `CH<n>` tag instead of the raw
  `dvrN_chM` stream id.

### Fixed

- **Relay: silence gohlslib CCTV log spam.** The HLS muxer logged a warning on
  every part/segment duration jitter ("…will cause an error in iOS clients") —
  benign noise from cameras' variable GOP timing (viewers prefer WS with HLS as
  fallback). The relay now filters those via `OnEncodeError` and only surfaces
  genuine encode errors.

## [0.4.0] - 2026-06-06

A multi-tenant relay release: one relay now hosts many agents (locations),
plus a built-in operator dashboard. Backward compatible — existing single-agent
deployments keep working unchanged (they become the "default" agent).

### Added

- **Relay: multi-agent (multi-tenant) hosting.** One relay can serve several
  locations at once, each with its own Ops screen, CCTV streams, watchers, and
  PIN — isolated from one another. The single-publisher `Hub` became a map of
  per-agent sessions. A publisher claims its session via `agent_id` + a
  per-agent token (`RELAY_AGENTS` registry; the legacy `RELAY_PUBLISHER_TOKEN`
  is the "default" agent). A watcher's PIN both selects and authenticates its
  tenant, so existing viewers connect unchanged. CCTV is namespaced per agent
  (`/surv/{agentID}/…`; the default agent keeps the flat `/surv/…` path).
- **Relay: operator dashboard** at `/dashboard`, served by the relay (enabled
  only when `RELAY_DASHBOARD_TOKEN` is set — otherwise the routes 404). Admin
  password login over an HMAC-signed cookie; an aggregated `/dashboard/api/state`
  (polled every 2 s) drives an agent-grouped UI — a relay overview with a card
  per location, and a per-agent drill-in with a status tab (publisher / watchers
  / streams / throughput) and a live tab that plays every channel over the
  fMP4-over-WS player (HLS fallback). Real URL routing (History API), so browser
  back/forward and deep links work.

### Fixed

- **Relay: CCTV WebSocket watchers no longer leak on half-open connections.**
  `/surv/ws/` handled clean disconnects fine (the client is removed and its
  goroutines exit), but a half-open TCP peer — a viewer whose Wi-Fi dropped or
  laptop slept without a RST — left `ReadMessage` blocked forever, so the client
  entry, its send channel, and two goroutines lingered until the OS TCP
  keepalive eventually reaped them (hours). Added ping/pong keepalive (54 s
  ping, 60 s pong deadline) and write deadlines, and the writer now closes the
  connection on exit to unblock the reader, so a vanished client is cleaned up
  within ~60 s.

## [0.3.7] - 2026-06-06

### Fixed

- **CCTV over WebSocket now actually plays in Safari / WKWebView** (so the macOS
  desktop viewer uses the `wss` path instead of falling back to HLS). Two
  WebKit-strict issues, both invisible in Chromium:
  - The relay's fMP4 fragments carry the RTSP stream's running
    `baseMediaDecodeTime` (often many hours in), so under MSE `'segments'` mode
    the buffered range starts far from `currentTime = 0`. Chrome auto-seeks;
    WebKit just shows black. Fixed by appending in `'sequence'` mode, which
    re-bases every fragment onto a 0-based timeline.
  - WebKit does not auto-start playback if `video.play()` was called before the
    SourceBuffer had any data, so the picture stayed frozen. Fixed by nudging
    `play()` after each append.
  The v0.3.6 Chromium-only gate is therefore lifted — WS+MSE is used on any
  engine with MSE (iOS, which has none, still uses HLS), and an H.265 substream
  (no H.264 MSE support outside Safari) still falls back to HLS via the
  `MediaSource.isTypeSupported` check.

## [0.3.6] - 2026-06-05

### Fixed

- **CCTV stays on HLS in Safari / WKWebView.** The v0.3.5 fMP4-over-WS path used
  Media Source Extensions, which Chromium plays fine but Safari/WKWebView accepts
  (`addSourceBuffer` succeeds) only to render black for the relay's raw
  fragments. Both viewers now gate the WS+MSE transport to Chromium engines
  (Chrome, Edge, Windows WebView2) and fall back to native/`hls.js` HLS
  everywhere else — restoring video in the macOS desktop viewer and in Safari.
- **Web viewer: `survMode is not defined` crash.** A leftover reference to a
  removed `survMode` variable threw inside the WebSocket `onopen` handler,
  aborting the OVP HELLO/AUTH handshake so the web viewer never connected. The
  relay CCTV streams are now always fetched on connect.

## [0.3.5] - 2026-06-05

### Added

- **Relay: fMP4-over-WebSocket CCTV endpoint** (`/surv/ws/{chID}`) alongside HLS,
  so CCTV can ride the same `wss` path as the Ops stream (e.g. through a
  Cloudflare Tunnel). The relay taps the existing RTSP→H264/H265 stream, builds
  an fMP4 init segment (lazily, from in-band SPS/PPS that DVRs like Hikvision
  omit from the SDP) plus one fragment per access unit, and fans them out per
  channel; clients start at a keyframe with the init segment sent first
  (MSE-ready). Verified end to end against a Hikvision iDS-7204HUHI.
- **Viewers auto-select the CCTV transport.** Both the desktop (Wails) and web
  viewers now prefer fMP4-over-WS via Media Source Extensions when available and
  fall back to HLS otherwise (iOS / no-MSE / H265 / older relays / any WS
  failure within 6 s). The WS path rides the same `wss` endpoint as the Ops
  stream, so CCTV works through a Cloudflare Tunnel without exposing the HLS
  port. The codec string is parsed from the init segment's `avcC` box; an
  unrecognized codec (e.g. H265) transparently falls back to HLS.

### Fixed

- **Relay: new viewers see the whole Ops screen immediately.** The relay caches
  the latest tile per position and sends a synthesized full keyframe to each
  watcher on join, instead of the screen filling in tile-by-tile. Also rebuilds
  the frame buffer as a codec-agnostic tile cache (the old BGRA buffer silently
  stopped working after the v0.3.3 switch to JPEG tiles), fixing the Ops PNG
  snapshot too.

## [0.3.4] - 2026-06-05

### Added

- **ONVIF DVR support** (third protocol alongside Hikvision ISAPI and Dahua) so
  non-Hikvision/Dahua DVRs — most Korean/Chinese white-label units — work
  without per-brand code. The agent hand-rolls a minimal ONVIF SOAP client
  (WS-Security UsernameToken **plus HTTP Digest** auth, which Hikvision requires
  on its ONVIF endpoint), resolves each channel's RTSP and snapshot URI via
  `GetProfiles`/`GetStreamUri`/`GetSnapshotUri` (falling back from `GetServices`
  to `GetCapabilities` for the media service), and the relay streams those
  device-provided RTSP URIs. Auto-detected in the probe chain
  (ISAPI→Dahua→ONVIF→RTSP) and selectable in the DVR settings. Snapshot fetches
  are SSRF-guarded. Verified live against a Hikvision iDS-7204HUHI.

## [0.3.3] - 2026-06-04

### Performance

- **Much lower 1080p screen latency.** Screen tiles are now encoded as JPEG
  (quality 85) instead of zstd-compressed raw BGRA. JPEG is ~5–10x smaller for
  screen content and the viewers decode it natively and asynchronously
  (`createImageBitmap`) off the main thread, instead of decompressing raw BGRA
  on it. This removes the viewer-side backlog that pushed 1080p latency to
  several seconds. Viewers still accept the legacy zstd tile codec for
  backward compatibility.

## [0.3.2] - 2026-06-04

### Fixed

- **Agent: editing a DVR no longer wipes its password.** The settings UI clears
  the password field on edit, but saving an unrelated change (e.g. a rename)
  overwrote the stored secret with an empty value. A blank password now means
  "unchanged". This was the root cause of DVRs silently losing auth and being
  misdetected as RTSP.
- **Agent: clearer DVR auth errors.** A Hikvision DVR that returns 401/403
  (wrong credentials, or an IP lock after repeated failed logins) is now
  classified ISAPI and reports a precise "인증 실패 / IP 잠금" message instead of
  falling through to an RTSP probe and reporting the misleading
  "no RTSP channels found".

## [0.3.1] - 2026-06-04

### Added

- **Agent: configure the publisher token in-app.** The shared relay secret
  (`RELAY_PUBLISHER_TOKEN`) can now be set in the agent's Settings (고급 설정 →
  Publisher Token) and is stored in `config.json`. The agent prefers this value
  and falls back to the `RELAY_PUBLISHER_TOKEN` / `AGENT_TOKEN` environment
  variable when it is empty, so existing env/service-based setups keep working.
  This removes the need to inject the token via environment variables for the
  download-and-run deployment.

## [0.3.0] - 2026-06-04

A large security + performance release: a full security audit (5 critical/high
issues plus extensive hardening) and Low-Latency HLS for CCTV.

### ⚠️ Breaking changes (read before upgrading)

- **Relay now requires `RELAY_PUBLISHER_TOKEN`** (falls back to `AGENT_TOKEN`)
  and **refuses to start without it**. Set the *same* value on the relay and the
  agent. Publishers authenticate against this shared secret (constant-time
  compare); the 6-digit viewer PIN is now a separate value.
- **Viewer auto-updater is fail-closed.** It verifies an **Ed25519 signature**
  over each installer before running it. CI signs releases with the
  `ED25519_UPDATE_PRIVATE_KEY` secret; the matching public key is embedded in
  the viewer. Unsigned builds will not auto-install.

### Security

**Critical**
- Relay authenticates publishers — previously *any* non-empty token was accepted
  and reused as the viewer PIN (publisher takeover / squatting / frame
  injection).
- DVR credentials no longer leak — `/api/surv` is PIN-gated and
  credential-redacted; credentials are stripped from client-facing config
  (previously exposed in plaintext to unauthenticated callers).
- Viewer auto-updater verifies Ed25519 signatures before executing installers
  (previously ran unverified downloads — remote code execution).

**High**
- Watcher PIN brute-force protection: per-IP rate limit + lockout, constant-time
  compare.
- Relay frame-buffer dimension caps and WebSocket read-size limit (memory-
  exhaustion DoS).
- `SurveillanceManager.ResetDB` is concurrency-safe and atomic (was a data race
  + use-after-close).
- Stored XSS via DVR/channel names fixed (output escaping in both viewers).
- RTSP→HLS setup runs off the publisher read loop (no longer stalls screen-frame
  ingestion).
- Agent warns when publishing over plaintext `ws://` to a public host.

**Medium / Low**
- WebSocket `Origin` allowlist (`RELAY_ALLOWED_ORIGINS`), RTSP SSRF guard
  (blocks loopback/link-local/metadata), credential redaction in logs, OVP
  payload-size cap.
- Agent web-UI DNS-rebinding guard, snapshot-request limiter, `0600`/`0700`
  secret-file permissions, idempotent shutdown.
- ISAPI channel-discovery correctness (gap handling, scan-error handling,
  channel-name sanitization), browser frame-bounds validation, viewer reorder
  transaction rollback.

### Performance

- **Low-Latency HLS (LL-HLS)** for CCTV streaming: ~200 ms parts, **sub-second**
  live latency (verified against a real Hikvision DVR), replacing standard HLS
  that drifted to 10–30 s. Includes player drift caps and a deeper segment
  window for jitter recovery.

### Tests / tooling

- Opt-in real-DVR discovery probe (`TestHVRProbe`, skips unless `HVR_ADDR` set).
- ISAPI discovery test fixtures updated to the real nested `<Video>` resolution
  XML.

[0.3.0]: https://github.com/stormixus/OpsView/releases/tag/v0.3.0
