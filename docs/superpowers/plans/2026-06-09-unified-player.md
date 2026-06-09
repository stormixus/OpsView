# Phase 2 — Unified Player Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 라이브 그리드 셀을 클릭하면 풀블리드 통합 플레이어로 들어가, 라이브 영상 + 우측 세로 줌-타임라인(녹화 커버리지 + 사람/차량/모션 이벤트 아이콘)을 휠로 스크럽해 라이브↔녹화를 매끄럽게 오간다.

**Architecture:** relay에 범위 단위 타임라인 API(`/api/rec-timeline`) 하나를 추가(기존 `segmentsForExport` + `eventsForDay` + `clusterEventItems` 재사용). 대시보드는 임베드 vanilla JS로 풀블리드 오버레이 `#uplayer`를 만들고, `<video>` 하나를 LIVE(기존 WS/HLS) ↔ REC(녹화 세그먼트 시크) 두 모드로 운용한다. 시간↔픽셀 매핑 등 순수 로직은 `timeline_math.js`로 분리해 node로 단위테스트한다.

**Tech Stack:** Go(relay, 표준 lib + 기존 헬퍼), 임베드 vanilla JS(빌드 없음, `//go:embed dashboard_assets`), node `--test`(JS 순수함수), ffmpeg(기존 rec-thumb 경로).

---

## Spec

승인된 설계: `docs/superpowers/specs/2026-06-09-unified-player-design.md`. 범위 밖(이 플랜에서 구현하지 않음): 스프라이트 썸네일 스트립/`FrameSource`/`/api/rec-sprite`, GPU·H.265 트랜스코드, 멀티캠 동기, 이벤트 카드→플레이어 연결, 날짜 프리셋.

## Conventions

- relay 빌드: `cd relay && PATH=/opt/homebrew/bin:$PATH go build ./...`
- relay 테스트: `cd relay && PATH=/opt/homebrew/bin:$PATH go test ./...` (race: `go test -race ./...`)
- gofmt: `PATH=/opt/homebrew/bin:$PATH gofmt -w <file>`
- JS 순수함수 테스트: `node --test relay/jstest/`
- JS 문법 체크: `node --check relay/dashboard_assets/<file>.js`
- 대시보드 스모크(런타임): `go build -o /tmp/opsrelay . && (RELAY_DASHBOARD_TOKEN=test RELAY_PUBLISHER_TOKEN=p /tmp/opsrelay & ) && sleep 1.5 && curl -s localhost:8080/dashboard | grep ...` (끝나면 `pkill -f opsrelay`)
- 커밋 메시지 끝에: `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`

## File Structure

- **Create** `relay/rectimeline.go` — `/api/rec-timeline` 핸들러 + `timelineFor` 조립 메서드 + `daysInRange` 헬퍼. (events.go/recordings.go를 건드리지 않고 한 책임으로 격리)
- **Create** `relay/rectimeline_test.go` — `daysInRange` + `timelineFor` 테스트.
- **Modify** `relay/dashboard.go` — `/dashboard/api/rec-timeline` 라우트 등록.
- **Create** `relay/dashboard_assets/timeline_math.js` — 순수함수(`timeToY`/`yToTime`/`niceTickInterval`/`clampWindow`/`firstTickTime`/`segmentAt`), 브라우저 전역 + node export 겸용.
- **Create** `relay/jstest/timeline_math.test.js` — node `--test`(임베드 자산 밖에 둬서 바이너리에 안 실림).
- **Modify** `relay/dashboard_assets/index.html` — `timeline_math.js`를 `app.js`보다 먼저 로드; 풀블리드 `#uplayer` 오버레이 마크업 추가.
- **Modify** `relay/dashboard_assets/app.js` — 통합 플레이어 모듈(셸/LIVE/REC/레일/데이터); 라이브 셀 클릭을 `openModal`→`openPlayer`로 전환; `REC_KIND_ICONS` 재사용.
- **Modify** `relay/dashboard_assets/style.css` — 플레이어 + 레일 스타일.

> **테스트 가능성 주의:** Task 1(Go)·Task 2(JS 순수함수)는 완전한 TDD. Task 3~7은 DOM/`<video>`/실제 DVR가 필요해 자동 단위테스트가 비현실적 → 로직을 최대한 Task 2의 순수함수로 밀어넣고, 통합 동작은 각 태스크 끝의 **수동 검증 체크리스트**로 확인한다. 이는 의도된 설계이지 placeholder가 아니다.

---

## Task 1: relay `/api/rec-timeline` (범위 커버리지 + 이벤트 union)

**Files:**
- Create: `relay/rectimeline.go`
- Create: `relay/rectimeline_test.go`
- Modify: `relay/dashboard.go` (라우트 등록, 현재 rec 계열 라우트는 `dashboard.go:440-445`)

응답 형태:
```json
{ "segments": [{"start":1780900000,"dur":600}], "events": [{"start":1780901234,"end":1780901260,"kind":"person"}] }
```

- [ ] **Step 1: `daysInRange` 실패 테스트 작성**

`relay/rectimeline_test.go` 생성:
```go
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDaysInRange(t *testing.T) {
	// 2026-06-07 23:00 local 부터 2026-06-09 01:00 local → 3일.
	a, _ := time.ParseInLocation("20060102_150405", "20260607_230000", time.Local)
	b, _ := time.ParseInLocation("20060102_150405", "20260609_010000", time.Local)
	got := daysInRange(a.Unix(), b.Unix())
	want := []string{"20260607", "20260608", "20260609"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	// 같은 날 → 1개
	if d := daysInRange(a.Unix(), a.Unix()+60); len(d) != 1 || d[0] != "20260607" {
		t.Fatalf("single day: %v", d)
	}
	// end<=start → 빈 슬라이스
	if d := daysInRange(b.Unix(), a.Unix()); len(d) != 0 {
		t.Fatalf("reversed: %v", d)
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -run TestDaysInRange ./...`
Expected: FAIL — `undefined: daysInRange`

- [ ] **Step 3: `relay/rectimeline.go` 생성(헬퍼 + 타입)**

```go
package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// tlSegment is one recorded span on the unified-player timeline (unix seconds).
type tlSegment struct {
	Start int64 `json:"start"`
	Dur   int   `json:"dur"`
}

// tlEvent is one (clustered) event mark on the timeline (unix seconds).
type tlEvent struct {
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	Kind  string `json:"kind"`
}

type recTimeline struct {
	Segments []tlSegment `json:"segments"`
	Events   []tlEvent   `json:"events"`
}

// daysInRange returns every YYYYMMDD (local) from start..end inclusive, oldest
// first. Empty when end<=start. Used to union per-day event files across a
// timeline window that can span multiple days.
func daysInRange(startSec, endSec int64) []string {
	if endSec <= startSec {
		return nil
	}
	loc := time.Local
	d := time.Unix(startSec, 0).In(loc)
	day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	last := time.Unix(endSec, 0).In(loc)
	var out []string
	for !day.After(last) {
		out = append(out, day.Format("20060102"))
		day = day.AddDate(0, 0, 1)
	}
	return out
}
```

