package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
)

func TestValidateUpdateURL(t *testing.T) {
	t.Parallel()
	ok := []string{
		"https://github.com/stormixus/OpsView/releases/download/v1/asset.dmg",
		"https://objects.githubusercontent.com/foo/bar",
		"https://release-assets.githubusercontent.com/x",
		"https://api.github.com/repos/x/y/releases/latest",
	}
	for _, u := range ok {
		if err := validateUpdateURL(u); err != nil {
			t.Errorf("expected %q to be allowed, got %v", u, err)
		}
	}
	bad := []string{
		"http://github.com/x/asset.dmg",            // not https
		"https://evil.com/asset.dmg",               // host not allowed
		"https://github.com.evil.com/asset.dmg",    // suffix trick
		"https://githubusercontent.com.evil.com/x", // suffix trick
		"ftp://github.com/x",                       // wrong scheme
		"file:///etc/passwd",                       // local
	}
	for _, u := range bad {
		if err := validateUpdateURL(u); err == nil {
			t.Errorf("expected %q to be rejected", u)
		}
	}
}

func TestVerifyUpdateSignature(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	digest := sha256.Sum256([]byte("the real installer bytes"))
	sig := ed25519.Sign(priv, digest[:])
	sigB64 := base64.StdEncoding.EncodeToString(sig)

	// Valid signature over the digest verifies.
	if err := verifyUpdateSignature(digest[:], sigB64, pubB64); err != nil {
		t.Fatalf("valid signature should verify: %v", err)
	}

	// Tampered payload (different digest) must fail.
	other := sha256.Sum256([]byte("malicious installer bytes"))
	if err := verifyUpdateSignature(other[:], sigB64, pubB64); err == nil {
		t.Fatal("tampered payload must fail verification")
	}

	// Empty embedded public key must fail closed.
	if err := verifyUpdateSignature(digest[:], sigB64, ""); err == nil {
		t.Fatal("empty public key must fail closed")
	}

	// Wrong key must fail.
	otherPub, _, _ := ed25519.GenerateKey(nil)
	if err := verifyUpdateSignature(digest[:], sigB64, base64.StdEncoding.EncodeToString(otherPub)); err == nil {
		t.Fatal("wrong public key must fail verification")
	}

	// Garbage signature must fail, not panic.
	if err := verifyUpdateSignature(digest[:], "not-base64!!", pubB64); err == nil {
		t.Fatal("garbage signature must fail")
	}
}

// downloadVerified must reject an installer whose signature does not match, and
// accept (returning the temp path) one that does — without executing anything.
func TestDownloadVerified(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	asset := []byte("OPSVIEW-INSTALLER-CONTENT-v9")
	digest := sha256.Sum256(asset)
	goodSig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, digest[:]))

	mux := http.NewServeMux()
	mux.HandleFunc("/asset.dmg", func(w http.ResponseWriter, r *http.Request) { w.Write(asset) })
	mux.HandleFunc("/asset.dmg.sig", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(serveSig(r)))
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	// Test seam: allow the httptest host (still https-only).
	prevHost := allowUpdateHostForTest
	prevKey := updatePublicKeyB64
	allowUpdateHostForTest = mustHost(t, srv.URL)
	updatePublicKeyB64 = base64.StdEncoding.EncodeToString(pub)
	defer func() { allowUpdateHostForTest = prevHost; updatePublicKeyB64 = prevKey }()

	u := NewUpdater()

	// Bad signature -> error, no temp file leaked.
	currentSig = "AAAA" // wrong
	if _, err := u.downloadVerifiedWithClient(srv.Client(), srv.URL+"/asset.dmg"); err == nil {
		t.Fatal("bad signature must be rejected")
	}

	// Good signature -> returns temp path with the exact bytes.
	currentSig = goodSig
	tmp, err := u.downloadVerifiedWithClient(srv.Client(), srv.URL+"/asset.dmg")
	if err != nil {
		t.Fatalf("valid signed asset should pass: %v", err)
	}
	defer os.Remove(tmp)
	got, _ := os.ReadFile(tmp)
	if string(got) != string(asset) {
		t.Fatalf("downloaded content mismatch")
	}

	// Empty embedded key -> fail closed even with a valid sig.
	updatePublicKeyB64 = ""
	if _, err := u.downloadVerifiedWithClient(srv.Client(), srv.URL+"/asset.dmg"); err == nil {
		t.Fatal("empty embedded key must fail closed")
	}
}

// currentSig lets the test swap the served signature per case.
var currentSig string

func serveSig(_ *http.Request) string { return currentSig }

func mustHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname()
}
