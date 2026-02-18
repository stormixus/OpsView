package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// channelStream holds an RTSP→WebRTC (H264) pipeline for one channel.
type channelStream struct {
	client *gortsplib.Client
	pc     *webrtc.PeerConnection
	track  *webrtc.TrackLocalStaticRTP
	cancel context.CancelFunc
	mu     sync.Mutex
}

func (cs *channelStream) stop() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.cancel != nil {
		cs.cancel()
		cs.cancel = nil
	}
	if cs.pc != nil {
		cs.pc.Close()
		cs.pc = nil
	}
	if cs.client != nil {
		cs.client.Close()
		cs.client = nil
	}
	cs.track = nil
}

// StreamResult is returned to the frontend.
type StreamResult struct {
	Method string `json:"method"` // "webrtc"
	SDP    string `json:"sdp"`    // WebRTC answer SDP
}

// StartChannelStream starts an RTSP→WebRTC pipeline for the given channel.
func (m *CCTVManager) StartChannelStream(dvrID int64, chNum int, offerSDP string) (*StreamResult, error) {
	key := fmt.Sprintf("%d_%d", dvrID, chNum)

	// Always replace existing stream for this key.
	// Reusing an old PeerConnection across page refreshes can return stale SDP.
	m.streamsMu.Lock()
	if cs, ok := m.streams[key]; ok {
		cs.stop()
		delete(m.streams, key)
	}
	m.streamsMu.Unlock()

	dvr, err := m.getDVR(dvrID)
	if err != nil {
		return nil, fmt.Errorf("get DVR: %w", err)
	}

	// Use DVR quality preference first, then fallback to the other stream.
	c, formaH264, baseURL, medi, err := m.connectH264(dvr, chNum)
	if err != nil {
		return nil, err
	}

	cs := &channelStream{client: c}

	if _, err := c.Setup(baseURL, medi, 0, 0); err != nil {
		c.Close()
		return nil, fmt.Errorf("RTSP setup: %w", err)
	}

	// Desktop app runs local frontend+backend, so host ICE candidates are enough.
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("WebRTC peer: %w", err)
	}

	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
		fmt.Sprintf("video_%d_%d", dvrID, chNum), fmt.Sprintf("cctv_%d", dvrID),
	)
	if err != nil {
		pc.Close()
		c.Close()
		return nil, fmt.Errorf("WebRTC track: %w", err)
	}
	if _, err := pc.AddTrack(track); err != nil {
		pc.Close()
		c.Close()
		return nil, fmt.Errorf("WebRTC add track: %w", err)
	}

	cs.pc = pc
	cs.track = track

	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}
	if err := pc.SetRemoteDescription(offer); err != nil {
		pc.Close()
		c.Close()
		return nil, fmt.Errorf("set offer: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		c.Close()
		return nil, fmt.Errorf("create answer: %w", err)
	}

	gatherDone := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		c.Close()
		return nil, fmt.Errorf("set answer: %w", err)
	}
	select {
	case <-gatherDone:
	case <-time.After(1200 * time.Millisecond):
		log.Printf("[cctv] DVR%d CH%d: ICE gather partial, continue", dvrID, chNum)
	}
	if pc.LocalDescription() == nil {
		pc.Close()
		c.Close()
		return nil, fmt.Errorf("missing local description")
	}

	// Fast path: forward camera RTP directly to WebRTC track (no decode/re-encode).
	c.OnPacketRTP(medi, formaH264, func(pkt *rtp.Packet) {
		cs.mu.Lock()
		t := cs.track
		cs.mu.Unlock()
		if t == nil {
			return
		}
		outPkt := pkt.Clone()
		if outPkt == nil {
			return
		}
		_ = t.WriteRTP(outPkt)
	})

	if _, err := c.Play(nil); err != nil {
		pc.Close()
		c.Close()
		return nil, fmt.Errorf("RTSP play: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cs.cancel = cancel

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[cctv] WebRTC DVR%d CH%d: %s", dvrID, chNum, state.String())
		if state == webrtc.PeerConnectionStateFailed ||
			state == webrtc.PeerConnectionStateClosed {
			cancel()
		}
	})

	go func() { <-ctx.Done(); c.Close() }()

	m.streamsMu.Lock()
	m.streams[key] = cs
	m.streamsMu.Unlock()

	log.Printf("[cctv] WebRTC stream started: DVR %d CH %d (H264)", dvrID, chNum)
	return &StreamResult{Method: "webrtc", SDP: pc.LocalDescription().SDP}, nil
}