- [ ] **Step 4: `daysInRange` 통과 확인**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -run TestDaysInRange ./...`
Expected: PASS

- [ ] **Step 5: `timelineFor` 실패 테스트 추가**

`relay/rectimeline_test.go`에 추가:
```go
func TestTimelineFor(t *testing.T) {
	dir := t.TempDir()
	rec := &Recorder{dir: dir, segSecs: 300}
	stream := "agentx/dvr0_ch1"
	sd := filepath.Join(dir, "agentx", "dvr0_ch1")
	os.MkdirAll(sd, 0o755)
	for _, name := range []string{"20260607_120000.mp4", "20260607_120500.mp4"} {
		os.WriteFile(filepath.Join(sd, name), []byte("x"), 0o644)
	}
	base, _ := time.ParseInLocation("20060102_150405", "20260607_120000", time.Local)
	start := base.Unix()

	ev := newEventStore(dir)
	// two motion edges 30s apart (within the 90s cooldown) -> cluster to one mark
	ev.add("agentx/dvr0_ch1", "motion", true, (start+60)*1000)
	ev.add("agentx/dvr0_ch1", "motion", false, (start+70)*1000)
	ev.add("agentx/dvr0_ch1", "motion", true, (start+95)*1000)
	ev.add("agentx/dvr0_ch1", "motion", false, (start+100)*1000)

	h := &Hub{rec: rec, events: ev}
	tl := h.timelineFor(stream, start, start+600)

	if len(tl.Segments) != 2 {
		t.Fatalf("segments: %+v", tl.Segments)
	}
	if len(tl.Events) != 1 {
		t.Fatalf("expected clustered to 1 event, got %+v", tl.Events)
	}
	if tl.Events[0].Kind != "motion" {
		t.Fatalf("kind: %+v", tl.Events[0])
	}
}
```

- [ ] **Step 6: 테스트 실패 확인**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -run TestTimelineFor ./...`
Expected: FAIL — `h.timelineFor undefined`

- [ ] **Step 7: `timelineFor` + 핸들러 구현(rectimeline.go에 추가)**

`relay/rectimeline.go`에 추가:
```go
// timelineFor assembles the recording coverage + clustered events for a stream
// over [start,end] unix seconds. Reuses segmentsForExport (range segments) and
// eventsForDay (per-day events), then applies the same clustering the event grid
// uses so one channel's flapping doesn't spray dozens of marks.
func (h *Hub) timelineFor(stream string, start, end int64) recTimeline {
	out := recTimeline{Segments: []tlSegment{}, Events: []tlEvent{}}
	if h.rec != nil {
		for _, s := range h.rec.segmentsForExport(stream, start, end) {
			out.Segments = append(out.Segments, tlSegment{Start: s.Start, Dur: s.Dur})
		}
	}
	if h.events != nil {
		// gather intervals across every day the window spans, clip to [start,end]
		var items []recEventItem
		for _, day := range daysInRange(start, end) {
			for _, iv := range h.events.eventsForDay(stream, day) {
				if iv.Start < end && iv.End > start {
					items = append(items, recEventItem{Stream: stream, Kind: iv.Kind, Start: iv.Start, End: iv.End})
				}
			}
		}
		for _, it := range clusterEventItems(items) {
			out.Events = append(out.Events, tlEvent{Start: it.Start, End: it.End, Kind: it.Kind})
		}
		sort.Slice(out.Events, func(i, j int) bool { return out.Events[i].Start < out.Events[j].Start })
	}
	return out
}

// HandleDashboardRecTimeline serves recording coverage + events for a stream over
// a unix-second range. Admin-gated. ?stream=<path>&start=<sec>&end=<sec>.
func (h *Hub) HandleDashboardRecTimeline(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.rec == nil {
		http.Error(w, "recording disabled", http.StatusConflict)
		return
	}
	q := r.URL.Query()
	stream := q.Get("stream")
	start, _ := strconv.ParseInt(q.Get("start"), 10, 64)
	end, _ := strconv.ParseInt(q.Get("end"), 10, 64)
	if stream == "" || end <= start {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	tl := h.timelineFor(stream, start, end)
	w.Header().Set("Content-Type", "application/json")
	// the window touches "now" only when end is within the last segment length of
	// real time; otherwise it's finalized history and safe to cache briefly.
	if end >= time.Now().Unix()-int64(h.recSegSeconds()) {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=60")
	}
	json.NewEncoder(w).Encode(tl)
}
```

- [ ] **Step 8: `recSegSeconds` 헬퍼 추가(없으면)**

`relay/rectimeline.go`에 추가(레코더의 nominal segment 길이를 안전하게 읽기):
```go
// recSegSeconds returns the recorder's nominal segment length in seconds, used to
// decide whether a timeline window touches the still-recording live edge.
func (h *Hub) recSegSeconds() int {
	if h.rec != nil && h.rec.segSecs > 0 {
		return h.rec.segSecs
	}
	return recSegSeconds
}
```
> 참고: `recSegSeconds`(상수)와 `r.segSecs`(필드)는 `recordings.go`에 이미 존재. 상수명이 충돌하면 이 메서드는 `Hub.recSegSeconds`라 메서드/상수 네임스페이스가 달라 충돌 없음. 빌드로 확인.

- [ ] **Step 9: 테스트 통과 확인**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -run 'TestDaysInRange|TestTimelineFor' ./...`
Expected: PASS

- [ ] **Step 10: 라우트 등록**

`relay/dashboard.go`의 rec 계열 라우트 블록(현재 `dashboard.go:440-445`)에 한 줄 추가:
```go
	mux.HandleFunc("/dashboard/api/rec-timeline", h.HandleDashboardRecTimeline)
```

- [ ] **Step 11: 전체 빌드 + 레이스 테스트 + gofmt**

Run:
```
cd relay && PATH=/opt/homebrew/bin:$PATH gofmt -w rectimeline.go rectimeline_test.go && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -race ./...
```
Expected: 빌드 OK, 전체 테스트 PASS

- [ ] **Step 12: 커밋**

```bash
git add relay/rectimeline.go relay/rectimeline_test.go relay/dashboard.go
git commit -m "feat(relay): /api/rec-timeline — range coverage + clustered events"
```

---

## Task 2: `timeline_math.js` 순수함수 + node 테스트

**Files:**
- Create: `relay/dashboard_assets/timeline_math.js`
- Create: `relay/jstest/timeline_math.test.js`

레일은 **위=과거, 아래=지금**. 즉 윈도우 `[t0,t1]`에서 `t0`(오래된)이 y=0(위), `t1`(최신)이 y=railH(아래).

- [ ] **Step 1: 실패 테스트 작성**

`relay/jstest/timeline_math.test.js` 생성:
```js
const test = require('node:test');
const assert = require('node:assert');
const M = require('../dashboard_assets/timeline_math.js');

test('timeToY maps t0->0 (top) and t1->railH (bottom)', () => {
  assert.strictEqual(M.timeToY(100, 100, 200, 600), 0);
  assert.strictEqual(M.timeToY(200, 100, 200, 600), 600);
  assert.strictEqual(M.timeToY(150, 100, 200, 600), 300);
});

test('yToTime is the inverse of timeToY', () => {
  const t0 = 1780900000, t1 = t0 + 7200, railH = 640;
  for (const t of [t0, t0 + 1000, t0 + 3600, t1]) {
    const y = M.timeToY(t, t0, t1, railH);
    assert.ok(Math.abs(M.yToTime(y, t0, t1, railH) - t) < 0.5);
  }
});

