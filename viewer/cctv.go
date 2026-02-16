package main

import (
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// --- Data types exposed to frontend ---

type DVRConfig struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Addr          string `json:"addr"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	RefreshRate   int    `json:"refresh_rate"`  // snapshot interval in ms (default 2000)
	StreamQuality string `json:"stream_quality"` // "main" or "sub"
	CreatedAt     string `json:"created_at"`
}

type ChannelConfig struct {
	ID       int    `json:"id"`
	DVRID    int64  `json:"dvr_id"`
	ChNum    int    `json:"ch_num"`
	Name     string `json:"name"`
	Order    int    `json:"order"`
	Enabled  bool   `json:"enabled"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// CCTVManager handles multiple DVR connections and channel configuration via SQLite.
type CCTVManager struct {
	ctx    context.Context
	mu     sync.RWMutex
	db     *sql.DB
	dbPath string
	client *http.Client
}

func NewCCTVManager() *CCTVManager {
	home, _ := os.UserHomeDir()
	dbDir := filepath.Join(home, ".opsview")
	os.MkdirAll(dbDir, 0755)

	dbPath := filepath.Join(dbDir, "cctv.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("[cctv] open db: %v", err)
	}

	m := &CCTVManager{
		db:     db,
		dbPath: dbPath,
		client: &http.Client{Timeout: 10 * time.Second},
	}
	m.migrate()
	return m
}

func (m *CCTVManager) startup(ctx context.Context) {
	m.ctx = ctx
}

func (m *CCTVManager) migrate() {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS dvrs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '',
			addr TEXT NOT NULL,
			port INTEGER NOT NULL DEFAULT 80,
			username TEXT NOT NULL DEFAULT 'admin',
			password TEXT NOT NULL DEFAULT '',
			refresh_rate INTEGER NOT NULL DEFAULT 2000,
			stream_quality TEXT NOT NULL DEFAULT 'sub',
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS channels (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			dvr_id INTEGER NOT NULL REFERENCES dvrs(id) ON DELETE CASCADE,
			ch_num INTEGER NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			display_order INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			width INTEGER NOT NULL DEFAULT 0,
			height INTEGER NOT NULL DEFAULT 0,
			UNIQUE(dvr_id, ch_num)
		)`,
	}
	for _, s := range stmts {
		if _, err := m.db.Exec(s); err != nil {
			log.Printf("[cctv] migrate: %v", err)
		}
	}
}

// --- DVR CRUD ---

func (m *CCTVManager) ListDVRs() ([]DVRConfig, error) {
	rows, err := m.db.Query(`SELECT id, name, addr, port, username, password, refresh_rate, stream_quality, created_at FROM dvrs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dvrs []DVRConfig
	for rows.Next() {
		var d DVRConfig
		rows.Scan(&d.ID, &d.Name, &d.Addr, &d.Port, &d.Username, &d.Password, &d.RefreshRate, &d.StreamQuality, &d.CreatedAt)
		dvrs = append(dvrs, d)
	}
	return dvrs, nil
}

func (m *CCTVManager) AddDVR(name, addr string, port int, username, password string) (DVRConfig, error) {
	if name == "" {
		name = addr
	}
	res, err := m.db.Exec(`INSERT INTO dvrs (name, addr, port, username, password) VALUES (?, ?, ?, ?, ?)`,
		name, addr, port, username, password)
	if err != nil {
		return DVRConfig{}, err
	}
	id, _ := res.LastInsertId()
	return DVRConfig{ID: id, Name: name, Addr: addr, Port: port, Username: username, Password: password, RefreshRate: 2000, StreamQuality: "sub"}, nil
}

func (m *CCTVManager) UpdateDVR(id int64, name, addr string, port int, username, password string, refreshRate int, streamQuality string) error {
	_, err := m.db.Exec(`UPDATE dvrs SET name=?, addr=?, port=?, username=?, password=?, refresh_rate=?, stream_quality=? WHERE id=?`,
		name, addr, port, username, password, refreshRate, streamQuality, id)
	return err
}

func (m *CCTVManager) DeleteDVR(id int64) error {
	_, err := m.db.Exec(`DELETE FROM dvrs WHERE id=?`, id)
	return err
}

// --- Channel management ---

func (m *CCTVManager) ListChannels(dvrID int64) ([]ChannelConfig, error) {
	rows, err := m.db.Query(`SELECT id, dvr_id, ch_num, name, display_order, enabled, width, height FROM channels WHERE dvr_id=? ORDER BY display_order`, dvrID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chs []ChannelConfig
	for rows.Next() {
		var ch ChannelConfig
		var en int
		rows.Scan(&ch.ID, &ch.DVRID, &ch.ChNum, &ch.Name, &ch.Order, &en, &ch.Width, &ch.Height)
		ch.Enabled = en == 1
		chs = append(chs, ch)
	}
	return chs, nil
}

func (m *CCTVManager) DiscoverChannels(dvrID int64) ([]ChannelConfig, error) {
	dvr, err := m.getDVR(dvrID)
	if err != nil {
		return nil, err
	}

	discovered, err := m.discoverFromDVR(dvr)
	if err != nil {
		return nil, err
	}

	// Upsert channels
	for _, ch := range discovered {
		_, err := m.db.Exec(`INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height)
			VALUES (?, ?, ?, ?, 1, ?, ?)
			ON CONFLICT(dvr_id, ch_num) DO UPDATE SET width=excluded.width, height=excluded.height`,
			dvrID, ch.ChNum, ch.Name, ch.Order, ch.Width, ch.Height)
		if err != nil {
			log.Printf("[cctv] upsert ch %d: %v", ch.ChNum, err)
		}
	}

	return m.ListChannels(dvrID)
}

func (m *CCTVManager) UpdateChannel(id int, name string, order int, enabled bool) error {
	en := 0
	if enabled {
		en = 1
	}
	_, err := m.db.Exec(`UPDATE channels SET name=?, display_order=?, enabled=? WHERE id=?`, name, order, en, id)
	return err
}

func (m *CCTVManager) ReorderChannels(dvrID int64, orderedChNums []int) error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	for i, chNum := range orderedChNums {
		tx.Exec(`UPDATE channels SET display_order=? WHERE dvr_id=? AND ch_num=?`, i, dvrID, chNum)
	}
	return tx.Commit()
}

// --- Snapshot fetching with auto resolution detection ---

func (m *CCTVManager) FetchSnapshot(dvrID int64, chNum int, qualityOverride string) ([]byte, error) {
	dvr, err := m.getDVR(dvrID)
	if err != nil {
		return nil, err
	}

	// Use sub stream for snapshots (less bandwidth) or main based on config
	streamID := "02" // sub
	if qualityOverride == "main" || (qualityOverride == "" && dvr.StreamQuality == "main") {
		streamID = "01"
	}

	url := fmt.Sprintf("http://%s:%d/ISAPI/Streaming/channels/%d%s/picture",
		dvr.Addr, dvr.Port, chNum, streamID)

	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth(dvr.Username, dvr.Password)

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DVR returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Auto-detect resolution from JPEG and update DB
	go m.detectAndSaveResolution(dvrID, chNum, data)

	return data, nil
}

func (m *CCTVManager) detectAndSaveResolution(dvrID int64, chNum int, jpegData []byte) {
	cfg, _, err := image.DecodeConfig(bytesReader(jpegData))
	if err != nil {
		return
	}
	if cfg.Width > 0 && cfg.Height > 0 {
		m.db.Exec(`UPDATE channels SET width=?, height=? WHERE dvr_id=? AND ch_num=?`,
			cfg.Width, cfg.Height, dvrID, chNum)
	}
}

// --- Hikvision ISAPI discovery ---

type isAPIChannelList struct {
	XMLName  xml.Name       `xml:"StreamingChannelList"`
	Channels []isAPIChannel `xml:"StreamingChannel"`
}

type isAPIChannel struct {
	ID      int    `xml:"id"`
	Name    string `xml:"channelName"`
	Enabled bool   `xml:"enabled"`
}

type isAPIVideoInfo struct {
	Width  int `xml:"videoResolutionWidth"`
	Height int `xml:"videoResolutionHeight"`
}

func (m *CCTVManager) discoverFromDVR(dvr DVRConfig) ([]ChannelConfig, error) {
	url := fmt.Sprintf("http://%s:%d/ISAPI/Streaming/channels", dvr.Addr, dvr.Port)
	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth(dvr.Username, dvr.Password)

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to DVR: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("DVR returned %d: %s", resp.StatusCode, string(body))
	}

	var list isAPIChannelList
	if err := xml.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("parse channel list: %w", err)
	}

	seen := make(map[int]bool)
	var channels []ChannelConfig
	for _, ch := range list.Channels {
		chNum := ch.ID / 100
		if chNum == 0 || seen[chNum] {
			continue
		}
		seen[chNum] = true

		name := ch.Name
		if name == "" {
			name = fmt.Sprintf("Channel %d", chNum)
		}

		// Try to get resolution from ISAPI
		w, h := m.fetchChannelResolution(dvr, chNum)

		channels = append(channels, ChannelConfig{
			DVRID: dvr.ID,
			ChNum: chNum,
			Name:  name,
			Order: chNum - 1,
			Width: w,
			Height: h,
		})
	}

	return channels, nil
}

