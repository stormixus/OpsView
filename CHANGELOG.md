# Changelog

All notable changes to OpsView are documented here.

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
