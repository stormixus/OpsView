package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// SurveillanceManager handles DVR connections and channel configuration via SQLite.
// This is the agent-side version (no upscale, no streaming — raw snapshots only).
type SurveillanceManager struct {
	mu          sync.RWMutex
	db          *sql.DB
	dbPath      string
	client      *http.Client
	shortClient *http.Client
	onChange    func() // called after DVR add/update/delete
}

func NewSurveillanceManager() *SurveillanceManager {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		home, _ := os.UserHomeDir()
		appData = filepath.Join(home, "AppData", "Roaming")
	}
	dbDir := filepath.Join(appData, "opsview-agent")
	os.MkdirAll(dbDir, 0755)

	dbPath := filepath.Join(dbDir, "cctv.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("[surv] open db: %v", err)
	}

	m := &SurveillanceManager{
		db:          db,
		dbPath:      dbPath,
		client:      &http.Client{Timeout: 10 * time.Second},
		shortClient: &http.Client{Timeout: 3 * time.Second},
	}
	m.migrate()
	return m
}

func (m *SurveillanceManager) migrate() {
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
			protocol TEXT NOT NULL DEFAULT 'isapi',
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
			log.Printf("[surv] migrate: %v", err)
		}
	}
	m.db.Exec(`ALTER TABLE dvrs ADD COLUMN protocol TEXT NOT NULL DEFAULT 'isapi'`)
}

// --- DVR CRUD ---

type DVRConfig struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Addr          string `json:"addr"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	RefreshRate   int    `json:"refresh_rate"`
	StreamQuality string `json:"stream_quality"`
	Protocol      string `json:"protocol"`
	CreatedAt     string `json:"created_at"`
}

type ChannelConfig struct {
	ID      int    `json:"id"`
	DVRID   int64  `json:"dvr_id"`
	ChNum   int    `json:"ch_num"`
	Name    string `json:"name"`
	Order   int    `json:"order"`
	Enabled bool   `json:"enabled"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

func (m *SurveillanceManager) ListDVRs() ([]DVRConfig, error) {
	rows, err := m.db.Query(`SELECT id, name, addr, port, username, password, refresh_rate, stream_quality, protocol, created_at FROM dvrs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dvrs []DVRConfig
	for rows.Next() {
		var d DVRConfig
		rows.Scan(&d.ID, &d.Name, &d.Addr, &d.Port, &d.Username, &d.Password, &d.RefreshRate, &d.StreamQuality, &d.Protocol, &d.CreatedAt)
		dvrs = append(dvrs, d)
	}
	return dvrs, nil
}

func (m *SurveillanceManager) AddDVR(name, addr string, port int, username, password, protocol string, refreshRate int, streamQuality string) (DVRConfig, error) {
	if name == "" {
		name = addr
	}
	if protocol == "" {
		protocol = "isapi"
	}
	if refreshRate <= 0 {
		refreshRate = 2000
	}
	if streamQuality == "" {
		streamQuality = "sub"
	}
	res, err := m.db.Exec(`INSERT INTO dvrs (name, addr, port, username, password, protocol, refresh_rate, stream_quality) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		name, addr, port, username, password, protocol, refreshRate, streamQuality)
	if err != nil {
		return DVRConfig{}, err
	}
	id, _ := res.LastInsertId()
	if m.onChange != nil {
		m.onChange()
	}
	return DVRConfig{ID: id, Name: name, Addr: addr, Port: port, Username: username, Password: password, RefreshRate: refreshRate, StreamQuality: streamQuality, Protocol: protocol}, nil
}

func (m *SurveillanceManager) UpdateDVR(id int64, name, addr string, port int, username, password string, refreshRate int, streamQuality, protocol string) error {
	if protocol == "" {
		protocol = "auto"
	}
	_, err := m.db.Exec(`UPDATE dvrs SET name=?, addr=?, port=?, username=?, password=?, refresh_rate=?, stream_quality=?, protocol=? WHERE id=?`,
		name, addr, port, username, password, refreshRate, streamQuality, protocol, id)
	if err == nil && m.onChange != nil {
		m.onChange()
	}
	return err
}

