package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Version is set at build time via ldflags.
var Version = "dev"

// updatePublicKeyB64 is the base64-encoded Ed25519 public key used to verify
// downloaded update installers before they are executed. An EMPTY value means
// updates are fail-closed (the updater refuses to install anything). Generate a
// keypair with `go run ./cmd/updatesign gen`, paste the public key here (or
// inject it via -ldflags "-X main.updatePublicKeyB64=..."), and keep the
// private key as the ED25519_UPDATE_PRIVATE_KEY CI secret.
var updatePublicKeyB64 = "ngC0jByNST35TESJxtjWxvbDFlFCO6lN/fSlOEDfB6Y="

// allowUpdateHostForTest is a test-only seam permitting one extra host (still
// https-only). Empty in production.
var allowUpdateHostForTest = ""

// allowedUpdateHost reports whether downloads/redirects to host are permitted.
func allowedUpdateHost(host string) bool {
	switch host {
	case "github.com", "api.github.com":
		return true
	}
	if strings.HasSuffix(host, ".githubusercontent.com") {
		return true
	}
	return allowUpdateHostForTest != "" && host == allowUpdateHostForTest
}

// validateUpdateURL enforces https and a pinned GitHub host so the updater can
// never be pointed at an attacker-controlled or plaintext endpoint.
func validateUpdateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid update URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("update URL must be https, got %q", u.Scheme)
	}
	if !allowedUpdateHost(u.Hostname()) {
		return fmt.Errorf("update URL host not allowed: %q", u.Hostname())
	}
	return nil
}

// verifyUpdateSignature checks an Ed25519 signature (base64) over the SHA-256
// digest of the downloaded installer using the embedded public key (base64). It
// fails closed: a missing/invalid key or signature is an error, never a pass.
func verifyUpdateSignature(digest []byte, sigB64, pubKeyB64 string) error {
	if pubKeyB64 == "" {
		return errors.New("no update public key embedded; refusing unsigned update")
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubKeyB64))
	if err != nil {
		return fmt.Errorf("bad update public key encoding: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("bad update public key size: %d", len(pub))
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil {
		return fmt.Errorf("bad update signature encoding: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("bad update signature size: %d", len(sig))
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), digest, sig) {
		return errors.New("update signature verification failed")
	}
	return nil
}

// newUpdateHTTPClient returns a client that re-validates every redirect hop so
// a redirect cannot bounce the download to a non-allowlisted host.
func newUpdateHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return validateUpdateURL(req.URL.String())
		},
	}
}

type UpdateInfo struct {
	Available   bool   `json:"available"`
	CurrentVer  string `json:"current_ver"`
	LatestVer   string `json:"latest_ver"`
	DownloadURL string `json:"download_url"`
	ReleaseURL  string `json:"release_url"`
	AssetSize   int64  `json:"asset_size"`
}

type Updater struct {
	ctx context.Context
}

const (
	ghOwner = "stormixus"
	ghRepo  = "OpsView"
)

func NewUpdater() *Updater {
	return &Updater{}
}

func (u *Updater) startup(ctx context.Context) {
	u.ctx = ctx
}

// GetVersion returns the current app version.
func (u *Updater) GetVersion() string {
	return Version
}

// CheckForUpdate queries GitHub Releases for a newer version.
func (u *Updater) CheckForUpdate() (*UpdateInfo, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", ghOwner, ghRepo)

	req, err := http.NewRequestWithContext(u.ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API: %s", resp.Status)
	}

	var release struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}

	info := &UpdateInfo{
		CurrentVer: Version,
		LatestVer:  release.TagName,
		ReleaseURL: release.HTMLURL,
	}

	if Version != "dev" && release.TagName != Version {
		info.Available = true
	}

	suffix := platformSuffix()
	for _, a := range release.Assets {
		if suffix != "" && strings.Contains(a.Name, suffix) {
			info.DownloadURL = a.BrowserDownloadURL
			info.AssetSize = a.Size
			break
		}
	}

	return info, nil
}

