

# OpsView — Codex 작업 지시서 (MVP)

목표: Windows(저사양)에서 **객실관리 앱 화면**을 “보기만(View-only)” 형태로 송출하고, **Relay 서버**를 통해 **집/사무실/가끔 모바일**에서 시청한다.

핵심:
- FFmpeg/WebRTC 의존 최소화
- 객실관리 앱은 정적/부분변경이 많음 → **타일 델타(변경 영역만)** 전송으로 저부하
- LAN에서도 동작, 나중에 공인IP(원격)로 확장 가능

---

## 0) 최종 아키텍처

```
[Windows Sender Service: opsview-agent]
  DXGI Desktop Duplication → Dirty Rect → Tile Delta(128x128) → Compress(zstd)
        |
        | WSS (LAN: ws:// / Public: wss://)
        v
[Relay: opsview-relay]
  Auth + Fan-out + Backpressure + Metrics
        |
        +--> [Viewer: Wails app] (집/사무실)
        +--> [Viewer: Web] (모바일/브라우저)
```

네트워크:
- LAN 모드: 내부 IP로 접속 (예: `ws://relay.lan:8080`)
- Public 모드: 공인 IP/도메인 + TLS (예: `wss://opsview.example.com`) → **외부는 443 하나만** 열어도 되게

해상도:
- 기본 1080p (1920x1080)
- 옵션 720p (1280x720)
- 720p는 **Sender에서 downscale 후 전송** (대역폭/CPU 절감)

---

## 1) 산출물(리포 구조)

```
opsview/
  agent/        # opsview-agent (Windows service)
  relay/        # opsview-relay (Go server)
  viewer/       # Wails viewer (desktop)
  web/          # web viewer (mobile)
  proto/        # OVP protocol structs + codecs
  docs/         # build/run/deploy 문서
```

---

## 2) 기능 요구사항

### 2.1 opsview-agent (Windows Service / Daemon)

- Windows에서 부팅 시 자동 시작(서비스)
- 캡처: **Desktop Duplication API(DXGI)**
- Dirty Rect(변경 영역) 기반 **타일 델타 전송**
- 타일 크기: **128×128**
- FPS 정책(기본):
  - 기본 **5~10 fps**
  - 변경량 많을 때 최대 15fps
  - 변경량 적을 때 2~5fps로 자동 하향(옵션)
- 해상도 프로파일:
  - `profile=1080` (1920x1080)
  - `profile=720`  (1280x720)
  - 전환 시 캡처 파이프라인만 재초기화(프로세스는 유지)
- 압축:
  - 기본: **zstd(level 1~3)**
  - (후순위 옵션) webp lossless 타일 코덱 추가 가능
- 전송:
  - Relay에 **WSS**로 publish
  - 인증: `publisher_token`
- 장애 복구(매우 중요):
  - 디스플레이 리셋/해상도 변경/세션 변화/잠금 등으로 캡처 실패 시
    - **크래시/종료 없이** 캡처 재초기화
    - exponential backoff (1s → 2s → 5s → 10s → 30s)
  - 네트워크 끊김 시 자동 재연결
- 로그:
  - 파일 로그 + 콘솔 출력
  - 상태: fps, tiles/sec, bytes/sec, last_error

#### 2.1.1 Windows 서비스 실행
- 서비스명: `opsview-agent`
- 설치/삭제/시작/중지 스크립트 제공
- 실패 시 자동 재시작(Windows 서비스 Recovery 설정 또는 NSSM 사용)

---

### 2.2 opsview-relay (Go Server)

- endpoints:
  - `WS /publish` : publisher(opsview-agent) **1개만** 허용 (중복 publish 거부)
  - `WS /watch`   : 다중 시청자(집/사무실/모바일)
  - `GET /health` : 헬스체크
  - `GET /metrics`: (옵션) 간단 메트릭 노출
- 인증:
  - `publisher_token` / `watcher_token` 분리
  - 토큰 만료/회수(denylist) 지원
- Fan-out:
  - publish 받은 패치 메시지를 모든 watcher에게 broadcast
- Backpressure:
  - 느린 watcher는 큐 제한(예: 2~4개 메시지)
  - 큐 초과 시 **최신만 유지**(drop old) 또는 해당 watcher disconnect
- 멀티 프로파일:
  - agent가 1080/720 중 하나를 publish
  - (옵션) relay가 watcher의 요청에 따라 agent에 profile 전환 control 전달
- 운영:
  - 연결 수, 최근 publish 시각, 평균 fps, bytes/sec, last_error를 노출

---

### 2.3 Viewer

#### 2.3.1 Wails Desktop Viewer (집/사무실)
- 동일한 렌더러를 web과 공유
- 탭 UI:
  - `Ops` (객실관리 화면)
  - `CCTV` (향후)
  - `Mixed` (향후)
- 보기 전용: 입력 이벤트 전송 없음

#### 2.3.2 Web Viewer (모바일)
- 브라우저에서 `/watch` WSS 연결
- Canvas에 타일 패치 적용
- 기본은 1화면(싱글), 필요 시 2x2만 제공(모바일 부하 방지)

---

## 3) 프로토콜: OVP (OpsView Protocol) v1

WebSocket(바이너리)로 송수신.

