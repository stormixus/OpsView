package main

import (
	"log"
	"net/http"
	"os"
	"strings"
)

func main() {
	cfg := loadConfig()

	hub := NewHub(cfg)
	go hub.Run()

	mux := http.NewServeMux()
	mux.HandleFunc("/publish", hub.HandlePublish)
	mux.HandleFunc("/watch", hub.HandleWatch)
	mux.HandleFunc("/health", hub.HandleHealth)
	mux.HandleFunc("/metrics", hub.HandleMetrics)

	// Serve web viewer static files
	webDir := os.Getenv("RELAY_WEB_DIR")
	if webDir == "" {
		webDir = "../web"
	}
	mux.Handle("/", http.FileServer(http.Dir(webDir)))

	addr := ":" + cfg.Port
	log.Printf("[relay] listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("[relay] server error: %v", err)
	}
}

// Config holds relay configuration loaded from environment variables.
type Config struct {
	Port            string
	PublisherToken  string
	WatcherTokens   map[string]bool
	MaxWatcherQueue int
}

func loadConfig() Config {
	port := os.Getenv("RELAY_PORT")
	if port == "" {
		port = "8080"
	}

	pubToken := os.Getenv("RELAY_PUBLISHER_TOKEN")
	if pubToken == "" {
		pubToken = "dev-publisher-token"
		log.Println("[relay] WARNING: using default publisher token (set RELAY_PUBLISHER_TOKEN)")
	}

	watcherTokens := make(map[string]bool)
	raw := os.Getenv("RELAY_WATCHER_TOKENS")
	if raw == "" {
		watcherTokens["dev-watcher-token"] = true
		log.Println("[relay] WARNING: using default watcher token (set RELAY_WATCHER_TOKENS)")
	} else {
		for _, t := range strings.Split(raw, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				watcherTokens[t] = true
			}
		}
	}

	return Config{
		Port:            port,
		PublisherToken:  pubToken,
		WatcherTokens:   watcherTokens,
		MaxWatcherQueue: 4,
	}
}
