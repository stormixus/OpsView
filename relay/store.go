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
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ip_labels (
		ip    TEXT PRIMARY KEY,
		label TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS hidden_agents (
		id TEXT PRIMARY KEY
	)`); err != nil {
		db.Close()
		return nil, err
	}
	return &agentStore{db: db}, nil
}

// hiddenAgents loads the set of operator-hidden agent ids.
func (s *agentStore) hiddenAgents() ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM hidden_agents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// setAgentHidden adds or removes an agent id from the hidden set.
func (s *agentStore) setAgentHidden(id string, hidden bool) error {
	if hidden {
		_, err := s.db.Exec(`INSERT OR IGNORE INTO hidden_agents (id) VALUES (?)`, id)
		return err
	}
	_, err := s.db.Exec(`DELETE FROM hidden_agents WHERE id=?`, id)
	return err
}

// ipLabels loads the operator-assigned IP -> name map.
func (s *agentStore) ipLabels() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT ip, label FROM ip_labels`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var ip, label string
		if err := rows.Scan(&ip, &label); err != nil {
			return nil, err
		}
		m[ip] = label
	}
	return m, rows.Err()
}

// setIPLabel upserts a name for an IP; an empty label removes it.
func (s *agentStore) setIPLabel(ip, label string) error {
	if label == "" {
		_, err := s.db.Exec(`DELETE FROM ip_labels WHERE ip=?`, ip)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO ip_labels (ip, label) VALUES (?,?)
		ON CONFLICT(ip) DO UPDATE SET label=excluded.label`, ip, label)
	return err
}

const settingDashboardToken = "dashboard_token"

const (
	settingAlertEnabled  = "alert_enabled"
	settingAlertTGToken  = "alert_telegram_token"
	settingAlertTGChat   = "alert_telegram_chat"
	settingAlertWebhook  = "alert_webhook_url"
)

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