// --- Lifecycle ---

func (m *CCTVManager) StopDVRStreams(dvrID int64) {
	prefix := fmt.Sprintf("%d_", dvrID)
	m.streamsMu.Lock()
	defer m.streamsMu.Unlock()
	for key, cs := range m.streams {
		if strings.HasPrefix(key, prefix) {
			cs.stop()
			delete(m.streams, key)
		}
	}
	log.Printf("[cctv] stopped streams for DVR %d", dvrID)
}

func (m *CCTVManager) StopAllStreams() {
	m.streamsMu.Lock()
	defer m.streamsMu.Unlock()
	for key, cs := range m.streams {
		cs.stop()
		delete(m.streams, key)
	}
	log.Printf("[cctv] all channel streams stopped")
}

// connectH264 tries preferred stream first to find an H264 track.
// Uses TCP interleaved transport for better stability on lossy LANs.
func (m *CCTVManager) connectH264(dvr DVRConfig, chNum int) (*gortsplib.Client, *format.H264, *base.URL, *description.Media, error) {
	var lastErr error

	streamOrder := []string{"02", "01"} // default: sub then main
	if dvr.StreamQuality == "main" {
		streamOrder = []string{"01", "02"}
	}

	for _, streamID := range streamOrder {
		rtspURL := buildRTSPURL(dvr.Username, dvr.Password, dvr.Addr, dvr.Port,
			fmt.Sprintf("/Streaming/Channels/%d%s", chNum, streamID))

		u, err := base.ParseURL(rtspURL)
		if err != nil {
			log.Printf("[cctv] CH%d/%s: bad URL: %v", chNum, streamID, err)
			lastErr = err
			continue
		}

		protocol := gortsplib.ProtocolTCP
		c := &gortsplib.Client{
			Scheme:       u.Scheme,
			Host:         u.Host,
			Protocol:     &protocol,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
		}

		if err := c.Start(); err != nil {
			log.Printf("[cctv] CH%d/%s: connect fail: %v", chNum, streamID, err)
			lastErr = fmt.Errorf("RTSP connect %s: %w", streamID, err)
			continue
		}

		desc, _, err := c.Describe(u)
		if err != nil {
			c.Close()
			log.Printf("[cctv] CH%d/%s: describe fail: %v", chNum, streamID, err)
			lastErr = fmt.Errorf("RTSP describe %s: %w", streamID, err)
			continue
		}

		// Log all tracks for debugging
		for i, md := range desc.Medias {
			for _, f := range md.Formats {
				log.Printf("[cctv] CH%d/%s: track%d codec=%s", chNum, streamID, i, f.Codec())
			}
		}

		var forma *format.H264
		if medi := desc.FindFormat(&forma); medi != nil {
			log.Printf("[cctv] CH%d/%s: H264 found!", chNum, streamID)
			return c, forma, desc.BaseURL, medi, nil
		}

		c.Close()
		lastErr = fmt.Errorf("stream %s has no H264 track", streamID)
	}
	if lastErr != nil {
		return nil, nil, nil, nil, fmt.Errorf("no H264: %w", lastErr)
	}
	return nil, nil, nil, nil, fmt.Errorf("no H264 on any stream — check DVR codec settings")
}

// TestDVRConnection tests RTSP connectivity and reports findings for debugging.
func (m *CCTVManager) TestDVRConnection(dvrID int64) (string, error) {
	dvr, err := m.getDVR(dvrID)
	if err != nil {
		return "", fmt.Errorf("get DVR: %w", err)
	}

	var report strings.Builder
	report.WriteString(fmt.Sprintf("DVR: %s (%s:%d) protocol=%s\n", dvr.Name, dvr.Addr, dvr.Port, dvr.Protocol))

	// 1. TCP port check
	if checkRTSPPort(dvr.Addr, dvr.Port) {
		report.WriteString(fmt.Sprintf("Port %d: OPEN\n", dvr.Port))
	} else {
		report.WriteString(fmt.Sprintf("Port %d: CLOSED — check address/port\n", dvr.Port))
		return report.String(), nil
	}

	// 2. Try RTSP DESCRIBE on channel 1
	for _, streamID := range []string{"01", "02"} {
		rtspURL := buildRTSPURL(dvr.Username, dvr.Password, dvr.Addr, dvr.Port,
			fmt.Sprintf("/Streaming/Channels/1%s", streamID))
		u, err := base.ParseURL(rtspURL)
		if err != nil {
			report.WriteString(fmt.Sprintf("CH1/%s: URL parse error: %v\n", streamID, err))
			continue
		}

		c := &gortsplib.Client{
			Scheme:       u.Scheme,
			Host:         u.Host,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		}
		if err := c.Start(); err != nil {
			report.WriteString(fmt.Sprintf("CH1/%s: connect fail: %v\n", streamID, err))
			continue
		}

		desc, _, err := c.Describe(u)
		if err != nil {
			c.Close()
			report.WriteString(fmt.Sprintf("CH1/%s: describe fail: %v\n", streamID, err))
			continue
		}

		for i, md := range desc.Medias {
			for _, f := range md.Formats {
				report.WriteString(fmt.Sprintf("CH1/%s: track%d → %s\n", streamID, i, f.Codec()))
			}
		}

		var forma *format.H264
		if medi := desc.FindFormat(&forma); medi != nil {
			report.WriteString(fmt.Sprintf("CH1/%s: H264 OK ✓\n", streamID))
		} else {
			report.WriteString(fmt.Sprintf("CH1/%s: no H264 track\n", streamID))
		}
		c.Close()
	}

	return report.String(), nil
}

