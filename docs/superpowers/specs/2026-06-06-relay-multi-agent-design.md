# Relay 멀티 에이전트(멀티테넌트) — Design

- 작성: 2026-06-06
- 대상: OpsView `relay` (핵심), `agent`/`proto`/뷰어 (연동)
- 상태: 승인됨 (구현 대기 — 대시보드보다 **선행**되어야 함)
- 관련: [릴레이 대시보드 스펙](2026-06-06-relay-dashboard-design.md), [채널 메타 싱크](2026-06-06-channel-metadata-sync-design.md)

## 1. 배경 & 목표

현재 relay는 **publisher 1개**만 받는다(`Hub.publisher *websocket.Conn`,
`publisherPIN` 단일, `survProxy` 단일 스트림셋, `frameBuf` 단일). 즉 relay 하나당
매장 1곳.

목표: **relay 하나에 여러 곳의 에이전트(지점)가 동시에 연결**되고, 각 에이전트가
자기만의 Ops 화면 + CCTV + 시청자 + PIN을 갖는다. 대시보드/뷰어는 에이전트(지점)
단위로 그룹화해 보여준다.

## 2. 결정 사항 (확정)

- **멀티테넌트**: 지점마다 다른 소유주. A지점 시청자가 B지점을 보면 안 됨 →
  **에이전트별 독립 토큰/PIN, 엄격 격리**.
- **하위 호환**: 기존 단일 에이전트 뷰어들은 **수정 없이 계속 동작**해야 함.

## 3. 핵심 아키텍처

### 3.1 에이전트 레지스트리 (relay 설정)

relay가 허용 에이전트를 **레지스트리**로 안다 (env 또는 설정 파일). 각 항목:

```
agentID  | name(지점명) | publisherToken | watcherPIN
gangnam  | 강남점        | <token-A>      | 481922
hongdae  | 홍대점        | <token-B>      | 773045
```

- `publisherToken`: 그 에이전트만 아는 비밀 → /publish 인증 (테넌트 위장 방지).
- `watcherPIN`: 그 에이전트 시청자용 PIN. **전 에이전트에서 유일**해야 함(relay가
  설정 로드 시 중복 검증). PIN이 곧 **테넌트 선택자 + 인증**.
- 기존 `RELAY_PUBLISHER_TOKEN`(단일)은 "agentID 없는 레거시 에이전트 1개" 항목으로
  매핑 → 하위 호환.

### 3.2 Hub 리팩터 — agentSession 맵

`Hub`의 단일 publisher/PIN/survProxy/frameBuf/metrics를 **`map[string]*agentSession`**
(키=agentID)로 이동:

```
type agentSession struct {
    id, name     string
    publisher    *websocket.Conn          // 그 지점 Ops
    watchers     map[*Watcher]struct{}
    survConfig   []byte
    survProxy    *SurvProxy               // 그 지점 CCTV 스트림셋
    frameBuf     *FrameBuffer             // 그 지점 Ops 스냅샷
    metrics      ...                      // bytesIn/out, watcherCount, lastPublishAt
    connectedAt  time.Time
}
```

Hub은 레지스트리 + `sessions map[string]*agentSession` + 전역 limiter를 보유.

### 3.3 인증 & 라우팅

- **/publish**: 에이전트가 HELLO에 `agent_id`를 싣고 AUTH에 `token` 제시 →
  relay가 `registry[agent_id].publisherToken`과 constant-time 비교 → 해당
  agentSession의 publisher 슬롯 점유. (레거시: agent_id 없으면 default 에이전트.)
- **/watch**: 시청자가 **PIN만** 제시(현행과 동일) → relay가 그 PIN을 가진
  agentSession을 찾아 라우팅. **기존 뷰어 무수정 동작** + PIN으로 테넌트 격리.
  per-IP brute-force limiter 유지.
- **스냅샷/survConfig**: 그 시청자가 붙은 agentSession 범위에서만 제공.

### 3.4 CCTV 스트림 네임스페이스 (격리 + 호환의 절충)

문제: `/surv/`·`/surv/ws/`는 **무인증·stateless**라 요청만 보고 어느 테넌트인지
모른다. 두 에이전트가 같은 `dvr1_ch1`을 가지면 충돌.

해결(절충):
- **신규 에이전트**: 스트림을 **agentID로 네임스페이스** → `/surv/{agentID}/dvr1_ch1/...`,
  `/surv/ws/{agentID}/dvr1_ch1`. agentSession의 survConfig는 agentID를 포함해
  내려가고, **에이전트 인식 뷰어**(대시보드, 갱신된 hotelpos5/뷰어)가 그 경로를 만든다.
