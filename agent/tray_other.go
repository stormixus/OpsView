//go:build !windows

package main

import "log"

func runTray(cfg Config) {
	log.Println("[tray] system tray not supported on this platform, running in CLI mode")
	pin, err := loadOrCreateAgentPIN()
	if err != nil {
		pin = "000000"
		log.Printf("[tray] PIN auto-gen failed: %v", err)
	}

	agentCfg := AgentConfig{
		RelayURL: cfg.RelayURL,
		PIN:      pin,
		Profile:  cfg.Profile,
		FPSMin:   5,
		FPSMax:   10,
		TileSize: 128,
	}
	agent := NewAgent(agentCfg)
	agent.survMgr = cfg.SurvMgr
	webSurvMgr = cfg.SurvMgr
	if agent.survMgr != nil {
		agent.survMgr.onChange = func() { agent.sendSurvConfig() }
	}
	agent.Run()
}

func setAutoStart(enable bool) {
	// Not implemented on non-Windows
}

func syncTrayAutoStart(enabled bool) {
	// Not implemented on non-Windows
}

func restartAgentIfRunning() {
	// Not implemented on non-Windows
}
