package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/NebulousLabs/go-upnp"
)

// runServer starts the HTTP server and returns a stop function
// that gracefully shuts down the server and hub.
func runServer() (stop func()) {
	cfg := loadConfig()

	// Fail closed: without a configured publisher secret, anyone reachable on
	// the (UPnP-forwarded) relay port could claim the publisher slot. Refuse to
	// start rather than run an unauthenticated relay.
	if cfg.PublisherToken == "" {
		log.Fatalf("[relay] RELAY_PUBLISHER_TOKEN (or AGENT_TOKEN) must be set; refusing to start without publisher authentication")
	}

	allowedOrigins = cfg.AllowedOrigins
	if len(allowedOrigins) == 0 {
		log.Printf("[relay] RELAY_ALLOWED_ORIGINS not set; accepting WebSocket connections from any Origin")
	}

	hub := NewHub(cfg)
	go hub.Run()
	go hub.alertMonitor() // fault alerts (agent offline/recovery) via telegram/webhook
	if rec := newRecorder(hub, cfg.Port); rec != nil {
		hub.rec = rec
		hub.events = newEventStore(os.Getenv("RELAY_REC_DIR")) // event timeline store (same root as recordings)
		go rec.Run()                                           // NVR: record active streams to RELAY_REC_DIR (opt-in)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/publish", hub.HandlePublish)
	mux.HandleFunc("/watch", hub.HandleWatch)
	mux.HandleFunc("/health", hub.HandleHealth)
	mux.HandleFunc("/metrics", hub.HandleMetrics)
	mux.HandleFunc("/api/surv", hub.HandleSurvConfig)
	mux.HandleFunc("/api/surv/streams", hub.HandleSurvStreams)
	mux.HandleFunc("/api/snapshot", hub.HandleSnapshot)
	mux.HandleFunc("/surv/walllayout", hub.ServeWallLayout) // live-wall overlay layout (exact path, beats /surv/)
	mux.HandleFunc("/surv/wallorder", hub.ServeWallOrder)   // set wall tile order (drag-to-reorder)
	mux.HandleFunc("/surv/ws/", hub.ServeSurvWS)            // fMP4-over-WebSocket (more specific than /surv/)
	mux.HandleFunc("/surv/", hub.ServeSurvHLS)
	hub.registerDashboard(mux)
	if cfg.DashboardToken != "" {
		log.Printf("[relay] dashboard enabled at /dashboard")
	}

	srv := &http.Server{Addr: ":" + cfg.Port, Handler: mux}

	// UPnP port-forwarding is opt-in (RELAY_UPNP=1): it only helps direct-IP
	// setups. Behind a tunnel (the default deployment) it's unnecessary, and SSDP
	// discovery fails noisily inside Docker — so skip it unless explicitly enabled.
	if os.Getenv("RELAY_UPNP") == "1" {
		go setupUPNP(cfg.Port)
	}

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
	// PublisherToken is the shared secret a publisher must present to claim the
	// single publisher slot. Loaded from RELAY_PUBLISHER_TOKEN (falling back to
	// AGENT_TOKEN). The relay refuses to start if it is empty (fail-closed).
	PublisherToken string
	// AllowedOrigins is the WebSocket Origin allowlist (RELAY_ALLOWED_ORIGINS,
	// comma-separated). Empty = accept any Origin.
	AllowedOrigins []string
	// Agents is the per-tenant publisher-token registry (RELAY_AGENTS JSON) plus
	// the default agent (authenticated by PublisherToken).
	Agents *agentRegistry
	// DashboardToken is the env-configured dashboard password (RELAY_DASHBOARD_TOKEN).
	// Used as the login password unless one is stored in the DB (Store). Empty +
	// no DB token => dashboard disabled (routes not registered).
	DashboardToken string
	// Store is the SQLite persistence (tenant registry + settings); nil if RELAY_DB
	// is unset.
	Store *agentStore
}

func loadConfig() Config {
	port := getPort()

	token := os.Getenv("RELAY_PUBLISHER_TOKEN")
	if token == "" {
		token = os.Getenv("AGENT_TOKEN")
	}

	var origins []string
	for _, o := range strings.Split(os.Getenv("RELAY_ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}

	reg, err := parseAgentRegistry(os.Getenv("RELAY_AGENTS"), token)
	if err != nil {
		log.Fatalf("[relay] invalid RELAY_AGENTS: %v", err)
	}
	// RELAY_DB (a path on a persistent volume) makes the named-agent registry and
	// dashboard password editable from the dashboard; unset = env-only.
	var store *agentStore
	if dbPath := os.Getenv("RELAY_DB"); dbPath != "" {
		var serr error
		store, serr = openAgentStore(dbPath)
		if serr != nil {
			log.Fatalf("[relay] open RELAY_DB %s: %v", dbPath, serr)
		}
		if serr := reg.useStore(store); serr != nil {
			log.Fatalf("[relay] agent store init: %v", serr)
		}
		initWallLayoutStore(store) // persist wall tile order/columns in SQLite (by wall uuid)
		log.Printf("[relay] tenant registry persisted to %s (%d named agents)", dbPath, len(reg.listNamed()))
	}

	return Config{
		Port:            port,
		MaxWatcherQueue: 4,
		PublisherToken:  token,
		AllowedOrigins:  origins,
		Agents:          reg,
		DashboardToken:  os.Getenv("RELAY_DASHBOARD_TOKEN"),
		Store:           store,
	}
}
