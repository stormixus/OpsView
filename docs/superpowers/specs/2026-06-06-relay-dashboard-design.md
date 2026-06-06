# OpsView Relay Dashboard — Design (M1: 멀티 에이전트 상태 + 라이브)

- 작성: 2026-06-06 (개정: 멀티 에이전트 반영)
- 대상: OpsView `relay` (Go 서버)
- 상태: 승인됨 (구현 대기)
- **선행 의존**: [relay 멀티 에이전트 스펙](2026-06-06-relay-multi-agent-design.md) — 먼저 구현되어야 함
- 디자인: `uploads/.../3fe58123-OpsView_Relay_Dashboard_1.html` (claude.ai/design, 에이전트 그룹화 버전)

## 1. 배경 & 목표

relay는 여러 곳의 **에이전트(지점)** 의 Ops 화면 공유(`/watch`)와 CCTV(HLS `/surv/`,
fMP4-over-WS `/surv/ws/`)를 중계한다. 운영자가 relay 상태를 한눈에 볼 방법이 없다.

이 스펙은 첫 마일스톤으로 relay가 직접 서빙하는 **웹 대시보드**를 만든다. 핵심은
**에이전트(지점)별 그룹화**:

- **전체(개요)**: relay 전역 요약 + 에이전트 카드 그리드
- **에이전트 드릴인**: 선택한 지점의 상태 탭(publisher/watcher/스트림/메트릭) + 라이브 탭(그 지점 CCTV 그리드)

녹화/타임라인, 객체·모션 감지, 카메라 설정 편집은 범위 밖(각각 별도 스펙).

## 2. 비목표 (Non-goals)

- 녹화/재생/타임라인, 객체/모션 감지 (별도 스펙)
- 카메라/DVR 설정 편집 (조회만; 편집은 [채널 메타 싱크](2026-06-06-channel-metadata-sync-design.md))
- 대시보드에서의 멀티 에이전트 **백엔드** 구현 — 그건 선행 스펙 소관. 본 스펙은 그
  위에 얹히는 **읽기 전용 대시보드**.

## 3. 아키텍처

- relay가 `/dashboard`에 vanilla 정적 SPA를 **Go `embed`** 로 내장 서빙(빌드 스텝 없음).
  프론트는 claude.ai/design 산출 HTML/CSS를 이식하고 `app.js`로 데이터 배선.
- admin 인증된 **단일 집계 JSON**(`/dashboard/api/state`)이 **전 에이전트** 상태를
  배열로 제공 → 클라가 2초 폴링.
- 라이브 그리드는 기존 `/surv/ws/...`·`/surv/.../index.m3u8`를 재사용(검증된
  sequence-mode MSE 플레이어 — `web/viewer.js`에서 이식).

### 3.1 신규/변경 파일

- `relay/dashboard.go` — 로그인/세션 미들웨어, `/dashboard/*` 핸들러, state 집계
- `relay/dashboard_assets/{index.html,app.js,style.css}` — `//go:embed` (디자인 이식)
- `relay/dashboard_session.go`, `relay/dashboard_state.go`, `relay/dashboard_test.go`
- `relay/main.go` — 라우트 등록(토큰 설정 시에만)
- `relay/version.go` — `relayVersion`
- 멀티 에이전트 스펙에서 만들어진 `agentSession`에 상태 getter 추가
  (세션별 watcher 목록/메트릭, `survWSHub.ClientCount()`, `fragMuxer.Codec()`)

## 4. 라우트

| 메서드 | 경로 | 인증 | 내용 |
|---|---|---|---|
| GET  | `/dashboard`            | 쿠키 없으면 로그인 화면 | SPA 셸 |
| GET  | `/dashboard/assets/*`   | 없음 | 정적 파일 |
| POST | `/dashboard/api/login`  | 없음 | 비번 확인 → 서명 쿠키 |
| POST | `/dashboard/api/logout` | 쿠키 | 쿠키 삭제 |
| GET  | `/dashboard/api/state`  | 쿠키 필수 | 전 에이전트 집계 JSON |

