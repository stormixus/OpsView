package main

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
)

func newTestSurvManager(t *testing.T) *SurveillanceManager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cctv.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1) // mirror NewSurveillanceManager
	t.Cleanup(func() { db.Close() })
	m := &SurveillanceManager{db: db, dbPath: path}
	m.migrate()
	return m
}

func TestResetDBWipesDataAndKeepsHandleUsable(t *testing.T) {
	m := newTestSurvManager(t)
	if _, err := m.AddDVR("cam", "10.0.0.1", 80, "", 0, "admin", "pw", "isapi", 2000, "sub"); err != nil {
		t.Fatalf("AddDVR: %v", err)
	}
	dvrs, err := m.ListDVRs()
	if err != nil || len(dvrs) != 1 {
		t.Fatalf("pre-reset ListDVRs = %d (%v), want 1", len(dvrs), err)
	}

	if err := m.ResetDB(); err != nil {
		t.Fatalf("ResetDB: %v", err)
	}

	// The handle must still be usable (no use-after-close) and the data gone.
	dvrs, err = m.ListDVRs()
	if err != nil {
		t.Fatalf("post-reset ListDVRs errored (handle unusable?): %v", err)
	}
	if len(dvrs) != 0 {
		t.Fatalf("post-reset ListDVRs = %d, want 0 (data not wiped)", len(dvrs))
	}

	// And inserts must still work after a reset.
	if _, err := m.AddDVR("cam2", "10.0.0.2", 80, "", 0, "admin", "pw", "isapi", 2000, "sub"); err != nil {
		t.Fatalf("AddDVR after reset: %v", err)
	}
}

// ResetDB must not race concurrent readers. Run with -race: the old
// implementation swapped m.db out from under in-flight callers.
func TestResetDBConcurrentReadsNoRace(t *testing.T) {
	m := newTestSurvManager(t)
	if _, err := m.AddDVR("cam", "10.0.0.1", 80, "", 0, "admin", "pw", "isapi", 2000, "sub"); err != nil {
		t.Fatalf("AddDVR: %v", err)
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					m.ListDVRs()
					m.ListChannels(1)
				}
			}
		}()
	}

	for i := 0; i < 20; i++ {
		if err := m.ResetDB(); err != nil {
			t.Errorf("ResetDB iteration %d: %v", i, err)
		}
	}
	close(done)
	wg.Wait()
}
