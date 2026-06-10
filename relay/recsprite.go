package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"net/http"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

const (
	spriteCacheCap = 128
	spriteCellW    = 160
	spriteCellH    = 90 // 16:9, no distortion
	spriteMaxN     = 48
	spriteWorkers  = 6 // parallel ffmpeg extractions per sprite
)

var (
	spriteCache   = make(map[string][]byte)
	spriteCacheMu sync.Mutex
)

// spriteCellTimes returns n unix-second sample times at the CENTER of each of the
// n equal buckets spanning [start,end).
func spriteCellTimes(start, end int64, n int) []int64 {
	if n <= 0 || end <= start {
		return nil
	}
	step := float64(end-start) / float64(n)
	out := make([]int64, n)
	for i := 0; i < n; i++ {
		out[i] = start + int64(float64(i)*step+step/2)
	}
	return out
}

// assembleStrip stacks uniform cells (cellW x cellH each) into one vertical JPEG.
// nil cells (no recording / extract failed) become a dark placeholder.
func assembleStrip(cells []image.Image, cellW, cellH int) ([]byte, error) {
	canvas := image.NewRGBA(image.Rect(0, 0, cellW, cellH*len(cells)))
	ph := &image.Uniform{color.RGBA{10, 15, 21, 255}}
	for i, c := range cells {
		r := image.Rect(0, i*cellH, cellW, (i+1)*cellH)
		if c != nil {
			draw.Draw(canvas, r, c, c.Bounds().Min, draw.Src)
		} else {
			draw.Draw(canvas, r, ph, image.Point{}, draw.Src)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: 72}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// spriteCell extracts one spriteCellW x spriteCellH frame at unix-second t, or nil
// if there's no finalized segment covering t. (No evthumb fast-path here: cells
// must be uniform size for tiling, so we always ffmpeg at the exact cell size.)
func (h *Hub) spriteCell(stream string, t int64) image.Image {
	segs := h.rec.segmentsForExport(stream, t, t+1)
	var s recSegment
	found := false
	for _, seg := range segs {
		if seg.Start <= t && t < seg.Start+int64(seg.Dur) {
			s = seg
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	path, ok := h.rec.segmentFile(stream, s.Name)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), thumbTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-ss", strconv.FormatInt(t-s.Start, 10), "-i", path,
		"-frames:v", "1", "-vf", "scale="+strconv.Itoa(spriteCellW)+":"+strconv.Itoa(spriteCellH),
		"-q:v", "6", "-f", "mjpeg", "pipe:1")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil || out.Len() == 0 {
		return nil
	}
	img, err := jpeg.Decode(&out)
	if err != nil {
		return nil
	}
	return img
}

// HandleDashboardRecSprite returns one vertical filmstrip JPEG sampling n frames
// across [start,end] — a single HTTP round-trip instead of n thumbnail fetches,
// for fast scrub previews. Cells are extracted in parallel. Admin-gated.
// ?stream=<path>&start=<sec>&end=<sec>&n=<count>
func (h *Hub) HandleDashboardRecSprite(w http.ResponseWriter, r *http.Request) {
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
	n, _ := strconv.Atoi(q.Get("n"))
	if stream == "" || end <= start || n < 1 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if n > spriteMaxN {
		n = spriteMaxN
	}
	live := end >= time.Now().Unix()-int64(h.recSegSeconds())
	key := stream + "|" + strconv.FormatInt(start, 10) + "|" + strconv.FormatInt(end, 10) + "|" + strconv.Itoa(n)

	spriteCacheMu.Lock()
	if img, ok := spriteCache[key]; ok {
		spriteCacheMu.Unlock()
		writeSprite(w, img, n, start, end, live)
		return
	}
	spriteCacheMu.Unlock()

	// extract the n cells in parallel (bounded), then tile.
	times := spriteCellTimes(start, end, n)
	cells := make([]image.Image, n)
	var wg sync.WaitGroup
	sem := make(chan struct{}, spriteWorkers)
	for i, t := range times {
		wg.Add(1)
		go func(i int, t int64) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cells[i] = h.spriteCell(stream, t)
		}(i, t)
	}
	wg.Wait()

	jpg, err := assembleStrip(cells, spriteCellW, spriteCellH)
	if err != nil {
		http.Error(w, "encode", http.StatusInternalServerError)
		return
	}
	if !live { // only finalized ranges are immutable / worth caching
		spriteCacheMu.Lock()
		if len(spriteCache) >= spriteCacheCap {
			spriteCache = make(map[string][]byte)
		}
		spriteCache[key] = jpg
		spriteCacheMu.Unlock()
	}
	writeSprite(w, jpg, n, start, end, live)
}

func writeSprite(w http.ResponseWriter, jpg []byte, n int, start, end int64, live bool) {
	hdr := w.Header()
	hdr.Set("Content-Type", "image/jpeg")
	hdr.Set("X-Sprite-N", strconv.Itoa(n))
	hdr.Set("X-Sprite-Start", strconv.FormatInt(start, 10))
	hdr.Set("X-Sprite-End", strconv.FormatInt(end, 10))
	hdr.Set("X-Sprite-Cellw", strconv.Itoa(spriteCellW))
	hdr.Set("X-Sprite-Cellh", strconv.Itoa(spriteCellH))
	if live {
		hdr.Set("Cache-Control", "no-store")
	} else {
		hdr.Set("Cache-Control", "private, max-age=31536000, immutable")
	}
	w.Write(jpg)
}
