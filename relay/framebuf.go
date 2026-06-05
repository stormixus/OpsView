package main

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/opsview/opsview/proto"
)

// FrameBuffer caches the latest raw tile per position so the relay can hand a
// freshly-connected watcher a complete "keyframe" instead of making them wait
// for every tile to change. It is codec-agnostic: tiles are stored exactly as
// received (JPEG or legacy zstd-BGRA), so no decode/re-encode happens on the hot
// path — only when synthesizing the join frame or a PNG snapshot.
type FrameBuffer struct {
	mu       sync.RWMutex
	seq      uint32
	profile  uint16
	width    uint16
	height   uint16
	tileSize uint16
	tiles    map[uint32]proto.Tile // key: tx<<16 | ty
	dec      *zstd.Decoder
}

// Frame dimension caps. The agent only emits 720p/1080p; these generous bounds
// (8K) prevent an attacker-controlled Width/Height from driving a huge alloc.
const (
	maxFrameWidth  = 7680
	maxFrameHeight = 4320
)

func validFrameDimensions(w, h int) bool {
	return w > 0 && h > 0 && w <= maxFrameWidth && h <= maxFrameHeight
}

func tileKey(tx, ty uint16) uint32 { return uint32(tx)<<16 | uint32(ty) }

func NewFrameBuffer() *FrameBuffer {
	dec, _ := zstd.NewReader(nil)
	return &FrameBuffer{tiles: make(map[uint32]proto.Tile), dec: dec}
}

// Update folds a frame delta into the cache. A change in geometry (resolution or
// tile size) resets the cache so stale tiles from the old layout are dropped.
func (fb *FrameBuffer) Update(fd *proto.FrameDelta) {
	if !validFrameDimensions(int(fd.Width), int(fd.Height)) || fd.TileSize == 0 {
		return
	}
	fb.mu.Lock()
	defer fb.mu.Unlock()

	if fd.Width != fb.width || fd.Height != fb.height || fd.TileSize != fb.tileSize {
		fb.tiles = make(map[uint32]proto.Tile)
		fb.width, fb.height, fb.tileSize = fd.Width, fd.Height, fd.TileSize
	}
	fb.profile = fd.Profile
	fb.seq = fd.Seq

	for i := range fd.Tiles {
		t := &fd.Tiles[i]
		// Copy the bytes: the decoded delta aliases the publisher read buffer,
		// which is reused for the next message.
		data := make([]byte, len(t.Data))
		copy(data, t.Data)
		fb.tiles[tileKey(t.TX, t.TY)] = proto.Tile{
			TX: t.TX, TY: t.TY, Codec: t.Codec, DataLen: uint32(len(data)), Data: data,
		}
	}
}

// FullFrameMessage builds a complete FRAME_DELTA OVP message (header + payload)
// containing every cached tile, ready to send to a newly-connected watcher.
// Returns (nil, false) when no frame has been cached yet.
func (fb *FrameBuffer) FullFrameMessage() ([]byte, bool) {
	fb.mu.RLock()
	defer fb.mu.RUnlock()
	if len(fb.tiles) == 0 || fb.width == 0 {
		return nil, false
	}
	fd := &proto.FrameDelta{
		Seq:       fb.seq,
		TsMs:      uint64(time.Now().UnixMilli()),
		Profile:   fb.profile,
		Width:     fb.width,
		Height:    fb.height,
		TileSize:  fb.tileSize,
		TileCount: uint16(len(fb.tiles)),
		Tiles:     make([]proto.Tile, 0, len(fb.tiles)),
	}
	for _, t := range fb.tiles {
		fd.Tiles = append(fd.Tiles, t)
	}
	return proto.MarshalMessage(proto.MsgFrameDelta, proto.EncodeFrameDelta(fd)), true
}

// SnapshotPNG renders the cached frame to PNG by decoding each cached tile.
func (fb *FrameBuffer) SnapshotPNG() ([]byte, error) {
	fb.mu.RLock()
	defer fb.mu.RUnlock()
	if len(fb.tiles) == 0 || fb.width == 0 || fb.height == 0 {
		return nil, fmt.Errorf("no frame available")
	}
	w, h, ts := int(fb.width), int(fb.height), int(fb.tileSize)
	canvas := image.NewNRGBA(image.Rect(0, 0, w, h))

	for _, t := range fb.tiles {
		ox, oy := int(t.TX)*ts, int(t.TY)*ts
		if ox < 0 || oy < 0 || ox >= w || oy >= h {
			continue
		}
		tileImg := fb.decodeTile(&t, ts)
		if tileImg == nil {
			continue
		}
		b := tileImg.Bounds()
		tw, th := b.Dx(), b.Dy()
		if ox+tw > w {
			tw = w - ox
		}
		if oy+th > h {
			th = h - oy
		}
		if tw <= 0 || th <= 0 {
			continue
		}
		draw.Draw(canvas, image.Rect(ox, oy, ox+tw, oy+th), tileImg, b.Min, draw.Src)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeTile turns a cached tile into an image. Must be called with fb.mu held
// (it reads fb.dec).
func (fb *FrameBuffer) decodeTile(t *proto.Tile, ts int) image.Image {
	switch t.Codec {
	case proto.CodecJPEG:
		img, err := jpeg.Decode(bytes.NewReader(t.Data))
		if err != nil {
			return nil
		}
		return img
	case proto.CodecZstdRawBGRA:
		raw, err := fb.dec.DecodeAll(t.Data, nil)
		if err != nil || len(raw) < 4 {
			return nil
		}
		// Tile pixels are square (ts x ts) unless it's an edge tile; infer height.
		px := len(raw) / 4
		tw := ts
		if tw > px {
			tw = px
		}
		th := px / tw
		img := image.NewNRGBA(image.Rect(0, 0, tw, th))
		for i := 0; i < tw*th; i++ {
			s := i * 4
			img.Pix[i*4+0] = raw[s+2] // R <- B
			img.Pix[i*4+1] = raw[s+1] // G
			img.Pix[i*4+2] = raw[s+0] // B <- R
			img.Pix[i*4+3] = raw[s+3] // A
		}
		return img
	}
	return nil
}
