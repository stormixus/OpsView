# Dual-Stream Recording Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let each channel record the high-resolution main stream while the live grid stays on the lightweight sub stream, via a per-channel opt-in toggle.

**Architecture:** Live pipeline always pulls the **sub** stream. A channel marked `record_hires` additionally starts a second `streamEntry` keyed `<id>@main` pulling the **main** stream; the recorder records that main stream into the channel's normal recording directory (no double-recording), and the full-screen player can toggle live HD onto it. Config flows agent SQLite → `proto` → relay; UI is the live-grid edit mode.

**Tech Stack:** Go (relay + agent), `proto` JSON messages, embedded vanilla-JS dashboard. Spec: `docs/superpowers/specs/2026-06-10-dual-stream-design.md`.

**Build/test commands:**
- Relay: `cd relay && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -race ./...`
- Agent: `cd agent && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -race ./...`
- Proto change → build BOTH relay and agent.
- JS: `node --check relay/dashboard_assets/app.js` and `node --test relay/jstest/<file>.test.js`

**Conventions:** All code stays H.264 (no 265). The main-stream key suffix is the constant `@main`. Recordings always land in the base channel dir (`Records/<agent>/dvr<D>_ch<C>/`), never a `@main` dir.

---

## File Structure

- `proto/json_messages.go` — add `ChannelInfo.RecordHighRes`; add `SurvMeta.Hires []ChannelHiRes` + `ChannelHiRes` type. (Task 1)
- `agent/surveillance.go` — `ChannelConfig.RecordHighRes`; `record_hires` column + one-time backfill migration; scan/insert include it; new `SetChannelHiRes`. (Task 1)
- `agent/agent.go` — map `RecordHighRes` into the pushed `proto.ChannelInfo`; apply `m.Hires` in `applySurvMeta`. (Task 1)
- `relay/surv_proxy.go` — `buildSurvRTSPURL`/`survRTSPURLForChannel` take an explicit `mainStream bool`; live forced to sub; start `<id>@main` pipeline for hires channels; stream-id helpers + constant. (Tasks 2, 3)
- `relay/recorder.go` — `recordTargets` pure resolver; `reconcile`/`startLocked` split record **source** vs **output dir**; restart on source change. (Task 4)
- `relay/dashboard_state.go` — hide `@main` streams from the dashboard stream list; surface `record_hires` on `channelMeta`. (Task 5)
- `relay/dashboard_assets/app.js`, `relay/dashboard_assets/style.css` — edit-mode HD-record toggle + 720p label; player HD live toggle. (Tasks 6, 7)
- Tests: `relay/dualstream_test.go` (new), `agent/surveillance_hires_test.go` (new), `relay/jstest/livestream.test.js` (new).

---

## Task 1: Data model — `record_hires` end to end (proto + agent)

**Files:**
- Modify: `proto/json_messages.go`
- Modify: `agent/surveillance.go`
- Modify: `agent/agent.go:343-347` (config push), `agent/agent.go:551-574` (`applySurvMeta`)
- Test: `agent/surveillance_hires_test.go` (create)

- [ ] **Step 1: Add proto fields**

In `proto/json_messages.go`, add to `ChannelInfo` (after `RtspURI`):
```go
	RecordHighRes bool `json:"record_hires"`
```
Add near `SurvMeta` (after the `ChannelRename` type):
```go
// ChannelHiRes toggles high-res (main-stream) recording for one channel.
type ChannelHiRes struct {
	ChNum int  `json:"ch_num"`
	On    bool `json:"on"`
}
```
Add to the `SurvMeta` struct (after `Renames`):
```go
	Hires []ChannelHiRes `json:"hires,omitempty"` // per-channel high-res record toggles
```

- [ ] **Step 2: Write the failing agent tests**

