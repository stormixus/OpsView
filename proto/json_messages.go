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
