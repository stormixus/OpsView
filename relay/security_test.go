package main

import "testing"

func TestRedactRTSPURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"rtsp://admin:secret@10.0.0.5:554/Streaming/Channels/101", "rtsp://10.0.0.5:554/Streaming/Channels/101"},
		{"rtsp://10.0.0.5:554/path", "rtsp://10.0.0.5:554/path"},
		{"not a url", "not a url"},
	}
	for _, c := range cases {
		if got := redactRTSPURL(c.in); got != c.want {
			t.Errorf("redactRTSPURL(%q) = %q, want %q", c.in, got, c.want)
		}
		if got := redactRTSPURL(c.in); containsSecret(got) {
			t.Errorf("redactRTSPURL(%q) leaked credentials: %q", c.in, got)
		}
	}
}

func containsSecret(s string) bool {
	for _, sub := range []string{"secret", "admin:"} {
		if indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestIsBlockedRTSPHost(t *testing.T) {
	blocked := []string{
		"127.0.0.1:554", "127.0.0.1", "[::1]:554",
		"169.254.169.254", "169.254.169.254:80", // cloud metadata
		"0.0.0.0",
	}
	for _, h := range blocked {
		if !isBlockedRTSPHost(h) {
			t.Errorf("host %q should be blocked", h)
		}
	}
	// Private LAN DVRs and public hosts must remain allowed.
	allowed := []string{"10.0.0.5:554", "192.168.1.64", "172.16.0.9:8554", "cam.example.com:554", "8.8.8.8"}
	for _, h := range allowed {
		if isBlockedRTSPHost(h) {
			t.Errorf("host %q should be allowed", h)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	// No Origin header (native clients: agent, Wails viewer) -> allowed.
	if !originAllowed("", "relay.example.com", []string{"https://app.example.com"}) {
		t.Error("empty origin must be allowed")
	}
	// Empty allowlist -> permissive (current default, non-breaking).
	if !originAllowed("https://evil.example.com", "relay.example.com", nil) {
		t.Error("empty allowlist must be permissive")
	}
	// With an allowlist: same-host and listed origins allowed, others rejected.
	allow := []string{"https://app.example.com"}
	if !originAllowed("https://app.example.com", "relay.example.com", allow) {
		t.Error("listed origin must be allowed")
	}
	if !originAllowed("https://relay.example.com", "relay.example.com", allow) {
		t.Error("same-host origin must be allowed")
	}
	if originAllowed("https://evil.example.com", "relay.example.com", allow) {
		t.Error("unlisted cross-origin must be rejected")
	}
}