Create `agent/surveillance_hires_test.go`:
```go
package main

import "testing"

func TestRecordHiresMigrationBackfillsMainDVRs(t *testing.T) {
	m := newTestSurveillanceManager(t) // same helper other surv tests use
	// a main-stream DVR's channels should be backfilled to record_hires=1
	dvrMain, _ := m.AddDVR("mainDVR", "1.2.3.4", 80, "", 0, "u", "p", "isapi", 5, "main")
	m.UpsertChannel(dvrMain.ID, 1, 1920, 1080, "", "")
	dvrSub, _ := m.AddDVR("subDVR", "1.2.3.5", 80, "", 0, "u", "p", "isapi", 5, "sub")
	m.UpsertChannel(dvrSub.ID, 1, 1920, 1080, "", "")

	// re-run migration (idempotent: must NOT clobber once the column exists)
	m.migrate()

	if got := m.channelHiRes(dvrMain.ID, 1); !got {
		t.Errorf("main-DVR channel: record_hires = false, want true (backfill)")
	}
	if got := m.channelHiRes(dvrSub.ID, 1); got {
		t.Errorf("sub-DVR channel: record_hires = true, want false")
	}
}

func TestSetChannelHiResRoundTrips(t *testing.T) {
	m := newTestSurveillanceManager(t)
	dvr, _ := m.AddDVR("d", "1.2.3.4", 80, "", 0, "u", "p", "isapi", 5, "sub")
	m.UpsertChannel(dvr.ID, 1, 1920, 1080, "", "")
	if err := m.SetChannelHiRes(dvr.ID, 1, true); err != nil {
		t.Fatalf("SetChannelHiRes: %v", err)
	}
	if !m.channelHiRes(dvr.ID, 1) {
		t.Errorf("after SetChannelHiRes(true): got false")
	}
}
```
Note: match the real helper names already used in `agent/surveillance_*_test.go` for constructing a manager and inserting a channel. Inspect `agent/surveillance_meta_test.go` and `agent/surveillance_test.go` first; if the channel-insert helper is named differently (e.g. `UpsertChannels` / direct `INSERT`), adapt these two tests to that helper rather than introducing a new one. `channelHiRes` is a tiny test-only read added in Step 3.

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd agent && PATH=/opt/homebrew/bin:$PATH go test -race -run 'RecordHires|SetChannelHiRes' ./...`
Expected: FAIL — `m.SetChannelHiRes` / `m.channelHiRes` undefined.

- [ ] **Step 4: Implement the agent data model**

In `agent/surveillance.go`:

(a) Add to `ChannelConfig` (after `RtspURI`):
```go
	RecordHighRes bool `json:"record_hires"`
```

(b) In the migration (`migrate`, the function holding the `CREATE TABLE`/`ALTER TABLE` `stmts`), AFTER the existing `stmts` loop add a one-time column add + backfill so it never clobbers later user toggles:
```go
	// record_hires: added once. Backfill main-stream DVRs to keep their current
	// (main) recording quality; sub DVRs default to 0. Only on first creation so a
	// later user toggle-off is never overwritten on restart.
	if !columnExists(m.db, "channels", "record_hires") {
		m.db.Exec(`ALTER TABLE channels ADD COLUMN record_hires INTEGER NOT NULL DEFAULT 0`)
		m.db.Exec(`UPDATE channels SET record_hires=1 WHERE dvr_id IN (SELECT id FROM dvrs WHERE stream_quality='main')`)
	}
```
Add the helper (same file):
```go
func columnExists(db *sql.DB, table, col string) bool {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk) == nil && name == col {
			return true
		}
	}
	return false
}
```
(`database/sql` is already imported.)

(c) Update the channel `SELECT` (`surveillance.go:257`) to include `record_hires` and scan it. Change the query string to end `... height, rtsp_uri, snapshot_uri, record_hires FROM channels WHERE dvr_id=? ORDER BY display_order` and add `&ch.RecordHighRes` to the matching `rows.Scan(...)`.

(d) Update the channel `INSERT ... ON CONFLICT` (`surveillance.go:334`) to NOT reset `record_hires` on conflict (it must survive a rediscovery): the column list and conflict-update set stay as-is (rediscovery updates width/height/rtsp/snapshot only). Leave `record_hires` out of the insert so new rows default 0 and existing toggles persist. No change needed there beyond confirming `record_hires` is absent from the `DO UPDATE SET`.

(e) Add the setter and a test-only reader:
```go
// SetChannelHiRes toggles high-res (main-stream) recording for one channel,
// then fires onChange so the new config propagates to the relay.
func (m *SurveillanceManager) SetChannelHiRes(dvrID int64, chNum int, on bool) error {
	v := 0
	if on {
		v = 1
	}
	_, err := m.db.Exec(`UPDATE channels SET record_hires=? WHERE dvr_id=? AND ch_num=?`, v, dvrID, chNum)
	if err == nil && m.onChange != nil {
		m.onChange()
	}
	return err
}

