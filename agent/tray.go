//go:build windows

package main

import (
	_ "embed"
	"log"
	"os/exec"
	"sync"

	"github.com/getlantern/systray"
)

var (
	trayAgent         *Agent
	trayAgentMu       sync.Mutex
	trayStatusItem    *systray.MenuItem
	trayStartItem     *systray.MenuItem
	trayPauseItem     *systray.MenuItem
	trayRestartItem   *systray.MenuItem
	trayAutoStartItem *systray.MenuItem
)

//go:embed tray.ico
var trayIcon []byte

func runTray(cfg Config) {
	systray.Run(func() { onTrayReady(cfg) }, onTrayExit)
}

func onTrayReady(cfg Config) {
	if len(trayIcon) > 0 {
		systray.SetIcon(trayIcon)
	}
	systray.SetTitle("OpsView Agent")
	systray.SetTooltip("OpsView Agent")

	trayStatusItem = systray.AddMenuItem("○ 포즈됨", "Agent status")
	trayStatusItem.Disable()

	systray.AddSeparator()

	mSettings := systray.AddMenuItem("설정...", "Open settings")
	trayAutoStartItem = systray.AddMenuItemCheckbox("Windows 시작 시 자동 실행", "Toggle auto-start", cfg.AutoStart)
	mAutoStart := trayAutoStartItem

	systray.AddSeparator()

	trayStartItem = systray.AddMenuItem("시작", "Start agent")
	trayPauseItem = systray.AddMenuItem("일시정지", "Pause agent")
	trayRestartItem = systray.AddMenuItem("재시작", "Restart agent")
	updateTrayControlMenu(false)

	systray.AddSeparator()

	mViewLog := systray.AddMenuItem("로그 보기", "Open log file")

	systray.AddSeparator()

	mQuit := systray.AddMenuItem("종료", "Quit OpsView Agent")

	// Start agent
	startAgent(cfg)

	go func() {
		for {
			select {
			case <-mSettings.ClickedCh:
				showSettings()
			case <-mAutoStart.ClickedCh:
				if mAutoStart.Checked() {
					mAutoStart.Uncheck()
					setAutoStart(false)
					c := loadConfig()
					c.AutoStart = false
					if err := saveConfig(c); err != nil {
						log.Printf("[tray] save auto-start config failed: %v", err)
					}
				} else {
					mAutoStart.Check()
					setAutoStart(true)
					c := loadConfig()
					c.AutoStart = true
					if err := saveConfig(c); err != nil {
						log.Printf("[tray] save auto-start config failed: %v", err)
					}
				}
			case <-mViewLog.ClickedCh:
				exec.Command("notepad.exe", logPath()).Start()
			case <-trayStartItem.ClickedCh:
				startAgent(loadConfig())
			case <-trayPauseItem.ClickedCh:
				stopAgent()
			case <-trayRestartItem.ClickedCh:
				restartAgent()
			case <-mQuit.ClickedCh:
				systray.Quit()
			}
		}
	}()
}

func onTrayExit() {
	stopAgent()
}

func startAgent(cfg Config) {
	trayAgentMu.Lock()
	defer trayAgentMu.Unlock()
	if trayAgent != nil {
		return
	}

	agentCfg := AgentConfig{
		RelayURL: cfg.RelayURL,
		Token:    cfg.Token,
		Profile:  cfg.Profile,
		FPSMin:   5,
		FPSMax:   10,
		TileSize: 128,
	}

	if agentCfg.Token == "" {
		var err error
		agentCfg.Token, err = loadOrCreateAgentToken()
		if err != nil {
			agentCfg.Token = "dev-publisher-token"
			log.Printf("[tray] token auto-gen failed: %v", err)
		} else {
			cfg.Token = agentCfg.Token
			if err := saveConfig(cfg); err != nil {
				log.Printf("[tray] save token config failed: %v", err)
			}
		}
	}

	trayAgent = NewAgent(agentCfg)
	go trayAgent.Run()

	updateTrayStatus(true)
	updateTrayControlMenu(true)
	log.Printf("[tray] agent started: relay=%s profile=%d", cfg.RelayURL, cfg.Profile)
}

func stopAgent() {
	trayAgentMu.Lock()
	defer trayAgentMu.Unlock()

	if trayAgent != nil {
		trayAgent.Stop()
		trayAgent = nil
		log.Println("[tray] agent stopped")
	}
	updateTrayStatus(false)
	updateTrayControlMenu(false)
}

func restartAgent() {
	stopAgent()
	cfg := loadConfig()
	startAgent(cfg)
}

func restartAgentIfRunning() {
	if isAgentRunning() {
		restartAgent()
	}
}

func isAgentRunning() bool {
	trayAgentMu.Lock()
	defer trayAgentMu.Unlock()
	return trayAgent != nil
}

func updateTrayStatus(running bool) {
	if trayStatusItem == nil {
		return
	}
	if running {
		trayStatusItem.SetTitle("● 실행 중")
		trayStatusItem.SetTooltip("Agent is running")
	} else {
		trayStatusItem.SetTitle("○ 포즈됨")
		trayStatusItem.SetTooltip("Agent is paused")
	}
}

func updateTrayControlMenu(running bool) {
	if trayStartItem == nil || trayPauseItem == nil || trayRestartItem == nil {
		return
	}
	if running {
		trayStartItem.Disable()
		trayPauseItem.Enable()
		trayRestartItem.Enable()
	} else {
		trayStartItem.Enable()
		trayPauseItem.Disable()
		trayRestartItem.Disable()
	}
}

// syncTrayAutoStart updates the tray menu checkbox to match settings dialog changes.
func syncTrayAutoStart(enabled bool) {
	if trayAutoStartItem == nil {
		return
	}
	if enabled {
		trayAutoStartItem.Check()
	} else {
		trayAutoStartItem.Uncheck()
	}
}
