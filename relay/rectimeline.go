package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// tlSegment is one recorded span on the unified-player timeline (unix seconds).
type tlSegment struct {
	Start int64 `json:"start"`
	Dur   int   `json:"dur"`
}

// tlEvent is one (clustered) event mark on the timeline (unix seconds).
type tlEvent struct {
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	Kind  string `json:"kind"`
}

type recTimeline struct {
	Segments []tlSegment `json:"segments"`
	Events   []tlEvent   `json:"events"`
}

// daysInRange returns every YYYYMMDD (local) from start..end inclusive, oldest
// first. Empty when end<=start. Used to union per-day event files across a
// timeline window that can span multiple days.
func daysInRange(startSec, endSec int64) []string {
	if endSec <= startSec {
		return nil
	}
	loc := time.Local
	d := time.Unix(startSec, 0).In(loc)
	day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	last := time.Unix(endSec, 0).In(loc)
	var out []string
	for !day.After(last) {
		out = append(out, day.Format("20060102"))
		day = day.AddDate(0, 0, 1)
	}
	return out
}

// timelineFor assembles the recording coverage + clustered events for a stream
// over [start,end] unix seconds. Reuses segmentsForExport (range segments) and
// eventsForDay (per-day events), then applies the same clustering the event grid
// uses so one channel's flapping doesn't spray dozens of marks.
func (h *Hub) timelineFor(stream string, start, end int64) recTimeline {
	out := recTimeline{Segments: []tlSegment{}, Events: []tlEvent{}}
	if h.rec != nil {
		for _, s := range h.rec.segmentsForExport(stream, start, end) {
			out.Segments = append(out.Segments, tlSegment{Start: s.Start, Dur: s.Dur})
		}
	}
	if h.events != nil {
		var items []recEventItem
		for _, day := range daysInRange(start, end) {
			for _, iv := range h.events.eventsForDay(stream, day) {
				if iv.Start < end && iv.End > start {
					items = append(items, recEventItem{Stream: stream, Kind: iv.Kind, Start: iv.Start, End: iv.End})
				}
			}
		}
		for _, it := range clusterEventItems(items) {
			out.Events = append(out.Events, tlEvent{Start: it.Start, End: it.End, Kind: it.Kind})
		}
		sort.Slice(out.Events, func(i, j int) bool { return out.Events[i].Start < out.Events[j].Start })
	}
	return out
}

// recSegSeconds returns the recorder's nominal segment length in seconds, used to
// decide whether a timeline window touches the still-recording live edge.
func (h *Hub) recSegSeconds() int {
	if h.rec != nil && h.rec.segSecs > 0 {
		return h.rec.segSecs
	}
	return recSegSeconds
}

// HandleDashboardRecTimeline serves recording coverage + events for a stream over
// a unix-second range. Admin-gated. ?stream=<path>&start=<sec>&end=<sec>.
func (h *Hub) HandleDashboardRecTimeline(w http.ResponseWriter, r *http.Request) {
	if !h.authedDashboard(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.rec == nil {
		http.Error(w, "recording disabled", http.StatusConflict)
		return
	}
	q := r.URL.Query()
	stream := q.Get("stream")
	start, _ := strconv.ParseInt(q.Get("start"), 10, 64)
	end, _ := strconv.ParseInt(q.Get("end"), 10, 64)
	if stream == "" || end <= start {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	tl := h.timelineFor(stream, start, end)
	w.Header().Set("Content-Type", "application/json")
	if end > time.Now().Unix()-int64(h.recSegSeconds()) {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=60")
	}
	json.NewEncoder(w).Encode(tl)
}
