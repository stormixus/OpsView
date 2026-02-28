# OpsView

Windows 객실관리 앱 화면을 원격으로 시청하는 View-only 스트리밍 시스템.

## Architecture

```
[Windows Agent]                    [Relay Server]                [Viewers]
 DXGI Capture                       Auth + Fan-out
 Dirty Rect → Tile(128x128)   WSS   ┌──────────┐   WSS    ┌─ Wails Desktop
 zstd Compress ──────────────────→  │opsview-  │──────────┤
                                    │relay     │          ├─ Web Browser
                                    └──────────┘          └─ Mobile
```

- **타일 델타 전송**: 변경 영역(128x128)만 zstd 압축하여 전송 → 저사양 PC에서도 동작
- **LAN / Public 모드**: 내부 IP(`ws://`) 또는 공인 도메인(`wss://`) 모두 지원
- **해상도 프로파일**: 1080p / 720p 전환 가능
- **접근 보완 보안**: 에이전트 화면에 표시되는 동적 6자리 PIN 번호를 통해 Viewer 인증 통일

## Components

| Component | Path | Description |
|-----------|------|-------------|
| **proto** | `proto/` | OVP(OpsView Protocol) v1 바이너리 프로토콜 |
| **relay** | `relay/` | Go WebSocket 릴레이 서버 (인증, fan-out, backpressure) |
| **agent** | `agent/` | Windows 화면 캡처 에이전트 (DXGI Desktop Duplication) |
| **viewer** | `viewer/` | Wails 데스크톱 뷰어 (Ops / CCTV / Mixed 탭) |
| **web** | `web/` | 브라우저 웹 뷰어 (Canvas + WASM zstd) |

## Quick Start

```bash
# 1) Relay 시작
cd relay
go build -o opsview-relay .
./opsview-relay

# 2) Agent 시작
cd agent
go build -o opsview-agent .
./opsview-agent

# 3) 브라우저에서 시청
open http://127.0.0.1:8080
```

## Environment Variables

```bash
# Relay
RELAY_PORT=8080
RELAY_PUBLISHER_TOKEN=your-secret

# Agent
AGENT_RELAY_URL=ws://127.0.0.1:8080/publish
AGENT_TOKEN=your-secret
AGENT_PROFILE=1080   # or 720

# Viewer
WATCH_URL=ws://127.0.0.1:8080/watch
WATCH_TOKEN=pin-number
```

See [`.env.example`](.env.example) for full list.

## Documentation

- [Build & Run Guide](docs/BUILD.md)
- [OVP Protocol Spec](docs/PROTOCOL.md)

## License

Private - All rights reserved.
