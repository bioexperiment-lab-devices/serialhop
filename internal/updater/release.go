package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// DefaultReleasesURL is the GitHub API endpoint for the project's latest release.
const DefaultReleasesURL = "https://api.github.com/repos/bioexperiment-lab-devices/serialhop/releases/latest"

// ReleasesByTagURL is the GitHub API endpoint for a specific release tag,
// e.g. ReleasesByTagURL("v2.3.0"). Pass the result to LatestRelease, which
// decodes the same Release shape for a tag as for /releases/latest.
func ReleasesByTagURL(tag string) string {
	return "https://api.github.com/repos/bioexperiment-lab-devices/serialhop/releases/tags/" + tag
}

// Release is the subset of the GitHub Releases API payload we care about.
type Release struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

// Asset is one binary attached to a GitHub release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// AssetByName returns the first asset with the given filename, or nil if absent.
func (r Release) AssetByName(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// LatestRelease GETs `url` (typically DefaultReleasesURL) and returns the
// decoded release. The caller owns the timeout via ctx.
func LatestRelease(ctx context.Context, hc *http.Client, url, userAgent string) (Release, error) {
	slog.Info("updater release fetch start", "url", url)

	req, err := newRequest(ctx, url, userAgent)
	if err != nil {
		return Release{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		slog.Error("updater release fetch failed", "url", url, "err", err.Error())
		return Release{}, fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		httpErr := fmt.Errorf("get %s: HTTP %d: %s", url, resp.StatusCode, string(body))
		slog.Error("updater release fetch failed", "url", url, "err", httpErr.Error())
		return Release{}, httpErr
	}
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return Release{}, fmt.Errorf("decode %s: %w", url, err)
	}
	slog.Info("updater release fetch ok", "url", url, "tag", rel.TagName)
	return rel, nil
}

func newRequest(ctx context.Context, url, userAgent string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	return req, nil
}
