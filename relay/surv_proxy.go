package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gohlslib/v2"
	"github.com/bluenviron/gohlslib/v2/pkg/codecs"
	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"
	"github.com/opsview/opsview/proto"
	"github.com/pion/rtp"
)

// streamEntry holds a single RTSP→HLS pipeline for one channel.
type streamEntry struct {
	id     string
	name   string
	muxer  *gohlslib.Muxer
	client *gortsplib.Client
	cancel context.CancelFunc
}

// SurvProxy manages multiple RTSP→HLS streams, one per surveillance channel.
type SurvProxy struct {
	mu      sync.RWMutex
	streams map[string]*streamEntry // "ch1", "ch2", ...
}

// StreamInfo is returned by the streams list API.
type StreamInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

func NewSurvProxy() *SurvProxy {
	return &SurvProxy{
		streams: make(map[string]*streamEntry),
	}
}

// HandleSurvConfig parses a MsgSurvConfig payload and starts/stops channels accordingly.
func (sp *SurvProxy) HandleSurvConfig(payload []byte) {
	var cfg proto.SurvConfig
	if err := json.Unmarshal(payload, &cfg); err != nil {
		log.Printf("[surv] config parse error: %v", err)
		return
	}

	// Build DVR lookup
	dvrMap := make(map[int64]proto.DVRInfo)
	for _, d := range cfg.DVRs {
		dvrMap[d.ID] = d
	}

	// Determine desired channel set, grouped by DVR for staggered connection
	desired := make(map[string]bool)
	type pendingCh struct {
		chID, name, rtspURL string
	}
	perDVR := make(map[int64][]pendingCh)

	for _, ch := range cfg.Channels {
		if !ch.Enabled {
			continue
		}
		dvr, ok := dvrMap[ch.DVRID]
		if !ok {
			continue
		}
		chID := fmt.Sprintf("dvr%d_ch%d", ch.DVRID, ch.ChNum)
		desired[chID] = true

		sp.mu.RLock()
		_, exists := sp.streams[chID]
		sp.mu.RUnlock()

		if !exists {
			perDVR[ch.DVRID] = append(perDVR[ch.DVRID], pendingCh{
				chID:    chID,
				name:    ch.Name,
				rtspURL: buildSurvRTSPURL(dvr, ch.ChNum),
			})
		}
	}

	// Start channels per DVR in parallel, but stagger within each DVR
	var wg sync.WaitGroup
	for _, channels := range perDVR {
		wg.Add(1)
		go func(chs []pendingCh) {
			defer wg.Done()
			for i, ch := range chs {
				if i > 0 {
					time.Sleep(300 * time.Millisecond)
				}
				if err := sp.StartChannel(ch.chID, ch.name, ch.rtspURL); err != nil {
					log.Printf("[surv] failed to start %s: %v", ch.chID, err)
				}
			}
		}(channels)
	}
	wg.Wait()

	// Stop channels no longer in config
	sp.mu.RLock()
	var toStop []string
	for id := range sp.streams {
		if !desired[id] {
			toStop = append(toStop, id)
		}
	}
	sp.mu.RUnlock()

	for _, id := range toStop {
		sp.StopChannel(id)
	}
}

