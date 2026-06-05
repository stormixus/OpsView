package main

import (
	"encoding/binary"
	"net/http"
	"strings"
	"sync"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4/seekablebuffer"
	mp4codecs "github.com/bluenviron/mediacommon/v2/pkg/formats/mp4/codecs"
	"github.com/gorilla/websocket"
)

// survWSTimescale is the H264/H265 RTP clock rate; PTS values from gortsplib are
// already in these units.
const survWSTimescale = 90000

// fragMuxer turns H264/H265 access units into an fMP4 init segment plus one
// fragment (moof+mdat) per access unit, for Media Source Extensions playback
// over a WebSocket. CCTV streams are typically B-frame-free, so DTS == PTS.
//
// Many DVRs (e.g. Hikvision) omit SPS/PPS from the RTSP SDP and send them
// in-band before each keyframe, so the init segment is built lazily once the
// parameter sets are seen in the stream.
type fragMuxer struct {
	mu            sync.Mutex
	isH265        bool
	vps, sps, pps []byte
	initSeg       []byte
	lastPTS       int64
	havePrev      bool
}

func newFragMuxerH264(sps, pps []byte) *fragMuxer {
	m := &fragMuxer{sps: sps, pps: pps}
	m.tryBuildInit()
	return m
}

func newFragMuxerH265(vps, sps, pps []byte) *fragMuxer {
	m := &fragMuxer{isH265: true, vps: vps, sps: sps, pps: pps}
	m.tryBuildInit()
	return m
}

// tryBuildInit builds the init segment once the needed parameter sets are known.
// Returns the new init bytes the first time it succeeds, else nil. Caller holds m.mu.
func (m *fragMuxer) tryBuildInit() []byte {
	if m.initSeg != nil {
		return nil
	}
	var codec mp4codecs.Codec
	if m.isH265 {
		if m.vps == nil || m.sps == nil || m.pps == nil {
			return nil
		}
		codec = &mp4codecs.H265{VPS: m.vps, SPS: m.sps, PPS: m.pps}
	} else {
		if m.sps == nil || m.pps == nil {
			return nil
		}
		codec = &mp4codecs.H264{SPS: m.sps, PPS: m.pps}
	}
	init := fmp4.Init{Tracks: []*fmp4.InitTrack{{ID: 1, TimeScale: survWSTimescale, Codec: codec}}}
	var b seekablebuffer.Buffer
	if err := init.Marshal(&b); err != nil {
		return nil
	}
	m.initSeg = append([]byte(nil), b.Bytes()...)
	return m.initSeg
}

// extractParams pulls VPS/SPS/PPS NAL units out of an access unit. Caller holds m.mu.
func (m *fragMuxer) extractParams(au [][]byte) {
	for _, nal := range au {
		if len(nal) == 0 {
			continue
		}
		if m.isH265 {
			switch (nal[0] >> 1) & 0x3F {
			case 32: // VPS
				m.vps = append([]byte(nil), nal...)
			case 33: // SPS
				m.sps = append([]byte(nil), nal...)
			case 34: // PPS
				m.pps = append([]byte(nil), nal...)
			}
		} else {
			switch nal[0] & 0x1F {
			case 7: // SPS
				m.sps = append([]byte(nil), nal...)
			case 8: // PPS
				m.pps = append([]byte(nil), nal...)
			}
		}
	}
}