## 5. 인증 (변경 없음)

- **`RELAY_DASHBOARD_TOKEN` 설정 시에만 대시보드 활성화** (미설정=라우트 미등록=404, fail-safe).
- 로그인: `POST /dashboard/api/login {password}` → `ConstantTimeCompare`.
- 세션 쿠키(stateless): `opsview_dash = base64url(exp) + "." + hex(HMAC_SHA256(payload, key))`,
  `key = SHA256(DashboardToken)`, 기본 만료 12h. `HttpOnly`/`SameSite=Strict`/`Path=/dashboard`/TLS면 `Secure`.
- Brute-force: 기존 per-IP `pinLimiter` 재사용.
- `RELAY_DASHBOARD_TOKEN`은 에이전트 publisherToken/watcherPIN과 **독립**(운영자 전용).

## 6. `/dashboard/api/state` 데이터 모델 (멀티 에이전트)

```json
{
  "relay": {
    "version": "0.3.8",
    "uptime_sec": 12345,
    "agents_online": 2,
    "agents_total": 3,
    "watchers_total": 7,
    "streams_total": 40,
    "bytes_in": 123456789,
    "bytes_out": 987654321
  },
  "agents": [
    {
      "id": "gangnam",
      "name": "강남점",
      "connected": true,
      "since": "2026-06-06T09:10:40Z",
      "last_publish_at": "2026-06-06T09:24:43Z",
      "pin_set": true,
      "bytes_in": 1234567,
      "bytes_out": 7654321,
      "watchers": [
        { "id": 1, "ip": "192.168.0.57", "since": "2026-06-06T09:10:40Z" }
      ],
      "streams": [
        { "id": "dvr4_ch1", "name": "101", "active": true, "codec": "h264",
          "ws_watchers": 2, "path": "gangnam/dvr4_ch1" }
      ],
      "dvrs": [
        { "id": 4, "name": "HIKVISION DVR1", "channels": 16 }
      ]
    }
  ]
}
```

- **`relay.*`**: 전역 합계. `bytes_in/out`(전역 누적)·각 agent `bytes_in/out`은
  폴링 델타로 throughput(kbps) 계산.
- **`agents[]`**: 에이전트별 상태. 오프라인 에이전트도 포함(`connected:false`,
  마지막 접속 `since`).
- **`stream.path`**: 플레이어가 칠 경로 세그먼트. 네임스페이스 규칙(신규=
  `{agentID}/dvr.._ch..`, 레거시 default=`dvr.._ch..`)을 **서버가 결정**해 내려주므로
  프론트는 분기 없이 `wss://host/surv/ws/{path}` · `/surv/{path}/index.m3u8` 만 만든다.
- `codec`: `h264|h265` (init 전이면 빈 문자열). `version`: ldflags 주입, 기본 `dev`.

## 7. 프론트엔드 (디자인 이식 + 배선)

claude.ai/design 산출 HTML/CSS를 이식하고 `app.js`로 state에 배선한다. 디자인의
element id를 그대로 사용:

### 7.1 화면 구조 (3 뷰)

- **로그인** (`#login`): 비밀번호 1개 → 입장. 오류/429 메시지(`#loginMsg`).
- **사이드바** (`#sidebar`): 연결된 에이전트 목록(상태 점 + 지점명 + 미니 통계).
  맨 위 "전체(개요)". 선택 시 메인 뷰 전환. 토글 버튼 `#menuBtn`.
- **개요 뷰** (`#overview-view`): 전역 stat 카드(`#ovAgents`/`#ovAgentsTotal`,
  `#ovWatchers`, `#ovStreams`, `#ovDown`/`#ovUp`, `#ovUptime`) + **에이전트 카드
  그리드**(`#agentGrid`, 카드=지점: 상태/시청자/스트림/마지막프레임/스냅샷 썸네일,
  클릭→드릴인). 0개면 `#noAgents` 빈 상태.
