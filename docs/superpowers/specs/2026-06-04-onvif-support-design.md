# ONVIF DVR Support — Design

Date: 2026-06-04
Status: Approved (design)

## Goal

Add ONVIF as a third surveillance protocol alongside Hikvision (ISAPI) and Dahua,
so that non-Hikvision/Dahua DVRs — most Korean/Chinese white-label units,
including EGPIS (이지피스, a Xiongmai/XMEye OEM) — work without per-brand code.
ONVIF is the industry standard; "Hikvision / Dahua / ONVIF (everything else)"
covers the large majority of devices.

Scope (full): auto-detect + channel discovery + RTSP streaming (via the existing
relay HLS proxy) + snapshots.

## Why ONVIF needs a different shape than ISAPI/Dahua

Today the **relay** (`relay/surv_proxy.go: buildSurvRTSPURL`) builds each RTSP URL
from a **fixed per-protocol path template** (`/Streaming/Channels/{ch}{stream}`
for ISAPI, `/cam/realmonitor?...` for Dahua) using the DVR address/port/channel.

ONVIF stream URLs are **device-specific** — you must ask the device for them
(`GetStreamUri`), so a fixed template does not work. Therefore:

- The **agent** (LAN side, already does discovery) resolves the per-channel RTSP
  and snapshot URIs at discovery time and stores them.
- The **relay** uses the stored per-channel RTSP URI directly when present,
  falling back to the template for ISAPI/Dahua.

## Architecture

| Component | Responsibility |
|---|---|
| `agent/onvif.go` (new) | Minimal SOAP client: probe, GetProfiles, GetStreamUri, GetSnapshotUri, WS-Security auth |
| `agent/surveillance.go` | `discoverFromDVROnvif`; ONVIF in the protocol probe chain; ONVIF snapshot; `channels.rtsp_uri`/`snapshot_uri` columns |
| `proto/json_messages.go` | `ChannelInfo.RtspURI` (agent → relay) |
| `relay/surv_proxy.go` | Use per-channel `RtspURI` when set (inject credentials if absent), else existing template |
| `agent/web_ui.go` | "ONVIF" option in the add/edit DVR protocol dropdowns |

## Agent ONVIF client (`agent/onvif.go`)

Hand-rolled SOAP over HTTP (no external dependency), matching the codebase's
raw-HTTP ISAPI/Dahua style. Uses `net/http`, `crypto/sha1`, `crypto/rand`,
`encoding/base64`, `encoding/xml`.

Calls (the minimum needed):
- `GetDeviceInformation` (device service) — **probe**: a valid SOAP 200 means the
  device speaks ONVIF.
- `GetServices` / `GetCapabilities` (device service) — resolve the **Media**
  service XAddr (Hikvision media is `/onvif/Media`; varies by device).
- `GetProfiles` (media) — list profiles: token, name, resolution, video source token.
- `GetStreamUri` (media, per profile) — RTSP URI (RTP/RTSP/TCP transport).
- `GetSnapshotUri` (media, per profile) — HTTP snapshot URI.

Auth: **WS-Security UsernameToken with PasswordDigest**:
`PasswordDigest = base64( sha1( nonce + created + password ) )`, where `nonce` is
random bytes (base64 in the header) and `created` is UTC ISO-8601. Some devices
also accept Basic/Digest HTTP auth; UsernameToken is the ONVIF standard and is
the primary path.

Device service URL: `http://{addr}:{port}/onvif/device_service`. Port: try
`dvr.Port` (usually 80) first; if it fails, try `8899` (common on Xiongmai).

## Discovery & detection (`agent/surveillance.go`)

- `discoverFromDVROnvif(dvr)`: `GetProfiles` → for each profile resolve
  `GetStreamUri` (RTSP) and `GetSnapshotUri`; map profiles to channels and store
  `ch_num`, `name`, `width`/`height`, `rtsp_uri`, `snapshot_uri`.
- `probeDVRProtocol`: order becomes **ISAPI → Dahua → ONVIF → RTSP**. Hikvision
  keeps using ISAPI (richer); ONVIF catches everything else before the bare-RTSP
  fallback.
- `discoverWithProtocol`: add `case "onvif"`.
- UI: add `onvif` to the protocol dropdowns so users can force it.

### Channel mapping (the device-specific risk)

ONVIF profiles do not map 1:1 to "channel N" the same way on every device. On a
multi-channel NVR, profiles carry a `VideoSourceConfiguration`/source token that
encodes the channel; main/sub streams appear as separate profiles. The mapping
(profile → `ch_num`, and which profile is the stream we route) must be **verified
against the real device** (Hikvision with ONVIF enabled for this project) and may
need a small per-quirk adjustment. Default heuristic: derive `ch_num` from the
video source token; pick the profile matching `StreamQuality` (main/sub).

## Data & protocol

- `channels` table: add `rtsp_uri TEXT NOT NULL DEFAULT ''` and
  `snapshot_uri TEXT NOT NULL DEFAULT ''` via `migrate()` ALTER (idempotent,
  ignore "duplicate column").
- `ChannelConfig`: add `RtspURI`, `SnapshotURI`.
- `proto.ChannelInfo`: add `RtspURI string json:"rtsp_uri,omitempty"`. (Snapshots
  are served by the agent over the existing WS snapshot request path, so
  `snapshot_uri` stays agent-side only.)
- `agent/agent.go: sendSurvConfig` fills `RtspURI`.

## Relay (`relay/surv_proxy.go`)

- `ChannelInfo` gains `RtspURI`. In the HLS path, look up the channel for
  `(dvrId, chNum)` from the cached surv config; if it has a non-empty `RtspURI`,
  use it (parse it and inject `dvr.Username`/`Password` if the URI has no
  userinfo), otherwise keep the current template logic. No behavior change for
  ISAPI/Dahua.

## Snapshot (`agent/surveillance.go: FetchSnapshot`)

- Add `case "onvif"`: HTTP GET the channel's `snapshot_uri` with the DVR
  credentials (try Basic; fall back to Digest if the device 401s with a Digest
  challenge). Returns the JPEG, same as the ISAPI/Dahua snapshot paths.

## Testing

- **Unit tests** (no device): WS-Security `PasswordDigest` computation against a
  known nonce/created/password vector; SOAP response parsing for `GetProfiles`,
  `GetStreamUri`, `GetSnapshotUri`, `GetDeviceInformation` using captured Hikvision
  ONVIF XML fixtures (served via `httptest`, mirroring the existing
  `surveillance_test.go` style).
- **Live verification**: enable ONVIF on a Hikvision DVR (Configuration → Network
  → Advanced → Integration Protocol → ONVIF; add an ONVIF user), then probe it
  from the dev machine and confirm discovery → HLS stream → snapshot end to end.

## Acceptance criteria

1. A DVR set to protocol `onvif` (or auto-detected as ONVIF) discovers its
   channels with resolutions.
2. Those channels stream as HLS through the relay (using the ONVIF-provided RTSP
   URI) and show in the viewer.
3. Snapshots work for ONVIF channels.
4. ISAPI and Dahua DVRs are unchanged (regression-free).
5. `go build`, `go vet`, `go test ./...` pass for `agent`, `proto`, `relay`;
   Windows cross-build of the agent passes.

## Out of scope

- WS-Discovery (UDP network scan). DVRs are added by IP, so direct connection is
  enough.
- ONVIF PTZ, events, two-way audio (possible future features).
- Auto-creating ONVIF users on the device.
