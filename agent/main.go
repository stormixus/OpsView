package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
		var err error
		token, err = loadOrCreateAgentToken()
		if err != nil {
			token = "dev-publisher-token"
			log.Printf("[agent] WARNING: token auto-generation failed: %v", err)
			log.Println("[agent] WARNING: falling back to default token (set AGENT_TOKEN)")
		}
	}

	profile := 1080
	if p := os.Getenv("AGENT_PROFILE"); p == "720" {
		profile = 720
	}

	fpsMin := envInt("AGENT_FPS_MIN", 5)
	fpsMax := envInt("AGENT_FPS_MAX", 10)
	tileSize := envInt("AGENT_TILE_SIZE", 128)
	if fpsMin < 1 {
		log.Printf("[agent] WARNING: invalid AGENT_FPS_MIN=%d, using 1", fpsMin)
		fpsMin = 1
	}
	if fpsMax < 1 {
		log.Printf("[agent] WARNING: invalid AGENT_FPS_MAX=%d, using 10", fpsMax)
		fpsMax = 10
	}
	if fpsMin > fpsMax {
		log.Printf("[agent] WARNING: AGENT_FPS_MIN(%d) > AGENT_FPS_MAX(%d), clamping min", fpsMin, fpsMax)
		fpsMin = fpsMax
	}
	if tileSize < 16 || tileSize > 512 {
		log.Printf("[agent] WARNING: invalid AGENT_TILE_SIZE=%d, using 128", tileSize)
		tileSize = 128
	}

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

func loadOrCreateAgentToken() (string, error) {
	tokenPath := strings.TrimSpace(os.Getenv("AGENT_TOKEN_FILE"))
	if tokenPath == "" {
		tokenPath = defaultAgentTokenPath()
	}

	token, created, err := readOrCreateToken(tokenPath)
	if err != nil {
		return "", err
	}
	if created {
		log.Printf("[agent] generated AGENT_TOKEN at %s (set RELAY_PUBLISHER_TOKEN to match)", tokenPath)
	} else {
		log.Printf("[agent] loaded AGENT_TOKEN from %s", tokenPath)
	}
	return token, nil
}

func defaultAgentTokenPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "agent_token.txt"
	}
	return filepath.Join(filepath.Dir(exe), "agent_token.txt")
}

func readOrCreateToken(path string) (token string, created bool, err error) {
	if b, readErr := os.ReadFile(path); readErr == nil {
		t := strings.TrimSpace(string(b))
		if t == "" {
			return "", false, fmt.Errorf("token file is empty: %s", path)
		}
		return t, false, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return "", false, readErr
	}

	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", false, err
		}
	}

	t, err := generateToken(32)
	if err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		return "", false, err
	}
	return t, true, nil
}

func generateToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
