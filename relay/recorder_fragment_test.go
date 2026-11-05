package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Test the muxer, not only the presence of command-line flags: before a nominal
// five-minute recording closes, a reader must already find initialization and
// a complete media fragment, probe its codec and decode its first frame.
func TestRecordingFragmentReadableBeforeRollover(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	dir := t.TempDir()
	input := filepath.Join(dir, "input.mp4")
	generate := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x96:rate=10", "-t", "3",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-g", "5", "-keyint_min", "5", "-sc_threshold", "0", "-pix_fmt", "yuv420p", input)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generate H264 fixture: %v: %s", err, output)
	}
	outDir := filepath.Join(dir, "recordings")
	if err := os.Mkdir(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-re", "-i", input}
	args = append(args, recordingOutputArgs(300, filepath.Join(outDir, "%Y%m%d_%H%M%S.mp4"))...)
	command := exec.CommandContext(ctx, ffmpeg, args...)
	var recordErrors bytes.Buffer
	command.Stderr = &recordErrors
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var recordErr error
	go func() { recordErr = command.Wait(); close(done) }()
	// Every fatal path must stop and reap the realtime recorder before TempDir
	// cleanup. Reading recordErrors is safe only after the done channel closes.
	defer func() { cancel(); <-done }()
	assertStillRecording := func() {
		t.Helper()
		select {
		case <-done:
			t.Fatalf("recording finished before active-file verification: %v: %s", recordErr, recordErrors.String())
		default:
		}
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var active string
	var observedBytes int
waitForFragment:
	for {
		select {
		case <-ctx.Done():
			t.Fatal("active recording never exposed a complete moov/moof/mdat fragment")
		case <-done:
			files, _ := filepath.Glob(filepath.Join(outDir, "*.mp4"))
			var finalizedBytes int
			var finalizedFragment bool
			if len(files) == 1 {
				data, _ := os.ReadFile(files[0])
				finalizedBytes = len(data)
				finalizedFragment = hasRecordingFragment(data)
			}
			t.Fatalf("no readable fragment before recorder exit: observed=%d bytes, finalized=%d bytes, finalizedFragment=%v, error=%v: %s", observedBytes, finalizedBytes, finalizedFragment, recordErr, recordErrors.String())
		case <-ticker.C:
			files, err := filepath.Glob(filepath.Join(outDir, "*.mp4"))
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 {
				continue
			}
			data, err := os.ReadFile(files[0])
			if len(data) > observedBytes {
				observedBytes = len(data)
			}
			if err == nil && hasRecordingFragment(data) {
				active = files[0]
				break waitForFragment
			}
		}
	}
	assertStillRecording()
	probeAndDecode := func(file string) {
		t.Helper()
		probe := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0",
			"-show_entries", "stream=codec_name", "-of", "csv=p=0", file)
		codec, err := probe.CombinedOutput()
		if err != nil || strings.TrimSpace(string(codec)) != "h264" {
			t.Fatalf("probe recording: codec=%q error=%v", codec, err)
		}
		decode := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i", file,
			"-frames:v", "1", "-pix_fmt", "yuv420p", "-f", "rawvideo", "pipe:1")
		var decodeErrors bytes.Buffer
		decode.Stderr = &decodeErrors
		frame, err := decode.Output()
		if err != nil || len(frame) != 160*96*3/2 {
			t.Fatalf("decode recording: %d frame bytes, error=%v: %s", len(frame), err, decodeErrors.String())
		}
	}
	probeAndDecode(active)
	assertStillRecording()
	select {
	case <-done:
		if recordErr != nil {
			t.Fatalf("recording failed: %v: %s", recordErr, recordErrors.String())
		}
	case <-ctx.Done():
		t.Fatal("realtime fixture did not finish within test deadline")
	}
	probeAndDecode(active)
	packetHashes := func(file string) []byte {
		t.Helper()
		probe := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0",
			"-show_packets", "-show_data_hash", "sha256", "-show_entries", "packet=data_hash", "-of", "csv=p=0", file)
		output, err := probe.CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("SHA256:")) {
			t.Fatalf("hash recording packets: %v: %s", err, output)
		}
		return output
	}
	if !bytes.Equal(packetHashes(input), packetHashes(active)) {
		t.Fatal("recording changed encoded H264 packet payloads; stream-copy quality was not preserved")
	}
}

// Parse complete top-level MP4 atoms; a coincidental 'moof' byte sequence inside
// media payload must not satisfy the early-readability condition.
func hasRecordingFragment(data []byte) bool {
	moov, moof := false, false
	for len(data) >= 8 {
		size := uint64(binary.BigEndian.Uint32(data[:4]))
		header := uint64(8)
		if size == 1 {
			if len(data) < 16 {
				return false
			}
			size, header = binary.BigEndian.Uint64(data[8:16]), 16
		}
		if size < header || size > uint64(len(data)) {
			return false
		}
		switch string(data[4:8]) {
		case "moov":
			moov = true
		case "moof":
			moof = true
		case "mdat":
			if moov && moof {
				return true
			}
		}
		data = data[size:]
	}
	return false
}
