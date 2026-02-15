package main

import (
	"net/http"
	"strconv"
	"strings"
)

// CCTVProxyMiddleware intercepts /cctv/* paths to proxy DVR snapshots/streams.
type CCTVProxyMiddleware struct {
	cctv *CCTVManager
}

func NewCCTVProxyMiddleware(cctv *CCTVManager) *CCTVProxyMiddleware {
	return &CCTVProxyMiddleware{cctv: cctv}
}

// Middleware returns a Wails-compatible middleware.
func (p *CCTVProxyMiddleware) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// /cctv/snapshot/{dvrID}/{chNum}
		if strings.HasPrefix(path, "/cctv/snapshot/") {
			p.handleSnapshot(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (p *CCTVProxyMiddleware) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	// Parse /cctv/snapshot/{dvrID}/{chNum}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/cctv/snapshot/"), "/")
	if len(parts) != 2 {
		http.Error(w, "expected /cctv/snapshot/{dvrID}/{chNum}", 400)
		return
	}

	dvrID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid dvr_id", 400)
		return
	}
	chNum, err := strconv.Atoi(parts[1])
	if err != nil {
		http.Error(w, "invalid ch_num", 400)
		return
	}

	data, err := p.cctv.FetchSnapshot(dvrID, chNum)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}
