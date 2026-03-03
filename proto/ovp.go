// Package proto defines the OpsView Protocol (OVP) v1 binary wire format.
//
// All multi-byte integers are little-endian.
package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	Magic   uint32 = 0x4F565031 // 'OVP1'
	Version uint16 = 1
)

// MessageType identifies the payload kind that follows the header.
type MessageType uint16

const (
	MsgHello      MessageType = 1
	MsgAuth       MessageType = 2
	MsgFrameDelta MessageType = 3
	MsgFullFrame  MessageType = 4
	MsgControl    MessageType = 5
	MsgHeartbeat  MessageType = 6
	MsgError        MessageType = 7
	MsgReady        MessageType = 8
	MsgSurvConfig   MessageType = 9  // Surveillance config (publisher→relay→watcher)
	MsgSurvSnapshot MessageType = 10 // Snapshot request/response
)

func (m MessageType) String() string {
	switch m {
	case MsgHello:
		return "HELLO"
	case MsgAuth:
		return "AUTH"
	case MsgFrameDelta:
		return "FRAME_DELTA"
	case MsgFullFrame:
		return "FULL_FRAME"
	case MsgControl:
		return "CONTROL"
	case MsgHeartbeat:
		return "HEARTBEAT"
	case MsgError:
		return "ERROR"
	case MsgReady:
		return "READY"
	case MsgSurvConfig:
		return "SURV_CONFIG"
	case MsgSurvSnapshot:
		return "SURV_SNAPSHOT"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", m)
	}
}

// HeaderSize is the fixed size of the OVP header in bytes.
const HeaderSize = 4 + 2 + 2 + 4 // magic(4) + version(2) + type(2) + payload_len(4) = 12

// Header is the common prefix of every OVP message.
type Header struct {
	Magic      uint32
	Version    uint16
	Type       MessageType
	PayloadLen uint32
}

// EncodeHeader writes a header into buf (must be >= HeaderSize).
func EncodeHeader(buf []byte, msgType MessageType, payloadLen uint32) {
	binary.LittleEndian.PutUint32(buf[0:4], Magic)
	binary.LittleEndian.PutUint16(buf[4:6], Version)
	binary.LittleEndian.PutUint16(buf[6:8], uint16(msgType))
	binary.LittleEndian.PutUint32(buf[8:12], payloadLen)
}

// DecodeHeader reads a header from buf (must be >= HeaderSize).
func DecodeHeader(buf []byte) (Header, error) {
	if len(buf) < HeaderSize {
		return Header{}, errors.New("ovp: buffer too short for header")
	}
	h := Header{
		Magic:      binary.LittleEndian.Uint32(buf[0:4]),
		Version:    binary.LittleEndian.Uint16(buf[4:6]),
		Type:       MessageType(binary.LittleEndian.Uint16(buf[6:8])),
		PayloadLen: binary.LittleEndian.Uint32(buf[8:12]),
	}
	if h.Magic != Magic {
		return h, fmt.Errorf("ovp: bad magic 0x%08X, want 0x%08X", h.Magic, Magic)
	}
	if h.Version != Version {
		return h, fmt.Errorf("ovp: unsupported version %d", h.Version)
	}
	return h, nil
}

// ReadMessage reads a complete OVP message (header + payload) from r.
func ReadMessage(r io.Reader) (Header, []byte, error) {
	hbuf := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, hbuf); err != nil {
		return Header{}, nil, err
	}
	h, err := DecodeHeader(hbuf)
	if err != nil {
		return h, nil, err
	}
	payload := make([]byte, h.PayloadLen)
	if h.PayloadLen > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return h, nil, err
		}
	}
	return h, payload, nil
}

// MarshalMessage creates a complete wire message (header + payload).
func MarshalMessage(msgType MessageType, payload []byte) []byte {
	msg := make([]byte, HeaderSize+len(payload))
	EncodeHeader(msg, msgType, uint32(len(payload)))
	copy(msg[HeaderSize:], payload)
	return msg
}
