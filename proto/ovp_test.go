package proto

import (
	"bytes"
	"testing"
)

// L-31: ReadMessage must reject an attacker-controlled oversized PayloadLen
// instead of allocating make([]byte, PayloadLen) (up to 4 GiB).
func TestReadMessageRejectsOversizedPayload(t *testing.T) {
	var hdr [HeaderSize]byte
	EncodeHeader(hdr[:], MsgFrameDelta, MaxPayloadSize+1)
	_, _, err := ReadMessage(bytes.NewReader(hdr[:]))
	if err == nil {
		t.Fatal("expected error for oversized payload length, got nil")
	}
}

func TestReadMessageAcceptsNormalPayload(t *testing.T) {
	payload := []byte("hello frame")
	msg := MarshalMessage(MsgControl, payload)
	h, got, err := ReadMessage(bytes.NewReader(msg))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if h.Type != MsgControl || !bytes.Equal(got, payload) {
		t.Fatalf("round-trip mismatch: type=%v payload=%q", h.Type, got)
	}
}
