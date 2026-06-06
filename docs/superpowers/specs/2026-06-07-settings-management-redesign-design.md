# 설정/지점관리 재구성 — 설계 (Settings & Management Redesign)

**Date:** 2026-06-07
**Status:** Approved (design)
**Scope:** Relay operator dashboard (`relay/dashboard_assets/`) — frontend only. No Go/server changes.

---

## 1. Problem

The operator dashboard crams everything into one ⚙ slide-in drawer: display
prefs (테마/액센트/정보밀도) **and** operational config (지점 관리, 장애 알림, 비밀번호).
The drawer is a single narrow scrolling sheet. 지점 관리 in particular is a thin
list of rows with a cramped add-form — inadequate for the primary audience
(나이 많은 모텔/호텔 점주, few branches each) who need a clear, legible place to
see each 지점's health and manage it.

**Goal:** Split personal display prefs (stay in a slim quick-drawer) from
operational management (move to a dedicated full-page **관리** screen with a
card-grid 지점 view).

---

## 2. Decisions (locked)

1. **Structure = A안:** Keep a lightweight ⚙ quick-drawer for *display prefs only*
   (테마 / 액센트 / 정보밀도, localStorage-backed). Move 지점 / 알림 / 보안 into a
   dedicated full-page **관리** view with a left sub-tab nav.
2. **지점 목록 = 카드 그리드:** each 지점 is a large card (not a table row). Cards
   chosen over a table because the audience manages few branches and benefits
   from large, legible, touch-friendly targets.

---

## 3. Architecture

### 3.1 Top-level views

Today `#content` holds two sibling `.view.main` sections toggled by `.active`:
`#overview-view` and `#agent-view`. Routing is History-API based (`app.js`
`BASE='/dashboard'`, `pathFor` / `go` / `routeFromPath` / `navTo`):

| Route | View |
|---|---|
| `/dashboard` | `#overview-view` |
| `/dashboard/agent/<id>` | `#agent-view` |

**Add a third top-level view** `#manage-view` and route:

| Route | View |
|---|---|
| `/dashboard/manage` | `#manage-view` (new) |

`navTo()`/`routeFromPath()` gain a `manage` branch. `manage` is a distinct
target (not an agent id); when active, the agent sidebar selection clears
(`selected` stays whatever it was for back-nav, but no `.agent-item` shows active —
see 3.4). The relay already serves `index.html` for any `/dashboard/*` non-asset
path, so deep-link + refresh on `/dashboard/manage` work with no server change.

### 3.2 Entry point

Add a **관리** icon-button in the header `.top-right` (next to the existing
`#gearBtn`), `id="manageBtn"`, with a building/gear-stack icon. Clicking it →
`go('manage')`.

The ⚙ `#gearBtn` stays and keeps opening the (now slim) quick-drawer.

### 3.3 Quick-drawer (slimmed)

`#drawer` keeps **only** the three display-pref `.set` blocks (테마, 액센트,
정보 밀도 — `index.html` current lines 266–268). These are localStorage-backed
(`PREF`/`savePref`/`applyPref`/`syncSegs`) and need no server.

Removed from the drawer (moved to 관리, see 3.4): `#tenant-set`, `#pw-set`,
`#alert-set`, `#hidden-block`.

