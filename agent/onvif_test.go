package main

import (
	"encoding/base64"
	"testing"
)

func TestOnvifPasswordDigest(t *testing.T) {
	nonce := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	created := "2026-06-04T00:00:00.000Z"
	got := onvifPasswordDigest(nonce, created, "test123")
	want := "f1hsgkxcZTTXJz1Cw+I7EKNskYY="
	if got != want {
		t.Fatalf("digest = %q, want %q", got, want)
	}
	// Sanity: base64 of a 20-byte SHA1.
	raw, err := base64.StdEncoding.DecodeString(got)
	if err != nil || len(raw) != 20 {
		t.Fatalf("digest not base64 sha1: err=%v len=%d", err, len(raw))
	}
}
