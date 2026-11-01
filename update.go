package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Update checking: the app asks GitHub for the latest release of
// s1mpdown/LMU-Setup-Builder and compares its tag with AppVersion.
// Releases are built by GitHub Actions from the repo source, so an
// update is: Jett pushes source + a VERSION bump, CI builds, the app
// pulls. No more manual exe downloads for Ari.

type releaseInfo struct {
	Tag      string
	AssetURL string
}

func httpClient() *http.Client { return &http.Client{Timeout: 20 * time.Second} }

func latestRelease() (releaseInfo, error) {
	var out releaseInfo
	req, err := http.NewRequest("GET", "https://api.github.com/repos/"+updateRepo+"/releases/latest", nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("User-Agent", "LMU-Setup-Builder/"+AppVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient().Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return out, fmt.Errorf("github answered %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return out, err
	}
	out.Tag = body.TagName
	for _, a := range body.Assets {
		if a.Name == updateAssetName {
			out.AssetURL = a.URL
		}
	}
	if out.Tag == "" || out.AssetURL == "" {
		return out, fmt.Errorf("release has no %s asset", updateAssetName)
	}
	return out, nil
}

func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := []int{0, 0, 0}
	for i, p := range strings.Split(v, ".") {
		if i > 2 {
			break
		}
		if n, err := strconv.Atoi(p); err == nil {
			parts[i] = n
		}
	}
	return parts
}

// versionNewer reports whether remote (e.g. "v1.3.0") is strictly newer
// than local (e.g. "1.2.0").
func versionNewer(remote, local string) bool {
	r, l := versionParts(remote), versionParts(local)
	for i := 0; i < 3; i++ {
		if r[i] != l[i] {
			return r[i] > l[i]
		}
	}
	return false
}
