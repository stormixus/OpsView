//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO deliberately stalls disk I/O. Unrelated channels and global state
// must remain usable until that I/O completes.
func TestEventStoreBlockedDiskDoesNotHoldGlobalLock(t *testing.T) {
	es := newEventStore(t.TempDir())
	day := "20260608"
	path := filepath.Join(es.recDir, "slow", ".events", day+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { es.eventsForDay("slow", day); close(done) }()
	// Opening the FIFO read/write will always unblock the reader, even on failure.
	defer func() {
		f, err := os.OpenFile(path, os.O_RDWR, 0600)
		if err != nil {
			t.Error(err)
			return
		}
		f.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("blocked reader did not exit")
		}
	}()
	deadline := time.Now().Add(time.Second)
	for es.streamLock("slow").TryLock() {
		es.streamLock("slow").Unlock()
		if time.Now().After(deadline) {
			t.Fatal("reader did not start")
		}
		time.Sleep(time.Millisecond)
	}
	for !es.mu.TryLock() {
		if time.Now().After(deadline) {
			t.Fatal("disk I/O holds global state lock")
		}
		time.Sleep(time.Millisecond)
	}
	es.mu.Unlock()
	other := "fast"
	for es.streamLock(other) == es.streamLock("slow") {
		other += "x"
	}
	fast := make(chan struct{})
	go func() {
		es.add(other, "motion", true, 1_000_000)
		es.add(other, "motion", false, 1_001_000)
		es.eventsForDay(other, dayKeyFromMs(1_000_000))
		close(fast)
	}()
	select {
	case <-fast:
	case <-time.After(time.Second):
		t.Fatal("unrelated stream stalled behind disk I/O")
	}
}