func (m *SurveillanceManager) channelHiRes(dvrID int64, chNum int) bool {
	var v int
	m.db.QueryRow(`SELECT record_hires FROM channels WHERE dvr_id=? AND ch_num=?`, dvrID, chNum).Scan(&v)
	return v == 1
}
```
(Confirm the onChange field name from `RenameChannel`/`ReorderChannels`; mirror exactly how they trigger it.)

- [ ] **Step 5: Run agent tests to verify they pass**

Run: `cd agent && PATH=/opt/homebrew/bin:$PATH go test -race -run 'RecordHires|SetChannelHiRes' ./...`
Expected: PASS.

- [ ] **Step 6: Wire the config push + meta apply**

In `agent/agent.go:343-347` (building `proto.ChannelInfo` for `cfg.Channels`), add the field:
```go
				RecordHighRes: ch.RecordHighRes,
```
In `agent/agent.go:applySurvMeta` (after the `m.Renames` loop), add:
```go
	for _, h := range m.Hires {
		if err := a.survMgr.SetChannelHiRes(m.DVRID, h.ChNum, h.On); err != nil {
			log.Printf("[agent] surv meta hires ch %d: %v", h.ChNum, err)
		}
	}
```

- [ ] **Step 7: Build both binaries + full agent test**

Run:
```
cd relay && PATH=/opt/homebrew/bin:$PATH go build ./...
cd ../agent && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -race ./...
```
Expected: both build; agent tests PASS.

- [ ] **Step 8: Commit**
```bash
git add proto/json_messages.go agent/surveillance.go agent/agent.go agent/surveillance_hires_test.go
git commit -m "feat(dual-stream): record_hires field + agent migration/setter + meta apply"
```

---

## Task 2: Relay stream URL — explicit main/sub, live forced to sub

**Files:**
- Modify: `relay/surv_proxy.go` (`buildSurvRTSPURL`, `survRTSPURLForChannel`, call site ~line 111)
- Test: `relay/dualstream_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `relay/dualstream_test.go`:
```go
package main

import (
	"strings"
	"testing"

	"github.com/opsview/opsview/proto"
)

func TestBuildSurvRTSPURLMainVsSub(t *testing.T) {
	dvr := proto.DVRInfo{Addr: "1.2.3.4", Username: "u", Password: "p", Protocol: "isapi"}
	main := buildSurvRTSPURL(dvr, 1, true)
	sub := buildSurvRTSPURL(dvr, 1, false)
	if !strings.Contains(main, "/Streaming/Channels/101") {
		t.Errorf("main: got %q, want .../Channels/101", main)
	}
	if !strings.Contains(sub, "/Streaming/Channels/102") {
		t.Errorf("sub: got %q, want .../Channels/102", sub)
	}
}

func TestBuildSurvRTSPURLDahuaMainVsSub(t *testing.T) {
	dvr := proto.DVRInfo{Addr: "1.2.3.4", Username: "u", Password: "p", Protocol: "dahua"}
	if got := buildSurvRTSPURL(dvr, 2, true); !strings.Contains(got, "subtype=0") {
		t.Errorf("dahua main: got %q, want subtype=0", got)
	}
	if got := buildSurvRTSPURL(dvr, 2, false); !strings.Contains(got, "subtype=1") {
		t.Errorf("dahua sub: got %q, want subtype=1", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -race -run BuildSurvRTSPURL ./...`
Expected: FAIL — `buildSurvRTSPURL` takes 2 args, not 3.

- [ ] **Step 3: Implement explicit stream selection**

In `relay/surv_proxy.go`, change `buildSurvRTSPURL` to take `mainStream bool` and drop the `StreamQuality` branch:
```go
func buildSurvRTSPURL(dvr proto.DVRInfo, chNum int, mainStream bool) string {
	port := resolveRTSPPort(dvr)
	host := fmt.Sprintf("%s:%d", resolveSurvHost(dvr), port)

	var path string
	switch dvr.Protocol {
	case "dahua":
		subtype := 1 // sub
		if mainStream {
			subtype = 0
		}
		path = fmt.Sprintf("/cam/realmonitor?channel=%d&subtype=%d", chNum, subtype)
	default: // "isapi", "rtsp", "", or any other
		streamID := "02" // sub
		if mainStream {
			streamID = "01"
		}
		path = fmt.Sprintf("/Streaming/Channels/%d%s", chNum, streamID)
	}

	u := &url.URL{Scheme: "rtsp", User: url.UserPassword(dvr.Username, dvr.Password), Host: host, Path: path}
	return u.String()
}
```
Change `survRTSPURLForChannel` to accept and forward `mainStream`:
```go
func survRTSPURLForChannel(dvr proto.DVRInfo, ch proto.ChannelInfo, mainStream bool) string {
	if ch.RtspURI != "" {
		// ONVIF-discovered URI is a single profile; main/sub selection unsupported
		// for ONVIF-only DVRs (open item in the spec). Template DVRs use the path below.
		u, err := url.Parse(ch.RtspURI)
		if err == nil {
			if u.User == nil && dvr.Username != "" {
				u.User = url.UserPassword(dvr.Username, dvr.Password)
			}
			return u.String()
		}
	}
	return buildSurvRTSPURL(dvr, ch.ChNum, mainStream)
}
```