// --- RTSP discovery ---

func (m *CCTVManager) discoverFromDVRRTSP(dvr DVRConfig) ([]ChannelConfig, error) {
	const maxChannels = 32
	const concurrency = 4

	type probeResult struct {
		ch    int
		found bool
	}

	sem := make(chan struct{}, concurrency)
	results := make(chan probeResult, maxChannels)
	var wg sync.WaitGroup

	for ch := 1; ch <= maxChannels; ch++ {
		wg.Add(1)
		go func(ch int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			rtspURL := buildRTSPURL(dvr.Username, dvr.Password, dvr.Addr, dvr.Port,
				fmt.Sprintf("/Streaming/Channels/%d01", ch))
			found := probeRTSPChannelGo(rtspURL)
			results <- probeResult{ch: ch, found: found}
			if found {
				log.Printf("[cctv] RTSP discover: ch%d OK", ch)
			}
		}(ch)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	foundSet := make(map[int]bool)
	for r := range results {
		if r.found {
			foundSet[r.ch] = true
		}
	}

	var channels []ChannelConfig
	consecutiveMisses := 0
	for ch := 1; ch <= maxChannels; ch++ {
		if foundSet[ch] {
			consecutiveMisses = 0
			channels = append(channels, ChannelConfig{
				DVRID: dvr.ID, ChNum: ch,
				Name: fmt.Sprintf("Channel %d", ch), Order: ch - 1,
			})
		} else if len(channels) > 0 {
			consecutiveMisses++
			if consecutiveMisses >= 3 {
				break
			}
		}
	}

	if len(channels) == 0 {
		return nil, fmt.Errorf("no RTSP channels found (check address, port, credentials)")
	}
	return channels, nil
}

func probeRTSPChannelGo(rtspURL string) bool {
	u, err := base.ParseURL(rtspURL)
	if err != nil {
		return false
	}
	c := &gortsplib.Client{
		Scheme:       u.Scheme,
		Host:         u.Host,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	if err := c.Start(); err != nil {
		return false
	}
	defer c.Close()
	_, _, err = c.Describe(u)
	return err == nil
}

// --- Helpers ---

func buildRTSPURL(username, password, addr string, port int, path string) string {
	u := &url.URL{
		Scheme: "rtsp",
		User:   url.UserPassword(username, password),
		Host:   fmt.Sprintf("%s:%d", addr, port),
		Path:   path,
	}
	return u.String()
}

// --- Snapshot fallback (ffmpeg) ---

func (m *CCTVManager) fetchSnapshotRTSP(dvr DVRConfig, chNum int) ([]byte, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg not found: install ffmpeg for RTSP snapshots")
	}
	streamID := "02"
	if dvr.StreamQuality == "main" {
		streamID = "01"
	}
	rtspURL := buildRTSPURL(dvr.Username, dvr.Password, dvr.Addr, dvr.Port,
		fmt.Sprintf("/Streaming/Channels/%d%s", chNum, streamID))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-rtsp_transport", "tcp", "-i", rtspURL,
		"-frames:v", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "-q:v", "2", "pipe:1",
	)
	data, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("RTSP snapshot timeout (10s)")
		}
		return nil, fmt.Errorf("ffmpeg: %w", err)
	}
	return data, nil
}

func checkRTSPPort(addr string, port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", addr, port), 3*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
