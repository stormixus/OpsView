package main

import "testing"

func TestNewAgentSessionDefaults(t *testing.T) {
	s := newAgentSession("gangnam", "강남점")
	if s.id != "gangnam" || s.name != "강남점" {
		t.Fatalf("id/name = %q/%q", s.id, s.name)
	}
	if s.survProxy == nil || s.frameBuf == nil || s.watchers == nil {
		t.Fatal("session must init survProxy/frameBuf/watchers")
	}
	if s.online() {
		t.Fatal("new session has no publisher => offline")
	}
}
