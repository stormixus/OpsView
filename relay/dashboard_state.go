package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/opsview/opsview/proto"
)

type dashboardState struct {
	Relay        relayInfo    `json:"relay"`
	Agents       []agentState `json:"agents"`
	HiddenAgents []agentRef   `json:"hidden_agents,omitempty"` // operator-hidden agents (for restore)
}

// agentRef is a minimal agent identity (used for the hidden-agents list).
type agentRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type relayInfo struct {
	Version       string `json:"version"`
	UptimeSec     int64  `json:"uptime_sec"`
	AgentsOnline  int    `json:"agents_online"`
	AgentsTotal   int    `json:"agents_total"`
	WatchersTotal int    `json:"watchers_total"`
	StreamsTotal  int    `json:"streams_total"`
	BytesIn       int64  `json:"bytes_in"`
	BytesOut      int64  `json:"bytes_out"`
}

type agentState struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Connected     bool          `json:"connected"`
	Since         string        `json:"since"`           // RFC3339, "" if never
	LastPublishAt string        `json:"last_publish_at"` // RFC3339, "" if never
	PINSet        bool          `json:"pin_set"`
	BytesIn       int64         `json:"bytes_in"`
	BytesOut      int64         `json:"bytes_out"`
	PublishCount  int64         `json:"publish_count"`
	Watchers      []WatcherInfo `json:"watchers"`
	Streams       []streamState `json:"streams"`
	DVRs          []dvrSummary  `json:"dvrs"`
	Channels      []channelMeta `json:"channels"` // all configured channels (for the editor)
}

type channelMeta struct {
	DVRID   int64  `json:"dvr_id"`
	ChNum   int    `json:"ch_num"`
	Name    string `json:"name"`
	Order   int    `json:"order"`
	Enabled bool   `json:"enabled"`
	Active  bool   `json:"active"`
}

type streamState struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Active     bool   `json:"active"`
	Codec      string `json:"codec"`
	WSWatchers int    `json:"ws_watchers"`
	Path       string `json:"path"` // path segment for /surv/{path} (agent-namespaced)
}

type dvrSummary struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Channels int    `json:"channels"`
}

// streamPath builds the /surv path segment for a stream, namespacing non-default
// agents (default agent keeps the legacy flat path).
func streamPath(agentID, streamID string) string {
	if agentID == "" || agentID == "default" {
		return streamID
	}
	return agentID + "/" + streamID
}

// streamIDFor builds the relay stream key for a channel (matches StreamStats IDs).
func streamIDFor(dvrID int64, chNum int) string {
	return fmt.Sprintf("dvr%d_ch%d", dvrID, chNum)
}

func msToRFC3339(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// buildDashboardState snapshots all agent sessions into one serializable struct.
func (h *Hub) buildDashboardState() dashboardState {
	sessions := h.allSessions()
	agents := make([]agentState, 0, len(sessions))
	hidden := make([]agentRef, 0)

	var online, watchersTotal, streamsTotal int
	var bin, bout int64

	for _, s := range sessions {
		if h.isAgentHidden(s.id) {
			hidden = append(hidden, agentRef{ID: s.id, Name: s.name})
			continue // excluded from the dashboard (operator-hidden)
		}
		s.mu.RLock()
		connected := s.publisher != nil
		pinSet := s.pin != ""
		s.mu.RUnlock()

		s.survConfigMu.RLock()
		raw := s.survConfig
		s.survConfigMu.RUnlock()

		streams := make([]streamState, 0)
		activeSet := map[string]bool{}
		for _, st := range s.survProxy.StreamStats() {
			streams = append(streams, streamState{
				ID: st.ID, Name: st.Name, Active: st.Active, Codec: st.Codec,
				WSWatchers: st.WSWatchers, Path: streamPath(s.id, st.ID),
			})
			if st.Active {
				activeSet[st.ID] = true
			}
		}

		dvrs := []dvrSummary{}
		channels := []channelMeta{}
		if len(raw) > proto.HeaderSize {
			var cfg proto.SurvConfig
			if json.Unmarshal(raw[proto.HeaderSize:], &cfg) == nil {
				counts := map[int64]int{}
				for _, ch := range cfg.Channels {
					counts[ch.DVRID]++
					channels = append(channels, channelMeta{
						DVRID: ch.DVRID, ChNum: ch.ChNum, Name: ch.Name, Order: ch.Order,
						Enabled: ch.Enabled,
						Active:  activeSet[streamIDFor(ch.DVRID, ch.ChNum)],
					})
				}
				for _, d := range cfg.DVRs {
					dvrs = append(dvrs, dvrSummary{ID: d.ID, Name: d.Name, Channels: counts[d.ID]})
				}
			}
		}

		watchers := s.watcherList()
		for i := range watchers {
			watchers[i].Label = h.getIPLabel(watchers[i].IP)
		}
		sbin := s.bytesIn.Load()
		sbout := s.bytesOut.Load()

		if connected {
			online++
		}
		watchersTotal += len(watchers)
		streamsTotal += len(streams)
		bin += sbin
		bout += sbout

		agents = append(agents, agentState{
			ID:            s.id,
			Name:          s.name,
			Connected:     connected,
			Since:         msToRFC3339(s.connectedAt.Load()),
			LastPublishAt: msToRFC3339(s.lastPublishAt.Load()),
			PINSet:        pinSet,
			BytesIn:       sbin,
			BytesOut:      sbout,
			PublishCount:  s.publishCount.Load(),
			Watchers:      watchers,
			Streams:       streams,
			DVRs:          dvrs,
			Channels:      channels,
		})
	}

	return dashboardState{
		Relay: relayInfo{
			Version:       relayVersion,
			UptimeSec:     int64(time.Since(h.startedAt).Seconds()),
			AgentsOnline:  online,
			AgentsTotal:   len(agents),
			WatchersTotal: watchersTotal,
			StreamsTotal:  streamsTotal,
			BytesIn:       bin,
			BytesOut:      bout,
		},
		Agents:       agents,
		HiddenAgents: hidden,
	}
}
