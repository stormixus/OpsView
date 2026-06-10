package main

import (
	"archive/zip"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// HandleDashboardLPRFrames streams a zip of the most recent N pre-stored event
// snapshots for a stream (the .evthumbs JPEGs the agent captures at motion edges)
// — a ready real-camera test set for validating / fine-tuning a Korean LPR model.
// Admin-gated. ?stream=<path>[&n=N]  (filenames are unix-seconds; newest first).
func (h *Hub) HandleDashboardLPRFrames(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	stream := r.URL.Query().Get("stream")
	recDir := h.recRootDir()
	if stream == "" || recDir == "" || strings.Contains(stream, "..") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	n := 200
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 {
		n = v
	}
	dir := filepath.Join(recDir, filepath.FromSlash(stream), evThumbDir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		http.Error(w, "no snapshots", http.StatusNotFound)
		return
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jpg") {
			names = append(names, e.Name())
		}
	}
	// unix-second filenames are fixed-width, so reverse string sort == newest first
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) > n {
		names = names[:n]
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="lpr-frames-`+strings.ReplaceAll(stream, "/", "_")+`.zip"`)
	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		f, err := zw.Create(name)
		if err != nil {
			continue
		}
		f.Write(b)
	}
}