test('niceTickInterval picks a round interval near the target count', () => {
  assert.strictEqual(M.niceTickInterval(7200, 6), 1800);   // 2h -> 30m
  assert.strictEqual(M.niceTickInterval(600, 6), 120);     // 10m -> 2m
  assert.strictEqual(M.niceTickInterval(86400, 6), 21600); // 1d -> 6h
});

test('clampWindow shifts a future window back so t1==now, keeping span', () => {
  const now = 1780900000;
  const r = M.clampWindow(now - 100, now + 50, now);
  assert.strictEqual(r.t1, now);
  assert.strictEqual(r.t0, now - 150);
  // already-past window is unchanged
  const p = M.clampWindow(now - 200, now - 50, now);
  assert.strictEqual(p.t0, now - 200);
  assert.strictEqual(p.t1, now - 50);
});

test('firstTickTime returns the first interval-aligned time >= t0', () => {
  assert.strictEqual(M.firstTickTime(1000, 300), 1200);
  assert.strictEqual(M.firstTickTime(1200, 300), 1200);
});

test('segmentAt finds the segment covering t, else -1', () => {
  const segs = [{ start: 1000, dur: 300 }, { start: 1300, dur: 300 }];
  assert.strictEqual(M.segmentAt(segs, 1100), 0);
  assert.strictEqual(M.segmentAt(segs, 1300), 1);
  assert.strictEqual(M.segmentAt(segs, 1700), -1); // gap/after
  assert.strictEqual(M.segmentAt(segs, 500), -1);  // before
});
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `node --test relay/jstest/`
Expected: FAIL — `Cannot find module '../dashboard_assets/timeline_math.js'`

- [ ] **Step 3: `timeline_math.js` 구현**

`relay/dashboard_assets/timeline_math.js` 생성:
```js
/* Pure timeline math for the unified player. No DOM. Loaded in the browser as a
   plain script (functions become window globals) and required by node for tests
   (module.exports at the bottom). Rail orientation: top = past (t0), bottom = now (t1). */
(function (root) {
  // "nice" tick intervals in seconds: 1s..1d.
  var NICE = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 21600, 43200, 86400];

  function timeToY(t, t0, t1, railH) {
    if (t1 === t0) return 0;
    return (t - t0) / (t1 - t0) * railH;
  }
  function yToTime(y, t0, t1, railH) {
    if (railH === 0) return t0;
    return t0 + (y / railH) * (t1 - t0);
  }
  function niceTickInterval(spanSec, targetTicks) {
    targetTicks = targetTicks || 6;
    var ideal = spanSec / targetTicks;
    for (var i = 0; i < NICE.length; i++) { if (NICE[i] >= ideal) return NICE[i]; }
    return NICE[NICE.length - 1];
  }
  function clampWindow(t0, t1, now) {
    if (t1 > now) { var span = t1 - t0; return { t0: now - span, t1: now }; }
    return { t0: t0, t1: t1 };
  }
  function firstTickTime(t0, intervalSec) {
    return Math.ceil(t0 / intervalSec) * intervalSec;
  }
  function segmentAt(segs, t) {
    for (var i = 0; i < segs.length; i++) {
      if (t >= segs[i].start && t < segs[i].start + segs[i].dur) return i;
    }
    return -1;
  }

  var api = { timeToY: timeToY, yToTime: yToTime, niceTickInterval: niceTickInterval,
              clampWindow: clampWindow, firstTickTime: firstTickTime, segmentAt: segmentAt };
  if (typeof module !== 'undefined' && module.exports) { module.exports = api; }
  else { for (var k in api) root[k] = api[k]; } // browser: expose as globals
})(typeof window !== 'undefined' ? window : this);
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `node --test relay/jstest/`
Expected: PASS (6 tests)

- [ ] **Step 5: 브라우저 문법 체크**

Run: `node --check relay/dashboard_assets/timeline_math.js`
Expected: 출력 없음(OK)

- [ ] **Step 6: 커밋**

```bash
git add relay/dashboard_assets/timeline_math.js relay/jstest/timeline_math.test.js
git commit -m "feat(dashboard): timeline_math pure fns (time<->px, ticks, clamp) + node tests"
```

---

## Task 3: 플레이어 셸 + LIVE 모드 (라이브 셀 클릭 진입)

**Files:**
- Modify: `relay/dashboard_assets/index.html` (`timeline_math.js` 로드 + `#uplayer` 마크업)
- Modify: `relay/dashboard_assets/app.js` (`openPlayer`/`closePlayer`/LIVE attach; 라이브 셀 클릭 라우팅)
- Modify: `relay/dashboard_assets/style.css` (오버레이/비디오 기본 스타일)

기존 라이브 확대는 `app.js`의 `openModal(id)`(라이브 셀 클릭 핸들러는 `app.js:542` 부근, `$('#grid').addEventListener('click', ... openModal(c.dataset.id))`). 이걸 통합 플레이어로 바꾼다. (이벤트 탭 카드 클릭은 이번 범위 밖이므로 그대로 둔다.)

- [ ] **Step 1: `timeline_math.js`를 `app.js`보다 먼저 로드**

`relay/dashboard_assets/index.html` 맨 아래 스크립트 태그(현재 `<script src="/dashboard/assets/app.js"></script>`) 바로 위에 추가:
```html
<script src="/dashboard/assets/timeline_math.js"></script>
```

- [ ] **Step 2: `#uplayer` 오버레이 마크업 추가**

`index.html`에서 기존 `<div class="modal" id="modal">...</div>` 블록 바로 다음에 추가:
```html
<!-- unified player (라이브 셀 클릭 → 풀블리드 라이브+타임라인 스크럽) -->
<div class="uplayer" id="uplayer" aria-hidden="true">
  <button class="uplayer-close" id="uplayerClose" title="닫기 (Esc)"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M18 6L6 18M6 6l12 12"/></svg></button>
  <div class="up-stage" id="upStage">
    <video class="up-video" id="upVideo" muted autoplay playsinline></video>
    <div class="up-toplabel"><span id="upTitle">—</span><span class="up-sub" id="upSub"></span></div>
    <div class="up-livebadge" id="upLiveBadge"><span class="dot live"></span>LIVE</div>
    <div class="up-state" id="upState"></div>
    <div class="up-preview" id="upPreview" hidden><img id="upPreviewImg" alt=""><span id="upPreviewTime" class="up-previewtime"></span></div>
  </div>
  <div class="up-rail" id="upRail" aria-label="타임라인 (휠=시간이동, 우측 호버=확장)"></div>
</div>
```

- [ ] **Step 3: 플레이어 상태 + open/close 셸 구현 (LIVE만)**

