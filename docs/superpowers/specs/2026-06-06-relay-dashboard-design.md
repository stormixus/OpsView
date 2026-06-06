# OpsView Relay Dashboard — Design (Milestone 1: Status + Live Grid)

- 작성: 2026-06-06
- 대상: OpsView `relay` (Go 서버)
- 상태: 승인됨 (구현 대기)

## 1. 배경 & 목표

OpsView relay는 Ops 화면 공유(`/watch`)와 CCTV(HLS `/surv/`, fMP4-over-WS
`/surv/ws/`)를 중계하는 Go 서버다. 현재 운영자가 relay 상태를 한눈에 볼 방법이
없다(`/health`, `/metrics` JSON을 직접 치는 정도).

장기 비전은 **Frigate 같은 NVR 대시보드**다. 이 스펙은 그 첫 마일스톤으로,
relay가 직접 서빙하는 **웹 대시보드**를 만든다:

- **상태 탭**: publisher/watcher/활성 스트림/메트릭/health
- **라이브 탭**: 전 채널 라이브 그리드 (기존 WS/HLS 플레이어 재사용)

녹화/타임라인, 객체·모션 감지, 카메라 설정 UI는 **이 스펙 범위 밖**이며 각각
별도 스펙으로 후속 진행한다.

## 2. 비목표 (Non-goals)

- 녹화/재생/타임라인 (디스크 저장소 필요 — 별도 스펙)
- 객체/모션 감지, 이벤트 (AI 파이프라인 — 별도 스펙)
- 카메라/DVR 설정 편집 (조회만, 편집은 후속)
- 기존 `/surv/`·`/surv/ws/`의 인증 모델 변경 (현행 유지)

## 3. 아키텍처

선택안: **relay 서빙 + 통합 `/dashboard/api/state` 엔드포인트** (대안 B[기존 엔드포인트
직접] / C[별도 앱]은 각각 요청 분산·CORS/배포 부담으로 기각).

- relay가 `/dashboard`에 vanilla 정적 SPA를 **Go `embed`** 로 내장 서빙 (기존 web
  뷰어처럼 빌드 스텝 없음).
- 상태 패널 데이터는 admin 인증된 **단일 집계 JSON**(`/dashboard/api/state`)으로 제공.
- 라이브 그리드는 기존 `/surv/ws/{id}`·`/surv/{id}/index.m3u8`를 그대로 재사용.
- CCTV 플레이어 로직(codecFromInit / `sb.mode='sequence'` / append마다 `play()` /
  WS→HLS 폴백)은 검증된 web 뷰어(`web/viewer.js`) 코드를 대시보드 `app.js`로 이식.

### 3.1 신규/변경 파일

- `relay/dashboard.go` — 로그인/세션 미들웨어, `/dashboard/*` 핸들러, state 집계
- `relay/dashboard_assets/{index.html,app.js,style.css}` — `//go:embed` 내장
- `relay/dashboard_test.go` — 인증/state 유닛 테스트
- `relay/main.go` — 라우트 등록 (토큰 설정 시에만)
- `relay/hub.go` — `Watcher.connectedAt`, `Hub.WatcherList()`, 시작시각/버전
- `relay/surv_ws.go` — `survWSHub.ClientCount()`, `fragMuxer.Codec()`
- `relay/surv_proxy.go` — `StreamInfo`에 `codec`, `ws_watchers` 노출

## 4. 라우트

| 메서드 | 경로 | 인증 | 내용 |
|---|---|---|---|
| GET  | `/dashboard`            | 쿠키 없으면 로그인 화면 | SPA 셸 (index.html) |
| GET  | `/dashboard/assets/*`   | 없음 | 정적 파일 (app.js, style.css) |
| POST | `/dashboard/api/login`  | 없음 | 비번 확인 → 서명 쿠키 set |
| POST | `/dashboard/api/logout` | 쿠키 | 쿠키 삭제 |
| GET  | `/dashboard/api/state`  | 쿠키 필수 | 집계 JSON |

라우트는 `/surv/ws/`·`/surv/`보다 더 구체적인 prefix(`/dashboard/`)라 mux 충돌 없음.

## 5. 인증

- **`RELAY_DASHBOARD_TOKEN` 환경변수가 설정된 경우에만 대시보드 활성화.**
  미설정이면 `/dashboard*` 라우트를 **등록하지 않음**(404). → 무인증 노출을
  원천 차단하는 fail-safe.
- **로그인**: `POST /dashboard/api/login {password}` → `DashboardToken`과
  `subtle.ConstantTimeCompare`. 성공 시 쿠키 발급.
- **세션 쿠키 (stateless)**: `opsview_dash = base64url(exp) + "." +
  hex(HMAC_SHA256(base64url(exp), key))`
  - `exp` = 만료 unix초 (기본 12시간)
  - `key` = `SHA256(DashboardToken)` (원시 토큰을 HMAC 키로 직접 쓰지 않고 고정길이 파생)
  - 속성: `HttpOnly`, `SameSite=Strict`, `Path=/dashboard`, 요청이 TLS면 `Secure`
  - 검증: HMAC 일치 + 미만료. 서버 세션 저장소 없음.
- **Brute-force 방어**: 기존 per-IP `pinLimiter` 패턴을 로그인에 적용
  (실패 누적 시 잠금 + constant-time).

