package main

import (
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
)

func main() {
	cfg := loadAgentConfig()

	log.Printf("[agent] relay=%s profile=%d fps_min=%d fps_max=%d",
		cfg.RelayURL, cfg.Profile, cfg.FPSMin, cfg.FPSMax)

	agent := NewAgent(cfg)

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("[agent] shutting down...")
		agent.Stop()
	}()

	agent.Run()
}

// AgentConfig holds agent settings from environment variables.
type AgentConfig struct {
	RelayURL string
	Token    string
	Profile  int // 1080 or 720
	FPSMin   int
	FPSMax   int
	TileSize int
}

func loadAgentConfig() AgentConfig {
	relayURL := os.Getenv("AGENT_RELAY_URL")
	if relayURL == "" {
		relayURL = "ws://127.0.0.1:8080/publish"
	}

	token := os.Getenv("AGENT_TOKEN")
	if token == "" {
		token = "dev-publisher-token"
		log.Println("[agent] WARNING: using default token (set AGENT_TOKEN)")
	}

	profile := 1080
	if p := os.Getenv("AGENT_PROFILE"); p == "720" {
		profile = 720
	}

	fpsMin := envInt("AGENT_FPS_MIN", 5)
	fpsMax := envInt("AGENT_FPS_MAX", 10)
	tileSize := envInt("AGENT_TILE_SIZE", 128)

	return AgentConfig{
		RelayURL: relayURL,
		Token:    token,
		Profile:  profile,
		FPSMin:   fpsMin,
		FPSMax:   fpsMax,
		TileSize: tileSize,
	}
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
