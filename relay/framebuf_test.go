package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/opsview/opsview/proto"
)

func TestValidFrameDimensions(t *testing.T) {
	cases := []struct {
		w, h int
		ok   bool
	}{
		{1920, 1080, true},
		{1, 1, true},
		{maxFrameWidth, maxFrameHeight, true},
		{0, 1080, false},
		{1920, 0, false},
		{-1, 100, false},
		{maxFrameWidth + 1, 4320, false},
		{1920, maxFrameHeight + 1, false},
		{65535, 65535, false},
	}
	for _, c := range cases {
		if got := validFrameDimensions(c.w, c.h); got != c.ok {
			t.Errorf("validFrameDimensions(%d,%d)=%v want %v", c.w, c.h, got, c.ok)
		}
	}
}

// A malicious frame with attacker-chosen huge dimensions must be rejected (no
// cache populated, no panic).
func TestFrameBufferRejectsHugeDimensions(t *testing.T) {
	fb := NewFrameBuffer()
	fb.Update(&proto.FrameDelta{Width: 65535, Height: 65535, TileSize: 128,
		Tiles: []proto.Tile{{TX: 0, TY: 0, Codec: proto.CodecJPEG, Data: []byte("x")}}})
	if _, ok := fb.FullFrameMessage(); ok {
		t.Fatal("oversized frame must not be cached")
	}
}

// A tile whose coordinates fall outside the frame must not panic on snapshot.
func TestFrameBufferOutOfRangeTileNoPanic(t *testing.T) {
	fb := NewFrameBuffer()
	fb.Update(&proto.FrameDelta{Width: 64, Height: 64, TileSize: 32,
		Tiles: []proto.Tile{{TX: 9999, TY: 9999, Codec: proto.CodecJPEG, Data: []byte{0x00}}}})
	_, _ = fb.SnapshotPNG() // must not panic
}

func jpegTile(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	return buf.Bytes()
}

func decodeFullFrame(t *testing.T, msg []byte) *proto.FrameDelta {
	t.Helper()
	if len(msg) < proto.HeaderSize {
		t.Fatalf("message too short: %d", len(msg))
	}
	fd, err := proto.DecodeFrameDelta(msg[proto.HeaderSize:])
	if err != nil {
		t.Fatalf("decode full frame: %v", err)
	}
	return fd
}

func TestFrameBufferFullFrameMergesLatestTiles(t *testing.T) {
	fb := NewFrameBuffer()
	if _, ok := fb.FullFrameMessage(); ok {
		t.Fatal("expected no frame before any Update")
	}

	// Full first frame: two tiles.
	fb.Update(&proto.FrameDelta{
		Seq: 1, Profile: 1080, Width: 256, Height: 128, TileSize: 128, TileCount: 2,
		Tiles: []proto.Tile{
			{TX: 0, TY: 0, Codec: proto.CodecJPEG, Data: []byte("A")},
			{TX: 1, TY: 0, Codec: proto.CodecJPEG, Data: []byte("B")},
		},
	})
	// Delta: only tile (0,0) changes.
	fb.Update(&proto.FrameDelta{
		Seq: 2, Profile: 1080, Width: 256, Height: 128, TileSize: 128, TileCount: 1,
		Tiles: []proto.Tile{{TX: 0, TY: 0, Codec: proto.CodecJPEG, Data: []byte("A2")}},
	})

	msg, ok := fb.FullFrameMessage()
	if !ok {
		t.Fatal("expected a cached full frame")
	}
	fd := decodeFullFrame(t, msg)
	if fd.TileCount != 2 || len(fd.Tiles) != 2 {
		t.Fatalf("tile count = %d, want 2", fd.TileCount)
	}
	if fd.Width != 256 || fd.Height != 128 || fd.TileSize != 128 {
		t.Fatalf("geometry = %dx%d ts=%d", fd.Width, fd.Height, fd.TileSize)
	}
	got := map[[2]uint16]string{}
	for _, tile := range fd.Tiles {
		got[[2]uint16{tile.TX, tile.TY}] = string(tile.Data)
	}
	if got[[2]uint16{0, 0}] != "A2" { // latest, not the original "A"
		t.Fatalf("tile (0,0) = %q, want A2", got[[2]uint16{0, 0}])
	}
	if got[[2]uint16{1, 0}] != "B" {
		t.Fatalf("tile (1,0) = %q, want B", got[[2]uint16{1, 0}])
	}
}

func TestFrameBufferResetsOnGeometryChange(t *testing.T) {
	fb := NewFrameBuffer()
	fb.Update(&proto.FrameDelta{Seq: 1, Width: 256, Height: 128, TileSize: 128, TileCount: 1,
		Tiles: []proto.Tile{{TX: 1, TY: 0, Codec: proto.CodecJPEG, Data: []byte("x")}}})
	// New resolution -> stale tiles dropped.
	fb.Update(&proto.FrameDelta{Seq: 2, Width: 128, Height: 128, TileSize: 128, TileCount: 1,
		Tiles: []proto.Tile{{TX: 0, TY: 0, Codec: proto.CodecJPEG, Data: []byte("y")}}})
	msg, _ := fb.FullFrameMessage()
	fd := decodeFullFrame(t, msg)
	if fd.TileCount != 1 || fd.Width != 128 {
		t.Fatalf("after reset: %d tiles, width %d (want 1 tile, 128)", fd.TileCount, fd.Width)
	}
}

func TestFrameBufferSnapshotPNGDecodesJPEGTiles(t *testing.T) {
	fb := NewFrameBuffer()
	red := jpegTile(t, 128, 128, color.RGBA{255, 0, 0, 255})
	fb.Update(&proto.FrameDelta{Seq: 1, Width: 128, Height: 128, TileSize: 128, TileCount: 1,
		Tiles: []proto.Tile{{TX: 0, TY: 0, Codec: proto.CodecJPEG, Data: red}}})

	pngBytes, err := fb.SnapshotPNG()
	if err != nil {
		t.Fatalf("SnapshotPNG: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	if img.Bounds().Dx() != 128 || img.Bounds().Dy() != 128 {
		t.Fatalf("png size = %v", img.Bounds())
	}
	r, g, b, _ := img.At(64, 64).RGBA()
	if r>>8 < 200 || g>>8 > 80 || b>>8 > 80 { // ~red after jpeg round-trip
		t.Fatalf("center pixel = %d,%d,%d, want ~red", r>>8, g>>8, b>>8)
	}
}
