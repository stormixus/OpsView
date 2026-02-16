package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
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
	"github.com/pion/rtp"
)

// StreamProxy converts RTSP to HLS for browser playback using pure Go (no ffmpeg).
type StreamProxy struct {
	mu      sync.Mutex
	client  *gortsplib.Client
	muxer   *gohlslib.Muxer
	cancel  context.CancelFunc
	running bool
}

func NewStreamProxy() *StreamProxy {
	return &StreamProxy{}
}

// StartStream connects to an RTSP URL and begins producing HLS segments.
func (p *StreamProxy) StartStream(rawURL string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.stopLocked()

	u, err := base.ParseURL(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid RTSP URL: %w", err)
	}

	c := &gortsplib.Client{
		Scheme: u.Scheme,
		Host:   u.Host,
	}

	if err := c.Start(); err != nil {
		return "", fmt.Errorf("RTSP connect: %w", err)
	}

	desc, _, err := c.Describe(u)
	if err != nil {
		c.Close()
		return "", fmt.Errorf("RTSP describe: %w", err)
	}

	track, err := p.setupCodec(c, desc)
	if err != nil {
		c.Close()
		return "", err
	}

	muxer := &gohlslib.Muxer{
		Variant:            gohlslib.MuxerVariantMPEGTS,
		SegmentCount:       5,
		SegmentMinDuration: 1 * time.Second,
		Tracks:             []*gohlslib.Track{track},
	}
	if err := muxer.Start(); err != nil {
		c.Close()
		return "", fmt.Errorf("HLS muxer: %w", err)
	}

	if _, err := c.Play(nil); err != nil {
		muxer.Close()
		c.Close()
		return "", fmt.Errorf("RTSP play: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.client = c
	p.muxer = muxer
	p.cancel = cancel
	p.running = true

	go func() {
		select {
		case <-ctx.Done():
			c.Close()
		}
	}()

	log.Printf("[stream] HLS ready: %s", rawURL)
	return "/ops/hls/index.m3u8", nil
}

// setupCodec detects H264/H265 in the RTSP stream and wires up the RTP→HLS pipeline.
func (p *StreamProxy) setupCodec(c *gortsplib.Client, desc *description.Session) (*gohlslib.Track, error) {
	// Try H264
	var formaH264 *format.H264
	if medi := desc.FindFormat(&formaH264); medi != nil {
		return p.setupH264(c, desc, medi, formaH264)
	}

	// Try H265
	var formaH265 *format.H265
	if medi := desc.FindFormat(&formaH265); medi != nil {
		return p.setupH265(c, desc, medi, formaH265)
	}

	return nil, fmt.Errorf("no H264/H265 track found in RTSP stream")
}

func (p *StreamProxy) setupH264(c *gortsplib.Client, desc *description.Session, medi *description.Media, forma *format.H264) (*gohlslib.Track, error) {
	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return nil, fmt.Errorf("H264 decoder: %w", err)
	}

	if _, err := c.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return nil, fmt.Errorf("RTSP setup: %w", err)
	}

	track := &gohlslib.Track{
		Codec: &codecs.H264{
			SPS: formaH264SPS(forma),
			PPS: formaH264PPS(forma),
		},
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
				log.Printf("[stream] H264 decode: %v", err)
			}
			return
		}
		p.mu.Lock()
		muxer := p.muxer
		p.mu.Unlock()
		if muxer != nil {
			muxer.WriteH264(track, time.Now(), pts, au)
		}
	})

	return track, nil
}

func (p *StreamProxy) setupH265(c *gortsplib.Client, desc *description.Session, medi *description.Media, forma *format.H265) (*gohlslib.Track, error) {
	rtpDec, err := forma.CreateDecoder()
	if err != nil {
		return nil, fmt.Errorf("H265 decoder: %w", err)
	}

	if _, err := c.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		return nil, fmt.Errorf("RTSP setup: %w", err)
	}

	track := &gohlslib.Track{
		Codec: &codecs.H265{
			VPS: formaH265VPS(forma),
			SPS: formaH265SPS(forma),
			PPS: formaH265PPS(forma),
		},
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
				log.Printf("[stream] H265 decode: %v", err)
			}
			return
		}
		p.mu.Lock()
		muxer := p.muxer
		p.mu.Unlock()
		if muxer != nil {
			muxer.WriteH265(track, time.Now(), pts, au)
		}
	})

	return track, nil
}

// StopStream stops the RTSP client and HLS muxer.
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
	if p.muxer != nil {
		p.muxer.Close()
		p.muxer = nil
	}
	if p.client != nil {
		p.client.Close()
		p.client = nil
	}
	p.running = false
}

// IsRunning returns whether streaming is active.
func (p *StreamProxy) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// ServeHLS delegates /ops/hls/* requests to the gohlslib muxer.
func (p *StreamProxy) ServeHLS(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	muxer := p.muxer
	p.mu.Unlock()

	if muxer == nil {
		http.Error(w, "no active stream", 404)
		return
	}

	// Strip /ops/hls prefix so the muxer sees paths like /index.m3u8
	r.URL.Path = "/" + strings.TrimPrefix(r.URL.Path, "/ops/hls/")
	muxer.Handle(w, r)
}

// Helpers to safely extract codec parameters (may be nil before first keyframe).
func formaH264SPS(f *format.H264) []byte {
	if f.SPS != nil {
		return f.SPS
	}
	return nil
}

func formaH264PPS(f *format.H264) []byte {
	if f.PPS != nil {
		return f.PPS
	}
	return nil
}

func formaH265VPS(f *format.H265) []byte {
	if f.VPS != nil {
		return f.VPS
	}
	return nil
}

func formaH265SPS(f *format.H265) []byte {
	if f.SPS != nil {
		return f.SPS
	}
	return nil
}

func formaH265PPS(f *format.H265) []byte {
	if f.PPS != nil {
		return f.PPS
	}
	return nil
}
