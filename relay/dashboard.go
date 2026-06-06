package main

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/opsview/opsview/proto"
)

//go:embed dashboard_assets
var dashboardAssets embed.FS

// dashboardEnabled reports whether the operator dashboard is configured.
func (h *Hub) dashboardEnabled() bool { return h.cfg.DashboardToken != "" }

// authedDashboard checks the session cookie.
func (h *Hub) authedDashboard(r *http.Request) bool {
	c, err := r.Cookie(dashboardCookieName)
	if err != nil {
		return false
	}
	return verifySession(h.cfg.DashboardToken, c.Value, time.Now())
}

// HandleDashboardLogin authenticates the admin password (rate-limited, constant-time).
func (h *Hub) HandleDashboardLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip := clientIP(r.RemoteAddr)
	if !h.pinLimiter.allowed(ip) {
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(h.cfg.DashboardToken)) != 1 {
		h.pinLimiter.recordFailure(ip)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	h.pinLimiter.recordSuccess(ip)
	exp := time.Now().Add(dashboardSessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     dashboardCookieName,
		Value:    signSession(h.cfg.DashboardToken, exp),
		Path:     "/dashboard",
		Expires:  exp,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	w.WriteHeader(http.StatusOK)
}

// HandleDashboardLogout clears the session cookie.
func (h *Hub) HandleDashboardLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: dashboardCookieName, Value: "", Path: "/dashboard", MaxAge: -1})
	w.WriteHeader(http.StatusOK)
}

// HandleDashboardState returns the aggregated multi-agent state JSON (cookie-gated).
func (h *Hub) HandleDashboardState(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(h.buildDashboardState())
}

// HandleDashboardChannelMeta forwards a channel-metadata edit (reorder/rename)
// from the dashboard to a specific agent's publisher (relay -> agent round-trip).
// The agent applies it to its DB and re-broadcasts the updated config.
func (h *Hub) HandleDashboardChannelMeta(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		AgentID string `json:"agent_id"`
		proto.SurvMeta
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s := h.sessionByID(body.AgentID)
	if s == nil {
		s = h.defaultSession()
	}
	if s == nil || !s.online() {
		http.Error(w, "agent offline", http.StatusConflict)
		return
	}
	payload, _ := json.Marshal(body.SurvMeta)
	s.sendToPublisher(proto.MarshalMessage(proto.MsgSurvMeta, payload))
	w.WriteHeader(http.StatusNoContent)
}

// HandleDashboardAgents manages the named-agent (tenant) registry from the
// dashboard: GET lists agents, POST upserts one, DELETE removes one. Editing
// requires a persistent store (RELAY_DB); without it the registry is read-only.
func (h *Hub) HandleDashboardAgents(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	reg := h.cfg.Agents
	switch r.Method {
	case http.MethodGet:
		type ag struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Token  string `json:"token"`
			Online bool   `json:"online"`
		}
		named := reg.listNamed()
		sort.Slice(named, func(i, j int) bool { return named[i].ID < named[j].ID })
		out := make([]ag, 0, len(named))
		for _, e := range named {
			online := false
			if s := h.sessionByID(e.ID); s != nil {
				online = s.online()
			}
			out = append(out, ag{ID: e.ID, Name: e.Name, Token: e.Token, Online: online})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"editable": reg.editable(), "agents": out})

	case http.MethodPost:
		if !reg.editable() {
			http.Error(w, "registry read-only: set RELAY_DB on a persistent volume", http.StatusConflict)
			return
		}
		var e agentEntry
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&e); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := reg.upsertNamed(e); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		if !reg.editable() {
			http.Error(w, "registry read-only: set RELAY_DB on a persistent volume", http.StatusConflict)
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		if err := reg.removeNamed(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleDashboardStatic serves embedded assets under /dashboard/assets/ and the
// SPA index.html for every other /dashboard* path (so client-side path routes
// like /dashboard/agent/<id> deep-link and survive a refresh). The /dashboard/api/*
// routes are registered separately and take precedence in the mux.
func (h *Hub) HandleDashboardStatic(w http.ResponseWriter, r *http.Request) {
	sub, _ := fs.Sub(dashboardAssets, "dashboard_assets")
	if strings.HasPrefix(r.URL.Path, "/dashboard/assets/") {
		http.StripPrefix("/dashboard/assets/", http.FileServer(http.FS(sub))).ServeHTTP(w, r)
		return
	}
	b, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

// registerDashboard wires routes onto mux only when the dashboard is enabled.
func (h *Hub) registerDashboard(mux *http.ServeMux) {
	if !h.dashboardEnabled() {
		return
	}
	mux.HandleFunc("/dashboard/api/login", h.HandleDashboardLogin)
	mux.HandleFunc("/dashboard/api/logout", h.HandleDashboardLogout)
	mux.HandleFunc("/dashboard/api/state", h.HandleDashboardState)
	mux.HandleFunc("/dashboard/api/channel-meta", h.HandleDashboardChannelMeta)
	mux.HandleFunc("/dashboard/api/agents", h.HandleDashboardAgents)
	mux.HandleFunc("/dashboard", h.HandleDashboardStatic)
	mux.HandleFunc("/dashboard/", h.HandleDashboardStatic)
}
