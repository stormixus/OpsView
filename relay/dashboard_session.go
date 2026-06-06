package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

const (
	dashboardCookieName = "opsview_dash"
	dashboardSessionTTL = 12 * time.Hour
)

// dashboardKey derives a fixed-length HMAC key from the token so the raw token
// is never used directly as the key.
func dashboardKey(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// signSession returns "b64url(exp).hexHMAC(b64url(exp))".
func signSession(token string, exp time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(exp.Unix(), 10)))
	mac := hmac.New(sha256.New, dashboardKey(token))
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

// verifySession returns true iff the value's HMAC matches and exp is in the future.
func verifySession(token, value string, now time.Time) bool {
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	mac := hmac.New(sha256.New, dashboardKey(token))
	mac.Write([]byte(parts[0]))
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[1])) != 1 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	exp, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return false
	}
	return now.Unix() < exp
}
