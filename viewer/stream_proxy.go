package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// StreamProxy converts RTSP (or any ffmpeg-supported URL) to HLS for browser playback.
type StreamProxy struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	hlsDir  string
	running bool
}

func NewStreamProxy() *StreamProxy {
	dir := filepath.Join(os.TempDir(), "opsview-hls")
	os.MkdirAll(dir, 0755)
	return &StreamProxy{hlsDir: dir}
}

// StartStream begins transcoding the given URL to HLS segments.
// Returns the local HLS playlist path to play.
func (p *StreamProxy) StartStream(url string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Stop any existing stream
	p.stopLocked()

	// Clean HLS dir
	entries, _ := os.ReadDir(p.hlsDir)
	for _, e := range entries {
		os.Remove(filepath.Join(p.hlsDir, e.Name()))
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	m3u8Path := filepath.Join(p.hlsDir, "stream.m3u8")

	// ffmpeg: RTSP → HLS with low-latency settings
	args := []string{
		"-rtsp_transport", "tcp",
		"-i", url,
		"-c:v", "copy",
		"-an",
		"-f", "hls",
		"-hls_time", "1",
		"-hls_list_size", "5",
		"-hls_flags", "delete_segments+append_list",
		"-hls_segment_filename", filepath.Join(p.hlsDir, "seg_%03d.ts"),
		m3u8Path,
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil // Suppress ffmpeg output

	if err := cmd.Start(); err != nil {
		cancel()
		return "", fmt.Errorf("ffmpeg start: %w", err)
	}

	p.cmd = cmd
	p.running = true

	// Wait for m3u8 to appear (up to 8 seconds)
	go func() {
		cmd.Wait()
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
	}()

	for i := 0; i < 40; i++ {
		time.Sleep(200 * time.Millisecond)
		if _, err := os.Stat(m3u8Path); err == nil {
			log.Printf("[stream] HLS ready: %s", url)
			return "/ops/hls/stream.m3u8", nil
		}
	}

	p.stopLocked()
	return "", fmt.Errorf("ffmpeg did not produce HLS within timeout")
}

// StopStream stops the ffmpeg process.
func (p *StreamProxy) StopStream() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
}

func (p *StreamProxy) stopLocked() {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
		p.cmd = nil
	}
	p.running = false
}

// IsRunning returns whether ffmpeg is currently transcoding.
func (p *StreamProxy) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// ServeHLS handles HTTP requests for /ops/hls/* files.
func (p *StreamProxy) ServeHLS(w http.ResponseWriter, r *http.Request) {
	// /ops/hls/stream.m3u8 or /ops/hls/seg_001.ts
	filename := strings.TrimPrefix(r.URL.Path, "/ops/hls/")
	if filename == "" || strings.Contains(filename, "..") {
		http.Error(w, "not found", 404)
		return
	}

	filePath := filepath.Join(p.hlsDir, filename)
	data, err := os.ReadFile(filePath)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}

	if strings.HasSuffix(filename, ".m3u8") {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	} else if strings.HasSuffix(filename, ".ts") {
		w.Header().Set("Content-Type", "video/mp2t")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(data)
}