- [ ] **Step 4: Force live to sub at the call site**

In `relay/surv_proxy.go:HandleSurvConfig` (~line 111), the base (live) pending stream forces sub:
```go
					rtspURL: survRTSPURLForChannel(dvr, ch, false),
```

- [ ] **Step 5: Run test + build to verify**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -race -run BuildSurvRTSPURL ./... && PATH=/opt/homebrew/bin:$PATH go build ./...`
Expected: PASS + build OK.

- [ ] **Step 6: Commit**
```bash
git add relay/surv_proxy.go relay/dualstream_test.go
git commit -m "feat(dual-stream): explicit main/sub RTSP selection; live always sub"
```

---

## Task 3: Relay — start `<id>@main` pipeline for hires channels

**Files:**
- Modify: `relay/surv_proxy.go` (stream-id helpers + constant; `HandleSurvConfig` start/stop)
- Test: `relay/dualstream_test.go` (extend)

- [ ] **Step 1: Write the failing test**

Append to `relay/dualstream_test.go`:
```go
import "reflect" // add to the import block if not present

func TestStreamIDHelpers(t *testing.T) {
	if mainStreamID("dvr3_ch1") != "dvr3_ch1@main" {
		t.Errorf("mainStreamID wrong: %q", mainStreamID("dvr3_ch1"))
	}
	if !isMainStreamID("dvr3_ch1@main") || isMainStreamID("dvr3_ch1") {
		t.Errorf("isMainStreamID wrong")
	}
	if baseStreamID("dvr3_ch1@main") != "dvr3_ch1" || baseStreamID("dvr3_ch1") != "dvr3_ch1" {
		t.Errorf("baseStreamID wrong")
	}
}