`relay/dashboard_assets/app.js` 끝부분(이벤트 그리드 섹션 뒤, RELAY/CONN 섹션 앞 등 적당한 위치)에 새 섹션 추가. 우선 LIVE만:
```js
/* ============================================================ UNIFIED PLAYER (통합 플레이어) */
var up = {
  open:false, stream:null, name:'', sub:'', codec:'h264', path:'',
  mode:'live',            // 'live' | 'rec' | 'gap'
  player:null,            // live WS/HLS handle (has .close)
  // timeline window (unix seconds): t0=top(past), t1=bottom(now/cursor edge)
  t0:0, t1:0, pxPerSec:0.25, // default zoom: 4s per px
  segs:[], events:[],     // from rec-timeline
  cursorT:0,
};
var upEl=$('#uplayer'), upVideo=$('#upVideo');

function openPlayer(stream, opts){
  opts=opts||{};
  if(selected===null) return; var a=agentById(selected); if(!a) return;
  var s=a.streams.filter(function(x){return x.id===stream || x.path===stream;})[0]; if(!s) return;
  up.open=true; up.stream=s.id; up.path=s.path; up.codec=s.codec; up.name=s.name; up.sub=a.name;
  up.mode='live';
  $('#upTitle').textContent=s.name; $('#upSub').textContent=a.name+' · CH'+s.ch;
  upEl.classList.add('show'); upEl.setAttribute('aria-hidden','false');
  upStartLive();
}
function upStartLive(){
  upStopVideo();
  up.mode='live'; upEl.classList.remove('up-rec'); $('#upLiveBadge').style.display='';
  $('#upState').textContent='';
  if(!up.path){ return; }
  var wsUrl=(location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/surv/ws/'+up.path;
  var hlsUrl=location.origin+'/surv/'+up.path+'/index.m3u8';
  up.player=playWS(upVideo, wsUrl, function(){ playHLS(upVideo, hlsUrl); });
}
function upStopVideo(){
  if(up.player){ try{ up.player.close&&up.player.close(); }catch(e){} up.player=null; }
  try{ upVideo.pause(); upVideo.removeAttribute('src'); upVideo.load(); }catch(e){}
}
function closePlayer(){
  if(!up.open) return;
  up.open=false; upStopVideo();
  upEl.classList.remove('show'); upEl.setAttribute('aria-hidden','true');
  if(up._raf){ cancelAnimationFrame(up._raf); up._raf=null; }
}
$('#uplayerClose').addEventListener('click', closePlayer);
upEl.addEventListener('click', function(e){ if(e.target===upEl) closePlayer(); });
document.addEventListener('keydown', function(e){ if(e.key==='Escape' && up.open) closePlayer(); });
```

- [ ] **Step 4: 라이브 셀 클릭을 통합 플레이어로 라우팅**

`app.js`의 라이브 그리드 클릭 핸들러(현재 `$('#grid').addEventListener('click', function(e){ ... openModal(c.dataset.id); })`, 대략 `app.js:542`)에서 `openModal(c.dataset.id)`를 `openPlayer(c.dataset.id)`로 교체:
```js
$('#grid').addEventListener('click', function(e){ if(liveEditing) return; var c=e.target.closest('.cell'); if(c) openPlayer(c.dataset.id); });
```
> `openModal`/`#modal`은 Ops 스냅샷·watcher 상세 등 다른 곳에서 계속 쓰이므로 **삭제하지 않는다**. 라이브 셀 진입만 새 플레이어로 옮긴다.

- [ ] **Step 5: 오버레이 기본 스타일**

`relay/dashboard_assets/style.css` 끝에 추가:
```css
/* ============================ unified player (통합 플레이어) ============================ */
.uplayer{position:fixed; inset:0; z-index:80; display:none; background:rgba(2,4,7,.92); backdrop-filter:blur(4px);}
.uplayer.show{display:flex;}
.up-stage{position:relative; flex:1; min-width:0; display:flex; align-items:center; justify-content:center; background:#05070a;}
.up-video{width:100%; height:100%; object-fit:contain; background:#000;}
.up-toplabel{position:absolute; left:18px; top:14px; display:flex; flex-direction:column; gap:2px; text-shadow:0 1px 6px #000; pointer-events:none;}
.up-toplabel #upTitle{color:#e6edf3; font-size:15px; font-weight:700;}
.up-toplabel .up-sub{color:#9aa6b4; font-size:12px;}
.up-livebadge{position:absolute; left:18px; bottom:16px; display:flex; align-items:center; gap:6px; color:#fff; font-size:12px; font-weight:700; background:rgba(0,0,0,.45); padding:5px 11px; border-radius:999px;}
.uplayer.up-rec .up-livebadge{display:none;}
.up-state{position:absolute; left:50%; bottom:16px; transform:translateX(-50%); color:#cdd6e0; font-size:12.5px; font-weight:600; background:rgba(0,0,0,.5); padding:4px 10px; border-radius:8px;}
.up-state:empty{display:none;}
.uplayer-close{position:absolute; right:16px; top:14px; z-index:2; width:40px; height:40px; border:0; border-radius:10px; background:rgba(17,24,32,.7); color:#e6edf3; cursor:pointer; display:flex; align-items:center; justify-content:center;}
.uplayer-close svg{width:22px; height:22px;}
.uplayer-close:hover{background:rgba(40,52,66,.9);}
.up-preview{position:absolute; pointer-events:none; width:160px; background:#0c1118; border:1px solid #2a3442; border-radius:8px; overflow:hidden; box-shadow:0 10px 30px rgba(0,0,0,.6);}
.up-preview img{display:block; width:100%; height:90px; object-fit:cover; background:#05070a;}
.up-previewtime{display:block; text-align:center; font-size:11px; font-weight:700; color:#e6edf3; padding:3px 0; font-family:var(--font-mono);}
/* rail filled in Task 4 */
.up-rail{position:relative; width:14px; background:linear-gradient(90deg,transparent,rgba(8,12,18,.7)); border-left:1px solid rgba(255,255,255,.08); transition:width .14s ease; flex:none;}
.up-rail.expanded{width:132px;}
```

- [ ] **Step 6: 문법 체크 + 런타임 스모크**

Run:
```
node --check relay/dashboard_assets/app.js && node --check relay/dashboard_assets/timeline_math.js
cd relay && PATH=/opt/homebrew/bin:$PATH go build -o /tmp/opsrelay . && (RELAY_DASHBOARD_TOKEN=test RELAY_PUBLISHER_TOKEN=p /tmp/opsrelay >/tmp/up.log 2>&1 &) && sleep 1.5
curl -s localhost:8080/dashboard | grep -o 'id="uplayer"\|timeline_math.js'
curl -s localhost:8080/dashboard/assets/timeline_math.js | grep -o 'niceTickInterval'
pkill -f opsrelay; rm -f /tmp/opsrelay /tmp/up.log
```
Expected: `id="uplayer"`, `timeline_math.js`, `niceTickInterval` 모두 출력.

- [ ] **Step 7: 수동 검증 체크리스트(실제/데모 대시보드)**

확인:
- 라이브 탭에서 활성 셀 클릭 → 풀블리드 오버레이가 뜨고 영상이 재생된다(h264=WS, h265=HLS).
- 우측 상단 X / Esc / 바깥 클릭으로 닫힌다.
- 닫으면 비디오가 정지(소리/네트워크 멈춤)된다.
- Ops 스냅샷 클릭·watcher 상세 등 기존 `#modal` 기능은 그대로 동작.

- [ ] **Step 8: 커밋**

```bash
git add relay/dashboard_assets/index.html relay/dashboard_assets/app.js relay/dashboard_assets/style.css
git commit -m "feat(dashboard): unified player shell + LIVE mode (live cell -> full-bleed)"
```

---

## Task 4: 타임라인 레일 — 데이터 fetch + 커버리지 + 이벤트 아이콘 (얇게/호버 확장)

