package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
)

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
	runTray(cfg)
}

// AgentConfig holds agent runtime settings.
type AgentConfig struct {
	RelayURL string
	Token    string
	Profile  int // 1080 or 720
	FPSMin   int
	FPSMax   int
	TileSize int
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
		log.Printf("[agent] generated token at %s", tokenPath)
	} else {
		log.Printf("[agent] loaded token from %s", tokenPath)
	}
	return token, nil
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
