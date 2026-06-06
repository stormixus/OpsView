package main

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"time"
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

// HandleDashboardStatic serves index.html (at /dashboard) and embedded assets.
func (h *Hub) HandleDashboardStatic(w http.ResponseWriter, r *http.Request) {
	sub, _ := fs.Sub(dashboardAssets, "dashboard_assets")
	if r.URL.Path == "/dashboard" || r.URL.Path == "/dashboard/" {
		b, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
		return
	}
	http.StripPrefix("/dashboard/assets/", http.FileServer(http.FS(sub))).ServeHTTP(w, r)
}

// registerDashboard wires routes onto mux only when the dashboard is enabled.
func (h *Hub) registerDashboard(mux *http.ServeMux) {
	if !h.dashboardEnabled() {
		return
	}
	mux.HandleFunc("/dashboard/api/login", h.HandleDashboardLogin)
	mux.HandleFunc("/dashboard/api/logout", h.HandleDashboardLogout)
	mux.HandleFunc("/dashboard/api/state", h.HandleDashboardState)
	mux.HandleFunc("/dashboard", h.HandleDashboardStatic)
	mux.HandleFunc("/dashboard/", h.HandleDashboardStatic)
}
