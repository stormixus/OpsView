package main

import (
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
	if rest == "wall" {
		s.survProxy.EnsureMosaic(agentID) // lazy-start the composite on first viewer
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/surv/ws/" + rest
	s.survProxy.ServeWS(w, r2)
}
