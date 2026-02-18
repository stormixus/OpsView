package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/jpeg"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/image/draw"
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
	Protocol      string `json:"protocol"`       // "isapi" or "rtsp"
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
	ctx        context.Context
	mu         sync.RWMutex
	db         *sql.DB
	dbPath     string
	client      *http.Client
	shortClient *http.Client // short timeout for fallback ISAPI probes
	esrganPath  string
	esrganSem  chan struct{}           // limits concurrent ESRGAN to 1
	streams    map[string]*channelStream // key: "{dvrID}_{chNum}"
	streamsMu  sync.Mutex
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
		db:          db,
		dbPath:      dbPath,
		client:      &http.Client{Timeout: 10 * time.Second},
		shortClient: &http.Client{Timeout: 3 * time.Second},
		esrganSem:   make(chan struct{}, 1),
		streams:     make(map[string]*channelStream),
	}
	m.migrate()
	m.initESRGAN()
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
			log.Printf("[cctv] migrate: %v", err)
		}
	}
	// Add protocol column for existing databases
	m.db.Exec(`ALTER TABLE dvrs ADD COLUMN protocol TEXT NOT NULL DEFAULT 'isapi'`)
}

// --- DVR CRUD ---

func (m *CCTVManager) ListDVRs() ([]DVRConfig, error) {
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

func (m *CCTVManager) AddDVR(name, addr string, port int, username, password, protocol string) (DVRConfig, error) {
	if name == "" {
		name = addr
	}
	if protocol == "" {
		protocol = "isapi"
	}
	res, err := m.db.Exec(`INSERT INTO dvrs (name, addr, port, username, password, protocol) VALUES (?, ?, ?, ?, ?, ?)`,
		name, addr, port, username, password, protocol)
	if err != nil {
		return DVRConfig{}, err
	}
	id, _ := res.LastInsertId()
	return DVRConfig{ID: id, Name: name, Addr: addr, Port: port, Username: username, Password: password, RefreshRate: 2000, StreamQuality: "sub", Protocol: protocol}, nil
}

func (m *CCTVManager) UpdateDVR(id int64, name, addr string, port int, username, password string, refreshRate int, streamQuality, protocol string) error {
	if protocol == "" {
		protocol = "isapi"
	}
	_, err := m.db.Exec(`UPDATE dvrs SET name=?, addr=?, port=?, username=?, password=?, refresh_rate=?, stream_quality=?, protocol=? WHERE id=?`,
		name, addr, port, username, password, refreshRate, streamQuality, protocol, id)
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

	var discovered []ChannelConfig
	switch dvr.Protocol {
	case "rtsp":
		discovered, err = m.discoverFromDVRRTSP(dvr)
	default: // "isapi"
		discovered, err = m.discoverFromDVRISAPI(dvr)
	}
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

func (m *CCTVManager) FetchSnapshot(dvrID int64, chNum int, upscale int, aiUpscale bool) ([]byte, error) {
	dvr, err := m.getDVR(dvrID)
	if err != nil {
		return nil, err
	}

	var data []byte
	switch dvr.Protocol {
	case "rtsp":
		// Try ISAPI first (faster, no ffmpeg needed) — Hikvision DVRs support both
		data, err = m.fetchSnapshotISAPIOnPort(dvr, chNum, 80)
		if err != nil {
			data, err = m.fetchSnapshotRTSP(dvr, chNum)
		}
	default: // "isapi"
		data, err = m.fetchSnapshotISAPI(dvr, chNum)
	}
	if err != nil {
		return nil, err
	}

	// Auto-detect resolution from JPEG and update DB (only if not yet known)
	m.maybeDetectResolution(dvrID, chNum, data)

	// Upscale if requested
	if upscale > 1 {
		if aiUpscale && m.hasESRGAN() {
			// Try to acquire semaphore (non-blocking); fall back to bicubic if busy
			select {
			case m.esrganSem <- struct{}{}:
				scaled, esrErr := m.upscaleESRGAN(data, upscale)
				<-m.esrganSem
				if esrErr == nil {
					return scaled, nil
				}
				log.Printf("[cctv] esrgan ch%d: %v, falling back to bicubic", chNum, esrErr)
			default:
				log.Printf("[cctv] esrgan busy, ch%d using bicubic", chNum)
			}
		}
		data, err = m.upscaleBicubic(data, upscale)
		if err != nil {
			log.Printf("[cctv] upscale ch%d: %v", chNum, err)
		}
	}

	return data, nil
}

// fetchSnapshotISAPI fetches a snapshot via Hikvision ISAPI HTTP REST.
func (m *CCTVManager) fetchSnapshotISAPI(dvr DVRConfig, chNum int) ([]byte, error) {
	streamID := "02"
	if dvr.StreamQuality == "main" {
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

	return io.ReadAll(resp.Body)
}

// fetchSnapshotISAPIOnPort tries ISAPI snapshot on a specific port (for RTSP DVRs that also have ISAPI).
func (m *CCTVManager) fetchSnapshotISAPIOnPort(dvr DVRConfig, chNum int, port int) ([]byte, error) {
	streamID := "02"
	if dvr.StreamQuality == "main" {
		streamID = "01"
	}
	url := fmt.Sprintf("http://%s:%d/ISAPI/Streaming/channels/%d%s/picture",
		dvr.Addr, port, chNum, streamID)
	req, _ := http.NewRequest("GET", url, nil)
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

// upscaleBicubic scales up using CatmullRom bicubic interpolation (fast fallback).
func (m *CCTVManager) upscaleBicubic(data []byte, factor int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, err
	}

	bounds := src.Bounds()
	newW := bounds.Dx() * factor
	newH := bounds.Dy() * factor

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 90}); err != nil {
		return data, err
	}
	return buf.Bytes(), nil
}

// hasESRGAN checks if realesrgan-ncnn-vulkan is available.
func (m *CCTVManager) hasESRGAN() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.esrganPath != ""
}