**Files:**
- Modify: `relay/dashboard_assets/app.js` (rec-timeline fetch + 레일 렌더 + 호버 확장)
- Modify: `relay/dashboard_assets/style.css` (레일 내부 요소)

LIVE 모드에서 윈도우 `[t0,t1]`는 `t1=now`, `t0=now - (railH/pxPerSec)`. 레일은 평소 얇고(커버리지+이벤트 점+지금), 우측 가장자리 호버 시 확장(라벨+아이콘).

- [ ] **Step 1: rec-timeline fetch + 캐시**

`app.js` 통합 플레이어 섹션에 추가:
```js
// fetch coverage+events for the current window (debounced). Cache by stream + a
// coarse window bucket so panning/zoom doesn't refetch on every frame.
up._tlCache={};
up._tlTimer=null;
function upFetchTimeline(){
  if(!up.stream) return;
  var pad=Math.round((up.t1-up.t0)*0.5); // fetch a little beyond the window
  var qs=up.t0-pad, qe=up.t1+pad;
  var key=up.path+'|'+Math.floor(qs/300)+'|'+Math.floor(qe/300);
  if(up._tlCache[key]){ up.segs=up._tlCache[key].segments; up.events=up._tlCache[key].events; upRenderRail(); return; }
  fetch(BASE+'/api/rec-timeline?stream='+encodeURIComponent(up.path)+'&start='+qs+'&end='+qe,{credentials:'same-origin'})
    .then(function(r){ return r.ok? r.json() : {segments:[],events:[]}; })
    .then(function(d){
      up._tlCache[key]=d; up.segs=d.segments||[]; up.events=d.events||[];
      upRenderRail();
    }).catch(function(){ up.segs=[]; up.events=[]; upRenderRail(); });
}
function upScheduleTimeline(){ clearTimeout(up._tlTimer); up._tlTimer=setTimeout(upFetchTimeline,150); }
```

- [ ] **Step 2: 레일 렌더 (순수함수 사용)**

`app.js`에 추가. `REC_KIND_ICONS`/`REC_KIND_NAMES`는 이벤트 탭에서 이미 정의되어 전역으로 재사용:
```js
var upRail=$('#upRail');
function upRailH(){ return upRail.clientHeight || 600; }
// recompute the LIVE window so t1=now and the rail height maps to a time span.
function upSyncLiveWindow(){
  var now=Math.floor(Date.now()/1000);
  var span=Math.round(upRailH()/up.pxPerSec);
  up.t1=now; up.t0=now-span;
}
function upRenderRail(){
  var H=upRailH(), html='';
  // recording coverage shading (recorded vs gap)
  up.segs.forEach(function(s){
    var yTop=timeToY(s.start, up.t0, up.t1, H);
    var yBot=timeToY(s.start+s.dur, up.t0, up.t1, H);
    var top=Math.max(0,Math.min(yTop,yBot)), h=Math.abs(yBot-yTop);
    if(top+h<0||top>H) return;
    html+='<div class="up-cov" style="top:'+top+'px;height:'+h+'px"></div>';
  });
  // tick labels (only when expanded; cheap to always emit, hidden by CSS when thin)
  var interval=niceTickInterval(up.t1-up.t0,6);
  for(var tt=firstTickTime(up.t0,interval); tt<=up.t1; tt+=interval){
    var y=timeToY(tt,up.t0,up.t1,H); if(y<0||y>H) continue;
    html+='<div class="up-tick" style="top:'+y+'px"></div>'+
          '<div class="up-tlabel" style="top:'+y+'px">'+upFmtTick(tt,interval)+'</div>';
  }
  // event marks: thin = colored dot, expanded = icon + time (CSS toggles)
  up.events.forEach(function(ev){
    var y=timeToY(ev.start,up.t0,up.t1,H); if(y<0||y>H) return;
    var k=ev.kind||'motion';
    html+='<button class="up-ev ev-'+escAttr(k)+'" data-t="'+ev.start+'" style="top:'+y+'px" title="'+escAttr((REC_KIND_NAMES[k]||k))+'">'+
            '<span class="up-ev-dot"></span>'+
            '<span class="up-ev-ic">'+(REC_KIND_ICONS[k]||'')+'</span>'+
            '<span class="up-ev-t">'+upClock(ev.start)+'</span>'+
          '</button>';
  });
  // "지금" anchor (only meaningful when window reaches now)
  var nowY=timeToY(Math.floor(Date.now()/1000),up.t0,up.t1,H);
  if(nowY>=0&&nowY<=H){ html+='<div class="up-now" style="top:'+nowY+'px"></div><div class="up-nowlbl" style="top:'+nowY+'px">지금</div>'; }
  // cursor (set during scrub; Task 5/6)
  if(up.mode==='rec'){ var cy=timeToY(up.cursorT,up.t0,up.t1,H); if(cy>=0&&cy<=H){ html+='<div class="up-cursor" style="top:'+cy+'px"></div><div class="up-curlbl" style="top:'+cy+'px">'+upClock(up.cursorT)+'</div>'; } }
  upRail.innerHTML=html;
}
function upFmtTick(t,interval){
  var d=new Date(t*1000);
  if(interval>=86400) return (d.getMonth()+1)+'/'+d.getDate();
  if(interval>=3600) return pad2(d.getHours())+'시';
  return pad2(d.getHours())+':'+pad2(d.getMinutes());
}
function upClock(t){ var d=new Date(t*1000); return pad2(d.getHours())+':'+pad2(d.getMinutes())+':'+pad2(d.getSeconds()); }
```

- [ ] **Step 3: 호버 확장 + LIVE 렌더 루프 시작**

`app.js`에 추가하고, `upStartLive()` 끝에서 루프를 켠다:
```js
upRail.addEventListener('mouseenter', function(){ upRail.classList.add('expanded'); upRenderRail(); });
upRail.addEventListener('mouseleave', function(){ if(up.mode!=='rec') upRail.classList.remove('expanded'); });
// LIVE 모드에서 1초마다 윈도우를 now에 맞춰 재렌더(타임라인이 흐르는 느낌)
function upLiveTick(){
  if(!up.open || up.mode!=='live') return;
  upSyncLiveWindow(); upRenderRail();
  up._liveTimer=setTimeout(upLiveTick,1000);
}
```
그리고 `upStartLive()` 함수 마지막 줄에 추가:
```js
  upSyncLiveWindow(); upScheduleTimeline(); clearTimeout(up._liveTimer); upLiveTick();
```
그리고 `closePlayer()`의 정리에 추가(기존 `if(up._raf)` 줄 근처):
```js
  clearTimeout(up._liveTimer); clearTimeout(up._tlTimer);
```

- [ ] **Step 4: 레일 내부 스타일**

