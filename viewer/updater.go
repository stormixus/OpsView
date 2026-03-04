package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Version is set at build time via ldflags.
var Version = "dev"

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

// DownloadAndInstall downloads the update and launches the platform installer.
func (u *Updater) DownloadAndInstall(downloadURL string) (string, error) {
	resp, err := http.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	fileName := filepath.Base(downloadURL)
	tmpPath := filepath.Join(os.TempDir(), fileName)

	f, err := os.Create(tmpPath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return "", err
	}
	f.Close()

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
