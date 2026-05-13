//go:build windows

package panel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/updater"
)

// updateCtl holds the state machine and current release info for the update
// flow. All fields are guarded by mu except where noted.
type updateCtl struct {
	mu       sync.Mutex
	state    UpdateState
	release  updater.Release
	exeAsset *updater.Asset
	exeFile  string // full path to the staged .exe (when ready)
	dlCancel context.CancelFunc
}

// fetchSums GETs the SHA256SUMS.txt body for an asset URL, with a 10s
// per-call timeout. The body is hard-capped at 1 MB so we don't fall
// over on a misconfigured server.
func fetchSums(hc *http.Client, userAgent, url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("fetch sums: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// cleanupStaleStagedFiles deletes any SerialHop-v*.exe inside installDir
// that doesn't match the current latest-release asset name. Best-effort.
func cleanupStaleStagedFiles(installDir, keep string) {
	matches, _ := filepath.Glob(filepath.Join(installDir, "SerialHop-v*.exe"))
	for _, m := range matches {
		if filepath.Base(m) == keep {
			continue
		}
		_ = os.Remove(m)
	}
}
