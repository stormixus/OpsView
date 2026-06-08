package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	ghOwner = "stormixus"
	ghRepo  = "OpsView"
)

// updatePublicKeyB64 is the base64-encoded Ed25519 public key used to verify
// downloaded update installers before they are executed. Same key as the viewer.
var updatePublicKeyB64 = "ngC0jByNST35TESJxtjWxvbDFlFCO6lN/fSlOEDfB6Y="

type UpdateInfo struct {
	Available   bool   `json:"available"`
	CurrentVer  string `json:"current_ver"`
	LatestVer   string `json:"latest_ver"`
	DownloadURL string `json:"download_url"`
	ReleaseURL  string `json:"release_url"`
}

// allowedUpdateHost reports whether downloads/redirects to host are permitted.
func allowedUpdateHost(host string) bool {
	switch host {
	case "github.com", "api.github.com":
		return true
	}
	if strings.HasSuffix(host, ".githubusercontent.com") {
		return true
	}
	return false
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

// downloadToTemp streams the asset to a temp file, computing its SHA-256 digest
// as it goes. Returns the temp path and digest.
func downloadToTemp(client *http.Client, downloadURL string) (string, []byte, error) {
	resp, err := client.Get(downloadURL)
	if err != nil {
		return "", nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("bad status: %s", resp.Status)
	}

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

// downloadVerifiedWithClient validates the URL, downloads the asset and its detached
// signature, and verifies the signature over the asset's SHA-256 digest. It
// returns the temp path of the verified installer, or an error (having removed
// any partial download). It never executes anything.
func downloadVerifiedWithClient(client *http.Client, downloadURL string) (string, error) {
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

	tmpPath, digest, err := downloadToTemp(client, downloadURL)
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

// downloadVerified validates the URL, downloads the asset and its detached
// signature, and verifies the signature over the asset's SHA-256 digest. It
// returns the temp path of the verified installer, or an error (having removed
// any partial download). It never executes anything.
func downloadVerified(downloadURL string) (string, error) {
	return downloadVerifiedWithClient(newUpdateHTTPClient(), downloadURL)
}

// runInstaller launches the verified installer for Windows silently. Non-Windows is a no-op error.
func runInstaller(tmpPath string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("auto-update only supported on Windows, current OS: %s", runtime.GOOS)
	}
	cmd := exec.Command(tmpPath, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NOCANCEL", "/NORESTART")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start installer: %w", err)
	}
	return nil
}

// CheckForUpdate queries GitHub Releases for a newer version.
func CheckForUpdate() (*UpdateInfo, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", ghOwner, ghRepo)

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
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

	suffix := agentAssetSuffix()
	for _, a := range release.Assets {
		if suffix != "" && strings.Contains(a.Name, suffix) {
			info.DownloadURL = a.BrowserDownloadURL
			break
		}
	}

	return info, nil
}

func agentAssetSuffix() string {
	return "opsview-agent-setup"
}

// DownloadAndInstall downloads the update, verifies its Ed25519 signature, and
// only then launches the Windows silent installer. Any verification failure aborts
// before anything is executed. On success, the agent exits so the installer can replace it.
func DownloadAndInstall(downloadURL string) error {
	log.Printf("[update] downloading and verifying update from %s", downloadURL)
	tmpPath, err := downloadVerified(downloadURL)
	if err != nil {
		return fmt.Errorf("download/verify failed: %w", err)
	}
	log.Printf("[update] signature verified, running installer from %s", tmpPath)
	if err := runInstaller(tmpPath); err != nil {
		return err
	}
	log.Printf("[update] installer started, exiting agent so it can be replaced")
	os.Exit(0)
	return nil
}

// AutoUpdateLoop periodically checks for updates and silently installs them.
// Runs until stop is closed. Recovers from errors and keeps looping.
func AutoUpdateLoop(stop <-chan struct{}) {
	// Initial delay before first check
	select {
	case <-time.After(60 * time.Second):
	case <-stop:
		return
	}

	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()

	for {
		// Skip auto-update in dev builds
		if Version != "dev" {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[update] recovered panic in update check: %v", r)
					}
				}()

				info, err := CheckForUpdate()
				if err != nil {
					log.Printf("[update] check failed: %v", err)
					return
				}

				if info.Available && info.DownloadURL != "" {
					log.Printf("[update] new version available: %s -> %s", info.CurrentVer, info.LatestVer)
					if err := DownloadAndInstall(info.DownloadURL); err != nil {
						log.Printf("[update] install failed: %v", err)
					}
					// If DownloadAndInstall succeeds, os.Exit(0) is called and we never reach here
				}
			}()
		}

		select {
		case <-ticker.C:
			// Loop continues
		case <-stop:
			return
		}
	}
}
