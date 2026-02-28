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
	"time"
)

var webSrv *http.Server
var webPort int

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
            <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
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

                <!-- Relay URL -->
                <div>
                    <label class="block text-sm font-medium text-slate-300 mb-2">Relay URL (고급)</label>
                    <input type="text" id="relay-url" class="block w-full bg-slate-800/80 border border-slate-700 text-white rounded-xl py-3 px-4 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent transition" placeholder="ws://127.0.0.1:8080/publish">
                </div>
            </div>

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
                relay_url: document.getElementById('relay-url').value,
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

        // Init
        loadSettings();
    </script>
</body>
</html>
`
