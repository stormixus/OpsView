# Live Wall (GPU mosaic) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A live-only "wall" where the relay composites an agent's channels into ONE GPU-encoded mosaic stream, so the viewer decodes a single video — killing the browser concurrent-decoder cap (rotation/black cells/freeze-frames).

**Architecture:** One supervised ffmpeg per watched agent reads each channel's already-running self-HLS (no extra DVR pull), NVDEC-decodes, `xstack`-composes a grid, NVENC-encodes one stream, and feeds the existing `fragMuxer`→`survWSHub` under stream id `wall`. A lean `/dashboard/wall` page plays that one stream via the existing `playWS` and overlays a CSS grid (names + click targets); clicking a cell opens that channel's single live stream. Reuses ~60% of `relay/surv_transcode.go`.

**Tech Stack:** Go (relay module `github.com/opsview/opsview/relay`), ffmpeg + `h264_nvenc`/`-hwaccel cuda` (RTX 4080 on the relay), vanilla embedded JS dashboard, MSE-over-WebSocket.

## Global Constraints

- Build: `cd relay && PATH=/opt/homebrew/bin:$PATH go build ./...`
- Test: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -race ./...`
- JS: `node --check relay/dashboard_assets/<file>.js`; pure JS units `node --test relay/jstest/*.test.js`
- Format: `gofmt -l <changed .go files>` must print nothing.
- This is **relay-only**. Do not touch `agent/` or `proto/`. The agent already sends channel config; the relay does all streaming.
- Embedded assets: `relay/dashboard.go` has `//go:embed dashboard_assets`; new files under `relay/dashboard_assets/` are bundled on rebuild automatically.
- Reuse, do NOT reimplement: `survWSHub` (`newSurvWSHub`, `setInit`, `broadcast`, `ClientCount`), `fragMuxer` (`newFragMuxerH264`, `writeAU`), `(*streamEntry).ingestAnnexB`, `splitNALUnits`, `nalType`, `relaySelfHLSBase()`, `getPort()`, `streamPath(agentID, streamID)` (dashboard_state.go), `playWS` (app.js), `authedDashboard(r)` (dashboard.go).
- Wall stream id is the literal string `wall`, registered in the agent's `SurvProxy.streams`. It is served by the existing `/surv/ws/[agent/]wall` route and consumed by `playWS` unchanged.
- GPU env (already wired for transcode-live): `RELAY_RUNTIME=nvidia`, `NVIDIA_VISIBLE_DEVICES`. New knobs: `RELAY_WALL=1`, `RELAY_WALL_RES=1080p|720p` (default `1080p`), `RELAY_WALL_FPS` (default `15`).
- ffmpeg encode params match transcode-live: `-c:v h264_nvenc -preset p4 -tune ll -bf 0 -bsf:v h264_metadata=aud=insert -f h264 pipe:1`, AUD-delimited so `splitNALUnits` has clean AU boundaries.

---

## File Structure

- **Create `relay/surv_mosaic.go`** — all mosaic server logic: env knobs, layout math, ffmpeg arg builder, input-list builder, `EnsureMosaic`/`stopMosaic`, `mosaicState`.
- **Create `relay/surv_mosaic_test.go`** — pure unit tests (layout, args, input list, env).
- **Modify `relay/surv_proxy.go`** — add `mosaic *mosaicState` field to `SurvProxy`; rebuild-on-change hook at the end of `HandleSurvConfig`; stop the mosaic in `StopAll`.
- **Modify `relay/surv_transcode.go`** — extract the shared ffmpeg supervise loop into `(*streamEntry).superviseEncode(ctx, args)`; have `superviseTranscode` call it. The mosaic reuses it.
- **Modify `relay/surv_router.go`** — in `ServeSurvWS`, lazy-start the mosaic when the requested stream is `wall`.
- **Modify `relay/dashboard.go` + `relay/main.go`** — add `HandleWallLayout` (`/dashboard/api/wall-layout`) and `HandleWallPage` (`/dashboard/wall`), both `authedDashboard`-gated; register routes.
- **Create `relay/dashboard_assets/wall.html`, `relay/dashboard_assets/wall.js`** — the lean wall page.
- **Modify `relay/dashboard_assets/style.css`** — wall overlay grid styles.
- **Modify `relay/docker-compose.yml`, `relay/.env.example`** — `RELAY_WALL*` knobs.

---

### Task 1: Mosaic layout + input-list math (pure)

**Files:**
- Create: `relay/surv_mosaic.go`
- Test: `relay/surv_mosaic_test.go`

**Interfaces:**
- Produces:
  - `mosaicLayout(n int) (rows, cols int)` — grid shape for n cells (cols = ceil(sqrt(n)), rows = ceil(n/cols)); returns `(0,0)` for n<=0.
  - `mosaicInputIDs(stats []StreamStat) []string` — the base channel stream ids to compose: every `stats[i].ID` that is not a main stream (`!isMainStreamID`) and not `"wall"`, sorted by (dvrNum, chNum) parsed from `dvrN_chM` (unparseable ids sort last, by id).

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"reflect"
	"testing"
)

func TestMosaicLayout(t *testing.T) {
	cases := []struct{ n, rows, cols int }{
		{1, 1, 1}, {2, 1, 2}, {3, 2, 2}, {4, 2, 2},
		{5, 2, 3}, {9, 3, 3}, {12, 3, 4}, {16, 4, 4},
	}
	for _, c := range cases {
		r, col := mosaicLayout(c.n)
		if r != c.rows || col != c.cols {
			t.Fatalf("mosaicLayout(%d) = %dx%d, want %dx%d", c.n, r, col, c.rows, c.cols)
		}
	}
	if r, c := mosaicLayout(0); r != 0 || c != 0 {
		t.Fatalf("mosaicLayout(0) = %d,%d, want 0,0", r, c)
	}
}

func TestMosaicInputIDs(t *testing.T) {
	stats := []StreamStat{
		{ID: "dvr1_ch10"}, {ID: "dvr1_ch2"}, {ID: "dvr1_ch2@main"},
		{ID: "dvr3_ch1"}, {ID: "wall"}, {ID: "dvr1_ch1"},
	}
	got := mosaicInputIDs(stats)
	want := []string{"dvr1_ch1", "dvr1_ch2", "dvr1_ch10", "dvr3_ch1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mosaicInputIDs = %v, want %v (numeric ch sort, no @main/wall)", got, want)
	}
}
```

- [ ] **Step 2: Run, verify FAIL**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -run 'TestMosaicLayout|TestMosaicInputIDs' ./...`
Expected: FAIL — `undefined: mosaicLayout` / `mosaicInputIDs`.

- [ ] **Step 3: Implement**

Create `relay/surv_mosaic.go`:

```go
package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// mosaicLayout returns the grid shape (rows, cols) for n cells: a near-square
// grid, columns first (cols = ceil(sqrt(n))), matching the dashboard's niceCols.
func mosaicLayout(n int) (rows, cols int) {
	if n <= 0 {
		return 0, 0
	}
	cols = int(math.Ceil(math.Sqrt(float64(n))))
	rows = int(math.Ceil(float64(n) / float64(cols)))
	return rows, cols
}

// dvrChOf parses "dvrA_chB" into (A, B). ok=false for any other shape.
func dvrChOf(id string) (dvr, ch int, ok bool) {
	if !strings.HasPrefix(id, "dvr") {
		return 0, 0, false
	}
	rest := strings.TrimPrefix(id, "dvr")
	i := strings.Index(rest, "_ch")
	if i < 0 {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(rest[:i])
	b, err2 := strconv.Atoi(rest[i+3:])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return a, b, true
}

// mosaicInputIDs picks the base channel stream ids to compose — excluding main
// (@main) streams and the wall itself — sorted by (dvr, ch); unparseable ids
// sort last by id.
func mosaicInputIDs(stats []StreamStat) []string {
	var out []string
	for _, s := range stats {
		if s.ID == "wall" || isMainStreamID(s.ID) {
			continue
		}
		out = append(out, s.ID)
	}
	sort.Slice(out, func(i, j int) bool {
		di, ci, oki := dvrChOf(out[i])
		dj, cj, okj := dvrChOf(out[j])
		if oki && okj {
			if di != dj {
				return di < dj
			}
			return ci < cj
		}
		if oki != okj {
			return oki // parseable ids first
		}
		return out[i] < out[j]
	})
	return out
}

var _ = fmt.Sprintf // retained for later steps in this file
```

- [ ] **Step 4: Run, verify PASS**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -run 'TestMosaicLayout|TestMosaicInputIDs' ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add relay/surv_mosaic.go relay/surv_mosaic_test.go
git commit -m "feat(relay): mosaic layout + input-list math (pure, TDD)"
```

---

### Task 2: Mosaic env knobs (pure)

**Files:**
- Modify: `relay/surv_mosaic.go`
- Test: `relay/surv_mosaic_test.go`

**Interfaces:**
- Produces:
  - `wallEnabled() bool` — `os.Getenv("RELAY_WALL") == "1"`.
  - `wallDims() (w, h int)` — `RELAY_WALL_RES`: `"720p"`→(1280,720), anything else (incl. empty/`"1080p"`)→(1920,1080).
  - `wallFPS() int` — `RELAY_WALL_FPS` parsed; default 15; clamp 1..30.

- [ ] **Step 1: Write the failing test**

Append to `relay/surv_mosaic_test.go`:

```go
import "os" // ensure os is imported in this file's import block

func TestWallEnv(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		os.Unsetenv("RELAY_WALL")
		os.Unsetenv("RELAY_WALL_RES")
		os.Unsetenv("RELAY_WALL_FPS")
		if wallEnabled() {
			t.Fatal("wall should be disabled by default")
		}
		if w, h := wallDims(); w != 1920 || h != 1080 {
			t.Fatalf("default dims = %dx%d, want 1920x1080", w, h)
		}
		if wallFPS() != 15 {
			t.Fatalf("default fps = %d, want 15", wallFPS())
		}
	})
	t.Run("overrides", func(t *testing.T) {
		t.Setenv("RELAY_WALL", "1")
		t.Setenv("RELAY_WALL_RES", "720p")
		t.Setenv("RELAY_WALL_FPS", "10")
		if !wallEnabled() {
			t.Fatal("RELAY_WALL=1 should enable")
		}
		if w, h := wallDims(); w != 1280 || h != 720 {
			t.Fatalf("720p dims = %dx%d, want 1280x720", w, h)
		}
		if wallFPS() != 10 {
			t.Fatalf("fps = %d, want 10", wallFPS())
		}
	})
	t.Run("fps clamp", func(t *testing.T) {
		t.Setenv("RELAY_WALL_FPS", "999")
		if wallFPS() != 30 {
			t.Fatalf("fps clamp = %d, want 30", wallFPS())
		}
	})
}
```

- [ ] **Step 2: Run, verify FAIL**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -run TestWallEnv ./...`
Expected: FAIL — `undefined: wallEnabled`.

- [ ] **Step 3: Implement**

In `relay/surv_mosaic.go`, replace the `var _ = fmt.Sprintf ...` placeholder line with the real helpers and update imports (`os`, `strings`, `strconv` already needed):

```go
func wallEnabled() bool { return os.Getenv("RELAY_WALL") == "1" }

// wallDims is the mosaic canvas. 720p halves bandwidth (better remote); 1080p is
// crisper per-tile (LAN). Detail comes from click-to-enlarge regardless.
func wallDims() (w, h int) {
	if strings.TrimSpace(os.Getenv("RELAY_WALL_RES")) == "720p" {
		return 1280, 720
	}
	return 1920, 1080
}

func wallFPS() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("RELAY_WALL_FPS")))
	if err != nil || n <= 0 {
		return 15
	}
	if n > 30 {
		return 30
	}
	return n
}
```

Add `"os"` to the import block; drop the `var _ = fmt.Sprintf` line and the `"fmt"` import if now unused (it IS used in Task 3's args builder — keep `"fmt"`).

- [ ] **Step 4: Run, verify PASS**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -run TestWallEnv ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add relay/surv_mosaic.go relay/surv_mosaic_test.go
git commit -m "feat(relay): wall env knobs (RELAY_WALL/_RES/_FPS)"
```

---

### Task 3: ffmpeg mosaic arg builder (pure)

**Files:**
- Modify: `relay/surv_mosaic.go`
- Test: `relay/surv_mosaic_test.go`

**Interfaces:**
- Produces:
  - `evenDown(x int) int` — largest even integer <= x (H.264 needs even dims).
  - `mosaicArgs(inputURLs []string, rows, cols, cellW, cellH, fps int) []string` — full ffmpeg argv. Each input is decoded on CUDA; scaled to cellWxcellH with aspect-preserving letterbox; `tpad` clones the last frame after EOF (a dead camera holds its last frame instead of stalling the grid); `xstack` tiles them at computed offsets; NVENC encodes Annex-B to stdout.

- [ ] **Step 1: Write the failing test**

Append to `relay/surv_mosaic_test.go`:

```go
import "strings" // ensure imported

func TestMosaicArgs(t *testing.T) {
	args := mosaicArgs([]string{"http://a/0.m3u8", "http://a/1.m3u8"}, 1, 2, 640, 360, 15)
	joined := strings.Join(args, " ")
	// both inputs present
	if !strings.Contains(joined, "-i http://a/0.m3u8") || !strings.Contains(joined, "-i http://a/1.m3u8") {
		t.Fatalf("missing inputs: %s", joined)
	}
	// per-input scale+pad+tpad and a 2-up xstack at x offsets 0 and 640
	if !strings.Contains(joined, "scale=640:360") || !strings.Contains(joined, "tpad=stop=-1:stop_mode=clone") {
		t.Fatalf("missing scale/tpad: %s", joined)
	}
	if !strings.Contains(joined, "xstack=inputs=2:layout=0_0|640_0") {
		t.Fatalf("missing/incorrect xstack layout: %s", joined)
	}
	// NVENC + Annex-B pipe + AUD bsf
	for _, want := range []string{"h264_nvenc", "h264_metadata=aud=insert", "-f h264 pipe:1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in: %s", want, joined)
		}
	}
}

func TestEvenDown(t *testing.T) {
	for in, want := range map[int]int{640: 640, 641: 640, 0: 0, 7: 6} {
		if got := evenDown(in); got != want {
			t.Fatalf("evenDown(%d) = %d, want %d", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run, verify FAIL**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -run 'TestMosaicArgs|TestEvenDown' ./...`
Expected: FAIL — `undefined: mosaicArgs`.

- [ ] **Step 3: Implement**

Append to `relay/surv_mosaic.go`:

```go
func evenDown(x int) int {
	if x < 0 {
		return 0
	}
	return x - x%2
}

// mosaicArgs builds the ffmpeg invocation. Inputs are CUDA-decoded self-HLS;
// each is letterboxed into a cell and held on its last frame after EOF (tpad),
// then tiled with xstack and NVENC-encoded to an AUD-delimited Annex-B pipe.
func mosaicArgs(inputURLs []string, rows, cols, cellW, cellH, fps int) []string {
	args := []string{"-hide_banner", "-loglevel", "error"}
	for _, u := range inputURLs {
		args = append(args,
			"-rw_timeout", "15000000",
			"-hwaccel", "cuda",
			"-i", u,
		)
	}
	var fc strings.Builder
	for i := range inputURLs {
		fmt.Fprintf(&fc,
			"[%d:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,fps=%d,tpad=stop=-1:stop_mode=clone,setsar=1[v%d];",
			i, cellW, cellH, cellW, cellH, fps, i)
	}
	for i := range inputURLs {
		fmt.Fprintf(&fc, "[v%d]", i)
	}
	fc.WriteString("xstack=inputs=" + strconv.Itoa(len(inputURLs)) + ":layout=")
	for i := range inputURLs {
		if i > 0 {
			fc.WriteByte('|')
		}
		x := (i % cols) * cellW
		y := (i / cols) * cellH
		fmt.Fprintf(&fc, "%d_%d", x, y)
	}
	fc.WriteString("[out]")

	args = append(args,
		"-filter_complex", fc.String(),
		"-map", "[out]",
		"-an",
		"-r", strconv.Itoa(fps),
		"-c:v", "h264_nvenc", "-preset", "p4", "-tune", "ll",
		"-b:v", "4M", "-maxrate", "6M", "-bufsize", "6M",
		"-g", strconv.Itoa(fps*2), "-bf", "0",
		"-bsf:v", "h264_metadata=aud=insert",
		"-f", "h264", "pipe:1",
	)
	return args
}
```

- [ ] **Step 4: Run, verify PASS**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -run 'TestMosaicArgs|TestEvenDown' ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add relay/surv_mosaic.go relay/surv_mosaic_test.go
git commit -m "feat(relay): ffmpeg mosaic arg builder (NVENC xstack, TDD)"
```

---

### Task 4: Shared ffmpeg supervise loop (refactor)

**Files:**
- Modify: `relay/surv_transcode.go`

**Interfaces:**
- Produces: `(e *streamEntry) superviseEncode(ctx context.Context, args []string)` — runs `ffmpeg <args>`, pipes stdout into `e.ingestAnnexB`, restarts with backoff until ctx is cancelled. (Generalized from `superviseTranscode`.)
- Consumes: existing `(*streamEntry).ingestAnnexB`.

- [ ] **Step 1: Refactor `superviseTranscode` to delegate**

In `relay/surv_transcode.go`, replace the body of `superviseTranscode` so it builds args and calls the shared loop, and extract the loop:

```go
func (e *streamEntry) superviseTranscode(ctx context.Context, srcHLSURL string) {
	e.superviseEncode(ctx, transcodeArgs(srcHLSURL))
}

// superviseEncode runs ffmpeg with the given args, feeding its Annex-B stdout into
// the channel's WS hub, and restarts it with backoff until ctx is cancelled.
func (e *streamEntry) superviseEncode(ctx context.Context, args []string) {
	backoff := 3 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		started := time.Now()
		cmd := exec.CommandContext(ctx, "ffmpeg", args...)
		stdout, err := cmd.StdoutPipe()
		if err == nil {
			if err = cmd.Start(); err == nil {
				e.ingestAnnexB(stdout)
				err = cmd.Wait()
			}
		}
		if ctx.Err() != nil {
			return
		}
		log.Printf("[encode] %s: ffmpeg exited (%v) — restarting in %s", e.id, err, backoff)
		if time.Since(started) > 30*time.Second {
			backoff = 3 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}
```

Delete the old loop body that used to live inside `superviseTranscode` (the `for { ... }` with `transcodeArgs(srcHLSURL)`), since it now lives in `superviseEncode`.

- [ ] **Step 2: Build + existing tests still pass**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -run 'TestTranscodeEnabledFor|TestSplitNALUnits' ./...`
Expected: build OK, tests PASS (behavior unchanged — pure refactor).

- [ ] **Step 3: Commit**

```bash
git add relay/surv_transcode.go
git commit -m "refactor(relay): extract superviseEncode shared by transcode + mosaic"
```

---

### Task 5: Mosaic lifecycle — EnsureMosaic / stopMosaic / state

**Files:**
- Modify: `relay/surv_mosaic.go`, `relay/surv_proxy.go`
- Test: `relay/surv_mosaic_test.go`

**Interfaces:**
- Produces:
  - `type mosaicCell struct { I int \`json:"i"\`; ID string \`json:"id"\`; Name string \`json:"name"\` }`
  - `type mosaicState struct { sig string; rows, cols, fps int; cells []mosaicCell; cancel context.CancelFunc }`
  - `SurvProxy.mosaic *mosaicState` field (guarded by `SurvProxy.mu`).
  - `(sp *SurvProxy) EnsureMosaic(agentID string)` — idempotent: builds the input set from current `StreamStats`; if no base channels, no-op; if a mosaic with the same signature already runs, no-op; otherwise (re)starts it.
  - `(sp *SurvProxy) stopMosaic()` — cancels + removes the wall entry and clears state.
  - `mosaicSig(ids []string) string` — `strings.Join(ids, ",")` (set-change detector).
- Consumes: `mosaicInputIDs`, `mosaicLayout`, `wallDims`, `wallFPS`, `evenDown`, `mosaicArgs`, `(*streamEntry).superviseEncode`, `relaySelfHLSBase`, `streamPath`, `newSurvWSHub`, `newFragMuxerH264`.

- [ ] **Step 1: Write the failing test (signature builder)**

Append to `relay/surv_mosaic_test.go`:

```go
func TestMosaicSig(t *testing.T) {
	if mosaicSig([]string{"dvr1_ch1", "dvr1_ch2"}) != "dvr1_ch1,dvr1_ch2" {
		t.Fatal("sig should join ids with commas")
	}
	if mosaicSig(nil) != "" {
		t.Fatal("empty sig for no inputs")
	}
}
```

- [ ] **Step 2: Run, verify FAIL**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -run TestMosaicSig ./...`
Expected: FAIL — `undefined: mosaicSig`.

- [ ] **Step 3: Implement state + lifecycle**

Add the `mosaic` field to `SurvProxy` in `relay/surv_proxy.go` (inside the struct, near `streams`):

```go
	mosaic *mosaicState // running live-wall composite, if any (guarded by mu)
```

Append to `relay/surv_mosaic.go` (add imports `context`, `strings`):

```go
type mosaicCell struct {
	I    int    `json:"i"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type mosaicState struct {
	sig        string
	rows, cols int
	fps        int
	cells      []mosaicCell
	cancel     context.CancelFunc
}

func mosaicSig(ids []string) string { return strings.Join(ids, ",") }

// EnsureMosaic (re)builds the agent's live-wall composite to match its current
// channel set. Idempotent: a no-op when nothing changed, a restart when the set
// changed, a no-op when there are no base channels.
func (sp *SurvProxy) EnsureMosaic(agentID string) {
	stats := sp.StreamStats()
	ids := mosaicInputIDs(stats)
	if len(ids) == 0 {
		sp.stopMosaic()
		return
	}
	sig := mosaicSig(ids)

	sp.mu.Lock()
	if sp.mosaic != nil && sp.mosaic.sig == sig {
		sp.mu.Unlock()
		return // already running with this exact channel set
	}
	// (re)build: drop any prior mosaic + wall entry first
	if sp.mosaic != nil && sp.mosaic.cancel != nil {
		sp.mosaic.cancel()
	}
	if e, ok := sp.streams["wall"]; ok {
		sp.stopEntryLocked(e)
		delete(sp.streams, "wall")
	}

	rows, cols := mosaicLayout(len(ids))
	w, h := wallDims()
	fps := wallFPS()
	cellW := evenDown(w / cols)
	cellH := evenDown(h / rows)

	inputs := make([]string, len(ids))
	cells := make([]mosaicCell, len(ids))
	nameByID := map[string]string{}
	for _, s := range stats {
		nameByID[s.ID] = s.Name
	}
	for i, id := range ids {
		inputs[i] = relaySelfHLSBase() + "/surv/" + streamPath(agentID, id) + "/index.m3u8"
		cells[i] = mosaicCell{I: i, ID: id, Name: nameByID[id]}
	}

	ctx, cancel := context.WithCancel(context.Background())
	entry := &streamEntry{id: "wall", name: "wall", wsHub: newSurvWSHub(), cancel: cancel, proxy: sp}
	sp.streams["wall"] = entry
	sp.mosaic = &mosaicState{sig: sig, rows: rows, cols: cols, fps: fps, cells: cells, cancel: cancel}
	sp.mu.Unlock()

	go entry.superviseEncode(ctx, mosaicArgs(inputs, rows, cols, cellW, cellH, fps))
	go sp.reapMosaicWhenIdle("wall")
	log.Printf("[mosaic] %s: wall %dx%d (%d ch) <- self-HLS", agentID, rows, cols, len(ids))
}

// stopMosaic cancels and removes a running wall.
func (sp *SurvProxy) stopMosaic() {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.mosaic != nil && sp.mosaic.cancel != nil {
		sp.mosaic.cancel()
	}
	sp.mosaic = nil
	if e, ok := sp.streams["wall"]; ok {
		sp.stopEntryLocked(e)
		delete(sp.streams, "wall")
	}
}
```

`log` is already imported in `surv_mosaic.go`? It is not yet — add `"log"` to the import block.

- [ ] **Step 4: Run, verify PASS + build**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go test -run TestMosaicSig ./... && PATH=/opt/homebrew/bin:$PATH go build ./...`
Expected: PASS; build will FAIL with `undefined: reapMosaicWhenIdle` — implemented in Task 6. (If running this task in isolation, temporarily stub `func (sp *SurvProxy) reapMosaicWhenIdle(id string) {}`; Task 6 replaces it. Otherwise implement Task 6 before building.)

- [ ] **Step 5: Commit** (after Task 6 builds, or with the stub)

```bash
git add relay/surv_mosaic.go relay/surv_proxy.go relay/surv_mosaic_test.go
git commit -m "feat(relay): mosaic lifecycle — EnsureMosaic/stopMosaic + state"
```

---

### Task 6: Lazy start, idle reaper, and config-change rebuild

**Files:**
- Modify: `relay/surv_mosaic.go` (reaper), `relay/surv_router.go` (lazy start), `relay/surv_proxy.go` (`HandleSurvConfig` rebuild + `StopAll`)

**Interfaces:**
- Produces: `(sp *SurvProxy) reapMosaicWhenIdle(id string)` — every 10s, if the wall hub has 0 clients for >=30s, calls `stopMosaic` and returns.
- Modifies: `ServeSurvWS` lazy-starts the mosaic when the requested stream is `wall`; `HandleSurvConfig` calls `EnsureMosaic` at the end iff a mosaic is already running (keep it matched to the live channel set); `StopAll` calls `stopMosaic`.

- [ ] **Step 1: Implement the idle reaper**

Append to `relay/surv_mosaic.go` (add `"time"` import):

```go
// reapMosaicWhenIdle stops the wall after a grace period with no WS watchers, so
// the GPU is idle when nobody is looking. Exits once it stops the mosaic (a new
// viewer lazy-restarts it).
func (sp *SurvProxy) reapMosaicWhenIdle(id string) {
	const grace = 30 * time.Second
	idleSince := time.Time{}
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for range tick.C {
		sp.mu.RLock()
		e, ok := sp.streams[id]
		sp.mu.RUnlock()
		if !ok || e.wsHub == nil {
			return // mosaic gone
		}
		if e.wsHub.ClientCount() > 0 {
			idleSince = time.Time{}
			continue
		}
		if idleSince.IsZero() {
			idleSince = time.Now()
			continue
		}
		if time.Since(idleSince) >= grace {
			sp.stopMosaic()
			log.Printf("[mosaic] %s idle — stopped", id)
			return
		}
	}
}
```

(Note: `time.Now()`/tickers are fine in the relay; the no-`Date.now` rule is a *workflow-script* constraint, not a relay-code one.)

- [ ] **Step 2: Lazy-start on WS connect**

In `relay/surv_router.go`, edit `ServeSurvWS` so a request for the wall starts it first:

```go
func (h *Hub) ServeSurvWS(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/surv/ws/")
	agentID, rest := h.splitSurvPath(p)
	s := h.sessionByID(agentID)
	if s == nil {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	if rest == "wall" {
		s.survProxy.EnsureMosaic(agentID) // lazy-start the composite on first viewer
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/surv/ws/" + rest
	s.survProxy.ServeWS(w, r2)
}
```

- [ ] **Step 3: Rebuild on config change + stop on StopAll**

In `relay/surv_proxy.go`, at the very end of `HandleSurvConfig` (after the stop-removed-channels block), add:

```go
	// Keep a running wall matched to the live channel set (no-op if none running).
	sp.mu.RLock()
	running := sp.mosaic != nil
	sp.mu.RUnlock()
	if running {
		sp.EnsureMosaic(cfg.AgentID)
	}
```

Confirm `proto.SurvConfig` has an `AgentID` field (set by `stampSurvConfigAgentID`); the local var is `cfg`. If the field is named differently, use that name.

In `StopAll`, before/after stopping streams, add `sp.stopMosaic()` — but `StopAll` already holds `sp.mu`; call an unlocked variant. Simplest: in `StopAll`, after the loop that deletes streams, also clear `sp.mosaic`:

```go
	if sp.mosaic != nil && sp.mosaic.cancel != nil {
		sp.mosaic.cancel()
	}
	sp.mosaic = nil
```

(The `wall` entry is already covered by the existing `for id, entry := range sp.streams` stop loop.)

Remove the temporary `reapMosaicWhenIdle` stub from Task 5 if you added one.

- [ ] **Step 4: Build + full race tests**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go build ./... && PATH=/opt/homebrew/bin:$PATH go test -race ./... && gofmt -l surv_mosaic.go surv_proxy.go surv_router.go surv_transcode.go`
Expected: build OK, all tests PASS, gofmt prints nothing.

- [ ] **Step 5: Commit**

```bash
git add relay/surv_mosaic.go relay/surv_router.go relay/surv_proxy.go
git commit -m "feat(relay): wall lazy-start, idle reaper, config-change rebuild"
```

---

### Task 7: Layout API + wall page route (server)

**Files:**
- Modify: `relay/dashboard.go`, `relay/main.go`
- Create: `relay/dashboard_assets/wall.html`

**Interfaces:**
- Produces:
  - `(h *Hub) HandleWallLayout(w, r)` — `GET /dashboard/api/wall-layout?agent=<id>`: `authedDashboard`-gated; resolves the session, calls `EnsureMosaic`, returns JSON `{enabled, rows, cols, fps, cells:[{i,id,name}], agent}`. When `!wallEnabled()` returns `{enabled:false}`.
  - `(h *Hub) HandleWallPage(w, r)` — `GET /dashboard/wall`: `authedDashboard`-gated; serves `dashboard_assets/wall.html`.
- Consumes: `authedDashboard`, `sessionByID`, `EnsureMosaic`, `sp.mosaic`, `wallEnabled`, `wallFPS`, embedded `dashboardAssets`.

- [ ] **Step 1: Implement handlers**

Add to `relay/dashboard.go`:

```go
func (h *Hub) HandleWallLayout(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	type resp struct {
		Enabled    bool         `json:"enabled"`
		Agent      string       `json:"agent"`
		Rows, Cols int          `json:"rows"`
		ColsX      int          `json:"cols"`
		FPS        int          `json:"fps"`
		Cells      []mosaicCell `json:"cells"`
	}
	if !wallEnabled() {
		json.NewEncoder(w).Encode(resp{Enabled: false})
		return
	}
	agent := r.URL.Query().Get("agent")
	if agent == "" {
		agent = "default"
	}
	s := h.sessionByID(agent)
	if s == nil {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	s.survProxy.EnsureMosaic(agent)
	s.survProxy.mu.RLock()
	m := s.survProxy.mosaic
	out := resp{Enabled: true, Agent: agent}
	if m != nil {
		out.Rows, out.ColsX, out.FPS, out.Cells = m.rows, m.cols, m.fps, m.cells
	}
	s.survProxy.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (h *Hub) HandleWallPage(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}
	b, err := dashboardAssets.ReadFile("dashboard_assets/wall.html")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}
```

Fix the `resp` struct so `rows`/`cols` serialize correctly (Go can't have two `json:"cols"`); use:

```go
	type resp struct {
		Enabled bool         `json:"enabled"`
		Agent   string       `json:"agent"`
		Rows    int          `json:"rows"`
		Cols    int          `json:"cols"`
		FPS     int          `json:"fps"`
		Cells   []mosaicCell `json:"cells"`
	}
```

and assign `out.Rows, out.Cols, out.FPS, out.Cells = m.rows, m.cols, m.fps, m.cells`.

Confirm `json` and `net/http` are imported in `dashboard.go` (they are — other handlers use them).

- [ ] **Step 2: Register routes**

In `relay/main.go`, where dashboard routes are registered (near the other `/dashboard/...` HandleFunc calls, guarded by `if cfg.DashboardToken != ""`), add:

```go
		mux.HandleFunc("/dashboard/wall", hub.HandleWallPage)
		mux.HandleFunc("/dashboard/api/wall-layout", hub.HandleWallLayout)
```

These must be registered BEFORE the catch-all `/dashboard*` static/SPA handler so they win.

- [ ] **Step 3: Minimal wall.html (replaced fully in Task 8)**

Create `relay/dashboard_assets/wall.html` with a stub so the route resolves:

```html
<!doctype html><meta charset="utf-8"><title>Live Wall</title>
<body style="margin:0;background:#000"><script src="/dashboard/assets/wall.js"></script></body>
```

- [ ] **Step 4: Build + smoke**

Run: `cd relay && PATH=/opt/homebrew/bin:$PATH go build ./... && gofmt -l dashboard.go main.go`
Expected: build OK, gofmt clean.

- [ ] **Step 5: Commit**

```bash
git add relay/dashboard.go relay/main.go relay/dashboard_assets/wall.html
git commit -m "feat(relay): wall layout API + /dashboard/wall route"
```

---

### Task 8: Wall page client (video + overlay grid + click-to-enlarge)

**Files:**
- Create: `relay/dashboard_assets/wall.js`
- Modify: `relay/dashboard_assets/wall.html`, `relay/dashboard_assets/style.css`, `relay/dashboard_assets/index.html`

**Interfaces:**
- Consumes (loaded as plain `<script>` before wall.js): `playWS` from app.js is NOT available standalone (app.js is the full dashboard). To keep the wall lean, copy the minimal MSE player into a small shared file the wall can include. Reuse the existing pure file `livestream.js` (`liveWsPath`) and inline a compact `playWS` in wall.js (same protocol the relay serves).

**Note for implementer:** `playWS` lives inside app.js (the full dashboard bundle), which the lean wall page must not load. Extract `playWS` + `_msCtor`/`codecFromInit` into a new shared file `relay/dashboard_assets/wsplayer.js` and have BOTH app.js (via `<script>` include in index.html, replacing its local copy) and wall.js use it. This is a DRY extraction, not a rewrite.

- [ ] **Step 1: Extract the shared MSE player**

Create `relay/dashboard_assets/wsplayer.js` containing `_isIOS`, `_msCtor`, `_wsUsable`, `_hx`, `codecFromInit`, and `playWS` (the 4-arg version with onFail/onEnd), moved verbatim from app.js. Guard the bottom for tests is unnecessary (DOM code), but keep functions global.

In `relay/dashboard_assets/index.html`, add `<script src="/dashboard/assets/wsplayer.js"></script>` BEFORE `app.js`. In `app.js`, DELETE the now-duplicated `_msCtor`/`codecFromInit`/`playWS`/`_isIOS` definitions (they come from wsplayer.js). Keep `_wsUsable` wherever it is used.

Verify: `node --check relay/dashboard_assets/wsplayer.js && node --check relay/dashboard_assets/app.js`.

- [ ] **Step 2: Write wall.js**

Create `relay/dashboard_assets/wall.js`:

```javascript
// Live Wall: one composited <video> + a transparent grid overlay (names + click
// targets). Clicking a cell opens that channel's single live stream. No rec/UI.
(function(){
  var qs=new URLSearchParams(location.search);
  var agent=qs.get('agent')||'default';
  var video=document.getElementById('wallvid');
  var overlay=document.getElementById('wallgrid');
  var wsBase=(location.protocol==='https:'?'wss':'ws')+'://'+location.host+'/surv/ws/';
  var wallPath=(agent==='default'?'wall':agent+'/wall');
  var player=null, backoff=0, reconn=null;

  function playWall(){
    if(player){ try{player.close&&player.close();}catch(e){} player=null; }
    player=playWS(video, wsBase+wallPath, function(){}, function(){ reconnect(); });
  }
  function reconnect(){
    backoff=backoff?Math.min(backoff*2,15000):1000;
    clearTimeout(reconn); reconn=setTimeout(playWall, backoff);
  }
  // reset backoff while frames advance
  setInterval(function(){ if(video.currentTime>0 && !video.paused) backoff=0; }, 3000);

  function buildOverlay(layout){
    overlay.style.gridTemplateColumns='repeat('+layout.cols+',1fr)';
    overlay.style.gridTemplateRows='repeat('+layout.rows+',1fr)';
    overlay.innerHTML='';
    (layout.cells||[]).forEach(function(c){
      var d=document.createElement('button');
      d.className='wcell'; d.title=c.name||c.id;
      d.innerHTML='<span class="wname"></span>';
      d.querySelector('.wname').textContent=c.name||c.id;
      d.addEventListener('click', function(){ enlarge(c); });
      overlay.appendChild(d);
    });
  }
  function enlarge(c){
    var big=document.getElementById('wallbig'), bv=document.getElementById('wallbigvid');
    big.classList.add('show');
    var p=playWS(bv, wsBase+(agent==='default'?'':agent+'/')+c.id, function(){}, function(){});
    big._p=p;
    big.querySelector('.wbig-name').textContent=c.name||c.id;
    function close(){ big.classList.remove('show'); if(big._p){try{big._p.close&&big._p.close();}catch(e){}} big._p=null; bv.removeAttribute('src'); document.removeEventListener('keydown', onKey); }
    function onKey(e){ if(e.key==='Escape') close(); }
    document.addEventListener('keydown', onKey);
    big.querySelector('.wbig-close').onclick=close;
    big.onclick=function(e){ if(e.target===big) close(); };
  }

  function loadLayout(){
    fetch('/dashboard/api/wall-layout?agent='+encodeURIComponent(agent),{credentials:'same-origin'})
      .then(function(r){ return r.json(); })
      .then(function(l){
        if(!l.enabled){ document.getElementById('wallmsg').textContent='Live Wall 비활성 (RELAY_WALL=1 필요)'; return; }
        if(!l.cells || !l.cells.length){ document.getElementById('wallmsg').textContent='활성 채널 없음'; return; }
        document.getElementById('wallmsg').textContent='';
        buildOverlay(l);
        playWall();
      })
      .catch(function(){ document.getElementById('wallmsg').textContent='레이아웃 로드 실패'; });
  }
  // re-fetch layout on tab re-show (channel set may have changed)
  document.addEventListener('visibilitychange', function(){ if(!document.hidden){ backoff=0; loadLayout(); } });
  loadLayout();
})();
```

- [ ] **Step 3: Final wall.html**

Replace `relay/dashboard_assets/wall.html`:

```html
<!doctype html>
<html lang="ko"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Live Wall</title>
<link rel="stylesheet" href="/dashboard/assets/style.css">
</head><body class="wallbody">
<div class="wallwrap">
  <video id="wallvid" class="wallvid" muted autoplay playsinline></video>
  <div id="wallgrid" class="wallgrid"></div>
  <div id="wallmsg" class="wallmsg">로딩…</div>
</div>
<div id="wallbig" class="wallbig">
  <video id="wallbigvid" class="wallbigvid" muted autoplay playsinline></video>
  <div class="wbig-bar"><span class="wbig-name"></span><button class="wbig-close">✕</button></div>
</div>
<script src="/dashboard/assets/wsplayer.js"></script>
<script src="/dashboard/assets/wall.js"></script>
</body></html>
```

- [ ] **Step 4: Styles**

Append to `relay/dashboard_assets/style.css`:

```css
.wallbody{margin:0;background:#000;overflow:hidden}
.wallwrap{position:fixed;inset:0;background:#000}
.wallvid{position:absolute;inset:0;width:100%;height:100%;object-fit:contain;background:#000}
.wallgrid{position:absolute;inset:0;display:grid;gap:0;pointer-events:none}
.wcell{pointer-events:auto;background:transparent;border:1px solid rgba(255,255,255,.06);cursor:pointer;position:relative;padding:0}
.wcell:hover{border-color:rgba(80,160,255,.7);background:rgba(80,160,255,.08)}
.wname{position:absolute;left:6px;bottom:6px;font:600 12px/1.2 system-ui,sans-serif;color:#fff;text-shadow:0 1px 3px #000;background:rgba(0,0,0,.4);padding:2px 6px;border-radius:4px;pointer-events:none}
.wallmsg{position:absolute;left:50%;top:50%;transform:translate(-50%,-50%);color:#9aa;font:500 14px system-ui}
.wallbig{position:fixed;inset:0;background:#000;display:none;z-index:10}
.wallbig.show{display:block}
.wallbigvid{position:absolute;inset:0;width:100%;height:100%;object-fit:contain;background:#000}
.wbig-bar{position:absolute;top:0;left:0;right:0;display:flex;justify-content:space-between;align-items:center;padding:10px 14px;color:#fff;background:linear-gradient(#000a,transparent)}
.wbig-name{font:600 15px system-ui}
.wbig-close{background:#0006;color:#fff;border:0;width:34px;height:34px;border-radius:50%;font-size:16px;cursor:pointer}
```

- [ ] **Step 5: Syntax check + build + JS tests**

Run:
```
node --check relay/dashboard_assets/wall.js && node --check relay/dashboard_assets/wsplayer.js && node --check relay/dashboard_assets/app.js
cd relay && PATH=/opt/homebrew/bin:$PATH go build ./... && node --test jstest/*.test.js
```
Expected: all checks pass; go build OK (embed bundles new assets); 15 JS tests still pass.

- [ ] **Step 6: Commit**

```bash
git add relay/dashboard_assets/wsplayer.js relay/dashboard_assets/wall.js relay/dashboard_assets/wall.html relay/dashboard_assets/style.css relay/dashboard_assets/index.html relay/dashboard_assets/app.js
git commit -m "feat(dashboard): lean Live Wall page — one composited video + click-to-enlarge"
```

---

### Task 9: Compose + env docs

**Files:**
- Modify: `relay/docker-compose.yml`, `relay/.env.example`

- [ ] **Step 1: Add knobs to compose**

In `relay/docker-compose.yml`, in the relay `environment:` list (next to `RELAY_TRANSCODE_DVR`), add:

```yaml
      # Experimental live wall: relay GPU-composites all of an agent's channels
      # into ONE stream at /dashboard/wall (viewer decodes 1 video — no browser
      # decoder cap). Requires RELAY_RUNTIME=nvidia + NVIDIA_VISIBLE_DEVICES.
      - RELAY_WALL=${RELAY_WALL:-}
      - RELAY_WALL_RES=${RELAY_WALL_RES:-1080p}
      - RELAY_WALL_FPS=${RELAY_WALL_FPS:-15}
```

- [ ] **Step 2: Document in .env.example**

In `relay/.env.example`, under the transcode/GPU section, add:

```
# --- Experimental: Live Wall (GPU mosaic) ---
# Composite all of an agent's channels into ONE stream at /dashboard/wall, so the
# viewer decodes a single video (no browser decoder cap / no rotation). Requires
# the same GPU wiring as transcode-live (RELAY_RUNTIME=nvidia + NVIDIA_VISIBLE_DEVICES).
#RELAY_WALL=1
#RELAY_WALL_RES=1080p   # or 720p (half bandwidth, better remote)
#RELAY_WALL_FPS=15
```

- [ ] **Step 3: Commit**

```bash
git add relay/docker-compose.yml relay/.env.example
git commit -m "chore(relay): RELAY_WALL* knobs (compose + .env.example)"
```

---

## Self-Review

**Spec coverage:**
- Single composited stream / kill decoder cap → Tasks 1–6 (mosaic pipeline) + Task 8 (1-video client). ✅
- Inputs = self-HLS, no extra DVR pull → Task 5 input URLs. ✅
- Reuse fragMuxer/survWSHub/ingest/playWS → Task 4 (superviseEncode), Task 5 (wsHub entry), Task 8 (wsplayer extraction). ✅
- Lean `/wall` page, click-to-enlarge → Task 7 route (`/dashboard/wall` for cookie scope), Task 8 page. ✅ (Spec said `/wall`; refined to `/dashboard/wall` so the dashboard auth cookie, scoped to `/dashboard`, applies — noted.)
- Layout JSON → Task 7 `HandleWallLayout`. ✅
- Robustness (dead input = last frame; supervise restart; config rebuild) → Task 3 `tpad` clone, Task 4 supervise, Task 6 rebuild. ✅
- Lazy start + idle reaper → Task 6. ✅
- Per-agent → Task 5/6 use `agentID`; stream id `wall` in the agent's SurvProxy. ✅
- Res/fps knobs → Task 2 + Task 9. ✅
- GPU-absent → `/wall` errors / layout `enabled:false`; normal grid intact → Task 7. ✅
- Testing (pure layout/args/inputs) → Tasks 1–3, 5. ✅

**Placeholder scan:** none — every code step has full code. The Task 7 `resp` struct shows a wrong-then-corrected form; the implementer must use the corrected struct (single `cols`).

**Type consistency:** `mosaicCell{I,ID,Name}` used in Task 5 (build) and Task 7 (JSON) match. `EnsureMosaic(agentID string)` called from Task 6 router and Task 7 handler with the agent id. `superviseEncode(ctx, args)` defined Task 4, used Task 5. `mosaicArgs(inputs,rows,cols,cellW,cellH,fps)` defined Task 3, called Task 5. Stream id literal `"wall"` consistent across router, lifecycle, reaper.

**Open risk flagged in spec:** v1 single-ffmpeg `xstack` robustness — `tpad` clone covers EOF; a non-EOF stall still waits on that input. If observed in testing, escalate to the two-stage compositor (spec "future"). Not built in v1.
