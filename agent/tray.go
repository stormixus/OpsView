//go:build windows

package main

import (
	"log"
	"sync"

	"github.com/getlantern/systray"
)

var (
	trayAgent      *Agent
	trayAgentMu    sync.Mutex
	trayStatusItem *systray.MenuItem
)

func runTray(cfg Config) {
	systray.Run(func() { onTrayReady(cfg) }, onTrayExit)
}

func onTrayReady(cfg Config) {
	systray.SetTitle("OpsView Agent")
	systray.SetTooltip("OpsView Agent")

	trayStatusItem = systray.AddMenuItem("Connecting...", "Connection status")
	trayStatusItem.Disable()

	systray.AddSeparator()

	mSettings := systray.AddMenuItem("설정...", "Open settings")
	mAutoStart := systray.AddMenuItemCheckbox("Windows 시작 시 자동 실행", "Toggle auto-start", cfg.AutoStart)

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
					saveConfig(c)
				} else {
					mAutoStart.Check()
					setAutoStart(true)
					c := loadConfig()
					c.AutoStart = true
					saveConfig(c)
				}
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
		}
	}

	trayAgent = NewAgent(agentCfg)
	go trayAgent.Run()

	updateTrayStatus(true)
	log.Printf("[tray] agent started: relay=%s profile=%d", cfg.RelayURL, cfg.Profile)
}

func stopAgent() {
	trayAgentMu.Lock()
	defer trayAgentMu.Unlock()

	if trayAgent != nil {
		trayAgent.Stop()
		trayAgent = nil
	}
	log.Println("[tray] agent stopped")
}

func restartAgent() {
	stopAgent()
	cfg := loadConfig()
	startAgent(cfg)
}

func updateTrayStatus(connected bool) {
	if trayStatusItem == nil {
		return
	}
	if connected {
		trayStatusItem.SetTitle("● Connected to relay")
		trayStatusItem.SetTooltip("Agent is running")
	} else {
		trayStatusItem.SetTitle("○ Disconnected")
		trayStatusItem.SetTooltip("Agent is not connected")
	}
}
