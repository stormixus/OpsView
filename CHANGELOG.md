# Changelog

All notable changes to OpsView are documented here.

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