// initESRGAN detects the realesrgan-ncnn-vulkan binary at startup.
func (m *CCTVManager) initESRGAN() {
	// Check common locations
	candidates := []string{
		"realesrgan-ncnn-vulkan",
	}
	// Check bundled path next to the executable
	if execPath, err := os.Executable(); err == nil {
		dir := filepath.Dir(execPath)
		candidates = append(candidates,
			filepath.Join(dir, "realesrgan-ncnn-vulkan"),
			filepath.Join(dir, "esrgan", "realesrgan-ncnn-vulkan"),
			// macOS .app bundle: Contents/MacOS/../Resources/esrgan/
			filepath.Join(dir, "..", "Resources", "esrgan", "realesrgan-ncnn-vulkan"),
		)
	}
	// Check user data dir
	home, _ := os.UserHomeDir()
	candidates = append(candidates,
		filepath.Join(home, ".opsview", "realesrgan-ncnn-vulkan"),
	)

	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			m.mu.Lock()
			m.esrganPath = p
			m.mu.Unlock()
			log.Printf("[cctv] Real-ESRGAN found: %s", p)
			return
		}
	}
	log.Printf("[cctv] Real-ESRGAN not found, using bicubic upscaling")
}

// upscaleESRGAN uses realesrgan-ncnn-vulkan for AI super-resolution.
func (m *CCTVManager) upscaleESRGAN(data []byte, scale int) ([]byte, error) {
	m.mu.RLock()
	esrganPath := m.esrganPath
	m.mu.RUnlock()

	if esrganPath == "" {
		return nil, fmt.Errorf("esrgan not available")
	}

	// Clamp scale to supported values (2, 3, 4)
	if scale < 2 {
		scale = 2
	}
	if scale > 4 {
		scale = 4
	}

	// Write input to temp file
	tmpIn := filepath.Join(os.TempDir(), fmt.Sprintf("opsview-esrgan-in-%d.jpg", time.Now().UnixNano()))
	tmpOut := filepath.Join(os.TempDir(), fmt.Sprintf("opsview-esrgan-out-%d.jpg", time.Now().UnixNano()))
	defer os.Remove(tmpIn)
	defer os.Remove(tmpOut)

	if err := os.WriteFile(tmpIn, data, 0644); err != nil {
		return nil, err
	}

	// Models directory is next to the binary
	modelsDir := filepath.Join(filepath.Dir(esrganPath), "models")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, esrganPath,
		"-i", tmpIn,
		"-o", tmpOut,
		"-s", fmt.Sprintf("%d", scale),
		"-n", "realesrgan-x4plus",
		"-m", modelsDir,
		"-f", "jpg",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("esrgan timeout (10s)")
		}
		return nil, fmt.Errorf("%w: %s", err, string(out))
	}

	result, err := os.ReadFile(tmpOut)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// maybeDetectResolution checks resolution only when DB has no value yet, avoiding goroutine spam.
