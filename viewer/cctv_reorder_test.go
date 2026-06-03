package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func newTestCCTVManager(t *testing.T) *CCTVManager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cctv.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	m := &CCTVManager{db: db, dbPath: path}
	m.migrate()
	return m
}

func TestReorderDVRsPersistsOrderAndCommits(t *testing.T) {
	m := newTestCCTVManager(t)
	for _, id := range []int64{1, 2, 3} {
		if _, err := m.db.Exec(`INSERT INTO dvrs (id, addr, display_order) VALUES (?,?,?)`, id, "10.0.0.1", 0); err != nil {
			t.Fatalf("insert dvr %d: %v", id, err)
		}
	}

	// New order: 3, 1, 2
	if err := m.ReorderDVRs([]int64{3, 1, 2}); err != nil {
		t.Fatalf("ReorderDVRs: %v", err)
	}

	want := map[int64]int{3: 0, 1: 1, 2: 2}
	rows, err := m.db.Query(`SELECT id, display_order FROM dvrs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var id int64
		var ord int
		if err := rows.Scan(&id, &ord); err != nil {
			t.Fatal(err)
		}
		if want[id] != ord {
			t.Errorf("dvr %d: display_order=%d, want %d", id, ord, want[id])
		}
		seen++
	}
	if seen != 3 {
		t.Fatalf("expected 3 dvrs, saw %d", seen)
	}
}
