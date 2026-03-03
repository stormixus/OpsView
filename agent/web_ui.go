package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var webSrv *http.Server
var webPort int
var webSurvMgr *SurveillanceManager

type APIStatus struct {
	PIN       string `json:"pin"`
	IP        string `json:"ip"`
	RelayURL  string `json:"relay_url"`
	Profile   int    `json:"profile"`
	AutoStart bool   `json:"autostart"`
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

func startWebUI() {
	if webSrv != nil {
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/api/status", handleAPIStatus)
	mux.HandleFunc("/api/save", handleAPISave)
	mux.HandleFunc("/api/surv/dvrs", handleSurvDVRs)
	mux.HandleFunc("/api/surv/dvrs/", handleSurvDVR)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Printf("[webui] failed to listen: %v", err)
		return
	}
	webPort = listener.Addr().(*net.TCPAddr).Port

	webSrv = &http.Server{Handler: mux}
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
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
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
		PIN:       pin,
		IP:        ip,
		RelayURL:  cfg.RelayURL,
		Profile:   cfg.Profile,
		AutoStart: cfg.AutoStart,
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
			Name     string `json:"name"`
			Addr     string `json:"addr"`
			Port     int    `json:"port"`
			Username string `json:"username"`
			Password string `json:"password"`
			Protocol string `json:"protocol"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Port == 0 {
			req.Port = 80
		}
		dvr, err := webSurvMgr.AddDVR(req.Name, req.Addr, req.Port, req.Username, req.Password, req.Protocol)
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
			if err := webSurvMgr.UpdateDVR(id, req.Name, req.Addr, req.Port, req.Username, req.Password, req.RefreshRate, req.StreamQuality, req.Protocol); err != nil {
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

const htmlTemplate = `
<!DOCTYPE html>
<html lang="ko">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>OpsView Agent Configuration</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <style>
        body {
            background-color: #0F172A;
            color: #F8FAFC;
            font-family: 'Inter', -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
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
                    return '<div class="bg-slate-800/50 rounded-xl p-4 border border-slate-700/50 flex items-center justify-between">' +
                        '<div>' +
                            '<div class="font-medium text-white text-sm">' + (d.name || d.addr) + '</div>' +
                            '<div class="text-xs text-slate-400 mt-0.5">' + d.addr + ':' + d.port + ' \u00b7 ' + d.protocol + '</div>' +
                        '</div>' +
                        '<div class="flex gap-2">' +
                            '<button onclick="discoverChannels(' + d.id + ')" class="text-xs bg-cyan-600/20 text-cyan-400 hover:bg-cyan-600/30 px-3 py-1.5 rounded-lg transition">\ucc44\ub110 \ud0d0\uc0c9</button>' +
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
                username: document.getElementById('dvr-username').value.trim(),
                password: document.getElementById('dvr-password').value,
                protocol: document.getElementById('dvr-protocol').value,
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

        // Init
        loadSettings();
        loadDVRs();
    </script>
</body>
</html>
`