func (m *CCTVManager) maybeDetectResolution(dvrID int64, chNum int, jpegData []byte) {
	var w, h int
	m.db.QueryRow(`SELECT width, height FROM channels WHERE dvr_id=? AND ch_num=?`, dvrID, chNum).Scan(&w, &h)
	if w > 0 && h > 0 {
		return // already known
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(jpegData))
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

type isAPIVideoInputList struct {
	XMLName  xml.Name          `xml:"VideoInputChannelList"`
	Channels []isAPIVideoInput `xml:"VideoInputChannel"`
}

type isAPIVideoInput struct {
	ID   int    `xml:"id"`
	Name string `xml:"inputPort>name"`
}

type isAPIDeviceInfo struct {
	XMLName           xml.Name `xml:"DeviceInfo"`
	DeviceName        string   `xml:"deviceName"`
	AnalogChannelNum  int      `xml:"analogChannelNum"`
	DigitalChannelNum int      `xml:"digitalChannelNum"`
}

func (m *CCTVManager) discoverFromDVRISAPI(dvr DVRConfig) ([]ChannelConfig, error) {
	// Strategy 1: Get device info for total channel count (most reliable)
	channels, err := m.discoverISAPIDeviceInfo(dvr)
	if err != nil {
		log.Printf("[cctv] ISAPI deviceInfo discovery failed: %v", err)
	}

	// Strategy 2: Parse streaming channels list
	if len(channels) == 0 {
		channels, err = m.discoverISAPIStreaming(dvr)
		if err != nil {
			log.Printf("[cctv] ISAPI streaming discovery failed: %v", err)
		}
	}

	// Strategy 3: Parse video input channels
	if len(channels) == 0 {
		log.Printf("[cctv] trying video inputs fallback")
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

// discoverISAPIDeviceInfo gets total channel count from /ISAPI/System/deviceInfo
// and generates channel list 1..N. This is the most reliable method.
func (m *CCTVManager) discoverISAPIDeviceInfo(dvr DVRConfig) ([]ChannelConfig, error) {
	url := fmt.Sprintf("http://%s:%d/ISAPI/System/deviceInfo", dvr.Addr, dvr.Port)
	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth(dvr.Username, dvr.Password)

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to DVR: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DVR returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	log.Printf("[cctv] ISAPI deviceInfo response: %s", string(body))

	var info isAPIDeviceInfo
	if err := xml.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("parse deviceInfo: %w", err)
	}

	totalChannels := info.AnalogChannelNum + info.DigitalChannelNum
	if totalChannels == 0 {
		return nil, fmt.Errorf("deviceInfo reports 0 channels")
	}
	log.Printf("[cctv] deviceInfo: analog=%d digital=%d total=%d", info.AnalogChannelNum, info.DigitalChannelNum, totalChannels)

	var channels []ChannelConfig
	for ch := 1; ch <= totalChannels; ch++ {
		w, h := m.fetchChannelResolution(dvr, ch)
		channels = append(channels, ChannelConfig{
			DVRID:  dvr.ID,
			ChNum:  ch,
			Name:   fmt.Sprintf("Channel %d", ch),
			Order:  ch - 1,
			Width:  w,
			Height: h,
		})
	}
	return channels, nil
}

func (m *CCTVManager) discoverISAPIStreaming(dvr DVRConfig) ([]ChannelConfig, error) {
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	log.Printf("[cctv] ISAPI streaming channels response (%d bytes): %s", len(body), string(body))

	var list isAPIChannelList
	if err := xml.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("parse channel list: %w", err)
	}

	seen := make(map[int]bool)
	var channels []ChannelConfig
	for _, ch := range list.Channels {
		var chNum int
		if ch.ID >= 100 {
			chNum = ch.ID / 100
		} else {
			chNum = ch.ID
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

		channels = append(channels, ChannelConfig{
			DVRID:  dvr.ID,
			ChNum:  chNum,
			Name:   name,
			Order:  chNum - 1,
			Width:  w,
			Height: h,
		})
	}

	return channels, nil
}

func (m *CCTVManager) discoverISAPIVideoInputs(dvr DVRConfig) ([]ChannelConfig, error) {
	url := fmt.Sprintf("http://%s:%d/ISAPI/System/Video/inputs/channels", dvr.Addr, dvr.Port)
	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth(dvr.Username, dvr.Password)

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to DVR: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DVR returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	log.Printf("[cctv] ISAPI video inputs response (%d bytes): %s", len(body), string(body))

	var list isAPIVideoInputList
	if err := xml.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("parse video input list: %w", err)
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

		channels = append(channels, ChannelConfig{
			DVRID:  dvr.ID,
			ChNum:  ch.ID,
			Name:   name,
			Order:  ch.ID - 1,
			Width:  w,
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
	err := m.db.QueryRow(`SELECT id, name, addr, port, username, password, refresh_rate, stream_quality, protocol FROM dvrs WHERE id=?`, id).
		Scan(&d.ID, &d.Name, &d.Addr, &d.Port, &d.Username, &d.Password, &d.RefreshRate, &d.StreamQuality, &d.Protocol)
	if d.Protocol == "" {
		d.Protocol = "isapi"
	}
	return d, err
}

// Shutdown closes the database and releases resources.
func (m *CCTVManager) Shutdown() {
	m.StopAllStreams()
	if m.db != nil {
		m.db.Close()
	}
}
