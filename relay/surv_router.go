package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// splitSurvPath separates an optional leading agentID from the channel path.
// If the first segment matches a non-default session id, it is the agent scope;
// otherwise the path belongs to the default (legacy) session.
func (h *Hub) splitSurvPath(p string) (agentID, rest string) {
	first := p
	if i := strings.IndexByte(p, '/'); i >= 0 {
		first = p[:i]
	}
	if first != "" && first != "default" {
		if s := h.sessionByID(first); s != nil {
			return first, strings.TrimPrefix(p, first+"/")
		}
	}
	return "default", p
}

// ServeSurvHLS dispatches /surv/[agentID/]chID/... to the right session proxy.
func (h *Hub) ServeSurvHLS(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/surv/")
	agentID, rest := h.splitSurvPath(p)
	s := h.sessionByID(agentID)
	if s == nil {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/surv/" + rest
	s.survProxy.ServeHLS(w, r2)
}

// ServeSurvWS dispatches /surv/ws/[agentID/]chID to the right session proxy.
func (h *Hub) ServeSurvWS(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/surv/ws/")
	agentID, rest := h.splitSurvPath(p)
	s := h.sessionByID(agentID)
	if s == nil {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	if strings.HasPrefix(rest, "wall") {
		s.survProxy.EnsureMosaic(agentID, rest) // lazy-start the composite on first viewer
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/surv/ws/" + rest
	s.survProxy.ServeWS(w, r2)
}

// ServeWallLayout returns the live-wall grid layout as JSON for a client's
// click-target overlay (ungated, same as the wall WS/HLS surface):
// GET /surv/walllayout?agent=<id>. Triggers EnsureMosaic so the layout reflects
// the running composite; returns {"enabled":false} when RELAY_WALL is off.
func (h *Hub) ServeWallLayout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	agent := r.URL.Query().Get("agent")
	s := h.sessionByID(agent)
	if s == nil {
		s = h.defaultSession()
	}
	if s == nil {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	out := struct {
		Enabled bool         `json:"enabled"`
		Rows    int          `json:"rows"`
		Cols    int          `json:"cols"`
		FPS     int          `json:"fps"`
		Cells   []mosaicCell `json:"cells"`
	}{}
	if !wallEnabled() {
		json.NewEncoder(w).Encode(out) // enabled:false
		return
	}
	out.Enabled = true
	if agent == "" {
		agent = "default"
	}
	wallID := r.URL.Query().Get("wall")
	if wallID == "" {
		wallID = "wall" // whole-agent default; viewer passes walldvr<N> for per-DVR
	}
	s.survProxy.EnsureMosaic(agent, wallID)
	if rows, cols, fps, cells, ok := s.survProxy.WallLayout(wallID); ok {
		out.Rows, out.Cols, out.FPS, out.Cells = rows, cols, fps, cells
	}
	json.NewEncoder(w).Encode(out)
}

// ServeWallOrder sets the operator's custom tile order for a wall (drag-to-reorder
// in the viewer) and rebuilds the mosaic in that order. Body is a JSON array of
// base stream ids. POST /surv/wallorder?agent=<id>&wall=<wallID>. Ungated, like the
// rest of the /surv surface; the order is persisted on the relay.
func (h *Hub) ServeWallOrder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	wallID := r.URL.Query().Get("wall")
	if wallID == "" {
		http.Error(w, "missing wall", http.StatusBadRequest)
		return
	}
	agent := r.URL.Query().Get("agent")
	s := h.sessionByID(agent)
	if s == nil {
		s = h.defaultSession()
	}
	if s == nil {
		http.Error(w, "no such agent", http.StatusNotFound)
		return
	}
	var ids []string
	if err := json.NewDecoder(r.Body).Decode(&ids); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if agent == "" {
		agent = "default"
	}
	setWallOrder(wallOrderKey(agent, wallID), ids)
	s.survProxy.EnsureMosaic(agent, wallID) // rebuild in the new order
	w.WriteHeader(http.StatusNoContent)
}
