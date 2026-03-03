package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/NebulousLabs/go-upnp"
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
	mux.HandleFunc("/api/surv", hub.HandleSurvConfig)

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: mux}

	// Try to setup UPnP
	go func() {
		setupUPNP(cfg.Port)
	}()

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

// setupUPNP attempts to discover a UPnP-enabled router and forward the relay port.
func setupUPNP(portStr string) {
	port, err := strconv.Atoi(portStr)
	if err != nil {
		log.Printf("[upnp] invalid port %s: %v", portStr, err)
		return
	}

	log.Printf("[upnp] discovering compatible routers...")
	d, err := upnp.Discover()
	if err != nil {
		log.Printf("[upnp] discovery failed (no UPnP router found): %v", err)
		return
	}

	ip, err := d.ExternalIP()
	if err != nil {
		log.Printf("[upnp] could not get external IP: %v", err)
		return
	}
	log.Printf("[upnp] found router. External IP: %s", ip)

	err = d.Forward(uint16(port), "OpsView Relay")
	if err != nil {
		log.Printf("[upnp] port forwarding failed for port %d: %v", port, err)
		return
	}

	log.Printf("[upnp] SUCCESS! Port %d is now forwarded. You can connect via ws://%s:%d/watch", port, ip, port)
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
	MaxWatcherQueue int
}

func loadConfig() Config {
	port := getPort()

	return Config{
		Port:            port,
		MaxWatcherQueue: 4,
	}
}
