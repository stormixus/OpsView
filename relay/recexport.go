package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const (
	recExportMaxSecs = 3600            // longest clip a single export may span (1h)
	recExportTimeout = 3 * time.Minute // ffmpeg wall-clock budget per export
)

// HandleDashboardRecExport cuts a [start,end] time window out of a stream's
// recorded segments and streams it back as a single MP4 download. ffmpeg
// concats the overlapping segments and copies (no re-encode) the requested
// window, emitting a fragmented MP4 so it can be piped without a seekable
// output. Admin-gated. ?stream=<path>&start=<unix>&end=<unix>.
func (h *Hub) HandleDashboardRecExport(w http.ResponseWriter, r *http.Request) {
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
	if start <= 0 || end <= start {
		http.Error(w, "bad range", http.StatusBadRequest)
		return
	}
	if end-start > recExportMaxSecs {
		http.Error(w, "range too long (max 1h)", http.StatusBadRequest)
		return
	}
	dir, ok := h.rec.safeStreamDir(stream)
	if !ok {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	segs := h.rec.segmentsForExport(stream, start, end)
	if len(segs) == 0 {
		http.Error(w, "no recordings in range", http.StatusNotFound)
		return
	}

	// concat list: absolute paths to the overlapping segments. Names are
	// charset-validated on disk (no quotes possible), so this can't inject.
	list, err := os.CreateTemp("", "opsview-clip-*.txt")
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	defer os.Remove(list.Name())
	for _, s := range segs {
		fmt.Fprintf(list, "file '%s'\n", filepath.Join(dir, s.Name))
	}
	list.Close()

	offset := start - segs[0].Start // seek into the first segment
	if offset < 0 {
		offset = 0
	}
	dur := end - start

	ctx, cancel := context.WithTimeout(r.Context(), recExportTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error",
		"-f", "concat", "-safe", "0",
		"-i", list.Name(),
		"-ss", strconv.FormatInt(offset, 10),
		"-t", strconv.FormatInt(dur, 10),
		"-c", "copy", "-an",
		"-movflags", "+frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4",
		"pipe:1",
	)
	fname := fmt.Sprintf("clip_%s-%s.mp4",
		time.Unix(start, 0).Local().Format("20060102_150405"),
		time.Unix(end, 0).Local().Format("150405"))
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`"`)
	cmd.Stdout = w
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		// Headers are likely already flushed, so we can't change the status —
		// just log the failure for diagnosis.
		log.Printf("[rec] export %s [%d-%d]: %v: %s", stream, start, end, err, errBuf.String())
	}
}
