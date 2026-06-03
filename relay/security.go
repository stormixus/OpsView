package main

import (
	"net"
	"net/url"
	"strings"
)

// redactRTSPURL removes userinfo (user:pass) from an RTSP URL so credentials
// never reach logs. Non-URL input is returned unchanged.
func redactRTSPURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

// isBlockedRTSPHost reports whether an RTSP target host must not be dialed.
// DVRs legitimately live on private LANs, so private ranges are allowed; we
// block loopback, link-local (incl. the 169.254.169.254 cloud-metadata
// endpoint), and the unspecified address to limit publisher-driven SSRF.
func isBlockedRTSPHost(host string) bool {
	h := host
	if hostOnly, _, err := net.SplitHostPort(host); err == nil {
		h = hostOnly
	}
	h = strings.Trim(h, "[]")
	ip := net.ParseIP(h)
	if ip == nil {
		return false // hostname — not an IP literal we can classify
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// originAllowed implements the WebSocket CheckOrigin policy. It is non-breaking
// by default: requests with no Origin (native agent/viewer clients) and, when
// no allowlist is configured, all origins are accepted. With an allowlist set
// (RELAY_ALLOWED_ORIGINS), only same-host origins and listed origins pass.
func originAllowed(origin, requestHost string, allowlist []string) bool {
	if origin == "" {
		return true
	}
	if len(allowlist) == 0 {
		return true
	}
	o := strings.ToLower(strings.TrimSpace(origin))
	for _, a := range allowlist {
		if o == strings.ToLower(strings.TrimSpace(a)) {
			return true
		}
	}
	if u, err := url.Parse(origin); err == nil && u.Host != "" {
		if strings.EqualFold(u.Host, requestHost) || strings.EqualFold(u.Hostname(), requestHost) {
			return true
		}
	}
	return false
}
