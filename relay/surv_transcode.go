package main

import (
	"context"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/opsview/opsview/proto"
)

// Relay-side transcode-live (experimental, opt-in via RELAY_TRANSCODE_DVR).
//
// Normally a high-res-recording channel pulls TWO RTSP streams from the DVR: the
// sub stream for the live grid and the main stream for recording. On NVRs with a
// hard concurrent-stream cap that doubles the load and starves the live (sub)
// stream — the grid stutters. With transcode-live enabled for a DVR, its HD
// channels pull ONLY the main stream (recorded as before, copy) and the relay
// NVENC-downscales that same main stream into a low-res H.264 feed for the live
// grid. DVR connections drop 2->1 per channel and the live is a clean downscale
// of the main, not a second DVR pull.
//
// The transcoded feed is wired into the SAME fMP4-over-WebSocket hub the RTSP
// path uses (fragMuxer -> survWSHub), registered under the channel's base id, so
// the dashboard's existing live request for "dvrN_chM" transparently gets it.

const (
	transcodeFPS   = 15  // live frame rate after downscale
	transcodeWidth = 640 // height is derived to preserve aspect (scale=W:-2)
)

// transcodeEnabledFor reports whether this DVR is opted into transcode-live.
// RELAY_TRANSCODE_DVR is either the numeric DVR id (matched against the channel's
// "dvrN_chM" stream id) or the DVR's host/IP (matched against the LAN addr the
// relay pulls RTSP from). Unset disables transcode-live entirely.
func transcodeEnabledFor(chID string, dvr proto.DVRInfo) bool {
	v := strings.TrimSpace(os.Getenv("RELAY_TRANSCODE_DVR"))
	if v == "" {
		return false
	}
	if isAllDigits(v) {
		return strings.HasPrefix(chID, "dvr"+v+"_")
	}
	return hostOnly(dvr.Addr) == v || hostOnly(dvr.ExtAddr) == v
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// hostOnly strips an optional :port from an address.
func hostOnly(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// relaySelfHLSBase is the relay's own HLS origin — the transcode ffmpeg reads the
// main stream's already-running self-HLS (so the DVR is not pulled a second time),
// mirroring how the recorder sources its input.
func relaySelfHLSBase() string {
	return "http://127.0.0.1:" + getPort()
}

// nalType returns the H.264 NAL unit type (low 5 bits of the first byte).
func nalType(nal []byte) byte {
	if len(nal) == 0 {
		return 0
	}
	return nal[0] & 0x1F
}

// splitNALUnits splits an Annex-B byte run at start codes (00 00 01, with any
// number of leading zero bytes) into complete NAL units with the start codes
// stripped. The final NAL is not yet terminated by a following start code, so it
// is returned in rest (start code included) for the caller to prepend to the next
// chunk. When no start code is present everything is returned as rest.
func splitNALUnits(data []byte) (nals [][]byte, rest []byte) {
	var starts []int
	for i := 0; i+2 < len(data); {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			starts = append(starts, i)
			i += 3
		} else {
			i++
		}
	}
	if len(starts) == 0 {
		return nil, data
	}
	for k := 0; k+1 < len(starts); k++ {
		nal := data[starts[k]+3 : starts[k+1]]
		// Trailing zero bytes belong to the next 4-byte start code's leading zeros.
		for len(nal) > 0 && nal[len(nal)-1] == 0 {
			nal = nal[:len(nal)-1]
		}
		if len(nal) > 0 {
			nals = append(nals, nal)
		}
	}
	return nals, data[starts[len(starts)-1]:]
}

// groupNALsIntoAUs groups a NAL sequence into access units, beginning a new AU at
// each Access Unit Delimiter (type 9) and dropping the AUD itself. Used by the
// streaming reader's logic and unit-tested directly.
func groupNALsIntoAUs(nals [][]byte) [][][]byte {
	var aus [][][]byte
	var cur [][]byte
	for _, n := range nals {
		if nalType(n) == 9 {
			if len(cur) > 0 {
				aus = append(aus, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, n)
	}
	if len(cur) > 0 {
		aus = append(aus, cur)
	}
	return aus
}

// StartTranscodeChannel registers a transcode-live stream under id (sourced from
// srcHLSURL, the main stream's self-HLS) and supervises the ffmpeg pipeline until
// the channel is stopped. The entry has no RTSP client or HLS muxer — only the
// WebSocket hub the live grid consumes.
func (sp *SurvProxy) StartTranscodeChannel(id, name, srcHLSURL string) {
	sp.mu.Lock()
	if e, ok := sp.streams[id]; ok {
		sp.stopEntryLocked(e)
		delete(sp.streams, id)
	}
	ctx, cancel := context.WithCancel(context.Background())
	entry := &streamEntry{id: id, name: name, wsHub: newSurvWSHub(), cancel: cancel, proxy: sp}
	sp.streams[id] = entry
	sp.mu.Unlock()

	go entry.superviseTranscode(ctx, srcHLSURL)
	log.Printf("[transcode] live %s <- %s (NVENC %dp)", id, srcHLSURL, transcodeWidth)
}

// transcodeArgs builds the ffmpeg invocation: GPU-decode the main self-HLS,
// downscale, NVENC-encode a low-latency H.264 elementary stream with an Access
// Unit Delimiter before every frame (so the reader has clean AU boundaries),
// emitted as raw Annex-B on stdout.
func transcodeArgs(srcHLSURL string) []string {
	return []string{
		"-hide_banner", "-loglevel", "error",
		"-rw_timeout", "15000000", // 15s I/O timeout so a dead source HLS doesn't hang
		"-hwaccel", "cuda",
		"-i", srcHLSURL,
		"-an",
		"-vf", "scale=" + strconv.Itoa(transcodeWidth) + ":-2,fps=" + strconv.Itoa(transcodeFPS),
		"-c:v", "h264_nvenc", "-preset", "p4", "-tune", "ll",
		"-b:v", "700k", "-maxrate", "1M", "-bufsize", "1M",
		"-g", strconv.Itoa(transcodeFPS * 2), "-bf", "0",
		"-bsf:v", "h264_metadata=aud=insert",
		"-f", "h264", "pipe:1",
	}
}

// superviseTranscode runs ffmpeg and restarts it (with backoff) until ctx is
// cancelled, feeding its Annex-B stdout into the channel's WS hub.
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

// ingestAnnexB reads an Annex-B H.264 elementary stream, groups it into access
// units at AUD boundaries, muxes each into an fMP4 fragment, and broadcasts it to
// the channel's WS hub — the same shape the RTSP path produces. PTS is synthesized
// at the fixed transcode frame rate (the raw elementary stream carries none).
func (e *streamEntry) ingestAnnexB(r io.Reader) {
	frag := newFragMuxerH264(nil, nil) // init is built lazily from in-band SPS/PPS
	e.mu.Lock()
	e.frag = frag
	e.mu.Unlock()

	step := int64(survWSTimescale / transcodeFPS)
	var pts int64
	var pending []byte
	var au [][]byte
	buf := make([]byte, 64*1024)

	emit := func() {
		if len(au) == 0 {
			return
		}
		fr, kf, newInit := frag.writeAU(pts, au)
		if newInit != nil {
			e.wsHub.setInit(newInit)
		}
		if fr != nil {
			e.wsHub.broadcast(fr, kf)
		}
		pts += step
		au = nil
	}

	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			nals, rest := splitNALUnits(pending)
			pending = append([]byte(nil), rest...)
			for _, nal := range nals {
				if nalType(nal) == 9 { // AUD -> AU boundary
					emit()
					continue
				}
				au = append(au, append([]byte(nil), nal...))
			}
		}
		if rerr != nil {
			return
		}
	}
}
