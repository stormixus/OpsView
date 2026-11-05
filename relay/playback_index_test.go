package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writePlaybackIndexSegment(t *testing.T, rec *Recorder, stream string, start time.Time) string {
	t.Helper()
	dir, ok := rec.safeStreamDir(stream)
	if !ok {
		t.Fatal("invalid fixture stream")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := start.Format(recNameLayout) + ".mp4"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("segment"), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestPlaybackIndexCrossMidnightKeepsShortDuration(t *testing.T) {
	rec := &Recorder{dir: t.TempDir(), segSecs: 300}
	stream := "site/dvr1_ch1"
	midnight := time.Date(2026, 6, 8, 0, 0, 0, 0, time.Local)
	first := writePlaybackIndexSegment(t, rec, stream, midnight.Add(-10*time.Second))
	second := writePlaybackIndexSegment(t, rec, stream, midnight.Add(10*time.Second))
	writePlaybackIndexSegment(t, rec, stream, midnight.Add(310*time.Second))

	// The day index by itself cannot see the following day's first segment.
	previousDay := rec.segments(stream, "20260607")
	if len(previousDay) != 1 || previousDay[0].Dur != 300 {
		t.Fatalf("previous day fixture: %+v", previousDay)
	}
	got := rec.segmentsForExport(stream, midnight.Unix()+5, midnight.Unix()+6)
	if len(got) != 1 || got[0].Name != first || got[0].Dur != 20 {
		t.Fatalf("cross-midnight short segment lost: %+v", got)
	}
	// Recomputing the range duration must not mutate the shared day index.
	if previousDay[0].Dur != 300 || rec.segments(stream, "20260607")[0].Dur != 300 {
		t.Fatal("range computation modified the cached day slice")
	}
	// A half-open query at the true end must only select the next segment.
	got = rec.segmentsForExport(stream, midnight.Unix()+10, midnight.Unix()+11)
	if len(got) != 1 || got[0].Name != second || got[0].Dur != 300 {
		t.Fatalf("adjacent-day boundary overlap: %+v", got)
	}
}

func TestPlaybackIndexInvalidatesOnAddDeleteAndOwnsResults(t *testing.T) {
	rec := &Recorder{dir: t.TempDir(), segSecs: 300}
	stream := "site/dvr1_ch1"
	base := time.Date(2026, 6, 7, 12, 0, 0, 0, time.Local)
	first := writePlaybackIndexSegment(t, rec, stream, base)
	last := writePlaybackIndexSegment(t, rec, stream, base.Add(5*time.Minute))
	dir, _ := rec.safeStreamDir(stream)
	// Set distinct directory mtimes explicitly so filesystem timestamp resolution
	// never makes this invalidation test depend on sleeps or execution speed.
	setDirectoryVersion := func(version int) {
		t.Helper()
		stamp := base.Add(time.Duration(version) * time.Hour)
		if err := os.Chtimes(dir, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	query := func() []recSegment {
		return rec.segmentsForExport(stream, base.Unix(), base.Unix()+600)
	}
	setDirectoryVersion(1)
	if got := query(); len(got) != 2 || got[0].Dur != 300 {
		t.Fatalf("initial index: %+v", got)
	}
	middle := writePlaybackIndexSegment(t, rec, stream, base.Add(time.Minute))
	setDirectoryVersion(2)
	got := query()
	if len(got) != 3 || got[0].Name != first || got[0].Dur != 60 || got[1].Name != middle || got[1].Dur != 240 || got[2].Name != last {
		t.Fatalf("new segment missing or stale durations: %+v", got)
	}
	got[0].Name = "caller mutation"
	got[1].Dur = -1
	if owned := query(); len(owned) != 3 || owned[0].Name != first || owned[1].Dur != 240 {
		t.Fatalf("returned slice shared with later queries: %+v", owned)
	}
	if err := os.Remove(filepath.Join(dir, middle)); err != nil {
		t.Fatal(err)
	}
	setDirectoryVersion(3)
	if got := query(); len(got) != 2 || got[0].Name != first || got[0].Dur != 300 || got[1].Name != last {
		t.Fatalf("deleted segment retained in day index: %+v", got)
	}
}

func TestPlaybackTimelineExposesRecordedFilename(t *testing.T) {
	h := NewHub(cfgWithDash("test-secret"))
	h.rec = &Recorder{dir: t.TempDir(), segSecs: 300}
	stream := "site/dvr1_ch1"
	start := time.Date(2026, 6, 7, 12, 0, 0, 0, time.Local)
	name := writePlaybackIndexSegment(t, h.rec, stream, start)
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/dashboard/api/rec-timeline?stream=%s&start=%d&end=%d", stream, start.Unix(), start.Unix()+60), nil)
	request.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: signSession(h.effectiveDashToken(), time.Now().Add(time.Hour))})
	response := httptest.NewRecorder()
	h.HandleDashboardRecTimeline(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("timeline status: %d", response.Code)
	}
	var body struct {
		Segments []struct {
			Name  string `json:"name"`
			Start int64  `json:"start"`
			Dur   int    `json:"dur"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Segments) != 1 || body.Segments[0].Name != name || body.Segments[0].Start != start.Unix() || body.Segments[0].Dur != 300 {
		t.Fatalf("timeline omitted usable segment filename: %+v", body.Segments)
	}
	if path, ok := h.rec.segmentFile(stream, body.Segments[0].Name); !ok {
		t.Fatal("timeline filename cannot resolve for playback")
	} else if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
