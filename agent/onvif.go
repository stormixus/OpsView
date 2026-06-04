package main

import (
	"crypto/sha1"
	"encoding/base64"
)

// onvifPasswordDigest computes the WS-Security UsernameToken PasswordDigest:
// Base64( SHA1( nonce + created + password ) ). nonce is the raw (pre-base64)
// nonce bytes; created is the UTC timestamp string used verbatim in the header.
func onvifPasswordDigest(nonce []byte, created, password string) string {
	h := sha1.New()
	h.Write(nonce)
	h.Write([]byte(created))
	h.Write([]byte(password))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
