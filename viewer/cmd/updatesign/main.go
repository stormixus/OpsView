// Command updatesign generates the Ed25519 update-signing keypair and signs
// release assets so the viewer's auto-updater can verify them before install.
//
//	go run ./viewer/cmd/updatesign gen
//	ED25519_UPDATE_PRIVATE_KEY=<base64> go run ./viewer/cmd/updatesign sign <file>...
//
// `sign` writes a detached <file>.sig (base64 of an Ed25519 signature over the
// file's SHA-256 digest) next to each input — the exact format the updater's
// verifyUpdateSignature expects.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "gen":
		gen()
	case "sign":
		if err := signFiles(os.Getenv("ED25519_UPDATE_PRIVATE_KEY"), os.Args[2:]); err != nil {
			fatal(err)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  updatesign gen")
	fmt.Fprintln(os.Stderr, "  ED25519_UPDATE_PRIVATE_KEY=<base64> updatesign sign <file>...")
	os.Exit(2)
}

func gen() {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fatal(err)
	}
	fmt.Println("# Public key — paste into viewer/updater.go `updatePublicKeyB64`")
	fmt.Println("#   (or inject via -ldflags \"-X main.updatePublicKeyB64=...\"):")
	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	fmt.Println()
	fmt.Println("# Private key — store as the ED25519_UPDATE_PRIVATE_KEY GitHub Actions")
	fmt.Println("#   secret. Keep it secret; anyone with it can sign malicious updates:")
	fmt.Println(base64.StdEncoding.EncodeToString(priv))
}

// signFiles signs each file, writing <file>.sig. Exposed (lowercase, same
// package) for testing the signer/verifier format contract.
func signFiles(keyB64 string, files []string) error {
	priv, err := decodePrivateKey(keyB64)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no files to sign")
	}
	for _, f := range files {
		sigB64, err := signFileToSig(priv, f)
		if err != nil {
			return err
		}
		out := f + ".sig"
		if err := os.WriteFile(out, []byte(sigB64+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Printf("signed %s -> %s\n", f, out)
	}
	return nil
}

func decodePrivateKey(keyB64 string) (ed25519.PrivateKey, error) {
	if strings.TrimSpace(keyB64) == "" {
		return nil, fmt.Errorf("ED25519_UPDATE_PRIVATE_KEY not set")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyB64))
	if err != nil {
		return nil, fmt.Errorf("decode private key: %w", err)
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key must be %d bytes, got %d", ed25519.PrivateKeySize, len(key))
	}
	return ed25519.PrivateKey(key), nil
}

// signFileToSig returns the base64 Ed25519 signature over the file's SHA-256
// digest — matching the updater's verification format.
func signFileToSig(priv ed25519.PrivateKey, path string) (string, error) {
	digest, err := sha256File(path)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, digest)), nil
}

func sha256File(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "updatesign:", err)
	os.Exit(1)
}
