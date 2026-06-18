package main

import (
	"fmt"
	"math"
	"os"
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
