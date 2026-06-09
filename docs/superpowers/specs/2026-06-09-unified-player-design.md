# Phase 2 — Unified Player (통합 플레이어) Design

**Status:** Approved (brainstorm) — 2026-06-09
**Goal:** 라이브 그리드 셀을 클릭하면 UniFi-Protect 식 풀블리드 통합 플레이어로 진입한다. 라이브 영상 + 우측 세로 타임라인을 스크롤해 라이브↔녹화를 매끄럽게 스크럽하고, 이벤트(사람/차량/모션)를 타임라인에서 바로 짚어 재생한다.

---

## Scope

**In scope (Phase 2 본체):**
- 라이브 셀 클릭 → 풀블리드 단일-카메라 통합 플레이어.
- `<video>` 1개를 LIVE(기존 WS/HLS) ↔ REC(녹화 세그먼트 시크) 두 모드로 전환.
- 우측 세로 타임라인 레일: 얇게 상시 + 우측 가장자리 호버 시 확장. 줌 가능. 휠 스크롤 = 시간 이동(과거↔지금, 연속).
- 타임라인에 녹화 커버리지 음영 + **이벤트 아이콘**(사람/차량/모션, 필터칩과 동일 색·아이콘 언어).
- 스크럽 미리보기: 사전저장 이벤트 썸네일(즉시) + settle 시 비디오 프레임(기존 `rec-thumb` 재사용).
- 신규 relay API: 범위 타임라인(`/api/rec-timeline`) **하나뿐**. 나머지는 기존 API 재사용.

**Out of scope (별도 후속 프로젝트):**
- **스프라이트 썸네일 스트립(버터 스크럽 엔진) + `FrameSource` 추상화 + `/api/rec-sprite`** — 바로 다음 증분. Phase 2 본체는 비디오 시크 + 이벤트 썸네일로 먼저 동작시키고, 만져보며 밀도/감을 정한다. (이 추상화를 Phase 2 본체에 미리 넣지 않는다 — 쓰는 데가 없으면 YAGNI.)
- **GPU(NVDEC/NVENC) 가속 + H.265 카메라 전환 + 서버 트랜스코드** — 디스크/터널 대역폭 절반의 큰 이득이 있으나 GPU 변환 파이프라인과 한 묶음이라 별도 프로젝트. 스프라이트 증분의 `FrameSource` 인터페이스로 무변경 드롭인.
- **멀티캠 동기 스크럽** — Phase 2는 단일 카메라(클릭한 셀)만.
- **이벤트 카드 → 통합 플레이어 seek 연결** — follow-up(작음). 이벤트 탭 썸네일 그리드 자체는 그대로 둔다.
- **날짜 프리셋(이번주/지난주/이번달)** — 범위 API가 생기면 쉬워지는 follow-up.

---

## Background / Current State

- 라이브 재생(viewer/대시보드): `playWS(video, wsUrl, onFail)` = MediaSource + WebSocket fMP4, **avc1(H.264)만** 지원(`codecFromInit`이 `avcC` 박스만 탐지). 폴백 `playHLS` = Safari/iOS 네이티브 HLS. h265 채널은 HLS 경로로만.
- 녹화: relay가 RTSP→HLS를 **passthrough**로 세그먼트 MP4 저장(`recSegment{Name,Start,Dur,Size}`). 트랜스코드 없음.
- 기존 녹화/이벤트 API:
  - `/api/rec?stream[&day]` → days 또는 segments.
  - `/api/rec-file?stream&name` → 세그먼트 MP4(Range 지원, finalized는 immutable 캐시).
  - `/api/rec-thumb?stream&t` → JPEG(사전저장 evthumb 우선 → ffmpeg 폴백).
  - `/api/rec-events?stream&day` → 한 day 이벤트 마커. `/api/rec-events-list?agent&day` → agent 전체 집계(클러스터링 적용).
- 현재 라이브 셀 확대는 `openModal(id)`(같은 라이브 플레이어를 모달에 부착). 이벤트 카드 클릭은 `openEventModal`(작은 클립 모달).
- 배포: relay는 master push → `ghcr.io/...:latest` 자동 배포. 대시보드 자산은 `//go:embed dashboard_assets`.

