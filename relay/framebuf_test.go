package main

import (
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

// A malicious frame with attacker-chosen huge dimensions (and a tiny payload)
// must not trigger a multi-GB allocation or panic.
func TestFrameBufferUpdateRejectsHugeDimensions(t *testing.T) {
	fb := NewFrameBuffer()
	fb.Update(&proto.FrameDelta{Width: 65535, Height: 65535, TileSize: 128})
	if fb.pixels != nil {
		t.Fatalf("oversized frame must not allocate (got %d bytes)", len(fb.pixels))
	}
}

func TestFrameBufferUpdateAllocatesValidFrame(t *testing.T) {
	fb := NewFrameBuffer()
	fb.Update(&proto.FrameDelta{Width: 64, Height: 64, TileSize: 32})
	if len(fb.pixels) != 64*64*4 {
		t.Fatalf("valid frame should allocate %d bytes, got %d", 64*64*4, len(fb.pixels))
	}
}

// A tile whose coordinates fall outside the frame must be skipped, not panic
// the relay via an out-of-range slice.
func TestFrameBufferUpdateOutOfRangeTileNoPanic(t *testing.T) {
	fb := NewFrameBuffer()
	fd := &proto.FrameDelta{
		Width: 64, Height: 64, TileSize: 32,
		Tiles: []proto.Tile{
			{TX: 9999, TY: 9999, Codec: proto.CodecZstdRawBGRA, Data: []byte{0x00}},
		},
	}
	fb.Update(fd) // must not panic
}
