# 채널 메타데이터 싱크 (순서 + 라벨) — Design

- 작성: 2026-06-06
- 대상: OpsView `agent`, `relay`, `proto`, 뷰어(desktop / web / hotelpos5)
- 상태: 승인됨 (구현 대기 — **릴레이 대시보드 M1 이후**)
- 관련: [릴레이 대시보드 스펙](2026-06-06-relay-dashboard-design.md)

## 1. 배경 & 목표

뷰어 사용자는 대부분 사장님(연령대 ↑)이라 채널 순서 지정·라벨링 같은 수동 설정을
귀찮아한다. 목표: **순서·라벨을 설치 시점에 한 번(에이전트 또는 릴레이 대시보드)
정하면 전 뷰어에 자동 반영**되어, 사장님은 뷰어에서 아무것도 안 만져도 처음부터
제대로 보이게 한다.

배관은 이미 상당 부분 존재한다:
- `proto.ChannelInfo`에 `Name` + `Order` 필드가 있고, 에이전트가 survConfig로 릴레이→뷰어에 전송 중.
- 에이전트 SQLite에 `display_order` + `name` 저장, `ORDER BY display_order`로 읽음.
- 라벨은 DVR 채널명(ISAPI)이 자동으로 흘러온다(예: `101/201/301` 방번호).

비어 있는 것: (1) 에이전트/대시보드의 편집 UI, (2) 양방향 싱크, (3) 뷰어 override 규칙.

## 2. 모델 (3-레이어 우선순위)

| 레이어 | 편집 | 싱크 규칙 |
|---|---|---|
| **에이전트 ↔ 릴레이(대시보드)** | 둘 다 순서·라벨 편집 가능 | 항상 서로 일치 (single source of truth = 에이전트 DB) |
| **뷰어** | 자기 스타일로 편집 가능 | `customized=false`면 기본값 따라감 / `customized=true`면 로컬 유지 |

- 뷰어엔 "기본값으로 초기화(재싱크)" 액션 → `customized`를 false로 되돌려 다시 따라가게.

## 3. 아키텍처 — 단일 진실원천 = 에이전트 DB

split-brain 방지를 위해 저장소를 둘로 가르지 않는다. **순서·라벨의 canonical
저장은 에이전트 DB 한 곳.** 릴레이 대시보드 편집은 *직접 저장하지 않고* 에이전트로
내려보내 적용시킨다.

```
[대시보드에서 편집]
   └─(HTTP) relay /dashboard/api/channel-meta
         └─(기존 publisher WS, relay→agent) MsgSurvMetaUpdate
               └─ agent: DB UPDATE display_order/name
                     └─ agent: survConfig 재전송 (agent→relay)
                           └─ relay 캐시 갱신 + 전 watcher에 push
                                 └─ 뷰어: customized=false면 반영
```

- **에이전트 직접 편집**(에이전트 웹UI)도 같은 종착점: DB UPDATE → survConfig 재전송.
- 그래서 "에이전트에서 편집"이든 "대시보드에서 편집"이든 결과 경로가 동일 →
  양쪽 항상 일치.
- **제약**: 대시보드 편집은 에이전트가 릴레이에 연결돼 있을 때만 내려간다.
  오프라인이면 대시보드 편집 UI를 비활성화(또는 "에이전트 오프라인" 안내).

## 4. 컴포넌트별 변경

### 4.1 proto
- 신규 메시지 **`MsgSurvMetaUpdate` (relay→agent)**: OVP 봉투 재사용.
  payload JSON `{ dvr_id, channels:[ {ch_num, name?, order?} ] }` (부분 편집 허용).
- `ChannelInfo.Name`/`Order`는 그대로 사용(추가 필드 없음).

### 4.2 agent
- 기존 publisher WS 읽기 루프에 `MsgSurvMetaUpdate` 핸들러 추가 →
  `surveillance.go`에서 `UPDATE channels SET display_order=?, name=? ...` 후
  survConfig 재빌드·재전송.
- 에이전트 웹UI(`web_ui.go`)에 **채널 드래그드롭 재정렬 + 인라인 rename** 추가
  (이미 `display_order` 컬럼·`ReorderChannels` 류 로직 존재 → UI만 연결).

### 4.3 relay
- 대시보드에 `POST /dashboard/api/channel-meta` (admin 게이트) 추가 →
  `MsgSurvMetaUpdate`를 publisher WS로 송신. 에이전트 미연결이면 409/503.
- 릴레이는 순서·라벨을 **저장하지 않는다**(에이전트가 재전송하는 survConfig만 캐시).

### 4.4 뷰어 (desktop / web / hotelpos5)
- 채널/DVR 메타에 **`customized` 플래그**(로컬 저장: 데스크톱 SQLite,
  web/hotelpos5는 localStorage/Redis).
- 렌더 순서·라벨 결정:
  - `customized=false` → survConfig의 `order`/`name` 사용 (config 갱신 때마다 재반영)
  - `customized=true` → 로컬 순서·라벨 유지, 들어오는 기본값 무시
- 뷰어에서 드래그/rename 하면 해당 DVR(또는 채널)의 `customized=true`로 표시.
- **"기본값으로 초기화"** 버튼 → `customized=false` + 로컬 오버라이드 삭제 → 재싱크.

## 5. 엣지 케이스

- **신규 채널 발견**: 에이전트가 새 채널을 붙이면 기본 order(말미)로 survConfig에 포함.
  `customized=true` 뷰어에서도 신규 채널은 끝에 추가(기존 커스텀 순서는 유지).
- **채널 삭제/비활성**: survConfig에서 빠지면 뷰어 로컬 오버라이드에서도 정리.
- **에이전트 오프라인 중 대시보드 편집**: 차단(409) + 안내. 큐잉은 하지 않음(YAGNI).
- **이름 출처**: 기본 라벨은 DVR 채널명. 에이전트/대시보드 rename은 그 위에 덮어씀.

## 6. 비목표

- 릴레이 측 독립 메타 저장소(split-brain) — 명시적 배제.
- 오프라인 편집 큐잉/병합.
- 뷰어별 세분화된 권한/역할.

## 7. 시퀀싱

1. **릴레이 대시보드 M1** (상태 + 라이브, 읽기전용) — 먼저.
2. **본 스펙** (채널 메타 싱크): proto 메시지 → agent 핸들러+웹UI → relay 대시보드
   편집 엔드포인트 → 뷰어 customized 플래그. 대시보드의 "편집" 탭/패널로 자연스럽게 합류.

## 8. 테스트 (개요)

- agent: `MsgSurvMetaUpdate` 적용 → DB 반영 + survConfig 재전송 단위 테스트.
- relay: 대시보드 편집 → publisher WS 송신, 미연결 시 409.
- 뷰어: `customized` 분기(false=싱크, true=유지), 초기화 버튼이 재싱크 트리거.