- **에이전트 뷰** (`#agent-view`): 헤더(`#agName`, 온/오프 배지 `#agBadge`,
  `#agSince`, 뒤로 `#backBtn`) + 탭(`status`/`live`):
  - **상태 탭**(`#status-pane`): stat 카드(`#pubState`/`#pubDot`/`#pubAgo`,
    `#watchNum`, `#streamNum`/`#streamTotal`, `#tpDown`/`#tpUp`) + Publisher 패널
    (`#pubPanel`: Ops 스냅샷 + `#pinBadge` + `#pubDvr`/`#pubBytes`/`#pubUptime`) +
    Watchers 테이블(`#watchBody`) + Streams 테이블(`#streamBody`).
  - **라이브 탭**(`#live-pane`): 그 지점 CCTV 그리드.

### 7.2 데이터 배선

- 진입 시 `GET /dashboard/api/state`로 세션 확인(401→로그인). 성공 시 개요 렌더.
- **2초 폴링**(`document.hidden`이면 정지)으로 사이드바/개요/현재 에이전트 뷰 갱신.
- 개요 throughput = `relay.bytes_in/out` 델타. 에이전트 throughput = 그 agent의
  `bytes_in/out` 델타.
- Ops 스냅샷: 에이전트 범위 스냅샷 엔드포인트(멀티 에이전트 스펙에서 `/api/snapshot`이
  에이전트 스코프로 확장됨)를 `<img>` src로. 미지원 시 자리표시.

### 7.3 라이브 그리드 (에이전트 범위)

- 선택 에이전트의 `streams`만 사용. 각 셀: `<video>` + 라벨.
- `wsUrl = (https?wss:ws)://host/surv/ws/{stream.path}`, `hlsUrl = origin + /surv/{stream.path}/index.m3u8`.
- 플레이어: WS+MSE(`sb.mode='sequence'`, append마다 `play()`) 우선 → 실패/iOS면 HLS.
- 에이전트 전환·탭 이탈 시 스트림 `close` 정리.

### 7.4 에러 처리

- `/api/state` 401 → 로그인 화면.
- fetch 실패 → 상단 배너(`#banner`) "relay 연결 끊김", 마지막 값 유지.
- 로그인 실패→메시지, 429→"잠시 후 시도".
- 오프라인 에이전트: 사이드바·카드 흐리게 + 마지막 접속 시간. 드릴인 시 라이브 비활성 안내.

## 8. 테스트

**Go 유닛**
- 쿠키 서명/검증(라운드트립/만료/변조), `ConstantTimeCompare` 로그인, 토큰 미설정 404,
  brute-force 제한, `/api/state` 인증 게이트(401).
- **state 집계**: 다중 agentSession에서 `agents[]` 형태/합계(`agents_online`,
  `watchers_total`, `streams_total`) 정확성. 오프라인 에이전트 포함. `stream.path`
  네임스페이스(신규 prefix vs 레거시 평면).
- 세션별 getter(watcher 목록/메트릭, WS count, codec).

**수동**
- 2+ 에이전트 연결 상태에서 로그인 → 개요에 에이전트 카드들·전역 합계 갱신.
- 카드 클릭→드릴인→상태/라이브 탭, 라이브 재생(Chrome/Safari).
- 한 에이전트만 있으면 사이드바 간소화. 0개면 빈 상태.

## 9. 시퀀싱

1. [relay 멀티 에이전트](2026-06-06-relay-multi-agent-design.md) — **선행**.
2. **본 대시보드** — 그 위 읽기전용 그룹화 UI.
3. [채널 메타 싱크](2026-06-06-channel-metadata-sync-design.md) — 대시보드에 편집 탭으로 합류.

## 10. 후속(별도 스펙)

녹화/타임라인 → 객체·모션 감지/이벤트 → 카메라·DVR 설정 편집. 대시보드 셸 위에 증분 추가.