핵심 제약: relay는 Cloudflare 터널(ops.hotelpos.net) 너머라 **체감 지연의 주범은 터널 RTT/대역폭**이지 서버 연산이 아니다. 빠릿함은 캐싱/프리페치 전략이 만든다.

---

## Architecture & Component Boundaries

### Relay (Go)

- **`/api/rec-timeline?stream&start&end`** *(이번 단계 유일한 신규 relay 코드)* — 임의 시각 범위의 녹화 커버리지 + 이벤트 union.
- 기존 `/api/rec-file`, `/api/rec-thumb`, `/api/rec` 재사용. 새 인터페이스/추상화 없음.

### Dashboard (vanilla JS, embedded)

잘 격리된 3개 모듈(파일 또는 app.js 내 섹션):
1. **player-core** — `<video>` 모드전환(LIVE↔REC), 세그먼트 로드/시크/이어붙임.
2. **timeline-rail** — 레일 렌더(눈금/라벨/커버리지/이벤트 아이콘/지금/커서), 호버 확장, 휠 이동, 줌, 스크럽.
3. **player-data** — `rec-timeline`/`rec-thumb` fetch + 캐시.

UI는 데이터 계약(`rec-timeline`/`rec-file`/`rec-thumb`)에만 의존. 미래 프레임 백엔드(스프라이트/GPU)는 이 경계 밖에서 교체.

---

## Relay API Detail

### `/api/rec-timeline?stream=<path>&start=<unixSec>&end=<unixSec>`

응답:
```json
{
  "segments": [ {"start": 1780900000, "dur": 600}, ... ],
  "events":   [ {"start": 1780901234, "end": 1780901260, "kind": "person"}, ... ]
}
```
- `segments`: `Recorder.segmentsForExport(stream, start, end)` 재사용(이미 범위 지원). 커버리지/공백 판정에 사용.
- `events`: `[start,end]`에 걸친 day들을 돌며 `eventStore.eventsForDay` union → 범위로 clip → **기존 `clusterEventItems` 클러스터링 적용**.
- 캐시: 범위가 완전히 과거(finalized)면 `private, max-age=60`+, today(라이브 엣지) 포함이면 `no-store`.
- Admin-gated(`authedDashboard`). `h.events`/`h.rec` 없으면 409.

### `/api/rec-sprite` — *이번 스펙 범위 밖(다음 증분)*

스크럽 프리뷰는 Phase 2 본체에선 `rec-thumb`(evthumb) + 비디오 프레임으로 충분. 버터 스크럽용 스프라이트 스트립 생성기 + `FrameSource` 추상화 + `/api/rec-sprite`는 다음 증분에서. 여기 적지 않는다(별 스펙).

---

## Dashboard: Unified Player

### 진입점
- 라이브 셀 클릭 → `openPlayer(stream, {mode:'live'})`. 기존 `openModal`(라이브 확대) 대체.
- (follow-up) 이벤트 카드 → `openPlayer(stream, {mode:'rec', t})`.

### DOM
풀블리드 오버레이 `#uplayer`: `<video>` + 우측 세로 레일 + 상단 라벨/LIVE 뱃지 + 닫기(Esc/바깥클릭).

### `<video>` 2모드
- **LIVE**: 기존 `playWS`(h264 MSE) → `playHLS` 폴백 재사용.
- **REC**: `video.src = /api/rec-file?stream&name=<seg>`, `currentTime = Tc - seg.start`. 세그먼트 끝 근처(`timeupdate`)에서 다음 연속 세그먼트 preload·교체로 연속 재생.

### 우측 레일
- 상태: 가시 윈도우 `[t0,t1]`, 줌 `pxPerSec`.
- 렌더:
  - 눈금 + 라벨(줌에 맞춘 nice interval).
  - **녹화 커버리지 음영**(녹화 구간 vs 공백).
  - **이벤트 마크 = kind 아이콘**: 평소 얇은 레일에선 작은 컬러 점(사람=파랑/차량=보라/모션=노랑), 호버 확장 시 **`REC_KIND_ICONS` SVG 아이콘 + 시각 라벨**. 필터칩과 동일 색·아이콘.
  - "지금" 앵커, 커서선 + 시각.
- 평소 ≈8px(커버리지+이벤트 점+지금) → 우측 가장자리 호버 시 ≈128px 확장(라벨+스크럽).

