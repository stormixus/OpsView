package main

import (
	"sync"
	"time"
)

// pinLimiter throttles watcher PIN-guessing by IP. After maxFails failed
// attempts within window, the IP is blocked for block. A successful auth clears
// the IP. The map is bounded to maxIPs (stale entries pruned) so the limiter
// itself cannot be turned into a memory-exhaustion vector.
type pinLimiter struct {
	mu       sync.Mutex
	attempts map[string]*ipAttempts
	now      func() time.Time
	maxFails int
	window   time.Duration
	block    time.Duration
	maxIPs   int
}

type ipAttempts struct {
	fails        int
	windowStart  time.Time
	blockedUntil time.Time
}

func newPinLimiter() *pinLimiter {
	return &pinLimiter{
		attempts: make(map[string]*ipAttempts),
		now:      time.Now,
		maxFails: 5,
		window:   1 * time.Minute,
		block:    5 * time.Minute,
		maxIPs:   10000,
	}
}

// allowed reports whether ip may currently attempt a watcher auth.
func (l *pinLimiter) allowed(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.attempts[ip]
	if a == nil {
		return true
	}
	return !l.now().Before(a.blockedUntil)
}

// recordFailure registers a failed attempt and blocks the IP once it reaches
// maxFails within the sliding window.
func (l *pinLimiter) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	a := l.attempts[ip]
	if a == nil {
		if len(l.attempts) >= l.maxIPs {
			l.pruneLocked(now)
		}
		a = &ipAttempts{windowStart: now}
		l.attempts[ip] = a
	}
	if now.Sub(a.windowStart) > l.window {
		a.fails = 0
		a.windowStart = now
	}
	a.fails++
	if a.fails >= l.maxFails {
		a.blockedUntil = now.Add(l.block)
		a.fails = 0
		a.windowStart = now
	}
}

// recordSuccess clears any failure state for ip.
func (l *pinLimiter) recordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}

// pruneLocked removes entries that are neither blocked nor within their window.
// Caller must hold l.mu.
func (l *pinLimiter) pruneLocked(now time.Time) {
	for ip, a := range l.attempts {
		if now.After(a.blockedUntil) && now.Sub(a.windowStart) > l.window {
			delete(l.attempts, ip)
		}
	}
}
