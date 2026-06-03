package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// The signer must produce a .sig the updater accepts: base64(Ed25519 over
// sha256(file)). This locks the format contract between the two.
func TestSignFileToSigVerifies(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "installer.bin")
	content := []byte("installer payload bytes 12345")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	sigB64, err := signFileToSig(priv, path)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	digest := sha256.Sum256(content)
	if !ed25519.Verify(pub, digest[:], sig) {
		t.Fatal("signer output did not verify against sha256(file)")
	}
	tampered := sha256.Sum256([]byte("tampered"))
	if ed25519.Verify(pub, tampered[:], sig) {
		t.Fatal("signature unexpectedly verified tampered content")
	}
}

func TestSignFilesRequiresValidKey(t *testing.T) {
	if err := signFiles("", []string{"x"}); err == nil {
		t.Fatal("missing key must error")
	}
	if err := signFiles("not-base64!!", []string{"x"}); err == nil {
		t.Fatal("bad key encoding must error")
	}
	if err := signFiles(base64.StdEncoding.EncodeToString([]byte("too-short")), []string{"x"}); err == nil {
		t.Fatal("wrong key size must error")
	}
}
