package proto

// Hello is the HELLO message payload (JSON-encoded).
type Hello struct {
	Role          string   `json:"role"`           // "publisher" or "watcher"
	Client        string   `json:"client"`         // "opsview-agent", "opsview-viewer", "opsview-web"
	ClientVersion string   `json:"client_version"` // e.g. "0.1.0"
	Supports      []string `json:"supports"`       // e.g. ["zstd"]
	WantProfile   *string  `json:"want_profile"`   // "1080", "720", or null
}

// Auth is the AUTH message payload (JSON-encoded).
type Auth struct {
	Token string `json:"token"`
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
}

// DVRInfo describes a single DVR/NVR.
type DVRInfo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Addr          string `json:"addr"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	RefreshRate   int    `json:"refresh_rate"`
	StreamQuality string `json:"stream_quality"`
	Protocol      string `json:"protocol"`
}

// ChannelInfo describes a single surveillance channel.
type ChannelInfo struct {
	ID      int   `json:"id"`
	DVRID   int64 `json:"dvr_id"`
	ChNum   int   `json:"ch_num"`
	Name    string `json:"name"`
	Order   int    `json:"order"`
	Enabled bool   `json:"enabled"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
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
