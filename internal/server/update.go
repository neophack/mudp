package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"mudp/internal/upgrader"
	"mudp/internal/version"
)

// githubReleasesURL points at the project's latest-release API. It is a var
// (not a const) so tests can redirect it at a stub server.
var githubReleasesURL = "https://api.github.com/repos/neophack/mudp/releases/latest"

const (
	updateCheckTimeout = 5 * time.Second
	// Successes are cached for an hour; failures only briefly, so a transient
	// GitHub hiccup doesn't pin a stale error for the full hour.
	updateCacheTTL      = time.Hour
	updateErrCacheTTL   = 2 * time.Minute
	updateMaxBodyBytes  = 1 << 20
	updateAssetBaseURL  = "https://github.com/neophack/mudp/releases/download"
)

type updateCheckResponse struct {
	Current    string            `json:"current"`
	Latest     string            `json:"latest,omitempty"`
	Available  bool              `json:"available"`
	CheckedAt  string            `json:"checkedAt,omitempty"`
	// Notes / ReleasedAt carry the release body and publish time so the update
	// dialog can show what changed (the "what's new" list) without the browser
	// calling GitHub itself.
	Notes      string            `json:"notes,omitempty"`
	ReleasedAt string            `json:"releasedAt,omitempty"`
	Downloads  map[string]string `json:"downloads,omitempty"`
	// AssetURL is the release asset matching THIS server's OS/arch — the
	// binary /api/admin/upgrade would download and swap in.
	AssetURL string `json:"assetUrl,omitempty"`
	Error    string `json:"error,omitempty"`
}

// updateCheck reports the running version and, by consulting the GitHub
// latest-release API (cached), whether a newer release exists and where its
// per-OS assets can be downloaded. `?refresh=1` skips the cache read (the
// manual "check now" button) while still refreshing the cached entry.
func (a *App) updateCheck(w http.ResponseWriter, r *http.Request) {
	a.updateMu.Lock()
	defer a.updateMu.Unlock()
	refresh := r.URL.Query().Get("refresh") == "1"
	if !refresh && a.updateCache != nil {
		ttl := updateCacheTTL
		if a.updateCache.Error != "" {
			ttl = updateErrCacheTTL
		}
		if time.Since(a.updateAt) < ttl {
			writeJSON(w, http.StatusOK, a.updateCache)
			return
		}
	}

	res := a.fetchLatestRelease(r.Context())
	res.Current = version.Version
	if res.Latest != "" {
		// Untagged dev builds stay quiet: every tag looks like an update.
		res.Available = version.Version != "dev" && version.Compare(version.Version, res.Latest) < 0
		res.Downloads = map[string]string{
			"windows":       updateAssetURL(res.Latest, upgrader.ArchiveName(res.Latest, "windows", "amd64")),
			"linux":         updateAssetURL(res.Latest, upgrader.ArchiveName(res.Latest, "linux", "amd64")),
			"windows-arm64": updateAssetURL(res.Latest, upgrader.ArchiveName(res.Latest, "windows", "arm64")),
			"linux-arm64":   updateAssetURL(res.Latest, upgrader.ArchiveName(res.Latest, "linux", "arm64")),
		}
		if url, err := upgrader.AssetURL(res.Latest, runtime.GOOS, runtime.GOARCH); err == nil {
			res.AssetURL = url
		}
	}
	a.updateCache = &res
	a.updateAt = time.Now()
	writeJSON(w, http.StatusOK, &res)
}

func (a *App) fetchLatestRelease(ctx context.Context) updateCheckResponse {
	res := updateCheckResponse{CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubReleasesURL, nil)
	if err != nil {
		res.Error = "invalid update-check request"
		return res
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "mudp/"+version.Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		res.Error = "GitHub API unreachable"
		return res
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("GitHub API returned %s", resp.Status)
		return res
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, updateMaxBodyBytes))
	if err != nil {
		res.Error = "failed reading GitHub API response"
		return res
	}
	var release struct {
		TagName    string `json:"tag_name"`
		Body       string `json:"body"`
		PublishedAt string `json:"published_at"`
	}
	if err := json.Unmarshal(body, &release); err != nil || release.TagName == "" {
		res.Error = "no published release found"
		return res
	}
	res.Latest = release.TagName
	res.Notes = strings.TrimSpace(release.Body)
	res.ReleasedAt = release.PublishedAt
	return res
}

func updateAssetURL(tag, name string) string {
	return fmt.Sprintf("%s/%s/%s", updateAssetBaseURL, tag, name)
}
