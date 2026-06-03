package main

import "testing"

func TestIsPlaintextPublicRelay(t *testing.T) {
	// Plaintext to a public host/IP -> warn.
	warn := []string{
		"ws://relay.example.com/publish",
		"ws://relay.example.com:8080/publish",
		"ws://8.8.8.8:8080/publish",
	}
	for _, u := range warn {
		if !isPlaintextPublicRelay(u) {
			t.Errorf("%q should warn (plaintext public)", u)
		}
	}
	// Loopback/private LAN ws:// (intended mode) and any wss:// -> no warning.
	ok := []string{
		"ws://127.0.0.1:8080/publish",
		"ws://localhost:8080/publish",
		"ws://192.168.1.5:8080/publish",
		"ws://10.0.0.5:8080/publish",
		"ws://172.16.0.9:8080/publish",
		"wss://relay.example.com/publish",
		"wss://8.8.8.8/publish",
	}
	for _, u := range ok {
		if isPlaintextPublicRelay(u) {
			t.Errorf("%q should NOT warn", u)
		}
	}
}