// DownloadAndInstall downloads the update, verifies its Ed25519 signature, and
// only then launches the platform installer. Any verification failure aborts
// before anything is executed.
func (u *Updater) DownloadAndInstall(downloadURL string) (string, error) {
	tmpPath, err := u.downloadVerified(downloadURL)
	if err != nil {
		return "", err
	}
	return u.runInstaller(tmpPath)
}

// downloadVerified validates the URL, downloads the asset and its detached
// signature, and verifies the signature over the asset's SHA-256 digest. It
// returns the temp path of the verified installer, or an error (having removed
// any partial download). It never executes anything.
func (u *Updater) downloadVerified(downloadURL string) (string, error) {
	return u.downloadVerifiedWithClient(newUpdateHTTPClient(), downloadURL)
}

func (u *Updater) downloadVerifiedWithClient(client *http.Client, downloadURL string) (string, error) {
	if err := validateUpdateURL(downloadURL); err != nil {
		return "", err
	}
	if updatePublicKeyB64 == "" {
		return "", errors.New("update verification key not configured; refusing to install")
	}
	sigURL := downloadURL + ".sig"
	if err := validateUpdateURL(sigURL); err != nil {
		return "", err
	}

	tmpPath, digest, err := u.downloadToTemp(client, downloadURL)
	if err != nil {
		return "", err
	}

	sigB64, err := fetchSignature(client, sigURL)
	if err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("fetch update signature: %w", err)
	}
	if err := verifyUpdateSignature(digest, sigB64, updatePublicKeyB64); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	return tmpPath, nil
}

// downloadToTemp streams the asset to a temp file, computing its SHA-256 digest
// as it goes, and emits progress events. Returns the temp path and digest.
func (u *Updater) downloadToTemp(client *http.Client, downloadURL string) (string, []byte, error) {
	resp, err := client.Get(downloadURL)
	if err != nil {
		return "", nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("bad status: %s", resp.Status)
	}

	totalSize := resp.ContentLength
	fileName := filepath.Base(downloadURL)
	tmpPath := filepath.Join(os.TempDir(), fileName)

	f, err := os.Create(tmpPath)
	if err != nil {
		return "", nil, err
	}

	h := sha256.New()
	buf := make([]byte, 32*1024)
	var downloaded int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				f.Close()
				os.Remove(tmpPath)
				return "", nil, writeErr
			}
			h.Write(buf[:n])
			downloaded += int64(n)
			if totalSize > 0 && u.ctx != nil {
				percent := float64(downloaded) / float64(totalSize) * 100
				wruntime.EventsEmit(u.ctx, "update-download-progress", percent)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			f.Close()
			os.Remove(tmpPath)
			return "", nil, readErr
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return "", nil, err
	}
	return tmpPath, h.Sum(nil), nil
}

// fetchSignature downloads the small detached signature file.
func fetchSignature(client *http.Client, sigURL string) (string, error) {
	resp, err := client.Get(sigURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("signature not available: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

// runInstaller launches the verified installer for the current platform.
func (u *Updater) runInstaller(tmpPath string) (string, error) {
	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command(tmpPath)
		if err := cmd.Start(); err != nil {
			return "", err
		}
		os.Exit(0)
		return "", nil

	case "darwin":
		cmd := exec.Command("open", tmpPath)
		if err := cmd.Start(); err != nil {
			return "", err
		}
		os.Exit(0)
		return "", nil

	case "linux":
		execPath, err := os.Executable()
		if err != nil {
			return "", err
		}
		dir := filepath.Dir(execPath)
		cmd := exec.Command("tar", "xzf", tmpPath, "-C", dir)
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("extract failed: %w", err)
		}
		os.Remove(tmpPath)
		return "업데이트 완료. 앱을 재시작하세요.", nil
	}

	return "", fmt.Errorf("unsupported platform: %s", runtime.GOOS)
}

func platformSuffix() string {
	switch runtime.GOOS {
	case "darwin":
		return "darwin-" + runtime.GOARCH + ".dmg"
	case "windows":
		return "windows-amd64-setup.exe"
	case "linux":
		return "linux-amd64.tar.gz"
	}
	return ""
}