`style.css`의 통합 플레이어 블록에 추가:
```css
.up-cov{position:absolute; right:0; width:100%; background:rgba(55,211,103,.16); border-right:2px solid rgba(55,211,103,.5);}
.up-tick{position:absolute; right:0; width:8px; height:1px; background:rgba(255,255,255,.22);}
.up-tlabel{position:absolute; right:16px; transform:translateY(-50%); font-size:10px; color:#9aa6b4; font-family:var(--font-mono); white-space:nowrap; opacity:0; transition:opacity .12s;}
.up-rail.expanded .up-tlabel{opacity:1;}
.up-now{position:absolute; left:0; right:0; height:2px; background:#37d367; box-shadow:0 0 8px rgba(55,211,103,.7);}
.up-nowlbl{position:absolute; right:16px; transform:translateY(-50%); font-size:9.5px; color:#37d367; font-weight:700; opacity:0; transition:opacity .12s;}
.up-rail.expanded .up-nowlbl{opacity:1;}
/* event mark: dot when thin, icon+time when expanded */
.up-ev{position:absolute; right:0; width:100%; height:0; border:0; background:transparent; padding:0; cursor:pointer; display:flex; align-items:center; justify-content:flex-end; gap:5px; transform:translateY(-50%);}
.up-ev-dot{flex:none; width:7px; height:7px; border-radius:50%; box-shadow:0 0 0 2px rgba(0,0,0,.4);}
.up-ev-ic{display:none; flex:none; width:14px; height:14px;}
.up-ev-ic svg{width:14px; height:14px; display:block;}
.up-ev-t{display:none; font-size:10px; color:#cdd6e0; font-family:var(--font-mono);}
.up-rail.expanded .up-ev{height:16px; padding-right:8px; background:rgba(0,0,0,.0);}
.up-rail.expanded .up-ev-ic{display:inline-flex;}
.up-rail.expanded .up-ev-t{display:inline;}
.up-rail.expanded .up-ev:hover{background:rgba(255,255,255,.06);}
.up-ev.ev-person .up-ev-dot,.up-ev.ev-linecross .up-ev-dot{background:#79b8ff;} .up-ev.ev-person .up-ev-ic{color:#79b8ff;}
.up-ev.ev-vehicle .up-ev-dot{background:#c8a6ff;} .up-ev.ev-vehicle .up-ev-ic{color:#c8a6ff;}
.up-ev.ev-motion .up-ev-dot{background:#e8c25a;} .up-ev.ev-motion .up-ev-ic{color:#e8c25a;}
.up-ev.ev-intrusion .up-ev-dot{background:#f87171;} .up-ev.ev-intrusion .up-ev-ic{color:#f87171;}
.up-cursor{position:absolute; left:0; right:0; height:0; border-top:2px dashed rgba(255,255,255,.9);}
.up-curlbl{position:absolute; right:16px; transform:translateY(-50%); background:#111820; border:1px solid #2a3442; color:#e6edf3; font-size:10px; font-weight:700; padding:2px 5px; border-radius:5px; font-family:var(--font-mono);}
```

- [ ] **Step 5: 문법 체크 + 스모크**

Run:
```
node --check relay/dashboard_assets/app.js
cd relay && PATH=/opt/homebrew/bin:$PATH go build -o /tmp/opsrelay . && (RELAY_DASHBOARD_TOKEN=test RELAY_PUBLISHER_TOKEN=p /tmp/opsrelay >/tmp/up.log 2>&1 &) && sleep 1.5
curl -s localhost:8080/dashboard/assets/app.js | grep -o 'upRenderRail\|rec-timeline\|up-ev-ic'
pkill -f opsrelay; rm -f /tmp/opsrelay /tmp/up.log
```
Expected: `upRenderRail`, `rec-timeline`, `up-ev-ic` 출력.

- [ ] **Step 6: 수동 검증 체크리스트**

확인(녹화/이벤트가 있는 실제 에이전트 필요):
- 플레이어 열면 우측 얇은 레일에 녹화 커버리지(초록 음영)와 이벤트 컬러 점이 보인다.
- 우측 레일에 마우스 올리면 132px로 확장 + 시각 라벨/이벤트 아이콘(사람/차량/모션)/“지금”이 보인다.
- 시간이 흐르면 “지금” 라인과 커버리지가 1초마다 갱신된다.
- 이벤트 점/아이콘 색이 이벤트 탭 필터칩과 동일하다.

- [ ] **Step 7: 커밋**

```bash
git add relay/dashboard_assets/app.js relay/dashboard_assets/style.css
git commit -m "feat(dashboard): player timeline rail — coverage + event icons, hover-expand"
```

---

## Task 5: REC 모드 + LIVE↔REC 상태머신 + 세그먼트 연속 재생

**Files:**
- Modify: `relay/dashboard_assets/app.js`

레일 클릭/이벤트 클릭으로 과거 시각 `Tc`를 고르면 REC로 전환해 해당 세그먼트를 로드·시크·재생한다. 재생이 진행되며 커서가 따라가고, 세그먼트 끝에서 다음 세그먼트로 이어붙인다. 커서가 “지금”에 닿으면 LIVE로 복귀.

- [ ] **Step 1: REC 진입(특정 시각으로 시크)**

`app.js` 통합 플레이어 섹션에 추가:
```js
// enter REC at unix-second time t: find the covering segment and seek into it.
function upSeekTo(t){
  var i=segmentAt(up.segs, t);
  if(i<0){ upGap(t); return; }
  up.mode='rec'; upEl.classList.add('up-rec'); upRail.classList.add('expanded');
  $('#upLiveBadge').style.display='none'; $('#upState').textContent='';
  up.cursorT=t;
  upStopVideo();
  var seg=up.segs[i];
  // rec-timeline segments carry start/dur but not the file name; resolve it via /api/rec day list.
  upResolveSegName(seg.start).then(function(name){
    if(!name){ upGap(t); return; }
    upVideo.src=BASE+'/api/rec-file?stream='+encodeURIComponent(up.path)+'&name='+encodeURIComponent(name);
    upVideo.currentTime=0;
    upVideo.onloadedmetadata=function(){ try{ upVideo.currentTime=Math.max(0,t-seg.start); }catch(e){} upVideo.play().catch(function(){}); };
    up._curSeg={start:seg.start, dur:seg.dur, name:name};
    upStartRecLoop();
  });
}
```
> 주의: `rec-timeline`의 segment에는 파일명이 없다(start/dur만). REC 재생은 파일명이 필요하므로 `/api/rec?stream&day`(이미 존재, `recSegsForDay`)로 그 날의 세그먼트 목록을 받아 start→name을 해석한다.

- [ ] **Step 2: 세그먼트 이름 해석 헬퍼**

`app.js`에 추가(기존 `recSegsForDay(stream,day)` 재사용):
```js
function upDayOf(t){ var d=new Date(t*1000); return ''+d.getFullYear()+pad2(d.getMonth()+1)+pad2(d.getDate()); }
// resolve the segment file name whose start == segStart (via the day's listing).
function upResolveSegName(segStart){
  return recSegsForDay(up.path, upDayOf(segStart)).then(function(list){
    var hit=(list||[]).filter(function(s){return s.start===segStart;})[0];
    return hit? hit.name : null;
  });
}
```

- [ ] **Step 3: REC 재생 루프(커서 추적 + 세그먼트 이어붙임 + LIVE 복귀)**

