package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opsview/opsview/proto"
)

func TestEventTimingColdWarmAndInvalidatedReads(t *testing.T) {
	es := newEventStore(t.TempDir())
	base := time.Date(2026, 6, 7, 12, 0, 0, 0, time.Local).UnixMilli()
	day := dayKeyFromMs(base)
	es.add("cam", "person", true, base)
	es.add("cam", "person", false, base+1000)
	var cold eventReadTiming
	first := es.eventsForDayMeasured("cam", day, &cold)
	if len(first) != 1 || cold.stat <= 0 || cold.load <= 0 {
		t.Fatalf("cold read lacks file phases: events=%+v timing=%+v", first, cold)
	}
	var warm eventReadTiming
	second := es.eventsForDayMeasured("cam", day, &warm)
	if !reflect.DeepEqual(first, second) || warm.stat <= 0 || warm.cache <= 0 || warm.load != 0 {
		t.Fatalf("warm read rescanned or changed results: events=%+v timing=%+v", second, warm)
	}
	second[0].Kind = "caller mutation"
	if got := es.eventsForDay("cam", day); got[0].Kind != "person" {
		t.Fatalf("measured cache read returned shared slice: %+v", got)
	}
	es.add("cam", "person", true, base+5000)
	es.add("cam", "person", false, base+6000)
	var refreshed eventReadTiming
	if got := es.eventsForDayMeasured("cam", day, &refreshed); len(got) != 2 || refreshed.load <= 0 {
		t.Fatalf("append did not invalidate measured read: events=%+v timing=%+v", got, refreshed)
	}
	var missing eventReadTiming
	if got := es.eventsForDayMeasured("missing", day, &missing); len(got) != 0 || missing.stat <= 0 || missing.load != 0 {
		t.Fatalf("missing file phase result: events=%+v timing=%+v", got, missing)
	}
}

func TestEventTimingHeaderContainsOnlyNumericPhases(t *testing.T) {
	h := NewHub(cfgWithDash("test-secret"))
	h.events = newEventStore(t.TempDir())
	session := h.getOrCreateSession("private-tenant", "private label")
	cfg := proto.SurvConfig{Channels: []proto.ChannelInfo{{DVRID: 1, ChNum: 1, Name: "private camera", Enabled: true}}}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	session.survConfig = proto.MarshalMessage(proto.MsgSurvConfig, payload)
	base := time.Date(2026, 6, 7, 12, 0, 0, 0, time.Local).UnixMilli()
	stream := streamPath(session.id, streamIDFor(1, 1))
	h.events.add(stream, "person", true, base)
	h.events.add(stream, "person", false, base+1000)
	url := "/dashboard/api/rec-events-list?agent=private-tenant&day=" + dayKeyFromMs(base)
	request := httptest.NewRequest(http.MethodGet, url, nil)
	request.AddCookie(&http.Cookie{Name: dashboardCookieName, Value: signSession(h.effectiveDashToken(), time.Now().Add(time.Hour))})
	response := httptest.NewRecorder()
	h.HandleDashboardRecEventsList(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("event list status: %d", response.Code)
	}
	// Result snapshots headers at the first body write, catching late header setting.
	header := response.Result().Header.Get("Server-Timing")
	want := map[string]bool{"app": true, "wait": true, "stat": true, "cache": true, "load": true, "merge": true, "encode": true}
	for _, phase := range strings.Split(header, ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(phase), ";dur=")
		if !ok || !want[name] {
			t.Fatalf("unexpected or duplicate timing phase: %q", phase)
		}
		milliseconds, err := strconv.ParseFloat(value, 64)
		if err != nil || milliseconds < 0 || math.IsNaN(milliseconds) || math.IsInf(milliseconds, 0) {
			t.Fatalf("non-numeric/sensitive timing value: %q", phase)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("missing timing phases: %v", want)
	}
	var body struct {
		Events []recEventItem `json:"events"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 1 || body.Events[0].Stream != stream || body.Events[0].Start != base/1000 {
		t.Fatalf("diagnostics changed event response: %+v", body.Events)
	}
	unauthorized := httptest.NewRecorder()
	h.HandleDashboardRecEventsList(unauthorized, httptest.NewRequest(http.MethodGet, url, nil))
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.Result().Header.Get("Server-Timing") != "" {
		t.Fatalf("diagnostics exposed before authentication: %d %v", unauthorized.Code, unauthorized.Result().Header)
	}
}
