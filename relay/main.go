package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// runServer starts the HTTP server and returns a stop function
// that gracefully shuts down the server and hub.
func runServer() (stop func()) {
	cfg := loadConfig()

	hub := NewHub(cfg)
	go hub.Run()

	mux := http.NewServeMux()
	mux.HandleFunc("/publish", hub.HandlePublish)
	mux.HandleFunc("/watch", hub.HandleWatch)
	mux.HandleFunc("/health", hub.HandleHealth)
	mux.HandleFunc("/metrics", hub.HandleMetrics)

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[relay] server error: %v", err)
		}
	}()

	log.Printf("[relay] listening on :%s", cfg.Port)

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		hub.Stop()
	}
}

// getPort returns the configured relay port.
func getPort() string {
	port := os.Getenv("RELAY_PORT")
	if port == "" {
		port = "8080"
	}
	return port
}

// Config holds relay configuration loaded from environment variables.
type Config struct {
	Port            string
	PublisherToken  string
	WatcherTokens   map[string]bool
	MaxWatcherQueue int
}

func loadConfig() Config {
	port := getPort()

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