`app.js`에 추가:
```js
function upStartRecLoop(){
  if(up._raf) cancelAnimationFrame(up._raf);
  function loop(){
    if(!up.open || up.mode!=='rec'){ up._raf=null; return; }
    if(up._curSeg){
      up.cursorT=up._curSeg.start + (upVideo.currentTime||0);
      // reached the live edge? hand back to LIVE.
      if(up.cursorT >= Math.floor(Date.now()/1000)-2){ upStartLive(); return; }
      // near end of this segment -> jump to the next contiguous one.
      if(upVideo.currentTime >= up._curSeg.dur-0.25){
        var next=segmentAt(up.segs, up._curSeg.start+up._curSeg.dur+1);
        if(next>=0){ upSeekTo(up.segs[next].start+0.1); return; }
      }
    }
    upSyncRecWindow(); upRenderRail();
    up._raf=requestAnimationFrame(loop);
  }
  up._raf=requestAnimationFrame(loop);
}
// keep the cursor comfortably in view as REC plays (cursor ~60% down the rail).
function upSyncRecWindow(){
  var H=upRailH(), span=Math.round(H/up.pxPerSec);
  up.t1=up.cursorT + span*0.4; up.t0=up.t1-span;
  var now=Math.floor(Date.now()/1000);
  var c=clampWindow(up.t0,up.t1,now); up.t0=c.t0; up.t1=c.t1;
}
function upGap(t){
  up.mode='gap'; up.cursorT=t; $('#upState').textContent='이 시각 녹화 없음';
  // snap to nearest segment edge if any
  var best=null,bestD=1e15;
  up.segs.forEach(function(s){ [s.start, s.start+s.dur-1].forEach(function(edge){ var d=Math.abs(edge-t); if(d<bestD){bestD=d;best=edge;} }); });
  upSyncRecWindow(); upRenderRail();
  if(best!=null){ setTimeout(function(){ if(up.mode==='gap') upSeekTo(best); }, 700); }
}
```

- [ ] **Step 4: 레일 클릭 → 시크 / “지금” 클릭 → LIVE**

`app.js`에 추가:
```js
upRail.addEventListener('click', function(e){
  var evb=e.target.closest('.up-ev');
  if(evb){ upSeekTo(+evb.dataset.t); return; }
  var H=upRailH(), rect=upRail.getBoundingClientRect();
  var y=e.clientY-rect.top;
  var t=Math.round(yToTime(y, up.t0, up.t1, H));
  var now=Math.floor(Date.now()/1000);
  if(t>=now-2){ upStartLive(); } else { upSeekTo(t); }
});
```

- [ ] **Step 5: 문법 체크 + 스모크**

Run:
```
node --check relay/dashboard_assets/app.js
cd relay && PATH=/opt/homebrew/bin:$PATH go build -o /tmp/opsrelay . && (RELAY_DASHBOARD_TOKEN=test RELAY_PUBLISHER_TOKEN=p /tmp/opsrelay >/tmp/up.log 2>&1 &) && sleep 1.5
curl -s localhost:8080/dashboard/assets/app.js | grep -o 'upSeekTo\|upStartRecLoop\|upResolveSegName'
pkill -f opsrelay; rm -f /tmp/opsrelay /tmp/up.log
```
Expected: 세 토큰 모두 출력.

- [ ] **Step 6: 수동 검증 체크리스트(실제 녹화 필요)**

확인:
- 레일의 과거 지점을 클릭 → 영상이 그 시각 녹화로 점프해 재생되고 LIVE 뱃지가 사라진다.
- 이벤트 아이콘 클릭 → 그 이벤트 시각으로 점프 재생.
- 재생이 세그먼트 경계를 넘어 끊김 없이 다음 세그먼트로 이어진다.
- 재생이 현재(지금)에 닿으면 자동으로 LIVE로 돌아온다.
- 녹화 없는 구간 클릭 → “이 시각 녹화 없음” 후 가장 가까운 구간으로 스냅.

- [ ] **Step 7: 커밋**

```bash
git add relay/dashboard_assets/app.js
git commit -m "feat(dashboard): REC mode + LIVE<->REC state machine + segment continuity"
```

---

## Task 6: 휠 이동 + 줌 + 스크럽 프리뷰 + 마무리

**Files:**
- Modify: `relay/dashboard_assets/app.js`
- Modify: `relay/dashboard_assets/style.css`

- [ ] **Step 1: 휠 = 시간 이동, Ctrl+휠 = 줌**

`app.js`에 추가:
```js
// wheel over the stage/rail: pan time (REC) or step back from LIVE into REC.
// ctrl/⌘+wheel: zoom (change pxPerSec) around the cursor.
function upOnWheel(e){
  if(!up.open) return;
  e.preventDefault();
  if(e.ctrlKey || e.metaKey){
    var factor=e.deltaY>0?1/1.15:1.15;
    up.pxPerSec=Math.max(0.02, Math.min(8, up.pxPerSec*factor)); // 8s/px .. 0.125s/px
    if(up.mode==='live'){ upSyncLiveWindow(); } else { upSyncRecWindow(); }
    upRenderRail(); upScheduleTimeline();
    return;
  }
  // pan: scrolling up (deltaY<0) = into the past.
  var H=upRailH(), span=up.t1-up.t0;
  var dt=(e.deltaY/H)*span;
  var center=(up.mode==='rec')? up.cursorT : Math.floor(Date.now()/1000);
  var newCursor=center+dt;
  var now=Math.floor(Date.now()/1000);
  if(newCursor>=now-2){ if(up.mode!=='live') upStartLive(); return; }
  upSeekTo(Math.round(newCursor));
}
$('#upStage').addEventListener('wheel', upOnWheel, {passive:false});
upRail.addEventListener('wheel', upOnWheel, {passive:false});
```

- [ ] **Step 2: 호버 스크럽 프리뷰(이벤트 근처 evthumb, 그 외 settle 시 비디오 프레임)**

`app.js`에 추가:
```js
var upPrev=$('#upPreview'), upPrevImg=$('#upPreviewImg');
up._prevTimer=null;
function upHoverPreview(e){
  if(!up.open || !upRail.classList.contains('expanded')) return;
  var H=upRailH(), rect=upRail.getBoundingClientRect();
  var y=e.clientY-rect.top;
  var t=Math.round(yToTime(y, up.t0, up.t1, H));
  var now=Math.floor(Date.now()/1000); if(t>=now){ upPrev.hidden=true; return; }
  // position the preview box left of the rail at the pointer
  upPrev.hidden=false;
  upPrev.style.top=Math.max(8,Math.min(H-100, y-50))+'px';
  upPrev.style.right='150px';
  $('#upPreviewTime').textContent=upClock(t);
  // debounce the (network) thumbnail fetch; thumb falls back to placeholder on error
  clearTimeout(up._prevTimer);
  up._prevTimer=setTimeout(function(){
    upPrevImg.onerror=function(){ upPrevImg.onerror=null; upPrevImg.removeAttribute('src'); upPrev.classList.add('na'); };
    upPrevImg.onload=function(){ upPrev.classList.remove('na'); };
    upPrevImg.src=BASE+'/api/rec-thumb?stream='+encodeURIComponent(up.path)+'&t='+t;
  }, 90);
}
upRail.addEventListener('mousemove', upHoverPreview);
upRail.addEventListener('mouseleave', function(){ upPrev.hidden=true; });
```

- [ ] **Step 3: 프리뷰 placeholder 스타일**

`style.css`의 통합 플레이어 블록에 추가:
```css
.up-preview.na img{background:#0a0f15 url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='22' height='22' fill='none' stroke='%23394150' stroke-width='1.6'%3E%3Crect x='3' y='5' width='16' height='12' rx='2'/%3E%3Cpath d='M3 14l4-4 3 3 3-4 6 6'/%3E%3C/svg%3E") center/26px no-repeat;}
```

