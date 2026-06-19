package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
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
func mosaicInputIDs(stats []StreamStat, dvrNum int) []string {
	var out []string
	for _, s := range stats {
		if strings.HasPrefix(s.ID, "wall") || isMainStreamID(s.ID) {
			continue
		}
		if dvrNum > 0 {
			if d, _, ok := dvrChOf(s.ID); !ok || d != dvrNum {
				continue // scope to one DVR
			}
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
		// Fill the cell ("cover"): scale up to cover cellWxcellH preserving aspect,
		// then center-crop the overflow — no black letterbox bars, no distortion
		// (a sliver of the edges is cropped instead).
		fmt.Fprintf(&fc,
			"[%d:v]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,fps=%d,tpad=stop=-1:stop_mode=clone,setsar=1[v%d];",
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
	agentID    string
}

func mosaicSig(ids []string) string { return strings.Join(ids, ",") }

// mosaicWallDVR parses the DVR scope from a wall stream id: "wall" -> 0 (whole
// agent, used by the dashboard), "walldvr<N>" -> N (one DVR, used by the viewer
// so each DVR tab shows only its own cameras). Returns -1 for a non-wall id.
func mosaicWallDVR(wallID string) int {
	if wallID == "wall" {
		return 0
	}
	if strings.HasPrefix(wallID, "walldvr") {
		if n, err := strconv.Atoi(strings.TrimPrefix(wallID, "walldvr")); err == nil && n > 0 {
			return n
		}
	}
	return -1
}

// EnsureMosaic (re)builds the live-wall composite identified by wallID ("wall" =
// whole agent, "walldvr<N>" = one DVR) to match its current channel set.
// Idempotent: no-op when unchanged, restart when the set changed, stop when empty.
// Multiple walls (one per watched DVR) can run concurrently, keyed by wallID.
func (sp *SurvProxy) EnsureMosaic(agentID, wallID string) {
	dvrNum := mosaicWallDVR(wallID)
	if dvrNum < 0 {
		return // not a recognized wall id
	}
	stats := sp.StreamStats()
	ids := mosaicInputIDs(stats, dvrNum)
	if len(ids) == 0 {
		sp.stopMosaicID(wallID)
		return
	}
	ids = applyWallOrder(wallOrderKey(agentID, wallID), ids) // operator's drag order, if any
	sig := mosaicSig(ids)

	sp.mu.Lock()
	if m := sp.mosaics[wallID]; m != nil && m.sig == sig {
		sp.mu.Unlock()
		return // already running with this exact channel set
	}
	// (re)build: drop any prior mosaic + entry for this wallID first
	if m := sp.mosaics[wallID]; m != nil && m.cancel != nil {
		m.cancel()
	}
	if e, ok := sp.streams[wallID]; ok {
		sp.stopEntryLocked(e)
		delete(sp.streams, wallID)
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
	entry := &streamEntry{id: wallID, name: wallID, wsHub: newSurvWSHub(), cancel: cancel, proxy: sp}
	sp.streams[wallID] = entry
	sp.mosaics[wallID] = &mosaicState{sig: sig, rows: rows, cols: cols, fps: fps, cells: cells, cancel: cancel, agentID: agentID}
	sp.mu.Unlock()

	go entry.superviseEncode(ctx, mosaicArgs(inputs, rows, cols, cellW, cellH, fps))
	go sp.reapMosaicWhenIdle(entry)
	log.Printf("[mosaic] %s: %s %dx%d (%d ch) <- self-HLS", agentID, wallID, rows, cols, len(ids))
}

// stopMosaicID cancels and removes one running wall.
func (sp *SurvProxy) stopMosaicID(wallID string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if m := sp.mosaics[wallID]; m != nil && m.cancel != nil {
		m.cancel()
	}
	delete(sp.mosaics, wallID)
	if e, ok := sp.streams[wallID]; ok {
		sp.stopEntryLocked(e)
		delete(sp.streams, wallID)
	}
}

// runningWalls snapshots the ids of all live walls (for config-change rebuilds)
// plus the agent they belong to.
func (sp *SurvProxy) runningWalls() (agentID string, wallIDs []string) {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	for id, m := range sp.mosaics {
		wallIDs = append(wallIDs, id)
		agentID = m.agentID
	}
	return agentID, wallIDs
}

// WallLayout snapshots a running wall's grid shape and ordered cells, so a client
// can place a click-target overlay that lines up with the composited tiles. ok is
// false when that wall is not running.
func (sp *SurvProxy) WallLayout(wallID string) (rows, cols, fps int, cells []mosaicCell, ok bool) {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	m := sp.mosaics[wallID]
	if m == nil {
		return 0, 0, 0, nil, false
	}
	return m.rows, m.cols, m.fps, m.cells, true
}

// reapMosaicWhenIdle stops the wall after a grace period with no WS watchers, so
// the GPU is idle when nobody is looking. Exits once it stops the mosaic (a new
// viewer lazy-restarts it).
func (sp *SurvProxy) reapMosaicWhenIdle(self *streamEntry) {
	const grace = 30 * time.Second
	idleSince := time.Time{}
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for range tick.C {
		sp.mu.RLock()
		e := sp.streams[self.id]
		sp.mu.RUnlock()
		if e != self { // replaced by a rebuild, or removed -> this reaper is stale
			return
		}
		if self.wsHub == nil {
			return
		}
		if self.wsHub.ClientCount() > 0 {
			idleSince = time.Time{}
			continue
		}
		if idleSince.IsZero() {
			idleSince = time.Now()
			continue
		}
		if time.Since(idleSince) >= grace {
			sp.stopMosaicID(self.id)
			log.Printf("[mosaic] %s idle — stopped", self.id)
			return
		}
	}
}
