package main

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
)

// Version is set at build time via ldflags.
var Version = "dev"

// logPath returns the log file path next to the config directory.
func logPath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		home, _ := os.UserHomeDir()
		appData = filepath.Join(home, "AppData", "Roaming")
	}
	return filepath.Join(appData, "opsview-agent", "agent.log")
}

func main() {
	// Set up file logging
	lp := logPath()
	os.MkdirAll(filepath.Dir(lp), 0755)
	logFile, err := os.OpenFile(lp, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		log.SetOutput(logFile)
		defer logFile.Close()
	}

	cfg := loadConfig()
	log.Printf("[agent] relay=%s profile=%d", cfg.RelayURL, cfg.Profile)
	cfg.SurvMgr = NewSurveillanceManager()
	defer cfg.SurvMgr.Shutdown()
	runTray(cfg)
}

// AgentConfig holds agent runtime settings.
type AgentConfig struct {
	RelayURL string
	PIN      string
	Profile  int // 1080 or 720
	FPSMin   int
	FPSMax   int
	TileSize int
}

func loadOrCreateAgentPIN() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		home, _ := os.UserHomeDir()
		appData = filepath.Join(home, "AppData", "Roaming")
	}
	pinPath := filepath.Join(appData, "opsview-agent", "agent_pin.txt")

	b, err := os.ReadFile(pinPath)
	if err == nil {
		pin := strings.TrimSpace(string(b))
		if len(pin) == 6 {
			return pin, nil
		}
	}

	// Generate 6-digit PIN
	pinInt, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	pin := fmt.Sprintf("%06d", pinInt.Int64())

	os.MkdirAll(filepath.Dir(pinPath), 0755)
	os.WriteFile(pinPath, []byte(pin+"\n"), 0644)
	return pin, nil
}
