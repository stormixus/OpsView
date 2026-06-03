package main

import (
	"testing"
	"time"
)

func TestPinLimiterBlocksAfterMaxFailures(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	now := base
	l := newPinLimiter()
	l.now = func() time.Time { return now }
	l.maxFails = 3
	l.window = time.Minute
	l.block = 5 * time.Minute

	ip := "1.2.3.4"
	if !l.allowed(ip) {
		t.Fatal("fresh IP should be allowed")
	}
	l.recordFailure(ip)
	l.recordFailure(ip)
	if !l.allowed(ip) {
		t.Fatal("under threshold should still be allowed")
	}
	l.recordFailure(ip) // 3rd failure within window -> blocked
	if l.allowed(ip) {
		t.Fatal("should be blocked after maxFails")
	}

	now = base.Add(4 * time.Minute)
	if l.allowed(ip) {
		t.Fatal("should remain blocked within block window")
	}

	now = base.Add(6 * time.Minute)
	if !l.allowed(ip) {
		t.Fatal("should be allowed after block window expires")
	}
}

func TestPinLimiterWindowResets(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	now := base
	l := newPinLimiter()
	l.now = func() time.Time { return now }
	l.maxFails = 3
	l.window = time.Minute

	ip := "5.6.7.8"
	l.recordFailure(ip)
	l.recordFailure(ip)
	now = base.Add(2 * time.Minute) // window elapsed -> counter resets
	l.recordFailure(ip)
	if !l.allowed(ip) {
		t.Fatal("failures spread beyond the window must not accumulate to a block")
	}
}

func TestPinLimiterSuccessClears(t *testing.T) {
	l := newPinLimiter()
	l.maxFails = 3
	ip := "9.9.9.9"
	l.recordFailure(ip)
	l.recordFailure(ip)
	l.recordSuccess(ip)
	l.recordFailure(ip)
	l.recordFailure(ip)
	if !l.allowed(ip) {
		t.Fatal("a success must reset the failure counter")
	}
}
