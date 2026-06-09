package proto

// Hello is the HELLO message payload (JSON-encoded).
type Hello struct {
	Role          string   `json:"role"`               // "publisher" or "watcher"
	AgentID       string   `json:"agent_id,omitempty"` // publisher only: tenant/agent id; empty = default agent
	Client        string   `json:"client"`             // "opsview-agent", "opsview-viewer", "opsview-web"
	ClientVersion string   `json:"client_version"`     // e.g. "0.1.0"
	Supports      []string `json:"supports"`           // e.g. ["zstd"]
	WantProfile   *string  `json:"want_profile"`       // "1080", "720", or null
}

// Auth is the AUTH message payload (JSON-encoded).
//
// For a publisher, Token is the shared relay secret (RELAY_PUBLISHER_TOKEN) and
// PIN is the viewer PIN it advertises to watchers. For a watcher, Token is the
// viewer PIN and PIN is unused.
type Auth struct {
	Token string `json:"token"`
	PIN   string `json:"pin,omitempty"` // publisher only: viewer PIN to advertise
}

// Control is the CONTROL message payload (JSON-encoded).
type Control struct {
	Cmd     string `json:"cmd"`     // e.g. "set_profile"
	Profile string `json:"profile"` // "1080" or "720"
}

// ErrorMsg is the ERROR message payload (JSON-encoded).
type ErrorMsg struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// SurvConfig is the MsgSurvConfig payload (JSON-encoded).
type SurvConfig struct {
	DVRs     []DVRInfo     `json:"dvrs"`
	Channels []ChannelInfo `json:"channels"`
	// AgentID is the tenant/session scope for /surv paths. The publisher leaves it
	// empty (it doesn't know its tenant id); the relay stamps it before forwarding
	// to watchers so a named agent's streams resolve as /surv/<id>/dvrN_chM.
	AgentID string `json:"agent_id,omitempty"`
}

// DVRInfo describes a single DVR/NVR.
type DVRInfo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Addr          string `json:"addr"`
	Port          int    `json:"port"`
	ExtAddr       string `json:"ext_addr,omitempty"`  // Public host for relay RTSP pull and viewer HLS playback
	ExtPort       int    `json:"ext_port,omitempty"`  // Public HTTP port for HLS (/surv/...); 0 = use viewer relay port
	RTSPPort      int    `json:"rtsp_port,omitempty"` // explicit RTSP port; 0 means default 554
	Username      string `json:"username"`
	Password      string `json:"password"`
	RefreshRate   int    `json:"refresh_rate"`
	StreamQuality string `json:"stream_quality"`
	Protocol      string `json:"protocol"`
}

// ChannelInfo describes a single surveillance channel.
type ChannelInfo struct {
	ID      int    `json:"id"`
	DVRID   int64  `json:"dvr_id"`
	ChNum   int    `json:"ch_num"`
	Name    string `json:"name"`
	Order   int    `json:"order"`
	Enabled bool   `json:"enabled"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	RtspURI string `json:"rtsp_uri,omitempty"`
}

// SurvMeta is the MsgSurvMeta payload (relay→publisher): a channel-metadata edit
// the agent applies to its DB. Either Order (reorder), Renames, or both.
type SurvMeta struct {
	DVRID   int64           `json:"dvr_id"`
	Order   []int           `json:"order,omitempty"`   // ch_nums in desired display order
	Renames []ChannelRename `json:"renames,omitempty"` // per-channel name changes
}

// ChannelRename is one channel's new display name.
type ChannelRename struct {
	ChNum int    `json:"ch_num"`
	Name  string `json:"name"`
}

// AgentControl is the MsgAgentControl payload (relay→publisher): an operator
// command relayed from the dashboard. Action "reconnect" makes the agent
// re-discover every DVR and re-publish its surveillance config, recovering
// streams that dropped (e.g. after a DVR reboot).
type AgentControl struct {
	Action string `json:"action"` // "reconnect"
}

// SnapshotRequest is sent by watcher to request a snapshot via agent proxy.
type SnapshotRequest struct {
	ReqID string `json:"req_id"`
	DVRID int64  `json:"dvr_id"`
	ChNum int    `json:"ch_num"`
}

// SnapshotResponse is sent by agent with the snapshot data.
type SnapshotResponse struct {
	ReqID string `json:"req_id"`
	DVRID int64  `json:"dvr_id"`
	ChNum int    `json:"ch_num"`
	Data  string `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// SurvEvent is one DVR event edge (publisher→relay). The relay pairs Active
// true/false edges per (AgentID,ChID,Kind) into intervals for the recording
// timeline and retention. AgentID is left empty by the publisher and stamped by
// the relay, mirroring SurvConfig.
type SurvEvent struct {
	AgentID string `json:"agent_id,omitempty"`
	ChID    string `json:"ch_id"`  // "dvr1_ch2" — matches the stream/segment path
	Kind    string `json:"kind"`   // "motion" | "linecross" | "person" | "vehicle" | ...
	Active  bool   `json:"active"` // true = event started, false = ended
	TS      int64  `json:"ts"`     // event time, unix milliseconds (from the source's local wall-clock)
}

// SurvEventThumb carries a small JPEG snapshot captured at an event's start, so
// the dashboard can show an event thumbnail without extracting from recordings.
// AgentID is stamped by the relay (left empty by the publisher), like SurvEvent.
type SurvEventThumb struct {
	AgentID string `json:"agent_id,omitempty"`
	ChID    string `json:"ch_id"` // "dvr1_ch2" — matches the event/stream path
	TS      int64  `json:"ts"`    // unix MILLISECONDS — SAME value as the matching SurvEvent edge that opened the event
	Jpeg    []byte `json:"jpeg"`  // raw JPEG (Go json encodes []byte as base64)
}
