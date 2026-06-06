package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var webSrv *http.Server
var webPort int
var webSurvMgr *SurveillanceManager

type APIStatus struct {
	PIN            string `json:"pin"`
	IP             string `json:"ip"`
	RelayURL       string `json:"relay_url"`
	Profile        int    `json:"profile"`
	AutoStart      bool   `json:"autostart"`
	PublisherToken string `json:"publisher_token"`
	AgentID        string `json:"agent_id"`
}

func getPublicIP() string {
	client := http.Client{
		Timeout: 3 * time.Second,
	}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return "Unknown"
	}
	defer resp.Body.Close()
	ip, _ := io.ReadAll(resp.Body)
	return string(ip)
}

// isLoopbackHost reports whether a request Host header targets the local agent.
func isLoopbackHost(hostHeader string) bool {
	h := hostHeader
	if hostOnly, _, err := net.SplitHostPort(hostHeader); err == nil {
		h = hostOnly
	}
	h = strings.Trim(h, "[]")
	switch h {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// localOnly rejects requests whose Host header is not loopback, defeating
// DNS-rebinding attacks against the localhost-bound web UI (incl. the
// destructive reset-db / clear-channels endpoints).
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func startWebUI() {
	if webSrv != nil {
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/api/status", handleAPIStatus)
	mux.HandleFunc("/api/save", handleAPISave)
	mux.HandleFunc("/api/update", handleAPIUpdate)
	mux.HandleFunc("/api/surv/dvrs", handleSurvDVRs)
	mux.HandleFunc("/api/surv/dvrs/", handleSurvDVR)
	mux.HandleFunc("/api/surv/reset-db", handleSurvResetDB)
	mux.HandleFunc("/api/surv/snapshot", handleSurvSnapshot)
	mux.HandleFunc("/api/surv/channels/reorder", handleSurvChannelReorder)
	mux.HandleFunc("/api/surv/channels/rename", handleSurvChannelRename)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Printf("[webui] failed to listen: %v", err)
		return
	}
	webPort = listener.Addr().(*net.TCPAddr).Port

	webSrv = &http.Server{Handler: localOnly(mux)}
	go func() {
		log.Printf("[webui] started on port %d", webPort)
		if err := webSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("[webui] server error: %v", err)
		}
	}()
}

func showSettings() {
	if webSrv == nil {
		startWebUI()
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", webPort)

	// Open the URL in the default system browser
	go openBrowser(url)
}

func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "windows":
		// Windows에서는 기본 탑재된 Edge의 Chromium App Mode를 사용하여 프레임리스(주소창 없는) 네이티브 앱 창처럼 띄웁니다.
		err = exec.Command("cmd", "/c", "start", "msedge", "--app="+url).Start()
		if err != nil {
			// Edge 실행 실패 시 크롬 시도 후, 최종 폴백으로 기본 브라우저 열기
			err = exec.Command("cmd", "/c", "start", "chrome", "--app="+url).Start()
			if err != nil {
				err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
			}
		}
	case "darwin":
		// macOS에서는 구글 크롬이 있으면 App Mode로 띄우고, 없으면 기본 브라우저로 엽니다.
		chromePath := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		if _, statErr := os.Stat(chromePath); statErr == nil {
			err = exec.Command(chromePath, "--app="+url).Start()
		} else {
			err = exec.Command("open", url).Start()
		}
	default:
		err = exec.Command("xdg-open", url).Start()
	}
	if err != nil {
		log.Printf("[webui] failed to open browser: %v", err)
	}
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(htmlTemplate))
}

func handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	cfg := loadConfig()
	pin, _ := loadOrCreateAgentPIN()
	ip := getPublicIP()

	status := APIStatus{
		PIN:            pin,
		IP:             ip,
		RelayURL:       cfg.RelayURL,
		Profile:        cfg.Profile,
		AutoStart:      cfg.AutoStart,
		PublisherToken: cfg.PublisherToken,
		AgentID:        cfg.AgentID,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func handleAPISave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req APIStatus
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cfg := loadConfig()

	// Apply updates
	cfg.RelayURL = req.RelayURL
	cfg.Profile = req.Profile
	cfg.PublisherToken = strings.TrimSpace(req.PublisherToken)
	cfg.AgentID = strings.TrimSpace(req.AgentID)

	newAutoStart := req.AutoStart
	if newAutoStart != cfg.AutoStart {
		setAutoStart(newAutoStart)
		cfg.AutoStart = newAutoStart
		syncTrayAutoStart(newAutoStart)
	}

	if err := saveConfig(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("[settings] saved via WebUI: relay=%s profile=%d autostart=%v", cfg.RelayURL, cfg.Profile, cfg.AutoStart)
	go restartAgentIfRunning()

	w.WriteHeader(http.StatusOK)
}

func handleAPIUpdate(w http.ResponseWriter, r *http.Request) {
	info, err := CheckForUpdate()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// --- Surveillance DVR API ---

func handleSurvDVRs(w http.ResponseWriter, r *http.Request) {
	if webSurvMgr == nil {
		http.Error(w, "surveillance manager not initialized", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		dvrs, err := webSurvMgr.ListDVRs()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(dvrs)
	case http.MethodPost:
		var req struct {
			Name          string `json:"name"`
			Addr          string `json:"addr"`
			Port          int    `json:"port"`
			ExtAddr       string `json:"ext_addr"`
			ExtPort       int    `json:"ext_port"`
			Username      string `json:"username"`
			Password      string `json:"password"`
			Protocol      string `json:"protocol"`
			RefreshRate   int    `json:"refresh_rate"`
			StreamQuality string `json:"stream_quality"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Port == 0 {
			req.Port = 80
		}
		dvr, err := webSurvMgr.AddDVR(req.Name, req.Addr, req.Port, req.ExtAddr, req.ExtPort, req.Username, req.Password, req.Protocol, req.RefreshRate, req.StreamQuality)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(dvr)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleSurvDVR(w http.ResponseWriter, r *http.Request) {
	if webSurvMgr == nil {
		http.Error(w, "surveillance manager not initialized", http.StatusServiceUnavailable)
		return
	}

	// Parse /api/surv/dvrs/{id}[/action]
	path := strings.TrimPrefix(r.URL.Path, "/api/surv/dvrs/")
	parts := strings.SplitN(path, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid DVR id", http.StatusBadRequest)
		return
	}

	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	w.Header().Set("Content-Type", "application/json")

	switch action {
	case "discover":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		chs, err := webSurvMgr.DiscoverChannels(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(chs)

	case "clear-channels":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		err := webSurvMgr.ClearDVRChannels(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	case "channels":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		chs, err := webSurvMgr.ListChannels(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(chs)

	case "":
		switch r.Method {
		case http.MethodPut:
			var req struct {
				Name          string `json:"name"`
				Addr          string `json:"addr"`
				Port          int    `json:"port"`
				ExtAddr       string `json:"ext_addr"`
				ExtPort       int    `json:"ext_port"`
				Username      string `json:"username"`
				Password      string `json:"password"`
				RefreshRate   int    `json:"refresh_rate"`
				StreamQuality string `json:"stream_quality"`
				Protocol      string `json:"protocol"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := webSurvMgr.UpdateDVR(id, req.Name, req.Addr, req.Port, req.ExtAddr, req.ExtPort, req.Username, req.Password, req.RefreshRate, req.StreamQuality, req.Protocol); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case http.MethodDelete:
			if err := webSurvMgr.DeleteDVR(id); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}

	default:
		http.NotFound(w, r)
	}
}

func handleSurvResetDB(w http.ResponseWriter, r *http.Request) {
	if webSurvMgr == nil {
		http.Error(w, "surveillance manager not initialized", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	err := webSurvMgr.ResetDB()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

const htmlTemplate = `
<!DOCTYPE html>
<html lang="ko">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>OpsView Agent Configuration</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <script src="https://cdn.jsdelivr.net/npm/sortablejs@1.15.2/Sortable.min.js"></script>
    <script>tailwind.config={theme:{fontFamily:{sans:['"Segoe UI"','"Malgun Gothic"','-apple-system','BlinkMacSystemFont','Roboto','Helvetica','Arial','sans-serif']}}}</script>
    <style>
        body {
            background-color: #0F172A;
            color: #F8FAFC;
            font-family: "Segoe UI", "Malgun Gothic", -apple-system, BlinkMacSystemFont, Roboto, Helvetica, Arial, sans-serif;
            background-image: radial-gradient(circle at 50% 0%, #1E293B, #0F172A 70%);
            min-height: 100vh;
        }
        .glass-panel {
            background: rgba(30, 41, 59, 0.7);
            backdrop-filter: blur(16px);
            -webkit-backdrop-filter: blur(16px);
            border: 1px solid rgba(255, 255, 255, 0.1);
            box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.5);
        }
        .gradient-text {
            background: linear-gradient(135deg, #38BDF8, #818CF8);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
        }
        .pin-display {
            letter-spacing: 0.25em;
            font-variant-numeric: tabular-nums;
        }
    </style>
</head>
<body class="flex items-center justify-center p-6">

    <div class="glass-panel w-full max-w-2xl rounded-3xl p-8 md:p-12 animate-fade-in-up transition-all duration-500">

        <!-- Update Banner -->
        <div id="update-banner" class="hidden mb-6 bg-amber-500/15 border border-amber-500/30 rounded-2xl p-4 flex items-center justify-between">
            <div class="flex items-center gap-3">
                <svg class="w-5 h-5 text-amber-400 flex-shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"/></svg>
                <div>
                    <span class="text-amber-300 text-sm font-medium">새 버전 사용 가능:</span>
                    <span id="update-version" class="text-white text-sm font-semibold ml-1"></span>
                    <span class="text-slate-400 text-xs ml-2">(현재: <span id="update-current"></span>)</span>
                </div>
            </div>
            <a id="update-link" href="#" target="_blank" class="bg-amber-500 hover:bg-amber-400 text-slate-900 text-sm font-semibold px-4 py-1.5 rounded-lg transition flex-shrink-0">다운로드</a>
        </div>

        <!-- Header -->
        <div class="text-center mb-10">
            <h1 class="text-4xl font-extrabold tracking-tight mb-2"><span class="gradient-text">OpsView</span> Agent</h1>
            <p class="text-slate-400">안전하고 가벼운 원격 화면 전송 시스템</p>
        </div>

        <!-- Connection Info (PIN & IP) -->
        <div class="bg-slate-800/50 rounded-2xl p-6 mb-8 border border-slate-700/50 flex flex-col items-center justify-center relative overflow-hidden group">
            <div class="absolute inset-0 bg-gradient-to-r from-cyan-500/10 to-blue-500/10 opacity-0 group-hover:opacity-100 transition-opacity duration-500"></div>
            <p class="text-slate-400 text-sm font-semibold uppercase tracking-wider mb-2 z-10">외부 접속 PIN 번호</p>
            <h2 id="pin-code" class="text-5xl md:text-6xl font-black text-white pin-display mb-4 z-10 drop-shadow-lg">------</h2>
            <div class="flex items-center space-x-2 text-sm z-10">
                <span class="text-slate-400">공인 IP:</span>
                <span id="ip-address" class="text-emerald-400 font-mono font-medium">Loading...</span>
            </div>
            <div class="mt-4 text-xs text-slate-500 z-10 text-center">
                모바일이나 밖에서 접속할 때 위 <strong class="text-slate-300">PIN 번호</strong>와 <strong class="text-slate-300">공인 IP</strong>를 입력하세요. (공유기 설정 불필요)
            </div>
        </div>

        <!-- Settings Form -->
        <form id="settings-form" class="space-y-6">
            <!-- Relay IP & Port -->
            <div class="grid grid-cols-3 gap-4">
                <div class="col-span-2">
                    <label class="block text-sm font-medium text-slate-300 mb-2">Relay IP</label>
                    <input type="text" id="relay-ip" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-xl py-3 px-4 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent transition" placeholder="192.168.0.100">
                </div>
                <div>
                    <label class="block text-sm font-medium text-slate-300 mb-2">Port</label>
                    <input type="number" id="relay-port" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-xl py-3 px-4 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent transition" placeholder="28186">
                </div>
            </div>

            <!-- Profile -->
            <div>
                <label class="block text-sm font-medium text-slate-300 mb-2">화면 품질 (Profile)</label>
                <div class="relative">
                    <select id="profile" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-xl py-3 px-4 appearance-none focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent transition">
                        <option value="1080">1080p (고화질)</option>
                        <option value="720">720p (저사양/모바일 최적화)</option>
                    </select>
                    <div class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-4 text-slate-400">
                        <svg class="fill-current h-4 w-4" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20"><path d="M9.293 12.95l.707.707L15.657 8l-1.414-1.414L10 10.828 5.757 6.586 4.343 8z"/></svg>
                    </div>
                </div>
            </div>

            <!-- Advanced: Relay URL (collapsible) -->
            <details class="group">
                <summary class="text-sm text-slate-500 cursor-pointer hover:text-slate-300 transition select-none flex items-center gap-1">
                    <svg class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
                    고급 설정
                </summary>
                <div class="mt-3">
                    <label class="block text-sm font-medium text-slate-300 mb-2">Relay URL (직접 입력)</label>
                    <input type="text" id="relay-url" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-xl py-3 px-4 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent transition font-mono text-sm" placeholder="ws://127.0.0.1:8080/publish">
                    <p class="text-xs text-slate-500 mt-1">직접 URL을 입력하면 위 IP/Port 대신 이 값이 사용됩니다.</p>
                </div>
                <div class="mt-4">
                    <label class="block text-sm font-medium text-slate-300 mb-2">Publisher Token (Relay 공유 시크릿)</label>
                    <input type="text" id="publisher-token" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-xl py-3 px-4 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent transition font-mono text-sm" placeholder="relay의 RELAY_PUBLISHER_TOKEN과 동일한 값">
                    <p class="text-xs text-slate-500 mt-1">Relay의 <code class="text-slate-400">RELAY_PUBLISHER_TOKEN</code>과 <strong class="text-slate-300">정확히 같은 값</strong>이어야 연결됩니다. 비워두면 환경변수(RELAY_PUBLISHER_TOKEN/AGENT_TOKEN)를 사용합니다.</p>
                </div>
                <div class="mt-4">
                    <label class="block text-sm font-medium text-slate-300 mb-2">지점 ID (멀티 매장용, 선택)</label>
                    <input type="text" id="agent-id" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-xl py-3 px-4 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent transition font-mono text-sm" placeholder="예: gangnam (비워두면 단일 매장)">
                    <p class="text-xs text-slate-500 mt-1">하나의 relay에 여러 매장을 연결할 때만 설정합니다. relay의 <code class="text-slate-400">RELAY_AGENTS</code>에 등록된 ID와 같아야 하며, Publisher Token도 그 지점 토큰으로 넣습니다. <strong class="text-slate-300">비워두면 기존과 동일</strong>(기본 단일 매장).</p>
                </div>
                <div class="mt-4 pt-4 border-t border-slate-700/50">
                    <label class="block text-sm font-medium text-slate-300 mb-2">데이터베이스 초기화</label>
                    <button type="button" onclick="resetDatabase()" class="text-xs bg-red-600/20 text-red-400 hover:bg-red-600/30 px-3 py-2 rounded-lg transition font-medium">전체 DB 초기화 (DVR 및 채널 전체 삭제)</button>
                    <p class="text-xs text-slate-500 mt-1">에이전트 데이터베이스(cctv.db)를 완전히 지우고 깨끗하게 재시작합니다.</p>
                </div>
            </details>

            <!-- Auto Start -->
            <div class="flex items-center mt-4">
                <div class="relative flex items-start">
                    <div class="flex h-6 items-center">
                        <input id="autostart" type="checkbox" class="h-5 w-5 rounded border-slate-600 bg-slate-800 text-blue-500 focus:ring-blue-600 focus:ring-offset-slate-900 transition">
                    </div>
                    <div class="ml-3 text-sm leading-6">
                        <label for="autostart" class="font-medium text-slate-200 cursor-pointer">Windows 시작 시 자동 실행</label>
                        <p class="text-slate-400">PC가 켜질 때 백그라운드에서 자동으로 스트리밍을 준비합니다.</p>
                    </div>
                </div>
            </div>

            <!-- Surveillance DVR Management -->
        <div class="border-t border-slate-700/50 pt-6 mt-6">
            <h2 class="text-lg font-semibold text-white mb-4">
                <span class="gradient-text">Surveillance</span> DVR 관리
            </h2>

            <!-- DVR List -->
            <div id="dvr-list" class="space-y-3 mb-4"></div>

            <!-- Add DVR Form -->
            <details class="group">
                <summary class="text-sm text-slate-400 cursor-pointer hover:text-slate-200 transition select-none flex items-center gap-1">
                    <svg class="w-4 h-4 transition-transform group-open:rotate-90" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4"/></svg>
                    DVR 추가
                </summary>
                <div class="mt-3 space-y-3 bg-slate-800/40 rounded-xl p-4 border border-slate-700/50">
                    <div class="grid grid-cols-2 gap-3">
                        <div>
                            <label class="block text-xs text-slate-400 mb-1">이름</label>
                            <input type="text" id="dvr-name" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" placeholder="DVR 이름">
                        </div>
                        <div>
                            <label class="block text-xs text-slate-400 mb-1">프로토콜</label>
                            <select id="dvr-protocol" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 appearance-none">
                                <option value="auto">자동 탐지</option>
                                <option value="isapi">Hikvision (ISAPI)</option>
                                <option value="dahua">Dahua</option>
                                <option value="rtsp">RTSP</option>
                                <option value="onvif">ONVIF (범용)</option>
                            </select>
                        </div>
                    </div>
                    <div class="grid grid-cols-3 gap-3">
                        <div class="col-span-2">
                            <label class="block text-xs text-slate-400 mb-1">주소</label>
                            <input type="text" id="dvr-addr" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" placeholder="192.168.0.100">
                        </div>
                        <div>
                            <label class="block text-xs text-slate-400 mb-1">포트</label>
                            <input type="number" id="dvr-port" value="80" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500">
                        </div>
                    </div>
                    <div class="grid grid-cols-3 gap-3">
                        <div class="col-span-2">
                            <label class="block text-xs text-slate-400 mb-1">외부 접속 주소 (선택)</label>
                            <input type="text" id="dvr-ext-addr" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" placeholder="domain.com (옵션)">
                        </div>
                        <div>
                            <label class="block text-xs text-slate-400 mb-1">HLS 포트 (HTTP)</label>
                            <input type="number" id="dvr-ext-port" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" placeholder="8080">
                        </div>
                    </div>
                    <div class="grid grid-cols-2 gap-3">
                        <div>
                            <label class="block text-xs text-slate-400 mb-1">사용자명</label>
                            <input type="text" id="dvr-username" value="admin" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500">
                        </div>
                        <div>
                            <label class="block text-xs text-slate-400 mb-1">비밀번호</label>
                            <input type="password" id="dvr-password" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500">
                        </div>
                    </div>
                    <div class="grid grid-cols-2 gap-3">
                        <div>
                            <label class="block text-xs text-slate-400 mb-1">갱신 주기 (ms)</label>
                            <input type="number" id="dvr-refresh" value="2000" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500">
                        </div>
                        <div>
                            <label class="block text-xs text-slate-400 mb-1">스트림 품질</label>
                            <select id="dvr-quality" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 appearance-none">
                                <option value="sub">서브스트림 (저화질)</option>
                                <option value="main">메인스트림 (고화질)</option>
                            </select>
                        </div>
                    </div>
                    <button type="button" onclick="addDVR()" class="w-full bg-blue-600 hover:bg-blue-500 text-white py-2 px-4 rounded-lg text-sm font-medium transition">
                        추가
                    </button>
                </div>
            </details>
        </div>

        <!-- Status Message -->
            <div id="status-msg" class="hidden text-sm py-3 px-4 rounded-xl mt-4 font-medium"></div>

            <!-- Actions -->
            <div class="pt-6 border-t border-slate-700/50 flex justify-end">
                <button type="button" onclick="if(window.closeNativeWindow) window.closeNativeWindow(); else window.close();" class="bg-slate-700 hover:bg-slate-600 text-white py-3 px-6 rounded-xl font-medium transition mr-4">
                    창 닫기
                </button>
                <button type="submit" class="bg-gradient-to-r from-blue-500 to-indigo-600 hover:from-blue-400 hover:to-indigo-500 text-white py-3 px-8 rounded-xl font-semibold shadow-lg shadow-blue-500/30 transform hover:-translate-y-0.5 transition duration-200 flex items-center">
                    <svg class="w-5 h-5 mr-2 -ml-1" fill="none" stroke="currentColor" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"></path></svg>
                    설정 저장
                </button>
            </div>
        </form>
    </div>

    <!-- Edit DVR Modal -->
    <div id="edit-modal" class="hidden fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm">
        <div class="glass-panel rounded-2xl p-6 w-full max-w-md mx-4">
            <h3 class="text-lg font-semibold text-white mb-4">DVR 수정</h3>
            <input type="hidden" id="edit-dvr-id">
            <div class="space-y-3">
                <div class="grid grid-cols-2 gap-3">
                    <div>
                        <label class="block text-xs text-slate-400 mb-1">이름</label>
                        <input type="text" id="edit-dvr-name" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500">
                    </div>
                    <div>
                        <label class="block text-xs text-slate-400 mb-1">프로토콜</label>
                        <select id="edit-dvr-protocol" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 appearance-none">
                            <option value="auto">자동 탐지</option>
                            <option value="isapi">Hikvision (ISAPI)</option>
                            <option value="dahua">Dahua</option>
                            <option value="rtsp">RTSP</option>
                            <option value="onvif">ONVIF (범용)</option>
                        </select>
                    </div>
                </div>
                <div class="grid grid-cols-3 gap-3">
                    <div class="col-span-2">
                        <label class="block text-xs text-slate-400 mb-1">주소</label>
                        <input type="text" id="edit-dvr-addr" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500">
                    </div>
                    <div>
                        <label class="block text-xs text-slate-400 mb-1">포트</label>
                        <input type="number" id="edit-dvr-port" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500">
                    </div>
                </div>
                <div class="grid grid-cols-3 gap-3">
                    <div class="col-span-2">
                        <label class="block text-xs text-slate-400 mb-1">외부 접속 주소 (선택)</label>
                        <input type="text" id="edit-dvr-ext-addr" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500">
                    </div>
                    <div>
                        <label class="block text-xs text-slate-400 mb-1">HLS 포트 (HTTP)</label>
                        <input type="number" id="edit-dvr-ext-port" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" placeholder="8080">
                    </div>
                </div>
                <div class="grid grid-cols-2 gap-3">
                    <div>
                        <label class="block text-xs text-slate-400 mb-1">사용자명</label>
                        <input type="text" id="edit-dvr-username" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500">
                    </div>
                    <div>
                        <label class="block text-xs text-slate-400 mb-1">비밀번호</label>
                        <input type="password" id="edit-dvr-password" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" placeholder="변경 시 입력">
                    </div>
                </div>
                <div class="grid grid-cols-2 gap-3">
                    <div>
                        <label class="block text-xs text-slate-400 mb-1">갱신 주기 (ms)</label>
                        <input type="number" id="edit-dvr-refresh" value="2000" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500">
                    </div>
                    <div>
                        <label class="block text-xs text-slate-400 mb-1">스트림 품질</label>
                        <select id="edit-dvr-quality" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-lg py-2 px-3 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 appearance-none">
                            <option value="sub">서브스트림 (저화질)</option>
                            <option value="main">메인스트림 (고화질)</option>
                        </select>
                    </div>
                </div>
            </div>
            <div class="flex justify-end gap-3 mt-5">
                <button onclick="closeEditModal()" class="bg-slate-700 hover:bg-slate-600 text-white py-2 px-5 rounded-lg text-sm font-medium transition">취소</button>
                <button onclick="saveDVR()" class="bg-blue-600 hover:bg-blue-500 text-white py-2 px-5 rounded-lg text-sm font-medium transition">저장</button>
            </div>
        </div>
    </div>

    <!-- Channel editor (thumbnail grid: drag to reorder, click name to edit) -->
    <div id="ch-modal" class="fixed inset-0 bg-black/70 z-50 hidden items-center justify-center p-4">
      <div class="bg-slate-900 border border-slate-700 rounded-2xl w-full max-w-5xl flex flex-col" style="max-height:88vh;">
        <div class="flex items-center justify-between px-5 py-4 border-b border-slate-700">
          <h3 class="font-semibold text-white" id="ch-modal-title">채널 편집</h3>
          <button onclick="closeChEditor()" class="text-sm text-slate-400 hover:text-white">닫기</button>
        </div>
        <p class="px-5 pt-3 text-xs text-slate-400">썸네일을 드래그해 순서 변경 · 이름 칸을 눌러 라벨 편집. 변경 즉시 저장되어 모든 뷰어에 반영됩니다.</p>
        <div id="ch-grid" class="p-5 grid gap-3 overflow-auto" style="grid-template-columns:repeat(auto-fill,minmax(150px,1fr));"></div>
      </div>
    </div>

    <script>
        // Parse relay URL into {ip, port}
        function parseRelayURL(url) {
            try {
                const m = url.match(/^wss?:\/\/([^:/]+):(\d+)/);
                if (m) return { ip: m[1], port: m[2] };
                const m2 = url.match(/^wss?:\/\/([^:/]+)/);
                if (m2) return { ip: m2[1], port: '8080' };
            } catch(e) {}
            return { ip: '127.0.0.1', port: '8080' };
        }

        // Build relay URL from IP/Port
        function buildRelayURL(ip, port) {
            return 'ws://' + ip + ':' + port + '/publish';
        }

        // Load settings data
        async function loadSettings() {
            try {
                const res = await fetch('/api/status');
                const data = await res.json();

                document.getElementById('pin-code').textContent = data.pin || '------';
                document.getElementById('ip-address').textContent = data.ip || 'Unknown';
                document.getElementById('profile').value = data.profile.toString();
                document.getElementById('relay-url').value = data.relay_url;
                document.getElementById('publisher-token').value = data.publisher_token || '';
                document.getElementById('agent-id').value = data.agent_id || '';
                document.getElementById('autostart').checked = data.autostart;

                // Populate IP/Port from URL
                const parsed = parseRelayURL(data.relay_url);
                document.getElementById('relay-ip').value = parsed.ip;
                document.getElementById('relay-port').value = parsed.port;

                // Sync: when IP/Port changes, update the hidden URL
                const syncURL = () => {
                    const ip = document.getElementById('relay-ip').value.trim();
                    const port = document.getElementById('relay-port').value.trim();
                    if (ip && port) {
                        document.getElementById('relay-url').value = buildRelayURL(ip, port);
                    }
                };
                document.getElementById('relay-ip').addEventListener('input', syncURL);
                document.getElementById('relay-port').addEventListener('input', syncURL);

                // Reverse sync: when URL changes manually, update IP/Port
                document.getElementById('relay-url').addEventListener('input', () => {
                    const p = parseRelayURL(document.getElementById('relay-url').value);
                    document.getElementById('relay-ip').value = p.ip;
                    document.getElementById('relay-port').value = p.port;
                });
            } catch (err) {
                console.error('Failed to load settings:', err);
                showMsg('데이터를 불러오는데 실패했습니다.', 'error');
            }
        }

        // Save settings data
        document.getElementById('settings-form').addEventListener('submit', async (e) => {
            e.preventDefault();

            const payload = {
                profile: parseInt(document.getElementById('profile').value),
                relay_url: document.getElementById('relay-url').value.trim(),
                publisher_token: document.getElementById('publisher-token').value.trim(),
                agent_id: document.getElementById('agent-id').value.trim(),
                autostart: document.getElementById('autostart').checked
            };

            const btn = document.querySelector('button[type="submit"]');
            const originalText = btn.innerHTML;
            btn.innerHTML = '<svg class="animate-spin -ml-1 mr-3 h-5 w-5 text-white" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path></svg> 저장 중...';

            try {
                const res = await fetch('/api/save', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(payload)
                });
                
                if (res.ok) {
                    showMsg('설정이 성공적으로 저장되었습니다. 에이전트가 재시작됩니다.', 'success');
                } else {
                    const text = await res.text();
                    showMsg('저장 실패: ' + text, 'error');
                }
            } catch (err) {
                showMsg('저장 중 오류가 발생했습니다.', 'error');
            } finally {
                setTimeout(() => { btn.innerHTML = originalText; }, 500);
            }
        });

        function showMsg(text, type) {
            const el = document.getElementById('status-msg');
            el.textContent = text;
            el.classList.remove('hidden', 'bg-emerald-500/20', 'text-emerald-400', 'bg-red-500/20', 'text-red-400');
            
            if (type === 'success') {
                el.classList.add('bg-emerald-500/20', 'text-emerald-400');
            } else {
                el.classList.add('bg-red-500/20', 'text-red-400');
            }
            
            setTimeout(() => el.classList.add('hidden'), 5000);
        }

        // --- Channel editor: thumbnail grid, drag-reorder + inline rename ---
        var chSortable = null;
        async function editChannels(dvrId) {
            var res = await fetch('/api/surv/dvrs/' + dvrId + '/channels');
            var chs = res.ok ? await res.json() : [];
            if (!chs || !chs.length) { showMsg('먼저 채널 탐색을 해주세요.', 'error'); return; }
            document.getElementById('ch-modal-title').textContent = '채널 편집 (' + chs.length + '개)';
            var grid = document.getElementById('ch-grid');
            grid.innerHTML = chs.map(function(c) {
                var nm = String(c.name || '').replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;');
                return '<div class="ch-card bg-slate-800 rounded-lg overflow-hidden border border-slate-700" data-ch="' + c.ch_num + '">' +
                    '<div class="relative bg-black cursor-grab" style="aspect-ratio:16/9;">' +
                        '<img src="/api/surv/snapshot?dvr=' + dvrId + '&ch=' + c.ch_num + '" class="w-full h-full object-cover" onerror="this.style.opacity=0.12">' +
                        '<span class="absolute top-1 left-1 text-[10px] bg-black/60 px-1.5 py-0.5 rounded text-slate-200">CH' + c.ch_num + '</span>' +
                    '</div>' +
                    '<input class="ch-name w-full bg-slate-900 text-white text-sm px-2 py-1.5 outline-none focus:bg-slate-700" value="' + nm + '" data-ch="' + c.ch_num + '">' +
                '</div>';
            }).join('');
            grid.querySelectorAll('.ch-name').forEach(function(inp) {
                inp.addEventListener('change', function() { renameChannel(dvrId, parseInt(inp.dataset.ch), inp.value); });
                inp.addEventListener('keydown', function(e) { if (e.key === 'Enter') inp.blur(); });
            });
            if (chSortable) chSortable.destroy();
            chSortable = Sortable.create(grid, { animation: 150, handle: '.cursor-grab', draggable: '.ch-card',
                onEnd: function() { saveChannelOrder(dvrId); } });
            var m = document.getElementById('ch-modal'); m.classList.remove('hidden'); m.classList.add('flex');
        }
        function closeChEditor() {
            var m = document.getElementById('ch-modal'); m.classList.add('hidden'); m.classList.remove('flex');
            if (chSortable) { chSortable.destroy(); chSortable = null; }
        }
        async function saveChannelOrder(dvrId) {
            var nums = [].slice.call(document.querySelectorAll('#ch-grid .ch-card')).map(function(el) { return parseInt(el.dataset.ch); });
            var res = await fetch('/api/surv/channels/reorder', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ dvr_id: dvrId, ch_nums: nums }) });
            showMsg(res.ok ? '순서 저장됨' : '순서 저장 실패', res.ok ? 'success' : 'error');
        }
        async function renameChannel(dvrId, chNum, name) {
            var res = await fetch('/api/surv/channels/rename', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ dvr_id: dvrId, ch_num: chNum, name: name }) });
            showMsg(res.ok ? '이름 저장됨' : '이름 저장 실패', res.ok ? 'success' : 'error');
        }

        // --- Surveillance DVR Management ---
        async function loadDVRs() {
            try {
                const res = await fetch('/api/surv/dvrs');
                const dvrs = await res.json();
                const list = document.getElementById('dvr-list');
                if (!dvrs || dvrs.length === 0) {
                    list.innerHTML = '<p class="text-sm text-slate-500 text-center py-4">등록된 DVR이 없습니다.</p>';
                    return;
                }
                list.innerHTML = dvrs.map(function(d) {
                    var dj = encodeURIComponent(JSON.stringify(d));
                    return '<div class="bg-slate-800/50 rounded-xl p-4 border border-slate-700/50 flex items-center justify-between">' +
                        '<div>' +
                            '<div class="font-medium text-white text-sm">' + (d.name || d.addr) + '</div>' +
                            '<div class="text-xs text-slate-400 mt-0.5">' + d.addr + ':' + d.port + ' \u00b7 ' + d.protocol + '</div>' +
                        '</div>' +
                        '<div class="flex gap-2">' +
                            '<button onclick="editDVR(\'' + dj + '\')" class="text-xs bg-slate-600/30 text-slate-300 hover:bg-slate-600/50 px-3 py-1.5 rounded-lg transition">\uc218\uc815</button>' +
                            '<button onclick="discoverChannels(' + d.id + ')" class="text-xs bg-cyan-600/20 text-cyan-400 hover:bg-cyan-600/30 px-3 py-1.5 rounded-lg transition">\ucc44\ub110 \ud0d0\uc0c9</button>' +
                            '<button onclick="editChannels(' + d.id + ')" class="text-xs bg-emerald-600/20 text-emerald-400 hover:bg-emerald-600/30 px-3 py-1.5 rounded-lg transition">\ucc44\ub110 \ud3b8\uc9d1</button>' +
                            '<button onclick="clearChannels(' + d.id + ')" class="text-xs bg-amber-600/20 text-amber-400 hover:bg-amber-600/30 px-3 py-1.5 rounded-lg transition">\ucc44\ub110 \ucd08\uae30\ud654</button>' +
                            '<button onclick="deleteDVR(' + d.id + ')" class="text-xs bg-red-600/20 text-red-400 hover:bg-red-600/30 px-3 py-1.5 rounded-lg transition">\uc0ad\uc81c</button>' +
                        '</div>' +
                    '</div>';
                }).join('');
            } catch (err) {
                console.error('Failed to load DVRs:', err);
            }
        }

        async function addDVR() {
            const payload = {
                name: document.getElementById('dvr-name').value.trim(),
                addr: document.getElementById('dvr-addr').value.trim(),
                port: parseInt(document.getElementById('dvr-port').value) || 80,
                ext_addr: document.getElementById('dvr-ext-addr').value.trim(),
                ext_port: parseInt(document.getElementById('dvr-ext-port').value) || 0,
                username: document.getElementById('dvr-username').value.trim(),
                password: document.getElementById('dvr-password').value,
                protocol: document.getElementById('dvr-protocol').value,
                refresh_rate: parseInt(document.getElementById('dvr-refresh').value) || 2000,
                stream_quality: document.getElementById('dvr-quality').value,
            };
            if (!payload.addr) { showMsg('DVR 주소를 입력하세요.', 'error'); return; }
            try {
                const res = await fetch('/api/surv/dvrs', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(payload)
                });
                if (res.ok) {
                    showMsg('DVR이 추가되었습니다.', 'success');
                    document.getElementById('dvr-name').value = '';
                    document.getElementById('dvr-addr').value = '';
                    document.getElementById('dvr-ext-addr').value = '';
                    document.getElementById('dvr-ext-port').value = '';
                    document.getElementById('dvr-password').value = '';
                    loadDVRs();
                } else {
                    showMsg('DVR 추가 실패: ' + await res.text(), 'error');
                }
            } catch (err) {
                showMsg('DVR 추가 중 오류가 발생했습니다.', 'error');
            }
        }

        async function deleteDVR(id) {
            if (!confirm('이 DVR을 삭제하시겠습니까?')) return;
            try {
                const res = await fetch('/api/surv/dvrs/' + id, { method: 'DELETE' });
                if (res.ok) {
                    showMsg('DVR이 삭제되었습니다.', 'success');
                    loadDVRs();
                } else {
                    showMsg('삭제 실패', 'error');
                }
            } catch (err) {
                showMsg('삭제 중 오류가 발생했습니다.', 'error');
            }
        }

        function editDVR(encoded) {
            var d = JSON.parse(decodeURIComponent(encoded));
            document.getElementById('edit-dvr-id').value = d.id;
            document.getElementById('edit-dvr-name').value = d.name || '';
            document.getElementById('edit-dvr-protocol').value = d.protocol || 'auto';
            document.getElementById('edit-dvr-addr').value = d.addr || '';
            document.getElementById('edit-dvr-port').value = d.port || 80;
            document.getElementById('edit-dvr-ext-addr').value = d.ext_addr || '';
            document.getElementById('edit-dvr-ext-port').value = d.ext_port || '';
            document.getElementById('edit-dvr-username').value = d.username || '';
            document.getElementById('edit-dvr-password').value = '';
            document.getElementById('edit-dvr-refresh').value = d.refresh_rate || 2000;
            document.getElementById('edit-dvr-quality').value = d.stream_quality || 'sub';
            document.getElementById('edit-modal').classList.remove('hidden');
        }

        function closeEditModal() {
            document.getElementById('edit-modal').classList.add('hidden');
        }

        async function saveDVR() {
            var id = document.getElementById('edit-dvr-id').value;
            var payload = {
                name: document.getElementById('edit-dvr-name').value.trim(),
                addr: document.getElementById('edit-dvr-addr').value.trim(),
                port: parseInt(document.getElementById('edit-dvr-port').value) || 80,
                ext_addr: document.getElementById('edit-dvr-ext-addr').value.trim(),
                ext_port: parseInt(document.getElementById('edit-dvr-ext-port').value) || 0,
                username: document.getElementById('edit-dvr-username').value.trim(),
                password: document.getElementById('edit-dvr-password').value,
                protocol: document.getElementById('edit-dvr-protocol').value,
                refresh_rate: parseInt(document.getElementById('edit-dvr-refresh').value) || 2000,
                stream_quality: document.getElementById('edit-dvr-quality').value,
            };
            try {
                var res = await fetch('/api/surv/dvrs/' + id, {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(payload)
                });
                if (res.ok) {
                    showMsg('DVR 설정이 저장되었습니다.', 'success');
                    closeEditModal();
                    loadDVRs();
                } else {
                    showMsg('DVR 저장 실패: ' + await res.text(), 'error');
                }
            } catch (err) {
                showMsg('DVR 저장 중 오류가 발생했습니다.', 'error');
            }
        }

        async function discoverChannels(id) {
            showMsg('채널 탐색 중...', 'success');
            try {
                const res = await fetch('/api/surv/dvrs/' + id + '/discover', { method: 'POST' });
                if (res.ok) {
                    const chs = await res.json();
                    showMsg('채널 ' + (chs ? chs.length : 0) + '개 발견', 'success');
                } else {
                    showMsg('채널 탐색 실패: ' + await res.text(), 'error');
                }
            } catch (err) {
                showMsg('채널 탐색 중 오류가 발생했습니다.', 'error');
            }
        }

        async function clearChannels(id) {
            if (!confirm('이 DVR의 탐색된 채널 정보를 모두 삭제하고 초기 상태로 되돌리시겠습니까?\n(DVR 연결 정보는 그대로 유지됩니다.)')) {
                return;
            }
            showMsg('채널 초기화 중...', 'success');
            try {
                const res = await fetch('/api/surv/dvrs/' + id + '/clear-channels', { method: 'POST' });
                if (res.ok) {
                    showMsg('채널이 성공적으로 초기화되었습니다.', 'success');
                } else {
                    showMsg('채널 초기화 실패: ' + await res.text(), 'error');
                }
            } catch (err) {
                showMsg('채널 초기화 중 오류가 발생했습니다.', 'error');
            }
        }

        async function resetDatabase() {
            if (!confirm('경고: 에이전트 데이터베이스를 완전히 초기화하시겠습니까?\n모든 DVR 설정과 탐색된 채널이 영구적으로 지워집니다.')) {
                return;
            }
            showMsg('DB 초기화 중...', 'success');
            try {
                const res = await fetch('/api/surv/reset-db', { method: 'POST' });
                if (res.ok) {
                    showMsg('데이터베이스가 초기화되었습니다. 에이전트를 재시작하거나 설정을 새로고침하세요.', 'success');
                    setTimeout(() => { location.reload(); }, 1500);
                } else {
                    showMsg('DB 초기화 실패: ' + await res.text(), 'error');
                }
            } catch (err) {
                showMsg('DB 초기화 중 오류가 발생했습니다.', 'error');
            }
        }

        // Check for updates
        async function checkUpdate() {
            try {
                const res = await fetch('/api/update');
                const info = await res.json();
                if (info.available) {
                    document.getElementById('update-version').textContent = info.latest_ver;
                    document.getElementById('update-current').textContent = info.current_ver;
                    document.getElementById('update-link').href = info.release_url;
                    document.getElementById('update-banner').classList.remove('hidden');
                }
            } catch (e) {
                console.log('Update check failed:', e);
            }
        }

        // Init
        loadSettings();
        loadDVRs();
        setTimeout(checkUpdate, 3000);
    </script>
</body>
</html>
`

// --- channel metadata editing (thumbnail grid: reorder + rename) ---

type snapCacheEntry struct {
	data []byte
	at   time.Time
}

var (
	snapCacheMu sync.Mutex
	snapCache   = map[string]snapCacheEntry{}
)

const snapCacheTTL = 8 * time.Second

// handleSurvSnapshot serves a channel's JPEG snapshot as a thumbnail, cached
// briefly so a 36-cell grid doesn't hammer the DVR on every render.
func handleSurvSnapshot(w http.ResponseWriter, r *http.Request) {
	if webSurvMgr == nil {
		http.Error(w, "surveillance manager not initialized", http.StatusServiceUnavailable)
		return
	}
	dvrID, err := strconv.ParseInt(r.URL.Query().Get("dvr"), 10, 64)
	if err != nil {
		http.Error(w, "bad dvr", http.StatusBadRequest)
		return
	}
	chNum, err := strconv.Atoi(r.URL.Query().Get("ch"))
	if err != nil {
		http.Error(w, "bad ch", http.StatusBadRequest)
		return
	}
	key := strconv.FormatInt(dvrID, 10) + ":" + strconv.Itoa(chNum)

	snapCacheMu.Lock()
	if e, ok := snapCache[key]; ok && time.Since(e.at) < snapCacheTTL {
		data := e.data
		snapCacheMu.Unlock()
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(data)
		return
	}
	snapCacheMu.Unlock()

	data, err := webSurvMgr.FetchSnapshot(dvrID, chNum)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	snapCacheMu.Lock()
	snapCache[key] = snapCacheEntry{data: data, at: time.Now()}
	snapCacheMu.Unlock()

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

// handleSurvChannelReorder applies a new channel order for one DVR.
func handleSurvChannelReorder(w http.ResponseWriter, r *http.Request) {
	if webSurvMgr == nil {
		http.Error(w, "surveillance manager not initialized", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		DVRID  int64 `json:"dvr_id"`
		ChNums []int `json:"ch_nums"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := webSurvMgr.ReorderChannels(req.DVRID, req.ChNums); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSurvChannelRename renames one channel.
func handleSurvChannelRename(w http.ResponseWriter, r *http.Request) {
	if webSurvMgr == nil {
		http.Error(w, "surveillance manager not initialized", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		DVRID int64  `json:"dvr_id"`
		ChNum int    `json:"ch_num"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := webSurvMgr.RenameChannel(req.DVRID, req.ChNum, strings.TrimSpace(req.Name)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