- [ ] **Step 4: REC→닫기 시 상태 완전 정리 보강**

`closePlayer()` 안에 모드/오버레이 상태 리셋을 추가(기존 함수 본문에 합치기):
```js
  up.mode='live'; upEl.classList.remove('up-rec'); upRail.classList.remove('expanded');
  upPrev.hidden=true; up._curSeg=null; up._tlCache={};
```

- [ ] **Step 5: 문법 체크 + 스모크 + 전체 빌드/테스트**

Run:
```
node --check relay/dashboard_assets/app.js
node --test relay/jstest/
cd relay && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test ./...
cd relay && PATH=/opt/homebrew/bin:$PATH go build -o /tmp/opsrelay . && (RELAY_DASHBOARD_TOKEN=test RELAY_PUBLISHER_TOKEN=p /tmp/opsrelay >/tmp/up.log 2>&1 &) && sleep 1.5
curl -s localhost:8080/dashboard/assets/app.js | grep -o 'upOnWheel\|upHoverPreview'
pkill -f opsrelay; rm -f /tmp/opsrelay /tmp/up.log
```
Expected: JS 테스트 PASS, Go 빌드/테스트 PASS, `upOnWheel`·`upHoverPreview` 출력.

- [ ] **Step 6: 수동 검증 체크리스트(실제 녹화 필요)**

확인:
- 휠 위로 스크롤 → 과거로 들어가며 REC 재생(스크럽), 휠 아래로 → 지금/LIVE로.
- Ctrl(⌘)+휠 → 타임라인 시간 밀도가 줌인/아웃(시→분→일 라벨 변화).
- 확장 레일에서 마우스 올리면 커서 시각의 미리보기 썸네일이 좌측에 뜬다(없으면 placeholder).
- 닫았다 다시 열면 LIVE로 깨끗하게 시작(이전 REC 상태 잔재 없음).
- 줌/팬 중 rec-timeline이 과도하게 재요청되지 않는다(디바운스/캐시 동작).

- [ ] **Step 7: 커밋**

```bash
git add relay/dashboard_assets/app.js relay/dashboard_assets/style.css
git commit -m "feat(dashboard): timeline wheel pan + zoom + hover scrub preview"
```

---

## Task 7: 마감 — 접근성/엣지 점검 + CHANGELOG + 최종 수동 패스

**Files:**
- Modify: `relay/dashboard_assets/app.js` (작은 보강만)
- Modify: `CHANGELOG.md`

- [ ] **Step 1: h265 녹화 재생 한계 안내(비-Safari)**

`upSeekTo`의 `onloadedmetadata` 직전에, 코덱이 h265이고 브라우저가 못 틀 때 안내를 띄운다:
```js
  if(up.codec==='h265' && !upVideo.canPlayType('video/mp4; codecs="hvc1"')){
    $('#upState').textContent='이 브라우저는 H.265 녹화 재생을 지원하지 않습니다 (라이브만 가능)';
  }
```
> h264 환경에선 절대 안 뜸. h265 카메라가 섞였을 때만 정직하게 알리는 가드(설계의 알려진 한계).

- [ ] **Step 2: 키보드 — 닫힘 외 충돌 없음 확인**

Run: `node --check relay/dashboard_assets/app.js`
그리고 코드 점검: 통합 플레이어의 `keydown`은 `Escape`만 처리하고 `up.open`일 때만 동작하므로 기존 모달/드로어 Esc 핸들러와 공존(둘 다 닫힘 시도해도 무해). 추가 작업 없음 확인.

- [ ] **Step 3: 전체 빌드 + 테스트 + gofmt + 임베드 스모크**

Run:
```
cd relay && PATH=/opt/homebrew/bin:$PATH gofmt -l . | grep -v '^store.go$' || true
cd relay && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -race ./...
node --test relay/jstest/
node --check relay/dashboard_assets/app.js && node --check relay/dashboard_assets/timeline_math.js
```
Expected: gofmt 깨끗(기존 store.go 제외), Go race PASS, JS 테스트 PASS, 문법 OK.

- [ ] **Step 4: CHANGELOG 갱신**

`CHANGELOG.md` 최상단에 항목 추가(기존 포맷에 맞춰):
```markdown
## Unreleased
### Added
- 통합 플레이어: 라이브 그리드 셀 클릭 → 풀블리드 플레이어. 우측 줌-타임라인(녹화 커버리지 + 사람/차량/모션 이벤트 아이콘)을 휠로 스크럽해 라이브↔녹화를 매끄럽게 이동, 호버 스크럽 미리보기.
- relay `/api/rec-timeline` — 시각 범위 단위 녹화 커버리지 + 클러스터링된 이벤트.
```

- [ ] **Step 5: 최종 통합 수동 검증(실제 DVR, 멀티데이)**

확인:
- 라이브 셀 클릭 → 통합 플레이어 LIVE.
- 휠/레일클릭/이벤트클릭으로 과거 스크럽 → REC, 세그먼트 경계 연속, 지금 닿으면 LIVE 복귀.
- 줌(시→분→일), 커버리지/이벤트 아이콘 정확, 공백 처리, 미리보기 썸네일.
- 날짜 경계를 넘는 과거(어제)로 스크럽해도 커버리지·이벤트가 보인다(rec-timeline range union).
- 닫기/재오픈 깨끗.

- [ ] **Step 6: 커밋**

```bash
git add relay/dashboard_assets/app.js CHANGELOG.md
git commit -m "feat(dashboard): unified player polish (h265 guard, changelog)"
```

---

## Self-Review (작성자 체크)

- **Spec coverage:** 라이브셀→플레이어(T3) · LIVE↔REC `<video>`(T3/T5) · 우측 줌 타임라인+이벤트 아이콘(T4/T6) · 휠 스크럽(T6) · evthumb/비디오 프레임 프리뷰(T5/T6) · `/api/rec-timeline`(T1) · 순수함수 테스트(T2) · 엣지(활성세그/공백/h265)(T5/T7) — 전부 태스크에 매핑됨. 범위 밖(스프라이트/GPU/h265변환/멀티캠/이벤트카드연결/날짜프리셋)은 의도적으로 제외.
- **Placeholder scan:** 모든 코드 스텝에 실제 코드. "수동 검증"은 DOM/`<video>`/실제 DVR가 필요한 통합 동작에 한정(자동 테스트 불가)하며 placeholder가 아님을 명시.
- **Type consistency:** `up` 상태 객체 키(stream/path/codec/mode/t0/t1/pxPerSec/segs/events/cursorT/_curSeg/_liveTimer/_tlTimer/_raf/_prevTimer/_tlCache)와 함수명(`openPlayer/closePlayer/upStartLive/upStopVideo/upSyncLiveWindow/upSyncRecWindow/upRenderRail/upFetchTimeline/upScheduleTimeline/upSeekTo/upResolveSegName/upStartRecLoop/upGap/upOnWheel/upHoverPreview/upClock/upFmtTick/upDayOf`)이 태스크 간 일관. 순수함수(`timeToY/yToTime/niceTickInterval/clampWindow/firstTickTime/segmentAt`)는 T2에서 정의 후 T4~T6에서 동일 시그니처 사용. relay `tlSegment/tlEvent/recTimeline/timelineFor/daysInRange/HandleDashboardRecTimeline/recSegSeconds`는 T1 내 일관.
