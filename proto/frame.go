package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// TileCodec identifies the compression/format of tile pixel data.
type TileCodec uint16

const (
	CodecZstdRawBGRA TileCodec = 1 // zstd-compressed raw BGRA pixels
)

// FrameDeltaHeaderSize is the fixed portion of a FRAME_DELTA payload.
// seq(4) + ts_ms(8) + profile(2) + width(2) + height(2) + tile_size(2) + tile_count(2) = 22
const FrameDeltaHeaderSize = 4 + 8 + 2 + 2 + 2 + 2 + 2

// FrameDelta is the header of a FRAME_DELTA payload.
type FrameDelta struct {
	Seq       uint32
	TsMs      uint64
	Profile   uint16
	Width     uint16
	Height    uint16
	TileSize  uint16
	TileCount uint16
	Tiles     []Tile
}

// TileHeaderSize is tx(2) + ty(2) + codec(2) + data_len(4) = 10
const TileHeaderSize = 2 + 2 + 2 + 4

// Tile represents a single changed tile in a frame delta.
type Tile struct {
	TX      uint16
	TY      uint16
	Codec   TileCodec
	DataLen uint32
	Data    []byte
}

// EncodeFrameDelta serialises a FrameDelta into a binary payload.
func EncodeFrameDelta(fd *FrameDelta) []byte {
	// Calculate total size.
	size := FrameDeltaHeaderSize
	for i := range fd.Tiles {
		size += TileHeaderSize + len(fd.Tiles[i].Data)
	}

	buf := make([]byte, size)
	off := 0

	binary.LittleEndian.PutUint32(buf[off:], fd.Seq)
	off += 4
	binary.LittleEndian.PutUint64(buf[off:], fd.TsMs)
	off += 8
	binary.LittleEndian.PutUint16(buf[off:], fd.Profile)
	off += 2
	binary.LittleEndian.PutUint16(buf[off:], fd.Width)
	off += 2
	binary.LittleEndian.PutUint16(buf[off:], fd.Height)
	off += 2
	binary.LittleEndian.PutUint16(buf[off:], fd.TileSize)
	off += 2
	binary.LittleEndian.PutUint16(buf[off:], fd.TileCount)
	off += 2

	for i := range fd.Tiles {
		t := &fd.Tiles[i]
		binary.LittleEndian.PutUint16(buf[off:], t.TX)
		off += 2
		binary.LittleEndian.PutUint16(buf[off:], t.TY)
		off += 2
		binary.LittleEndian.PutUint16(buf[off:], uint16(t.Codec))
		off += 2
		binary.LittleEndian.PutUint32(buf[off:], t.DataLen)
		off += 4
		copy(buf[off:], t.Data)
		off += len(t.Data)
	}

	return buf
}

// DecodeFrameDelta parses a FRAME_DELTA payload from binary data.
func DecodeFrameDelta(data []byte) (*FrameDelta, error) {
	if len(data) < FrameDeltaHeaderSize {
		return nil, errors.New("ovp: FRAME_DELTA payload too short")
	}

	off := 0
	fd := &FrameDelta{}

	fd.Seq = binary.LittleEndian.Uint32(data[off:])
	off += 4
	fd.TsMs = binary.LittleEndian.Uint64(data[off:])
	off += 8
	fd.Profile = binary.LittleEndian.Uint16(data[off:])
	off += 2
	fd.Width = binary.LittleEndian.Uint16(data[off:])
	off += 2
	fd.Height = binary.LittleEndian.Uint16(data[off:])
	off += 2
	fd.TileSize = binary.LittleEndian.Uint16(data[off:])
	off += 2
	fd.TileCount = binary.LittleEndian.Uint16(data[off:])
	off += 2

	fd.Tiles = make([]Tile, fd.TileCount)
	for i := uint16(0); i < fd.TileCount; i++ {
		if off+TileHeaderSize > len(data) {
			return nil, fmt.Errorf("ovp: truncated tile %d header", i)
		}
		t := &fd.Tiles[i]
		t.TX = binary.LittleEndian.Uint16(data[off:])
		off += 2
		t.TY = binary.LittleEndian.Uint16(data[off:])
		off += 2
		t.Codec = TileCodec(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		t.DataLen = binary.LittleEndian.Uint32(data[off:])
		off += 4

		if off+int(t.DataLen) > len(data) {
			return nil, fmt.Errorf("ovp: truncated tile %d data (need %d, have %d)", i, t.DataLen, len(data)-off)
		}
		t.Data = data[off : off+int(t.DataLen)]
		off += int(t.DataLen)
	}

	return fd, nil
}