func (m *CCTVManager) fetchChannelResolution(dvr DVRConfig, chNum int) (int, int) {
	url := fmt.Sprintf("http://%s:%d/ISAPI/Streaming/channels/%d01", dvr.Addr, dvr.Port, chNum)
	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth(dvr.Username, dvr.Password)

	resp, err := m.client.Do(req)
	if err != nil {
		return 0, 0
	}
	defer resp.Body.Close()

	var info isAPIVideoInfo
	xml.NewDecoder(resp.Body).Decode(&info)
	return info.Width, info.Height
}

// --- Helpers ---

func (m *CCTVManager) getDVR(id int64) (DVRConfig, error) {
	var d DVRConfig
	err := m.db.QueryRow(`SELECT id, name, addr, port, username, password, refresh_rate, stream_quality FROM dvrs WHERE id=?`, id).
		Scan(&d.ID, &d.Name, &d.Addr, &d.Port, &d.Username, &d.Password, &d.RefreshRate, &d.StreamQuality)
	return d, err
}

type bytesReaderType struct{ b []byte; i int }
func (r *bytesReaderType) Read(p []byte) (int, error) {
	if r.i >= len(r.b) { return 0, io.EOF }
	n := copy(p, r.b[r.i:]); r.i += n; return n, nil
}
func bytesReader(b []byte) io.Reader { return &bytesReaderType{b: b} }
