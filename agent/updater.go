package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	ghOwner = "stormixus"
	ghRepo  = "OpsView"
)

type UpdateInfo struct {
	Available   bool   `json:"available"`
	CurrentVer  string `json:"current_ver"`
	LatestVer   string `json:"latest_ver"`
	DownloadURL string `json:"download_url"`
	ReleaseURL  string `json:"release_url"`
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
	return "agent-windows-amd64"
}