func TestDesiredStreamIDs(t *testing.T) {
	chans := []proto.ChannelInfo{
		{DVRID: 3, ChNum: 1, Enabled: true, RecordHighRes: true},
		{DVRID: 3, ChNum: 2, Enabled: true, RecordHighRes: false},
		{DVRID: 3, ChNum: 9, Enabled: false, RecordHighRes: true}, // disabled => no stream
	}
	got := desiredStreamIDs(chans)
	want := map[string]bool{"dvr3_ch1": true, "dvr3_ch1@main": true, "dvr3_ch2": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("desiredStreamIDs = %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -race -run 'StreamIDHelpers|DesiredStreamIDs' ./...`
Expected: FAIL — helpers undefined.

- [ ] **Step 3: Implement helpers + desired-set**

In `relay/surv_proxy.go` (near the top, after imports):
```go
// mainStreamSuffix marks the second (high-res main) pipeline for a channel; the
// base id is the live sub stream. e.g. base "dvr3_ch1", main "dvr3_ch1@main".
const mainStreamSuffix = "@main"

func mainStreamID(base string) string   { return base + mainStreamSuffix }
func isMainStreamID(id string) bool      { return strings.HasSuffix(id, mainStreamSuffix) }
func baseStreamID(id string) string      { return strings.TrimSuffix(id, mainStreamSuffix) }

// desiredStreamIDs is the set of relay stream keys that should run for a config:
// the base (sub, live) stream for every enabled channel, plus a "<id>@main"
// stream for channels that record high-res.
func desiredStreamIDs(channels []proto.ChannelInfo) map[string]bool {
	out := map[string]bool{}
	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		id := fmt.Sprintf("dvr%d_ch%d", ch.DVRID, ch.ChNum)
		out[id] = true
		if ch.RecordHighRes {
			out[mainStreamID(id)] = true
		}
	}
	return out
}
```
(`strings` is already imported.)

- [ ] **Step 4: Run helper tests to verify pass**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -race -run 'StreamIDHelpers|DesiredStreamIDs' ./...`
Expected: PASS.

- [ ] **Step 5: Start the `@main` pipeline + use desired-set for stop**

In `relay/surv_proxy.go:HandleSurvConfig`, inside the `for _, ch := range cfg.Channels` loop, AFTER the existing base-channel block that appends to `perDVR[ch.DVRID]`, add the main pipeline when requested:
```go
		if ch.RecordHighRes {
			mid := mainStreamID(chID)
			desired[mid] = true
			sp.mu.RLock()
			_, mexists := sp.streams[mid]
			sp.mu.RUnlock()
			if !mexists {
				perDVR[ch.DVRID] = append(perDVR[ch.DVRID], pendingCh{
					chID:    mid,
					name:    ch.Name,
					rtspURL: survRTSPURLForChannel(dvr, ch, true), // main
				})
			}
		}
```
The existing stop loop already removes any `sp.streams` id not in `desired`, so a channel toggled OFF at runtime has its `@main` stream stopped automatically. No change needed there.

- [ ] **Step 6: Build + full relay test**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -race ./...`
Expected: build OK, tests PASS.

- [ ] **Step 7: Commit**
```bash
git add relay/surv_proxy.go relay/dualstream_test.go
git commit -m "feat(dual-stream): start <id>@main pipeline for record_hires channels"
```

---

## Task 4: Recorder — record main into base dir, no double-recording

**Files:**
- Modify: `relay/recorder.go` (`recordTargets`, `reconcile`, `startLocked`, `recProc`)
- Test: `relay/dualstream_test.go` (extend)

- [ ] **Step 1: Write the failing test**

Append to `relay/dualstream_test.go`:
```go
func TestRecordTargets(t *testing.T) {
	cases := []struct {
		name   string
		active []string
		want   map[string]string // outputBase -> sourceID
	}{
		{"sub only", []string{"dvr3_ch2"}, map[string]string{"dvr3_ch2": "dvr3_ch2"}},
		{"main present", []string{"dvr3_ch1", "dvr3_ch1@main"}, map[string]string{"dvr3_ch1": "dvr3_ch1@main"}},
		{"agent prefixed", []string{"a1/dvr3_ch1", "a1/dvr3_ch1@main"}, map[string]string{"a1/dvr3_ch1": "a1/dvr3_ch1@main"}},
		{"mixed", []string{"dvr3_ch1", "dvr3_ch1@main", "dvr3_ch2"},
			map[string]string{"dvr3_ch1": "dvr3_ch1@main", "dvr3_ch2": "dvr3_ch2"}},
	}
	for _, c := range cases {
		got := recordTargets(c.active)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: recordTargets(%v) = %v, want %v", c.name, c.active, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -race -run RecordTargets ./...`
Expected: FAIL — `recordTargets` undefined.

- [ ] **Step 3: Implement `recordTargets`**

In `relay/recorder.go`:
```go
// recordTargets maps each channel's recording OUTPUT path to the stream id to
// record FROM. A channel with a "<id>@main" stream records the main stream into
// its base dir and is NOT also recorded from its sub; other channels record their
// own (sub) stream. Operates on full streamPath()s (agent prefix preserved).
func recordTargets(active []string) map[string]string {
	has := map[string]bool{}
	for _, id := range active {
		has[id] = true
	}
	out := map[string]string{}
	for _, id := range active {
		if isMainStreamID(id) {
			out[baseStreamID(id)] = id
			continue
		}
		if !has[mainStreamID(id)] {
			out[id] = id
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -race -run RecordTargets ./...`
Expected: PASS.

- [ ] **Step 5: Drive `reconcile`/`startLocked` from `recordTargets`**

In `relay/recorder.go`, add a `src` field to `recProc` (so a source change restarts ffmpeg):
```go
type recProc struct {
	cancel context.CancelFunc
	src    string
}
```
Replace `reconcile` body with:
```go
func (r *Recorder) reconcile() {
	var active []string
	for _, s := range r.hub.allSessions() {
		for _, st := range s.survProxy.StreamStats() {
			if st.Active {
				active = append(active, streamPath(s.id, st.ID))
			}
		}
	}
	want := recordTargets(active) // outputBase -> sourceID
	r.mu.Lock()
	defer r.mu.Unlock()
	for out, src := range want {
		if cur, ok := r.recs[out]; !ok {
			r.startLocked(out, src)
		} else if cur.src != src {
			cur.cancel() // source flipped (sub<->main); restart against the new one
			r.startLocked(out, src)
		}
	}
	for out, p := range r.recs {
		if _, ok := want[out]; !ok {
			p.cancel()
			delete(r.recs, out)
		}
	}
}
```
Replace `startLocked` to split source vs output:
```go
// startLocked launches a supervised ffmpeg recorder: records FROM sourcePath's
// self-HLS INTO outputPath's directory (so a main stream lands in the channel dir).
func (r *Recorder) startLocked(outputPath, sourcePath string) {
	ctx, cancel := context.WithCancel(context.Background())
	r.recs[outputPath] = &recProc{cancel: cancel, src: sourcePath}
	outDir := filepath.Join(r.dir, filepath.FromSlash(outputPath))
	hlsURL := r.selfHLS + "/surv/" + sourcePath + "/index.m3u8"
	go r.supervise(ctx, outputPath, hlsURL, outDir)
	log.Printf("[rec] recording %s (source %s) -> %s", outputPath, sourcePath, outDir)
}
```
(`supervise`, `stopAll`, janitor are unchanged — they key on the output path, which stays the base channel dir.)

- [ ] **Step 6: Build + full relay test**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -race ./...`
Expected: build OK, all PASS.

- [ ] **Step 7: Commit**
```bash
git add relay/recorder.go relay/dualstream_test.go
git commit -m "feat(dual-stream): recorder records main into base dir, no double-record"
```

---

## Task 5: Dashboard state — hide `@main`, surface `record_hires`

**Files:**
- Modify: `relay/dashboard_state.go` (`channelMeta`, `buildDashboardState` streams loop + channel build)
- Test: build/manual (state assembly is integration glue; covered by manual check)

- [ ] **Step 1: Hide `@main` streams from the dashboard stream list**

In `relay/dashboard_state.go:buildDashboardState`, the loop over `StreamStats()` (~line 156) must skip the main pipeline (it is an implementation detail, not a user-facing channel). The base sub stream still represents the channel's live/active state:
```go
		for _, st := range s.survProxy.StreamStats() {
			if isMainStreamID(st.ID) {
				if st.Active {
					activeSet[st.ID] = true // keep active flag for recorder/debug
				}
				continue // not a user-facing stream row
			}
			streams = append(streams, streamState{
				ID: st.ID, Name: st.Name, Active: st.Active, Codec: st.Codec,
				WSWatchers: st.WSWatchers, Path: streamPath(s.id, st.ID),
			})
			if st.Active {
				activeSet[st.ID] = true
			}
		}
```

- [ ] **Step 2: Surface `record_hires` + `height` on `channelMeta`**

Add both fields to `channelMeta` (`height` powers the 720p label in Task 6):
```go
	Height      int  `json:"height"`
	RecordHiRes bool `json:"record_hires"`
```
And set them when building channels (~line 174):
```go
					channels = append(channels, channelMeta{
						DVRID: ch.DVRID, ChNum: ch.ChNum, Name: ch.Name, Order: ch.Order,
						Enabled:     ch.Enabled,
						Active:      activeSet[streamIDFor(ch.DVRID, ch.ChNum)],
						Height:      ch.Height,
						RecordHiRes: ch.RecordHighRes,
					})
```

- [ ] **Step 3: Build + relay test**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -race ./...`
Expected: build OK, PASS.

- [ ] **Step 4: Commit**
```bash
git add relay/dashboard_state.go
git commit -m "feat(dual-stream): hide @main from stream list, expose record_hires to dashboard"
```

---

## Task 6: Dashboard UI — edit-mode HD-record toggle + 720p label

**Files:**
- Modify: `relay/dashboard_assets/app.js` (channel mapping ~line 90; `liveCellHTML`; `wireLiveEdit`; `postMeta`)
- Modify: `relay/dashboard_assets/style.css` (toggle + label styles)
- Test: manual checklist (DOM/UI)

- [ ] **Step 1: Thread `record_hires` into the JS channel objects**

In `relay/dashboard_assets/app.js` (~line 90), include the fields in the channel map (`record_hires` + `height` were added to `channelMeta` JSON in Task 5):
```js
      chans:(a.channels||[]).map(function(c){return {dvr_id:c.dvr_id, ch_num:c.ch_num, name:c.name, order:c.order, enabled:c.enabled, active:c.active, record_hires:!!c.record_hires, height:c.height||0};}),
```
Ensure the per-stream cell object `s` used by `renderGrid`/`liveCellHTML` carries `s.hires` and `s.h720`. Where the grid builds its stream list for the selected agent, set on each `s` from its matching channel record `chMeta` (the same lookup the grid already does for `s.name`/`s.ch`):
```js
      s.hires = !!chMeta.record_hires;
      s.h720 = (chMeta.height && chMeta.height <= 720);
```

- [ ] **Step 2: Add the toggle to the edit-mode cell**

In `liveCellHTML(s)` (`app.js`), inside the `liveEditing` branch of `.clabel`, append an HD toggle after the name input:
```js
      (liveEditing?'<label class="cell-hd'+(s.h720?' is720':'')+'"><input type="checkbox" class="cell-hd-cb" data-ch="'+s.ch+'" data-dvr="'+s.dvrId+'"'+(s.hires?' checked':'')+'>HD'+(s.h720?' <span class="hd720">720p</span>':'')+'</label>':'')+
```
(Place it within the existing `clabel` template string, adjacent to the `cell-name` input.)

- [ ] **Step 3: Wire the toggle to persist via `postMeta`**

In `wireLiveEdit(a)` (`app.js`), after the `.cell-name` wiring, add:
```js
  $$('#grid .cell-hd-cb').forEach(function(cb){
    cb.addEventListener('change', function(){ postMeta(selected, parseInt(cb.dataset.dvr), null, null, [{ ch_num: parseInt(cb.dataset.ch), on: cb.checked }]); });
    cb.addEventListener('pointerdown', function(e){ e.stopPropagation(); });
  });
```
Extend `postMeta` to carry hires:
```js
function postMeta(agentId, dvrId, order, renames, hires){
  var body = { agent_id: agentId, dvr_id: dvrId };
  if (order) body.order = order;
  if (renames) body.renames = renames;
  if (hires) body.hires = hires;
  fetch('/dashboard/api/channel-meta', { method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body) })
    .then(function(r){ if (r.status===409) { alert('에이전트 오프라인 — 편집을 적용할 수 없습니다.'); } });
}
```
Update the existing two `postMeta(...)` call sites (rename at ~line 1832, order at ~line 1880) to pass the new 5th arg as `null` (rename: `postMeta(selected, ..., null, [{...}])` → already 4 args, add trailing `, null`; order call already passes `(selected, dvrId, active.concat(inactive), null)` → add trailing `, null`). The relay `HandleDashboardChannelMeta` already decodes the whole `proto.SurvMeta` (which now has `Hires`), so no relay handler change is needed.

- [ ] **Step 4: Style the toggle (style.css)**

Add to `relay/dashboard_assets/style.css`:
```css
.cell-hd{display:inline-flex;align-items:center;gap:4px;font-size:11px;font-weight:700;color:rgba(255,255,255,.85);cursor:pointer;user-select:none;}
.cell-hd input{margin:0;cursor:pointer;}
.cell-hd .hd720{font-size:9.5px;font-weight:700;color:#f5b454;}
.cell-hd.is720{opacity:.85;}
```

- [ ] **Step 5: Syntax check + build (assets are embedded)**

Run:
```
node --check relay/dashboard_assets/app.js
cd relay && PATH=/opt/homebrew/bin:$PATH go build ./...
```
Expected: no syntax errors; build OK.

- [ ] **Step 6: Commit**
```bash
git add relay/dashboard_assets/app.js relay/dashboard_assets/style.css relay/dashboard_state.go
git commit -m "feat(dual-stream): edit-mode HD-record toggle + 720p label"
```

---

## Task 7: Player — manual HD live toggle (Always channels only)

**Files:**
- Create: `relay/jstest/livestream.test.js`
- Modify: `relay/dashboard_assets/app.js` (`liveWsPath` helper; `openPlayer`/`upStartLive`; HD button)
- Modify: `relay/dashboard_assets/index.html` (HD button in the player transport/overlay), `style.css`

- [ ] **Step 1: Write the failing JS test**

Create `relay/jstest/livestream.test.js`:
```js
const test = require('node:test');
const assert = require('node:assert');
const { liveWsPath } = require('../dashboard_assets/livestream.js');

test('liveWsPath: sub by default', () => {
  assert.strictEqual(liveWsPath('a1/dvr3_ch1', true, false), 'a1/dvr3_ch1');
  assert.strictEqual(liveWsPath('a1/dvr3_ch1', false, false), 'a1/dvr3_ch1');
});

test('liveWsPath: main only when hires AND hd toggled on', () => {
  assert.strictEqual(liveWsPath('a1/dvr3_ch1', true, true), 'a1/dvr3_ch1@main');
  assert.strictEqual(liveWsPath('a1/dvr3_ch1', false, true), 'a1/dvr3_ch1'); // not hires -> never main
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `node --test relay/jstest/livestream.test.js`
Expected: FAIL — cannot find `../dashboard_assets/livestream.js`.

- [ ] **Step 3: Implement the pure helper (shared file)**

Create `relay/dashboard_assets/livestream.js`:
```js
// liveWsPath returns the /surv/ws path segment for a channel's live stream:
// the base (sub) stream, or the "<base>@main" high-res stream only when the
// channel records hi-res AND the player's HD toggle is on.
function liveWsPath(basePath, hires, hdOn) {
  return (hires && hdOn) ? basePath + '@main' : basePath;
}
if (typeof module !== 'undefined' && module.exports) { module.exports = { liveWsPath }; }
```
Load it in the dashboard: add to `relay/dashboard_assets/index.html` before `app.js`:
```html
    <script src="livestream.js"></script>
```
(Confirm the asset is embedded/served — `dashboard_assets` is `//go:embed`-ed; new files in that dir are served automatically.)

- [ ] **Step 4: Run test to verify it passes**

Run: `node --test relay/jstest/livestream.test.js`
Expected: PASS.

- [ ] **Step 5: Use the helper + add the HD button in the player**

In `relay/dashboard_assets/app.js`, where the full-screen player starts live (`upStartLive`, building `wsUrl` from `up.path` ~line 980), route through the helper and current HD state:
```js
  var wsPath = liveWsPath(up.path, up.hires, up.hdOn);
  var wsUrl=(location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/surv/ws/'+wsPath;
```
In `openPlayer`, set `up.hires` from the channel's `record_hires` (look it up the same way the player resolves the channel name) and default `up.hdOn=false`. Show an HD button only when `up.hires`:
```js
  var hdBtn=$('#upHdBtn'); if(hdBtn){ hdBtn.hidden=!up.hires; hdBtn.classList.toggle('on', up.hdOn); }
```
Add the button to the player overlay in `index.html` (near the transport / `up-toplabel`):
```html
      <button type="button" class="up-hd-btn" id="upHdBtn" hidden>HD</button>
```
Wire it (in the player setup code, alongside other player buttons):
```js
  if($('#upHdBtn')) $('#upHdBtn').addEventListener('click', function(){
    if(!up.open || !up.hires) return;
    up.hdOn=!up.hdOn;
    this.classList.toggle('on', up.hdOn);
    if(up.mode==='live'){ if(up.player&&up.player.close)up.player.close(); upStartLive(); } // reconnect on the new stream
  });
```
Add minimal style to `style.css`:
```css
.up-hd-btn{position:absolute;top:14px;right:120px;z-index:4;padding:4px 10px;border-radius:8px;border:1px solid rgba(255,255,255,.3);background:rgba(0,0,0,.45);color:#fff;font-weight:800;font-size:12px;cursor:pointer;}
.up-hd-btn.on{background:var(--accent-2,#22d3ee);color:#04121a;border-color:transparent;}
```
(Adjust the `right:` offset to clear existing top-right controls.)

- [ ] **Step 6: Syntax check + build + JS tests**

Run:
```
node --check relay/dashboard_assets/app.js && node --check relay/dashboard_assets/livestream.js
node --test relay/jstest/livestream.test.js
cd relay && PATH=/opt/homebrew/bin:$PATH go build ./...
```
Expected: all pass.

- [ ] **Step 7: Commit**
```bash
git add relay/dashboard_assets/livestream.js relay/dashboard_assets/app.js relay/dashboard_assets/index.html relay/dashboard_assets/style.css relay/jstest/livestream.test.js
git commit -m "feat(dual-stream): player HD live toggle on hi-res channels"
```

---

## Final verification (after all tasks)

- [ ] **Full build + tests, both modules:**
```
cd relay && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -race ./...
cd ../agent && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -race ./...
node --check relay/dashboard_assets/app.js && node --test relay/jstest/livestream.test.js
gofmt -l relay agent proto   # expect no output
```

- [ ] **Manual checklist (against a live agent/DVR):**
  - Toggle a 1080p channel's "HD" in edit mode → recordings for that channel become 1080p (check file sizes / `ffprobe`) and play back in desktop Chrome via the unified player.
  - Live grid stays light (sub) even with several HD channels on; no black cells.
  - The DVR shows 2 RTSP sessions for an HD channel (sub + main); 1 for others.
  - Player "HD" button appears only on HD channels; toggling shows crisp main and back to sub.
  - A ≤720p channel shows the "720p" label next to its toggle.
  - Toggle HD off → `@main` stream + its recorder stop; channel reverts to sub recording.

- [ ] **Update memory:** mark dual-stream shipped in `unified-player-roadmap` / note in `codec-streaming-strategy` that high-res main is now available as a future LPR source.