func (m *SurveillanceManager) DeleteDVR(id int64) error {
	_, err := m.db.Exec(`DELETE FROM dvrs WHERE id=?`, id)
	if err == nil && m.onChange != nil {
		m.onChange()
	}
	return err
}

// --- Channel management ---

func (m *SurveillanceManager) ListChannels(dvrID int64) ([]ChannelConfig, error) {
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

func (m *SurveillanceManager) DiscoverChannels(dvrID int64) ([]ChannelConfig, error) {
	dvr, err := m.getDVR(dvrID)
	if err != nil {
		return nil, err
	}

	if dvr.Protocol == "auto" || dvr.Protocol == "" {
		dvr.Protocol = m.probeDVRProtocol(dvr)
		m.db.Exec(`UPDATE dvrs SET protocol=? WHERE id=?`, dvr.Protocol, dvr.ID)
	}

	var discovered []ChannelConfig
	switch dvr.Protocol {
	case "rtsp":
		discovered, err = m.discoverFromDVRRTSP(dvr)
	case "dahua":
		discovered, err = m.discoverFromDVRDahua(dvr)
	default:
		discovered, err = m.discoverFromDVRISAPI(dvr)
	}
	if err != nil {
		return nil, err
	}

	for _, ch := range discovered {
		_, err := m.db.Exec(`INSERT INTO channels (dvr_id, ch_num, name, display_order, enabled, width, height)
			VALUES (?, ?, ?, ?, 1, ?, ?)
			ON CONFLICT(dvr_id, ch_num) DO UPDATE SET width=excluded.width, height=excluded.height`,
			dvrID, ch.ChNum, ch.Name, ch.Order, ch.Width, ch.Height)
		if err != nil {
			log.Printf("[surv] upsert ch %d: %v", ch.ChNum, err)
		}
	}

	if m.onChange != nil {
		m.onChange()
	}
	return m.ListChannels(dvrID)
}

// --- Snapshot fetching (raw, no upscale) ---

func (m *SurveillanceManager) FetchSnapshot(dvrID int64, chNum int) ([]byte, error) {
	dvr, err := m.getDVR(dvrID)
	if err != nil {
		return nil, err
	}

	var data []byte
	switch dvr.Protocol {
	case "rtsp":
		data, err = m.fetchSnapshotISAPIOnPort(dvr, chNum, 80)
		if err != nil {
			data, err = m.fetchSnapshotRTSP(dvr, chNum)
		}
	case "dahua":
		data, err = m.fetchSnapshotDahua(dvr, chNum)
	default:
		data, err = m.fetchSnapshotISAPI(dvr, chNum)
	}
	return data, err
}

func (m *SurveillanceManager) fetchSnapshotISAPI(dvr DVRConfig, chNum int) ([]byte, error) {
	streamID := "02"
	if dvr.StreamQuality == "main" {
		streamID = "01"
	}
	u := fmt.Sprintf("http://%s:%d/ISAPI/Streaming/channels/%d%s/picture",
		dvr.Addr, dvr.Port, chNum, streamID)
	req, _ := http.NewRequest("GET", u, nil)
	req.SetBasicAuth(dvr.Username, dvr.Password)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DVR returned %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (m *SurveillanceManager) fetchSnapshotISAPIOnPort(dvr DVRConfig, chNum int, port int) ([]byte, error) {
	streamID := "02"
	if dvr.StreamQuality == "main" {
		streamID = "01"
	}
	u := fmt.Sprintf("http://%s:%d/ISAPI/Streaming/channels/%d%s/picture",
		dvr.Addr, port, chNum, streamID)
	req, _ := http.NewRequest("GET", u, nil)
	req.SetBasicAuth(dvr.Username, dvr.Password)
	resp, err := m.shortClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("ISAPI port %d returned %d", port, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (m *SurveillanceManager) fetchSnapshotDahua(dvr DVRConfig, chNum int) ([]byte, error) {
	streamID := 1
	if dvr.StreamQuality == "sub" {
		streamID = 2
	}
	u := fmt.Sprintf("http://%s:%d/cgi-bin/snapshot.cgi?channel=%d&subtype=%d", dvr.Addr, dvr.Port, chNum, streamID)
	req, _ := http.NewRequest("GET", u, nil)
	req.SetBasicAuth(dvr.Username, dvr.Password)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Dahua snapshot returned %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func (m *SurveillanceManager) fetchSnapshotRTSP(dvr DVRConfig, chNum int) ([]byte, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg not found: install ffmpeg for RTSP snapshots")
	}
	streamID := "02"
	if dvr.StreamQuality == "main" {
		streamID = "01"
	}
	rtspURL := buildRTSPURL(dvr.Username, dvr.Password, dvr.Addr, dvr.Port,
		fmt.Sprintf("/Streaming/Channels/%d%s", chNum, streamID))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-rtsp_transport", "tcp", "-i", rtspURL,
		"-frames:v", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "-q:v", "2", "pipe:1",
	)
	data, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("RTSP snapshot timeout (10s)")
		}
		return nil, fmt.Errorf("ffmpeg: %w", err)
	}
	return data, nil
}

// --- Protocol detection ---

func (m *SurveillanceManager) probeDVRProtocol(dvr DVRConfig) string {
	urlHik := fmt.Sprintf("http://%s:%d/ISAPI/System/deviceInfo", dvr.Addr, dvr.Port)
	reqHik, _ := http.NewRequest("GET", urlHik, nil)
	reqHik.SetBasicAuth(dvr.Username, dvr.Password)
	if respHik, err := m.shortClient.Do(reqHik); err == nil {
		respHik.Body.Close()
		if respHik.StatusCode == 200 {
			log.Printf("[surv] Probed ISAPI (Hikvision) for %s:%d", dvr.Addr, dvr.Port)
			return "isapi"
		}
	}

	urlDahua := fmt.Sprintf("http://%s:%d/cgi-bin/magicBox.cgi?action=getSystemInfo", dvr.Addr, dvr.Port)
	reqDahua, _ := http.NewRequest("GET", urlDahua, nil)
	reqDahua.SetBasicAuth(dvr.Username, dvr.Password)
	if respDahua, err := m.shortClient.Do(reqDahua); err == nil {
		respDahua.Body.Close()
		if respDahua.StatusCode == 200 || respDahua.StatusCode == 401 {
			log.Printf("[surv] Probed Dahua CGI for %s:%d", dvr.Addr, dvr.Port)
			return "dahua"
		}
	}

	log.Printf("[surv] Probe fallback to RTSP for %s:%d", dvr.Addr, dvr.Port)
	return "rtsp"
}

// --- Discovery ---

type isAPIDeviceInfo struct {
	XMLName           xml.Name `xml:"DeviceInfo"`
	AnalogChannelNum  int      `xml:"analogChannelNum"`
	DigitalChannelNum int      `xml:"digitalChannelNum"`
}

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

type isAPIVideoInputList struct {
	XMLName  xml.Name          `xml:"VideoInputChannelList"`
	Channels []isAPIVideoInput `xml:"VideoInputChannel"`
}

type isAPIVideoInput struct {
	ID   int    `xml:"id"`
	Name string `xml:"inputPort>name"`
}

func (m *SurveillanceManager) discoverFromDVRISAPI(dvr DVRConfig) ([]ChannelConfig, error) {
	channels, err := m.discoverISAPIDeviceInfo(dvr)
	if err != nil {
		log.Printf("[surv] ISAPI deviceInfo discovery failed: %v", err)
	}
	if len(channels) == 0 {
		channels, err = m.discoverISAPIStreaming(dvr)
		if err != nil {
			log.Printf("[surv] ISAPI streaming discovery failed: %v", err)
		}
	}
	if len(channels) == 0 {
		channels, err = m.discoverISAPIVideoInputs(dvr)
		if err != nil {
			return nil, fmt.Errorf("ISAPI discovery failed: %w", err)
		}
	}
	if len(channels) == 0 {
		return nil, fmt.Errorf("no channels found via ISAPI")
	}
	return channels, nil
}

func (m *SurveillanceManager) discoverISAPIDeviceInfo(dvr DVRConfig) ([]ChannelConfig, error) {
	u := fmt.Sprintf("http://%s:%d/ISAPI/System/deviceInfo", dvr.Addr, dvr.Port)
	req, _ := http.NewRequest("GET", u, nil)
	req.SetBasicAuth(dvr.Username, dvr.Password)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DVR returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var info isAPIDeviceInfo
	if err := xml.Unmarshal(body, &info); err != nil {
		return nil, err
	}
	total := info.AnalogChannelNum + info.DigitalChannelNum
	if total == 0 {
		return nil, fmt.Errorf("deviceInfo reports 0 channels")
	}
	log.Printf("[surv] deviceInfo: analog=%d digital=%d total=%d", info.AnalogChannelNum, info.DigitalChannelNum, total)
	var channels []ChannelConfig
	for ch := 1; ch <= total; ch++ {
		w, h := m.fetchChannelResolution(dvr, ch)
		channels = append(channels, ChannelConfig{DVRID: dvr.ID, ChNum: ch, Name: fmt.Sprintf("Channel %d", ch), Order: ch - 1, Width: w, Height: h})
	}
	return channels, nil
}

func (m *SurveillanceManager) discoverISAPIStreaming(dvr DVRConfig) ([]ChannelConfig, error) {
	u := fmt.Sprintf("http://%s:%d/ISAPI/Streaming/channels", dvr.Addr, dvr.Port)
	req, _ := http.NewRequest("GET", u, nil)
	req.SetBasicAuth(dvr.Username, dvr.Password)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DVR returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var list isAPIChannelList
	if err := xml.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	seen := make(map[int]bool)
	var channels []ChannelConfig
	for _, ch := range list.Channels {
		chNum := ch.ID
		if ch.ID >= 100 {
			chNum = ch.ID / 100
		}
		if chNum == 0 || seen[chNum] {
			continue
		}
		seen[chNum] = true
		name := ch.Name
		if name == "" {
			name = fmt.Sprintf("Channel %d", chNum)
		}
		w, h := m.fetchChannelResolution(dvr, chNum)
		channels = append(channels, ChannelConfig{DVRID: dvr.ID, ChNum: chNum, Name: name, Order: chNum - 1, Width: w, Height: h})
	}
	return channels, nil
}

func (m *SurveillanceManager) discoverISAPIVideoInputs(dvr DVRConfig) ([]ChannelConfig, error) {
	u := fmt.Sprintf("http://%s:%d/ISAPI/System/Video/inputs/channels", dvr.Addr, dvr.Port)
	req, _ := http.NewRequest("GET", u, nil)
	req.SetBasicAuth(dvr.Username, dvr.Password)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DVR returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var list isAPIVideoInputList
	if err := xml.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	var channels []ChannelConfig
	for _, ch := range list.Channels {
		if ch.ID == 0 {
			continue
		}
		name := ch.Name
		if name == "" {
			name = fmt.Sprintf("Channel %d", ch.ID)
		}
		w, h := m.fetchChannelResolution(dvr, ch.ID)
		channels = append(channels, ChannelConfig{DVRID: dvr.ID, ChNum: ch.ID, Name: name, Order: ch.ID - 1, Width: w, Height: h})
	}
	return channels, nil
}

func (m *SurveillanceManager) discoverFromDVRDahua(dvr DVRConfig) ([]ChannelConfig, error) {
	urlSys := fmt.Sprintf("http://%s:%d/cgi-bin/magicBox.cgi?action=getSystemInfo", dvr.Addr, dvr.Port)
	reqSys, _ := http.NewRequest("GET", urlSys, nil)
	reqSys.SetBasicAuth(dvr.Username, dvr.Password)

	totalChannels := 4
	if respSys, err := m.client.Do(reqSys); err == nil {
		defer respSys.Body.Close()
		sysBody, _ := io.ReadAll(respSys.Body)
		if bytes.Contains(sysBody, []byte("maxTotal=32")) {
			totalChannels = 32
		} else if bytes.Contains(sysBody, []byte("maxTotal=16")) {
			totalChannels = 16
		} else if bytes.Contains(sysBody, []byte("maxTotal=8")) {
			totalChannels = 8
		}
	}

	var channels []ChannelConfig
	for ch := 1; ch <= totalChannels; ch++ {
		channels = append(channels, ChannelConfig{DVRID: dvr.ID, ChNum: ch, Name: fmt.Sprintf("Channel %d", ch), Order: ch - 1})
	}
	return channels, nil
}

func (m *SurveillanceManager) discoverFromDVRRTSP(dvr DVRConfig) ([]ChannelConfig, error) {
	const maxChannels = 32
	const concurrency = 4

	type probeResult struct {
		ch    int
		found bool
	}

	sem := make(chan struct{}, concurrency)
	results := make(chan probeResult, maxChannels)
	var wg sync.WaitGroup

	for ch := 1; ch <= maxChannels; ch++ {
		wg.Add(1)
		go func(ch int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rtspURL := buildRTSPURL(dvr.Username, dvr.Password, dvr.Addr, dvr.Port,
				fmt.Sprintf("/Streaming/Channels/%d01", ch))
			found := probeRTSPChannel(rtspURL)
			results <- probeResult{ch: ch, found: found}
		}(ch)
	}

	go func() { wg.Wait(); close(results) }()

	foundSet := make(map[int]bool)
	for r := range results {
		if r.found {
			foundSet[r.ch] = true
		}
	}

	var channels []ChannelConfig
	misses := 0
	for ch := 1; ch <= maxChannels; ch++ {
		if foundSet[ch] {
			misses = 0
			channels = append(channels, ChannelConfig{DVRID: dvr.ID, ChNum: ch, Name: fmt.Sprintf("Channel %d", ch), Order: ch - 1})
		} else if len(channels) > 0 {
			misses++
			if misses >= 3 {
				break
			}
		}
	}
	if len(channels) == 0 {
		return nil, fmt.Errorf("no RTSP channels found")
	}
	return channels, nil
}

func probeRTSPChannel(rtspURL string) bool {
	// Simple TCP connect probe to RTSP port
	u, err := url.Parse(rtspURL)
	if err != nil {
		return false
	}
	conn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (m *SurveillanceManager) fetchChannelResolution(dvr DVRConfig, chNum int) (int, int) {
	u := fmt.Sprintf("http://%s:%d/ISAPI/Streaming/channels/%d01", dvr.Addr, dvr.Port, chNum)
	req, _ := http.NewRequest("GET", u, nil)
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

func buildRTSPURL(username, password, addr string, port int, path string) string {
	u := &url.URL{
		Scheme: "rtsp",
		User:   url.UserPassword(username, password),
		Host:   fmt.Sprintf("%s:%d", addr, port),
		Path:   path,
	}
	return u.String()
}

// --- Helpers ---

func (m *SurveillanceManager) getDVR(id int64) (DVRConfig, error) {
	var d DVRConfig
	err := m.db.QueryRow(`SELECT id, name, addr, port, username, password, refresh_rate, stream_quality, protocol FROM dvrs WHERE id=?`, id).
		Scan(&d.ID, &d.Name, &d.Addr, &d.Port, &d.Username, &d.Password, &d.RefreshRate, &d.StreamQuality, &d.Protocol)
	if d.Protocol == "" {
		d.Protocol = "isapi"
	}
	return d, err
}

func (m *SurveillanceManager) Shutdown() {
	if m.db != nil {
		m.db.Close()
	}
}
