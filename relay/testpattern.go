package main

import (
	"log"
	"math"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/opsview/opsview/proto"
)

// TestPattern generates synthetic OVP frames when no publisher is connected.
type TestPattern struct {
	hub      *Hub
	encoder  *zstd.Encoder
	stopCh   chan struct{}
	stopped  chan struct{}
	mu       sync.Mutex
	running  bool
	seq      uint32
	width    int
	height   int
	tileSize int
}

func NewTestPattern(hub *Hub) *TestPattern {
	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	return &TestPattern{
		hub:      hub,
		encoder:  enc,
		width:    1920,
		height:   1080,
		tileSize: 128,
	}
}

// Start begins generating test frames. Safe to call multiple times.
func (tp *TestPattern) Start() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	if tp.running {
		return
	}
	tp.running = true
	tp.stopCh = make(chan struct{})
	tp.stopped = make(chan struct{})
	go tp.loop()
	log.Println("[relay] test pattern started")
}

// Stop halts the test pattern generator. Blocks until stopped.
func (tp *TestPattern) Stop() {
	tp.mu.Lock()
	if !tp.running {
		tp.mu.Unlock()
		return
	}
	tp.running = false
	close(tp.stopCh)
	stopped := tp.stopped
	tp.mu.Unlock()
	<-stopped
	log.Println("[relay] test pattern stopped")
}

func (tp *TestPattern) loop() {
	defer close(tp.stopped)
	ticker := time.NewTicker(500 * time.Millisecond) // 2 fps
	defer ticker.Stop()

	for {
		select {
		case <-tp.stopCh:
			return
		case <-ticker.C:
			tp.generateFrame()
		}
	}
}

func (tp *TestPattern) generateFrame() {
	tp.seq++
	now := uint64(time.Now().UnixMilli())

	tilesX := (tp.width + tp.tileSize - 1) / tp.tileSize
	tilesY := (tp.height + tp.tileSize - 1) / tp.tileSize

	// Generate a moving color bar pattern
	phase := float64(tp.seq) * 0.05
	tiles := make([]proto.Tile, 0, tilesX*tilesY)

	for ty := 0; ty < tilesY; ty++ {
		for tx := 0; tx < tilesX; tx++ {
			tileW := tp.tileSize
			tileH := tp.tileSize
			if (tx+1)*tp.tileSize > tp.width {
				tileW = tp.width - tx*tp.tileSize
			}
			if (ty+1)*tp.tileSize > tp.height {
				tileH = tp.height - ty*tp.tileSize
			}

			// Color based on position + animated phase
			cx := float64(tx*tp.tileSize+tileW/2) / float64(tp.width)
			cy := float64(ty*tp.tileSize+tileH/2) / float64(tp.height)

			r := uint8(128 + 127*math.Sin(cx*6.28+phase))
			g := uint8(128 + 127*math.Sin(cy*6.28+phase*1.3))
			b := uint8(128 + 127*math.Sin((cx+cy)*3.14+phase*0.7))

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

			compressed := tp.encoder.EncodeAll(raw, nil)
			tiles = append(tiles, proto.Tile{
				TX:      uint16(tx),
				TY:      uint16(ty),
				Codec:   proto.CodecZstdRawBGRA,
				DataLen: uint32(len(compressed)),
				Data:    compressed,
			})
		}
	}

	fd := &proto.FrameDelta{
		Seq:       tp.seq,
		TsMs:      now,
		Profile:   1080,
		Width:     uint16(tp.width),
		Height:    uint16(tp.height),
		TileSize:  uint16(tp.tileSize),
		TileCount: uint16(len(tiles)),
		Tiles:     tiles,
	}

	payload := proto.EncodeFrameDelta(fd)
	msg := proto.MarshalMessage(proto.MsgFrameDelta, payload)

	tp.hub.broadcast <- msg
}