### 인터랙션
- **휠** = 시간 이동(과거↔지금). 미래는 지금에서 클램프; 지금 엣지 닿으면 LIVE.
- **줌** = `Ctrl+휠` / `±` 버튼 / 핀치 → `pxPerSec` 변경 → 재렌더 + 타임라인 refetch(디바운스 ~150ms).
- **호버/드래그** = 커서가 포인터 따라 `Tc` → 미리보기(이벤트 근처면 evthumb 즉시 / 그 외 settle 시 비디오 프레임).
- **놓기/클릭 @Tc** = `Tc`가 지금 창이면 LIVE, 아니면 REC 시크.

### 순수 함수(테스트 대상, UI 독립)
`timeToY(t,t0,t1,railH)`, `yToTime(y,...)`, `niceTickInterval(spanSec)`, `clampWindow(t0,t1,now)`.

---

## State Machine & Data Flow

**상태:** `LIVE` · `REC` · `LOADING` · `GAP`

**전이**
- `open(live)` → **LIVE**: WS/HLS attach, `t1=지금`, 커서 지금 고정, 레일 자동 스크롤.
- LIVE —과거 스크럽→ **REC**: WS 닫기 → `Tc` 포함 세그먼트 해석 → `rec-file` 로드·시크·재생. 세그먼트 없으면 **GAP**(최근접 구간 스냅).
- **REC 재생 중**: 커서 = `seg.start + video.currentTime`, 레일이 커서 따라감. 세그 끝 근처 → 다음 연속 세그 preload·교체. 다음이 지금/라이브 엣지면 → **REC→LIVE**.
- REC —지금 스크럽→ LIVE. `open(rec,t)` → **REC@t**.

**데이터 흐름:** 윈도우 변경(디바운스 ~150ms) → `rec-timeline(stream,t0,t1)` fetch → 커버리지 + 이벤트 아이콘 렌더. 프리뷰 썸네일 lazy.

**캐싱:** rec-timeline finalized immutable · rec-file 세그먼트 immutable(재시크 즉각).

---

## Error Handling / Edge Cases

- **활성(녹화중) 세그먼트**: moov 미완 → 최신 ~1세그 rec-file 시크 불가. 지금 근처는 LIVE 우선; 그 구간 스크럽 시 best-effort(HLS 라이브 엣지)/"실시간 구간" 표시.
- **공백**(DVR 끊김/미녹화): 커버리지 빈칸, 커서 진입 시 "녹화 없음" + 스냅.
- **h265 녹화 재생(비-Safari)**: 디코드 안 됨 → 알려진 한계. h264 환경이라 현재 무관, 추후 h265+변환 프로젝트가 해결.
- **WS 실패** → HLS 폴백(기존).
- **터널 지연**: immutable 캐시 + 디바운스 + coarse-first.

---

## Testing

- **Go 유닛**: `rec-timeline` 범위 union(날짜 경계 가로지르기) + 클러스터링 적용 검증.
- **JS 유닛**: `timeToY`/`yToTime`/`niceTickInterval`/`clampWindow` 순수함수 + 모드전환 리듀서.
- **수동(실제 DVR)**: 스크럽 라이브↔녹화, 세그먼트 경계 연속성, 줌 밀도, 이벤트 아이콘, 공백 처리, 멀티데이.

---

## Build / Deploy Notes

- relay: `cd relay && PATH=/opt/homebrew/bin:$PATH go build ./...`; `go test -race ./...`; gofmt.
- 대시보드 자산 임베드: `//go:embed dashboard_assets`. 스모크는 `/dashboard`로(자산 index.html 직접 X).
- relay master push → `:latest` 자동 배포. 버전은 git tag ldflags 주입.

---

## Sequencing (후속 정리)

1. **Phase 2 본체(이 스펙)**: 통합 플레이어 + LIVE↔REC + 줌 타임라인(이벤트 아이콘) + 비디오시크/evthumb 프리뷰 + `rec-timeline`.
2. **스프라이트 스트립 증분**: 백그라운드 생성기 + `/api/rec-sprite` → 버터 스크럽.
3. **이벤트 카드 → 플레이어 seek**(작은 follow-up), 날짜 프리셋.
4. **별도 프로젝트**: H.265 카메라 전환 + GPU NVENC 온디맨드 변환(+ HEVC-가능 클라 passthrough 하이브리드).
