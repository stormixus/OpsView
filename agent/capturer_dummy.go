//go:build !windows

package main

import (
	"image"
	"image/color"
	"log"
	"math/rand"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/opsview/opsview/proto"
)

// DummyCapturer generates synthetic frames for development/testing on non-Windows.
type DummyCapturer struct {
	cfg      AgentConfig
	width    int
	height   int
	tileSize int
	encoder  *zstd.Encoder
	canvas   *image.RGBA
	frameNum int
}

func NewCapturer(cfg AgentConfig) (Capturer, error) {
	width := 1920
	height := 1080
	if cfg.Profile == 720 {
		width = 1280
		height = 720
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return nil, err
	}

	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	// Fill with dark background
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			canvas.Set(x, y, color.RGBA{30, 30, 40, 255})
		}
	}

	log.Printf("[capturer] dummy mode %dx%d tile=%d", width, height, cfg.TileSize)
	return &DummyCapturer{
		cfg:      cfg,
		width:    width,
		height:   height,
		tileSize: cfg.TileSize,
		encoder:  enc,
		canvas:   canvas,
	}, nil
}

func (c *DummyCapturer) CaptureFrame() ([]proto.Tile, int, int, error) {
	c.frameNum++

	// Simulate partial screen changes: 2-8 random dirty tiles per frame
	numDirty := 2 + rand.Intn(7)
	tilesX := (c.width + c.tileSize - 1) / c.tileSize
	tilesY := (c.height + c.tileSize - 1) / c.tileSize

	tiles := make([]proto.Tile, 0, numDirty)

	for i := 0; i < numDirty; i++ {
		tx := rand.Intn(tilesX)
		ty := rand.Intn(tilesY)

		tileW := c.tileSize
		tileH := c.tileSize
		if (tx+1)*c.tileSize > c.width {
			tileW = c.width - tx*c.tileSize
		}
		if (ty+1)*c.tileSize > c.height {
			tileH = c.height - ty*c.tileSize
		}

		// Generate some visual content: colored blocks with movement
		r := uint8(50 + rand.Intn(200))
		g := uint8(50 + rand.Intn(200))
		b := uint8(50 + rand.Intn(200))

		// BGRA pixel data
		raw := make([]byte, tileW*tileH*4)
		for py := 0; py < tileH; py++ {
			for px := 0; px < tileW; px++ {
				off := (py*tileW + px) * 4
				raw[off+0] = b   // B
				raw[off+1] = g   // G
				raw[off+2] = r   // R
				raw[off+3] = 255 // A
			}
		}

		compressed := c.encoder.EncodeAll(raw, nil)
		tiles = append(tiles, proto.Tile{
			TX:      uint16(tx),
			TY:      uint16(ty),
			Codec:   proto.CodecZstdRawBGRA,
			DataLen: uint32(len(compressed)),
			Data:    compressed,
		})
	}

	// Simulate capture time
	time.Sleep(5 * time.Millisecond)

	return tiles, c.width, c.height, nil
}

func (c *DummyCapturer) Close() {
	if c.encoder != nil {
		c.encoder.Close()
	}
	log.Println("[capturer] dummy closed")
}