### 3.1 공통 헤더
- 모든 메시지는 다음 구조를 가진다.

```
struct Header {
  u32 magic = 0x4F565031; // 'OVP1'
  u16 version = 1;
  u16 type;              // MessageType
  u32 payload_len;       // bytes
}
```

- 엔디안: little-endian

### 3.2 MessageType
- `1 = HELLO`
- `2 = AUTH`
- `3 = FRAME_DELTA`
- `4 = FULL_FRAME` (옵션: 초기 동기화에 사용)
- `5 = CONTROL` (옵션: profile 전환)
- `6 = HEARTBEAT`
- `7 = ERROR`

### 3.3 HELLO (publisher / watcher)
- 연결 직후 capabilities 교환.

Payload(JSON 또는 msgpack 중 택1. MVP는 JSON 추천):
```json
{
  "role": "publisher" | "watcher",
  "client": "opsview-agent" | "opsview-viewer" | "opsview-web",
  "client_version": "0.1.0",
  "supports": ["zstd"],
  "want_profile": "1080" | "720" | null
}
```

### 3.4 AUTH
Payload(JSON):
```json
{
  "token": "...publisher_token or watcher_token..."
}
```

Relay는 token 검증 실패 시 ERROR 후 close.

### 3.5 FRAME_DELTA
- 타일 델타 패치 메시지.

Payload(binary):

```
struct FrameDelta {
  u32 seq;
  u64 ts_ms;
  u16 profile;      // 1080=1080, 720=720
  u16 width;
  u16 height;
  u16 tile_size;    // 128
  u16 tile_count;
  // followed by tile_count tiles
}

struct Tile {
  u16 tx;           // tile x index
  u16 ty;           // tile y index
  u16 codec;        // 1=zstd_raw_bgra (MVP)
  u32 data_len;
  u8  data[data_len];
}
```

- codec `1=zstd_raw_bgra`:
  - 원본 타일 픽셀 포맷: BGRA 8bit (width=tile_size, height=tile_size)
  - 마지막 줄/열 타일은 화면 경계에서 잘릴 수 있으므로, 렌더 시 (width,height)로 클립

### 3.6 FULL_FRAME (옵션)
- 최초 접속 watcher 동기화용.
- 구현이 부담되면 MVP에서는 watcher가 접속해도 다음 delta부터 그리며, 화면이 채워질 때까지 기다리는 방식으로 시작해도 됨.

### 3.7 CONTROL (옵션)
- watcher → relay → publisher로 전달하는 제어(보기 전용이라 최소화)

Payload(JSON):
```json
{
  "cmd": "set_profile",
  "profile": "1080" | "720"
}
```

### 3.8 HEARTBEAT
- keepalive (예: 5초)

---

## 4) 캡처/타일링 구현 메모 (Windows)

- 캡처: DXGI Desktop Duplication
- Dirty rect를 받아 타일 좌표로 매핑:
  - `tx0 = floor(x / tile_size)`
  - `tx1 = floor((x+w-1) / tile_size)`
  - `ty0 = floor(y / tile_size)`
  - `ty1 = floor((y+h-1) / tile_size)`
- 변경 타일만 GPU→CPU로 복사 (가능하면 한 번에 맵핑 후 슬라이스)
- 720p 프로파일:
  - 1080p를 캡처한 뒤 GPU에서 스케일(가능하면) 또는 CPU 스케일
  - 저사양이면 **처음부터 720p 캡처 영역**으로 맞추는 것도 허용

---

## 5) 보안/운영

- Public 모드에서는 반드시 TLS(HTTPS/WSS)
- 토큰 분리:
  - `publisher_token`: 송출 권한
  - `watcher_token`: 시청 권한
- 토큰은 환경변수/설정파일로 로드
- relay는 접속 로그(IP, role, client, time) 기록

---

## 6) 실행 방법 (예시)

### 6.1 relay (개발)
- `RELAY_PUBLISHER_TOKEN`, `RELAY_WATCHER_TOKENS`(comma-separated) 환경변수 사용
- 기본 포트 8080

### 6.2 agent (개발)
- `AGENT_RELAY_URL=ws://relay.lan:8080/publish`
- `AGENT_TOKEN=...publisher_token...`
- `AGENT_PROFILE=1080|720`

### 6.3 viewer/web
- `WATCH_URL=ws(s)://relay.../watch`
- `WATCH_TOKEN=...watcher_token...`

---

## 7) MVP 우선순위

1) relay + agent + web viewer: **타일 델타가 안정적으로 보이기**
2) agent: 캡처 실패/해상도 변경/네트워크 끊김에도 **죽지 않고 복구**
3) wails viewer: web viewer 임베드 + 탭 UI
4) 옵션: CONTROL(set_profile)
5) 옵션: FULL_FRAME 초기 동기화 최적화

---

## 8) 완료 기준(Definition of Done)

- LAN에서: agent → relay → viewer가 10분 이상 끊김 없이 동작
- Public 모드에서: 443(WSS) 하나로 외부 시청 가능
- 시청자 3명 동시 접속 시:
  - agent 업로드는 1회(publish 1개)
  - relay fan-out으로 모두 수신
- 1080/720 프로파일 전환이 동작(재연결 또는 CONTROL)
- 캡처 실패 시 프로세스 종료 없이 자동 복구