`openDrawer()` no longer calls `loadTenants()`/`loadHiddenAgents()` (those move to
the manage view's loader). The drawer note text is trimmed to reference display
settings only.

### 3.4 관리 view layout

```
#manage-view (.view.main)
 ├─ .manage-head      ← title "관리" + back-to-overview affordance (optional; header brand already goes home)
 └─ .manage-body
     ├─ .manage-nav   ← left sub-tab rail: [지점] [알림] [보안]
     └─ .manage-panes
         ├─ #mg-branches  (지점 — card grid)   .active by default
         ├─ #mg-alerts    (알림)
         └─ #mg-security  (보안)
```

Sub-tab switching is **client-only** (no URL change for sub-tabs — keeps routing
simple; the deep-linkable unit is the 관리 page). A `setManageTab(name)` toggles
`.active` on `.manage-nav` buttons and on the three panes, mirroring the existing
`setTab()` pattern. Last sub-tab persisted to `localStorage` (`opsview.mgtab`),
default `branches`.

On entering `#manage-view`, call `loadManage()` which runs `loadBranches()` +
(for the active/needed panes) `loadAlertCfg()`; `loadBranches()` internally
handles hidden 지점.

---

## 4. 지점 (Branches) pane — card grid

### 4.1 Data join (no server change)

- `GET /dashboard/api/agents` → `{editable:bool, agents:[{id,name,token,online}]}`
  (registry + auth source-of-truth; `editable` is true only when `RELAY_DB` set).
- Join each registry agent with the in-memory `state.agents` (already polled) via
  `agentById(id)` to enrich with **live** fields:
  - `watchers.length` → 시청자 수
  - `activeStreams(a).length` / total channels (`a.dvrs` sum of `channels`, or
    `a.streams.length`) → 활성/전체 채널
  - `(a.dvrs||[]).length` → DVR 수
  - `a.last_publish_ms` + `fmtAgo()` → 마지막 접속 (when offline)
- If a registry agent has no matching live entry (never connected), show it as
  offline with “접속 기록 없음”.

### 4.2 Card markup (one per 지점)

```
.mg-card[.offline]
 ├─ .mgc-head
 │   ├─ .mgc-dot(.on)                  ← online status dot
 │   ├─ .mgc-name (이름) + .mgc-id (mono, id)
 │   └─ .mgc-badge  ●온라인 / ○오프라인
 ├─ .mgc-stats   (3 stat cells)
 │   ├─ DVR·채널   "2 DVR · 12/16ch"
 │   ├─ 시청자      "3"
 │   └─ 상태        online→"실시간"  offline→"마지막 접속 12분 전"
 ├─ .mgc-token   (가림 표시 + 👁 토글 + 복사)   ← masked by default
 └─ .mgc-actions
     ├─ [이름변경]   (rename)
     ├─ [토큰 재발급] (regenerate)
     ├─ [재접속]      (reconnect; disabled if offline)
     ├─ [숨기기]      (hide)
     └─ [삭제]        (delete)
```

Plus a trailing **[+ 지점 추가]** affordance card that expands an inline add-form
(reuses the existing fields: 지점 ID / 이름 / 토큰+생성). The form posts the same
`POST /dashboard/api/agents`.

### 4.3 Actions (all via existing endpoints)

| Action | Endpoint | Notes |
|---|---|---|
| 이름변경 | `POST /api/agents {id, name, token}` | inline edit; **must resend current token** (upsert overwrites the row by id). Token is known from the GET. |
| 토큰 재발급 | `POST /api/agents {id, name, token:<new>}` | generate 32-hex (`genToken` logic). **Warn:** “이 지점 에이전트 설정의 토큰도 새 값으로 바꿔야 다시 연결됩니다.” confirm before commit. |
| 재접속 | `POST /api/agent-control {agent_id, action:'reconnect'}` | existing; 409 → 오프라인 안내. Button disabled when offline. |
| 숨기기 | `hideAgent(id, true)` (existing) | hides from overview/sidebar; restorable from 숨긴 지점 section. |
| 삭제 | `DELETE /api/agents?id=<id>` | `confirm()` first; “그 매장 에이전트는 더 이상 연결 못 함”. |
| 추가 | `POST /api/agents {id,name,token}` | id+token required. |

Token display: masked (e.g. `••••••••`) with 👁 reveal toggle and click-to-copy
(reuse existing copy + `tnMsg`-style feedback, scoped per-card via a status line).

### 4.4 숨긴 지점

A collapsible section at the bottom of the 지점 pane (reuses `loadHiddenAgents()`
logic + `hideAgent(id,false)` restore). Reads `state.hidden_agents` from
`/api/state`.

### 4.5 Read-only mode (`editable:false`, no `RELAY_DB`)

- Cards still render (live status is useful read-only).
- 추가/이름변경/토큰 재발급/삭제 controls hidden or disabled.
- 재접속 / 숨기기 / 복원 remain available (they don't touch the registry DB).
- A banner explains: “읽기 전용 — relay에 `RELAY_DB`(영속 볼륨)를 설정하면 지점을
  추가/삭제할 수 있습니다.”

### 4.6 Legacy "default" agent

Single-site setups expose a `default` agent authed by `RELAY_PUBLISHER_TOKEN`
(not a registry row). It appears as a card with live status but **no token edit /
재발급 / 삭제** (only 재접속 / 숨기기). Detected when its id is absent from the
`/api/agents` registry list but present in `state.agents`.

---

## 5. 알림 (Alerts) pane

Move the existing `#alert-set` block verbatim into `#mg-alerts`. Same IDs
(`#al-enabled`, `#al-tg-token`, `#al-tg-chat`, `#al-webhook`, `#al-save`,
`#al-test`, `#al-msg`) so the existing handlers (`loadAlertCfg`, `#al-save`,
`#al-test`) keep working unchanged. Gated on `editable` like today.

## 6. 보안 (Security) pane

Move the existing `#pw-set` block verbatim into `#mg-security`. Same IDs
(`#pw-new`, `#pw-save`, `#pw-msg`). Handler unchanged. Gated on `editable`.

---

## 7. Files touched

| File | Change |
|---|---|
| `relay/dashboard_assets/index.html` | Add `#manageBtn` to header; slim `#drawer` to 3 display-pref blocks; add `#manage-view` section with `.manage-nav` + 3 panes; relocate tenant/alert/pw/hidden markup into panes (card-grid container `#mg-branches`). |
| `relay/dashboard_assets/app.js` | Add `manage` to `pathFor`/`go`/`routeFromPath`/`navTo`; add `setManageTab` + `loadManage`/`loadBranches` (card render + per-card action wiring incl. rename/regen/reveal); move drawer-load calls out of `openDrawer`; keep alert/pw/hidden handlers (IDs unchanged). |
| `relay/dashboard_assets/style.css` | `#manage-view` shell, `.manage-nav` rail, `.mg-card` grid (`grid-template-columns` responsive → 1-col mobile), stats/token/actions, add-card form, read-only banner. Use design tokens only. |

**No Go changes.** All endpoints already exist:
`/api/agents` (GET/POST/DELETE), `/api/agent-control`, agent-hide (`hideAgent`),
`/api/alert-config`, `/api/alert-test`, `/api/password`, `/api/state`.

---

## 8. Edge cases

- **Read-only** (no RELAY_DB): registry-mutating controls hidden; live controls stay.
- **default agent**: limited card (no token ops).
- **Offline 지점**: 재접속 disabled; show 마지막 접속 시각; live stats show 0/last-known.
- **Token regen / rename**: upsert requires resending the full row; rename must
  carry current token; regen must warn about updating the agent side.
- **Delete**: confirm dialog; agent disconnects.
- **Mobile width**: card grid collapses to 1 column; `.manage-nav` becomes a top
  row of pills.
- **Back/forward**: `/dashboard/manage` ↔ `/dashboard` via History API; refresh
  on manage URL re-renders manage view.

---

## 9. Testing

- **Go tests**: unchanged code → `cd relay && go build ./... && go test ./...`
  must still pass (regression guard; no server logic changed).
- **JS**: `node --check relay/dashboard_assets/app.js`.
- **Manual smoke** (both `RELAY_DB` set and unset):
  - 관리 button → `/dashboard/manage`; refresh stays; back returns to overview.
  - Sub-tabs 지점/알림/보안 switch; last sub-tab persists.
  - 지점 cards show correct online/offline, channel/viewer/DVR counts vs sidebar.
  - 추가 → card appears; 이름변경 persists; 토큰 재발급 warns + changes token;
    복사/reveal work; 재접속 (online vs offline); 숨기기→복원; 삭제 confirm.
  - Read-only: mutating controls hidden, banner shown, 재접속/숨기기 still work.
  - 알림 저장/테스트 and 비밀번호 변경 still work from their new panes.
  - Quick-drawer now shows only 테마/액센트/정보밀도 and still applies live.

No frontend test harness exists (vanilla SPA); manual smoke + `node --check` is
the established practice for this codebase.