// StartChannel connects to an RTSP URL and begins producing HLS segments.
func (sp *SurvProxy) StartChannel(id, name, rawURL string) error {
	sp.mu.Lock()
	if entry, ok := sp.streams[id]; ok {
		sp.stopEntryLocked(entry)
		delete(sp.streams, id)
	}
	sp.mu.Unlock()

	u, err := base.ParseURL(rawURL)
	if err != nil {
		return fmt.Errorf("invalid RTSP URL: %w", err)
	}

	c := &gortsplib.Client{
		Scheme:       u.Scheme,
		Host:         u.Host,
		Protocol:     ptrProto(gortsplib.ProtocolTCP),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	if err := c.Start(); err != nil {
		return fmt.Errorf("RTSP connect: %w", err)
	}

	desc, _, err := c.Describe(u)
	if err != nil {
		c.Close()
		return fmt.Errorf("RTSP describe: %w", err)
	}

	entry := &streamEntry{id: id, name: name}

	track, isH265, err := setupSurvCodec(c, desc, entry)
	if err != nil {
		c.Close()
		return err
	}

	variant := gohlslib.MuxerVariantMPEGTS
	if isH265 {
		variant = gohlslib.MuxerVariantFMP4
	}

	muxer := &gohlslib.Muxer{
		Variant:            variant,
		SegmentCount:       5,
		SegmentMinDuration: 5 * time.Second,
		Tracks:             []*gohlslib.Track{track},
	}
	if err := muxer.Start(); err != nil {
		c.Close()
		return fmt.Errorf("HLS muxer: %w", err)
	}

	if _, err := c.Play(nil); err != nil {
		muxer.Close()
		c.Close()
		return fmt.Errorf("RTSP play: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	entry.client = c
	entry.muxer = muxer
	entry.cancel = cancel

	go func() {
		<-ctx.Done()
		c.Close()
	}()

	sp.mu.Lock()
	sp.streams[id] = entry
	sp.mu.Unlock()

	log.Printf("[surv] started %s (%s)", id, rawURL)
	return nil
}

// StopChannel stops a single channel stream.
func (sp *SurvProxy) StopChannel(id string) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if entry, ok := sp.streams[id]; ok {
		sp.stopEntryLocked(entry)
		delete(sp.streams, id)
		log.Printf("[surv] stopped %s", id)
	}
}

// StopAll stops all channel streams.
func (sp *SurvProxy) StopAll() {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	for id, entry := range sp.streams {
		sp.stopEntryLocked(entry)
		delete(sp.streams, id)
	}
	log.Printf("[surv] all streams stopped")
}

func (sp *SurvProxy) stopEntryLocked(e *streamEntry) {
	if e.cancel != nil {
		e.cancel()
	}
	if e.muxer != nil {
		e.muxer.Close()
	}
	// client is closed by the context goroutine
}

// ListStreams returns info about all channels.
func (sp *SurvProxy) ListStreams() []StreamInfo {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	out := make([]StreamInfo, 0, len(sp.streams))
	for id, e := range sp.streams {
		out = append(out, StreamInfo{
			ID:     id,
			Name:   e.name,
			Active: true,
		})
	}
	return out
}

// ServeHLS handles /surv/{chID}/... requests by delegating to the appropriate muxer.
func (sp *SurvProxy) ServeHLS(w http.ResponseWriter, r *http.Request) {
	// CORS headers for all responses (including errors)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Origin, Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Path: /surv/dvr1_ch1/index.m3u8 or /surv/dvr1_ch1/segment123.ts
	path := strings.TrimPrefix(r.URL.Path, "/surv/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	chID := parts[0]
	remainder := parts[1]

	sp.mu.RLock()
	entry, ok := sp.streams[chID]
	sp.mu.RUnlock()

	if !ok || entry.muxer == nil {
		http.Error(w, "no active stream for "+chID, http.StatusNotFound)
		return
	}

	// No-cache headers for live HLS
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")

	// Rewrite path so muxer sees /index.m3u8 or /segment.ts
	r.URL.Path = "/" + remainder
	entry.muxer.Handle(w, r)
}

// --- Codec setup (ported from viewer/stream_proxy.go) ---

func setupSurvCodec(c *gortsplib.Client, desc *description.Session, entry *streamEntry) (*gohlslib.Track, bool, error) {
	var formaH264 *format.H264
	if medi := desc.FindFormat(&formaH264); medi != nil {
		track, err := setupSurvH264(c, desc, medi, formaH264, entry)
		return track, false, err
	}

	var formaH265 *format.H265
	if medi := desc.FindFormat(&formaH265); medi != nil {
		track, err := setupSurvH265(c, desc, medi, formaH265, entry)
		return track, true, err
	}

	return nil, false, fmt.Errorf("no H264/H265 track found in RTSP stream")
}

func setupSurvH264(c *gortsplib.Client, desc *description.Session, medi *description.Media, forma *format.H264, entry *streamEntry) (*gohlslib.Track, error) {
	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return nil, fmt.Errorf("H264 decoder: %w", err)
	}

	if _, err := c.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return nil, fmt.Errorf("RTSP setup: %w", err)
	}

	var sps, pps []byte
	if forma.SPS != nil {
		sps = forma.SPS
	}
	if forma.PPS != nil {
		pps = forma.PPS
	}

	track := &gohlslib.Track{
		Codec:     &codecs.H264{SPS: sps, PPS: pps},
		ClockRate: 90000,
	}

	c.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
		pts, ok := c.PacketPTS(medi, pkt)
		if !ok {
			return
		}
		au, err := rtpDec.Decode(pkt)
		if err != nil {
			if !errors.Is(err, rtph264.ErrNonStartingPacketAndNoPrevious) &&
				!errors.Is(err, rtph264.ErrMorePacketsNeeded) {
				log.Printf("[surv] %s H264 decode: %v", entry.id, err)
			}
			return
		}
		if entry.muxer != nil {
			entry.muxer.WriteH264(track, time.Now(), pts, au)
		}
	})

	return track, nil
}

func setupSurvH265(c *gortsplib.Client, desc *description.Session, medi *description.Media, forma *format.H265, entry *streamEntry) (*gohlslib.Track, error) {
	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return nil, fmt.Errorf("H265 decoder: %w", err)
	}

	if _, err := c.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return nil, fmt.Errorf("RTSP setup: %w", err)
	}

	var vps, sps, pps []byte
	if forma.VPS != nil {
		vps = forma.VPS
	}
	if forma.SPS != nil {
		sps = forma.SPS
	}
	if forma.PPS != nil {
		pps = forma.PPS
	}

	track := &gohlslib.Track{
		Codec:     &codecs.H265{VPS: vps, SPS: sps, PPS: pps},
		ClockRate: 90000,
	}

	c.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
		pts, ok := c.PacketPTS(medi, pkt)
		if !ok {
			return
		}
		au, err := rtpDec.Decode(pkt)
		if err != nil {
			if !errors.Is(err, rtph265.ErrNonStartingPacketAndNoPrevious) &&
				!errors.Is(err, rtph265.ErrMorePacketsNeeded) {
				log.Printf("[surv] %s H265 decode: %v", entry.id, err)
			}
			return
		}
		if entry.muxer != nil {
			entry.muxer.WriteH265(track, time.Now(), pts, au)
		}
	})

	return track, nil
}

// --- Helpers ---

func buildSurvRTSPURL(dvr proto.DVRInfo, chNum int) string {
	streamID := "02" // default: sub stream
	if dvr.StreamQuality == "main" {
		streamID = "01"
	}
	u := &url.URL{
		Scheme: "rtsp",
		User:   url.UserPassword(dvr.Username, dvr.Password),
		Host:   fmt.Sprintf("%s:%d", dvr.Addr, rtspPort(dvr.Port)),
		Path:   fmt.Sprintf("/Streaming/Channels/%d%s", chNum, streamID),
	}
	return u.String()
}

func rtspPort(httpPort int) int {
	if httpPort == 80 || httpPort == 0 {
		return 554
	}
	return httpPort
}

func ptrProto(v gortsplib.Protocol) *gortsplib.Protocol {
	return &v
}