## 6. `/dashboard/api/state` 데이터 모델

```json
{
  "relay": {
    "version": "0.3.8",
    "uptime_sec": 12345,
    "publisher_connected": true,
    "publisher_pin_set": true,
    "last_publish_at": "2026-06-06T09:24:43Z",
    "bytes_in": 123456789,
    "bytes_out": 987654321,
    "publish_count": 42
  },
  "watchers": {
    "count": 3,
    "list": [
      { "id": 1, "ip": "192.168.0.57", "since": "2026-06-06T09:10:40Z" }
    ]
  },
  "streams": [
    { "id": "dvr4_ch1", "name": "101", "active": true, "codec": "h264", "ws_watchers": 2 }
  ],
  "dvrs": [
    { "id": 4, "name": "HIKVISION DVR1", "channels": 16 }
  ]
}
```

- `bytes_in/out`는 **누적 카운터** — 클라가 폴링 간 델타로 throughput(kbps) 계산.
- `watchers.list`는 Ops `/watch` 시청자 (IP, 접속시각). CCTV WS 시청자는
  스트림별 `ws_watchers`로 집계.
- `codec`: `h264` | `h265` (fragMuxer 기준). 아직 init 미생성이면 빈 문자열 가능.
- `version`: 빌드 시 ldflags 주입(`-X main.relayVersion=...`), 기본 `"dev"`.

## 7. 프론트엔드

### 7.1 레이아웃 (탭 2개)

```
┌─ OpsView Relay ─────────────────────────[상태]│[라이브]──[로그아웃]─┐
│  [Publisher card] [Watchers card] [Streams card] [Throughput card] │
│  ┌─ Publisher ──────────┐  ┌─ Watchers ───────────────────┐        │
│  │ Ops 스냅샷 썸네일      │  │ ID  IP            접속         │        │
│  │ PIN 설정됨 · 누적 N   │  │ 1   192.168.0.57  00:14:03    │        │
│  └──────────────────────┘  └──────────────────────────────┘        │
│  ┌─ Streams ───────────────────────────────────────────────┐       │
│  │ 채널   코덱   전송       WS시청  상태                      │       │
│  │ 101   h264   WS+HLS     2      ● live                    │       │
│  └─────────────────────────────────────────────────────────┘       │
└────────────────────────────────────────────────────────────────────┘
```

- **상태 탭**: stat 카드(Publisher/Watchers/Streams/Throughput) + Publisher 패널
  (Ops 스냅샷 `/api/snapshot` 썸네일) + Watchers 테이블 + Streams 테이블.
- **라이브 탭**: 채널 라이브 그리드 = 기존 뷰어 라이브뷰(WS 우선 → HLS 폴백, 채널 라벨).

### 7.2 실시간 갱신 (폴링)

- 상태 탭: `/dashboard/api/state` **2초 폴링**. `document.hidden`이면 폴링 정지.
- Throughput: 폴링 간 `bytes_in/out` 델타 / 경과시간 → kbps.
- 라이브 탭: WS 스트림 연속. 탭 전환 시 비활성 탭 스트림 정리(`close`)·재개.

### 7.3 에러 처리

- `/api/state` 401 → 로그인 화면 리다이렉트(쿠키 만료/무효).
- state fetch 실패(네트워크) → 상단 "relay 연결 끊김" 배너, **마지막 값 유지**.
- 로그인 실패 → 에러 메시지; rate-limit(429) 시 "잠시 후 시도".
- 스트림 재생 실패 → 기존 WS→HLS 폴백 로직 그대로.

## 8. 테스트

**Go 유닛 (`dashboard_test.go`)**
- 쿠키 서명/검증 라운드트립, 만료 거부, 변조 거부.
- `ConstantTimeCompare` 로그인 (정답/오답).
- `RELAY_DASHBOARD_TOKEN` 미설정 → `/dashboard*` 404.
- 로그인 brute-force 제한 동작.
- `/dashboard/api/state` JSON 형태 + 인증 게이트(쿠키 없으면 401).
- `Hub.WatcherList()`, `survWSHub.ClientCount()`, `fragMuxer.Codec()` 헬퍼.

**수동**
- 토큰 설정 후 `/dashboard` 로그인 → 상태 패널이 2초마다 갱신.
- 라이브 탭에서 전 채널 재생(WS 배지 없이도 정상 재생).
- 잘못된 비번/만료 쿠키 → 로그인 화면.

## 9. 보안 메모

- 대시보드는 admin 게이트되지만, 그것이 여는 라이브 스트림(`/surv/ws/`·`/surv/`)은
  현행대로 무인증이다(기존 동작 유지). 같은 오리진에서 재생하므로 대시보드 사용엔
  문제없으나, "스트림 자체 인증"은 후속 과제로 남긴다.
- `RELAY_DASHBOARD_TOKEN`은 `RELAY_PUBLISHER_TOKEN`/watcher PIN과 **독립**이다.
- 쿠키 키를 토큰에서 파생하므로 토큰 교체 시 기존 세션 자동 무효화.

## 10. 후속(별도 스펙)

녹화/타임라인 → 객체·모션 감지/이벤트 → 카메라·DVR 설정 편집. 각 단계는 이
대시보드 셸 위에 탭/뷰로 증분 추가.
