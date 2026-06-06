package main

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

// agentStore persists the named-agent (tenant) registry in SQLite so the
// dashboard can manage locations without editing env + restarting.
type agentStore struct {
	db *sql.DB
}

func openAgentStore(path string) (*agentStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS agents (
		id    TEXT PRIMARY KEY,
		name  TEXT NOT NULL DEFAULT '',
		token TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	return &agentStore{db: db}, nil
}

const settingDashboardToken = "dashboard_token"

// getSetting returns a stored setting value ("" if unset).
func (s *agentStore) getSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// setSetting upserts a setting value.
func (s *agentStore) setSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *agentStore) count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&n)
	return n, err
}

func (s *agentStore) list() ([]agentEntry, error) {
	rows, err := s.db.Query(`SELECT id, name, token FROM agents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []agentEntry
	for rows.Next() {
		var e agentEntry
		if err := rows.Scan(&e.ID, &e.Name, &e.Token); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *agentStore) upsert(e agentEntry) error {
	_, err := s.db.Exec(`INSERT INTO agents (id, name, token) VALUES (?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, token=excluded.token`,
		e.ID, e.Name, e.Token)
	return err
}

func (s *agentStore) remove(id string) error {
	_, err := s.db.Exec(`DELETE FROM agents WHERE id=?`, id)
	return err
}

func (s *agentStore) close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}
