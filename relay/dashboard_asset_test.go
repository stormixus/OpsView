package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestDashboardHTMLVersionsEmbeddedAssets(t *testing.T) {
	h := &Hub{}
	w := httptest.NewRecorder()
	h.HandleDashboardStatic(w, httptest.NewRequest("GET", "/dashboard/agent/example", nil))
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("HTML response: %d %v", w.Code, w.Header())
	}
	refs := regexp.MustCompile(`/dashboard/assets/([^?"']+)\?v=([a-f0-9]+)`).FindAllStringSubmatch(w.Body.String(), -1)
	if len(refs) < 8 {
		t.Fatalf("missing versioned stylesheet/scripts/images: %d", len(refs))
	}
	for _, ref := range refs {
		want, err := dashboardAssets.ReadFile("dashboard_assets/" + ref[1])
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(want)
		if ref[2] != fmt.Sprintf("%x", sum[:8]) {
			t.Fatalf("%s version does not match embedded content", ref[1])
		}
		asset := httptest.NewRecorder()
		h.HandleDashboardStatic(asset, httptest.NewRequest("GET", ref[0], nil))
		if asset.Code != 200 || !bytes.Equal(asset.Body.Bytes(), want) {
			t.Fatalf("versioned URL %s did not serve asset", ref[0])
		}
	}
}

func TestWallHTMLVersionsAssets(t *testing.T) {
	b, err := dashboardAssets.ReadFile("dashboard_assets/wall.html")
	if err != nil {
		t.Fatal(err)
	}
	got := string(versionDashboardHTML(b))
	for _, name := range []string{"style.css", "wsplayer.js", "wall.js"} {
		if !strings.Contains(got, "/dashboard/assets/"+name+"?v=") {
			t.Fatalf("wall page missing version for %s", name)
		}
	}
}
