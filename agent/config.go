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
	Profile   int    `json:"profile"`
	AutoStart bool   `json:"auto_start"`
	// AgentID names this location for a multi-tenant relay (RELAY_AGENTS).
	// Empty = the default agent (single-site, unchanged behavior).
	AgentID string `json:"agent_id"`
	// PublisherToken is the shared relay secret presented to claim the publisher
	// slot. Configurable in-app (Settings); when empty, resolvePublisherToken
	// falls back to the RELAY_PUBLISHER_TOKEN / AGENT_TOKEN environment variable.
	PublisherToken string               `json:"publisher_token"`
	SurvMgr        *SurveillanceManager `json:"-"`
}

func defaultConfig() Config {
	return Config{
		RelayURL:  "ws://127.0.0.1:8080/publish",
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
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600) // may contain tokens/secrets
}
