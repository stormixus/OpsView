package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// Config represents the JSON configuration stored at %APPDATA%/opsview-agent/config.json.
type Config struct {
	RelayURL  string `json:"relay_url"`
	Token     string `json:"token"`
	Profile   int    `json:"profile"`
	AutoStart bool   `json:"auto_start"`
}

func defaultConfig() Config {
	return Config{
		RelayURL:  "ws://127.0.0.1:8080/publish",
		Token:     "",
		Profile:   1080,
		AutoStart: false,
	}
}

func configPath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		home, _ := os.UserHomeDir()
		appData = filepath.Join(home, "AppData", "Roaming")
	}
	return filepath.Join(appData, "opsview-agent", "config.json")
}

func loadConfig() Config {
	cfg := defaultConfig()
	data, err := os.ReadFile(configPath())
	if err != nil {
		return cfg
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("[config] parse error: %v", err)
		return defaultConfig()
	}
	if cfg.Profile != 720 && cfg.Profile != 1080 {
		cfg.Profile = 1080
	}
	if cfg.RelayURL == "" {
		cfg.RelayURL = "ws://127.0.0.1:8080/publish"
	}
	return cfg
}

func saveConfig(cfg Config) error {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
