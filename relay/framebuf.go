package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/opsview/opsview/proto"
)

type FrameBuffer struct {
	mu     sync.RWMutex
	width  int
	height int
	pixels []byte // BGRA row-major
	ready  bool
	dec    *zstd.Decoder
}

// Frame dimension caps. The agent only emits 720p/1080p; these generous bounds
// (8K) prevent an attacker-controlled Width/Height from driving a multi-GB
// allocation (up to 65535*65535*4 ≈ 17 GB before this guard).
const (
	maxFrameWidth  = 7680
	maxFrameHeight = 4320
)

// validFrameDimensions rejects zero/negative and out-of-bounds frame sizes.
func validFrameDimensions(w, h int) bool {
	return w > 0 && h > 0 && w <= maxFrameWidth && h <= maxFrameHeight
}

func NewFrameBuffer() *FrameBuffer {
	dec, _ := zstd.NewReader(nil)
	return &FrameBuffer{dec: dec}
}

func (fb *FrameBuffer) Update(fd *proto.FrameDelta) {
	fb.mu.Lock()
	defer fb.mu.Unlock()

	w := int(fd.Width)
	h := int(fd.Height)
	ts := int(fd.TileSize)

	// Reject malformed/oversized frames before allocating or indexing.
	if !validFrameDimensions(w, h) || ts <= 0 {
		return
	}

	if w != fb.width || h != fb.height {
		fb.width = w
		fb.height = h
		fb.pixels = make([]byte, w*h*4)
	}

	for i := range fd.Tiles {
		t := &fd.Tiles[i]
		if t.Codec != proto.CodecZstdRawBGRA {
			continue
		}
		raw, err := fb.dec.DecodeAll(t.Data, nil)
		if err != nil {
			continue
		}

		ox := int(t.TX) * ts
		oy := int(t.TY) * ts
		// Skip tiles whose origin lies outside the frame (prevents negative
		// widths / out-of-range slice panics below).
		if ox < 0 || oy < 0 || ox >= w || oy >= h {
			continue
		}
		tw := ts
		th := ts
		if ox+tw > w {
			tw = w - ox
		}
		if oy+th > h {
			th = h - oy
		}
		if tw <= 0 || th <= 0 {
			continue
		}

		for row := 0; row < th; row++ {
			srcOff := row * ts * 4
			dstOff := ((oy+row)*w + ox) * 4
			rowBytes := tw * 4
			if srcOff+rowBytes <= len(raw) && dstOff+rowBytes <= len(fb.pixels) {
				copy(fb.pixels[dstOff:dstOff+rowBytes], raw[srcOff:srcOff+rowBytes])
			}
		}
	}

	fb.ready = true
}

func (fb *FrameBuffer) SnapshotPNG() ([]byte, error) {
	fb.mu.RLock()
	defer fb.mu.RUnlock()

	if !fb.ready || fb.width == 0 || fb.height == 0 {
		return nil, fmt.Errorf("no frame available")
	}

	img := image.NewNRGBA(image.Rect(0, 0, fb.width, fb.height))
	for y := 0; y < fb.height; y++ {
		srcRow := fb.pixels[y*fb.width*4 : (y+1)*fb.width*4]
		dstOff := y * img.Stride
		for x := 0; x < fb.width; x++ {
			si := x * 4
			di := dstOff + x*4
			img.Pix[di+0] = srcRow[si+2] // R ← B
			img.Pix[di+1] = srcRow[si+1] // G
			img.Pix[di+2] = srcRow[si+0] // B ← R
			img.Pix[di+3] = srcRow[si+3] // A
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
