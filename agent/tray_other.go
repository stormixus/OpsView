//go:build !windows

package main

import "log"

func runTray(cfg Config) {
	log.Println("[tray] system tray not supported on this platform, running in CLI mode")
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
		}
	}
	agent := NewAgent(agentCfg)
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