- **레거시(default) 에이전트**: 기존 평면 경로(`/surv/dvr1_ch1/...`) 유지 →
  **기존 뷰어 무수정 동작**.
- relay 내부 스트림 키는 항상 `{agentID}::dvr1_ch1`로 유일하게 관리하고, default
  에이전트만 라우팅 시 빈 prefix로 노출.

> ⚠️ **격리 한계 명시**: 무인증 `/surv/`는 스트림ID를 알면 누구나 가져올 수 있다(현행
> 그대로). 멀티테넌트 완전 격리를 원하면 `/surv/`에 **단기 토큰/세션 인증**을 추가해야
> 하며, 이는 **후속 과제**로 분리한다(레거시 호환 깨지므로 신중히).

## 4. proto / 메시지 변경

- HELLO(publisher)에 `agent_id` 필드 추가(없으면 default).
- survConfig 전송 시 `agent_id`(또는 스트림 prefix) 포함 → 에이전트 인식 뷰어가 사용.
- 기존 메시지 타입/봉투는 유지.

## 5. 컴포넌트별 영향

- **relay**: Hub 대수술(단일→세션 맵), /publish·/watch 라우팅, surv 네임스페이스,
  레지스트리 로딩/검증. (가장 큰 작업)
- **agent**: HELLO에 `agent_id`+토큰 전송(설정에 지점 ID/토큰 추가). 단일 배포는
  미설정 시 레거시로 동작.
- **proto**: HELLO `agent_id`, survConfig prefix.
- **뷰어**:
  - 기존(레거시 에이전트 대상): **무변경**.
  - 대시보드/신규: 에이전트 목록을 받아 그룹화(별도 대시보드 스펙 개정).
  - hotelpos5: 멀티 지점 보려면 agentID 인식하도록 갱신(원하는 시점에).

## 6. 대시보드 스펙에의 영향 (개정 필요)

[대시보드 스펙](2026-06-06-relay-dashboard-design.md)·계획은 단일 publisher 전제로
작성됨. 멀티 에이전트 확정에 따라:
- `/dashboard/api/state`가 **`agents: [...]` 배열**(에이전트별 relay/watchers/streams/dvrs)로 바뀜.
- UI: 좌측 에이전트 사이드바 + 전체 개요 + 에이전트 드릴인 (claude.ai/design 수정 프롬프트 반영).
- → 대시보드 스펙/계획은 **이 멀티 에이전트 스펙 구현 이후** 개정·진행.

## 7. 비목표

- `/surv/` 무인증 스트림에 대한 완전 격리(단기 토큰) — 후속 과제로 분리.
- 에이전트 동적 자가 등록(레지스트리 없이 아무 에이전트나 합류) — 명시적 배제(위장 방지).
- 지점 간 watcher 공유/크로스 뷰.

## 8. 리스크 / 고려

- **확장**: N 에이전트 × M 스트림 → relay의 RTSP→HLS/WS 부하 N배. 동시 스트림
  상한/리소스 모니터링 필요(대시보드가 가시화).
- **PIN 유일성**: 레지스트리 로드 시 watcherPIN 중복이면 기동 거부(fail-closed).
- **레거시 default 에이전트**: 1개만 default 가능. 둘 이상이 agent_id 없이 붙으면 거부.
- **마이그레이션**: 현 단일 배포 = default 에이전트로 자동 흡수, 무중단.

## 9. 시퀀싱 (전체 로드맵 갱신)

1. **본 스펙 (relay 멀티 에이전트)** — 선행. Hub 세션화 + 라우팅 + 레지스트리.
2. **대시보드 스펙 개정** (agents 배열 + 그룹화 UI) → 구현.
3. **채널 메타 싱크** — 에이전트(테넌트)별로 동작하도록 자연 확장.

## 10. 테스트 (개요)

- relay: 레지스트리 로딩/PIN중복 거부, /publish 토큰 검증(에이전트별), /watch PIN→
  올바른 세션 라우팅 + 타 세션 격리, surv 네임스페이스 라우팅(신규 prefix vs 레거시 평면).
- 하위호환: agent_id 없는 publish가 default 세션에 붙고 기존 watcher 흐름 그대로 동작.
- 격리: 에이전트 A PIN으로는 B의 Ops/스냅샷/스트림 목록에 접근 불가.