// writeAU builds the fMP4 fragment for one access unit. Returns the fragment
// bytes, whether it is a keyframe, and (the first time the init segment becomes
// available) the init bytes. Returns nil fragment until the init is ready.
func (m *fragMuxer) writeAU(pts int64, au [][]byte) (frag []byte, keyframe bool, newInit []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.initSeg == nil {
		m.extractParams(au)
		newInit = m.tryBuildInit()
	}
	if m.initSeg == nil {
		return nil, false, nil
	}

	dur := int64(survWSTimescale / 30) // default ~30fps until the cadence is known
	if m.havePrev {
		if d := pts - m.lastPTS; d > 0 && d < survWSTimescale*2 {
			dur = d
		}
	}
	m.lastPTS = pts
	m.havePrev = true

	keyframe = h264.IsRandomAccess(au)
	if m.isH265 {
		keyframe = h265.IsRandomAccess(au)
	}

	part := fmp4.Part{Tracks: []*fmp4.PartTrack{{
		ID:       1,
		BaseTime: uint64(pts),
		Samples: []*fmp4.Sample{{
			Duration:        uint32(dur),
			PTSOffset:       0,
			IsNonSyncSample: !keyframe,
			Payload:         annexBToAVCC(au),
		}},
	}}}
	var b seekablebuffer.Buffer
	if err := part.Marshal(&b); err != nil {
		return nil, false, newInit
	}
	return append([]byte(nil), b.Bytes()...), keyframe, newInit
}

// annexBToAVCC concatenates NAL units as 4-byte length-prefixed (AVCC) data,
// which is what fMP4 samples carry.
func annexBToAVCC(au [][]byte) []byte {
	size := 0
	for _, nal := range au {
		size += 4 + len(nal)
	}
	out := make([]byte, 0, size)
	var lp [4]byte
	for _, nal := range au {
		binary.BigEndian.PutUint32(lp[:], uint32(len(nal)))
		out = append(out, lp[:]...)
		out = append(out, nal...)
	}
	return out
}

// --- per-channel WebSocket fan-out ---

type survWSClient struct {
	send    chan []byte
	started bool // true once the client has been handed a keyframe
}

type survWSHub struct {
	mu      sync.Mutex
	clients map[*survWSClient]struct{}
	initSeg []byte
}

func newSurvWSHub() *survWSHub {
	return &survWSHub{clients: make(map[*survWSClient]struct{})}
}

func (h *survWSHub) setInit(seg []byte) {
	h.mu.Lock()
	h.initSeg = seg
	h.mu.Unlock()
}

// broadcast fans a fragment out to all clients. A client only starts receiving
// at the first keyframe, and is given the init segment immediately before that
// first fragment, so MSE always gets init then a decodable keyframe.
func (h *survWSHub) broadcast(frag []byte, keyframe bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if !c.started {
			if !keyframe {
				continue
			}
			c.started = true
			if h.initSeg != nil {
				select {
				case c.send <- h.initSeg:
				default:
					continue // can't seed init -> skip this round
				}
			}
		}
		select {
		case c.send <- frag:
		default: // drop for a slow client rather than block the RTSP reader
		}
	}
}

func (h *survWSHub) add() *survWSClient {
	c := &survWSClient{send: make(chan []byte, 256)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *survWSHub) remove(c *survWSClient) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
}

// ServeWS streams a channel as fMP4-over-WebSocket: GET /surv/ws/{chID}. The
// client receives the fMP4 init segment immediately before the first keyframe
// fragment; subsequent binary messages are media fragments (moof+mdat). Pair
// with MSE on the client. Like the HLS endpoint, this is not separately
// authenticated.
func (sp *SurvProxy) ServeWS(w http.ResponseWriter, r *http.Request) {
	chID := strings.TrimPrefix(r.URL.Path, "/surv/ws/")
	if chID == "" {
		http.Error(w, "missing channel", http.StatusBadRequest)
		return
	}
	sp.mu.RLock()
	entry, ok := sp.streams[chID]
	sp.mu.RUnlock()
	if !ok || entry.wsHub == nil {
		http.Error(w, "no active stream for "+chID, http.StatusNotFound)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	client := entry.wsHub.add()
	defer entry.wsHub.remove(client)

	go func() {
		for frag := range client.send {
			if err := conn.WriteMessage(websocket.BinaryMessage, frag); err != nil {
				return
			}
		}
	}()

	// Block until the client disconnects (also drains control frames).
	conn.SetReadLimit(1024)
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}
