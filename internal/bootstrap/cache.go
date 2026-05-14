// Package bootstrap resolves the agent's per-startup parameters
// (chisel listen port, Loki push URL, forward tunnels, reverse-tunnel
// port) by calling the lab-bridge HTTPS API at startup. Results are
// cached on disk in a user-anchored JSON file so the service can come
// back up after a restart even if the server is briefly unreachable.
package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

const cacheCurrentVersion = 1

// ErrCacheMissing is returned by ReadCache when the cache file is
// absent, unparseable, version-mismatched, or anchored to a different
// user. Callers should treat all of these the same: fall back to a
// live fetch.
var ErrCacheMissing = errors.New("bootstrap: cache missing")

// Cache is the on-disk schema for server-info.cache.json. The User
// field anchors the cache to a specific identity so that changing
// lab_bridge.user in the YAML invalidates stale data automatically.
type Cache struct {
	Version        int                  `json:"version"`
	FetchedAt      string               `json:"fetched_at"`
	User           string               `json:"user"`
	ServerInfo     labbridge.ServerInfo `json:"server_info"`
	RemotePort     int                  `json:"remote_port"`
	ActualRestPort int                  `json:"actual_rest_port"`
}

// WriteCache atomically writes c to path. Any existing file at path is
// replaced. Permissions are 0600.
func WriteCache(path string, c Cache) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("bootstrap: marshal cache: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "server-info.cache.json.*.tmp")
	if err != nil {
		return fmt.Errorf("bootstrap: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("bootstrap: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("bootstrap: close temp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("bootstrap: chmod temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("bootstrap: rename temp: %w", err)
	}
	return nil
}

// ReadCache reads the cache file at path and returns it if it is valid
// and anchored to user. Any failure (missing file, parse error, version
// mismatch, user mismatch) returns ErrCacheMissing; corrupt or
// version-mismatched files are deleted as a side effect so the next
// successful WriteCache starts from a clean slate.
func ReadCache(path, user string) (Cache, error) {
	c, err := ReadCacheUnchecked(path)
	if err != nil {
		// Normalize all underlying failure modes to ErrCacheMissing so
		// existing callers that check `err == ErrCacheMissing` continue
		// to work.
		return Cache{}, ErrCacheMissing
	}
	if c.User != user {
		slog.Info("bootstrap: cache user mismatch; ignoring", "cache_user", c.User, "cfg_user", user)
		return Cache{}, ErrCacheMissing
	}
	return c, nil
}

// ReadCacheUnchecked is ReadCache without the user-anchor check, and
// without collapsing every error into ErrCacheMissing — it returns the
// concrete reason a read failed so diagnostic logging can surface the
// actual root cause. Use it when only the local state in the cache
// matters (ActualRestPort, ServerInfo) and not the identity of whoever
// wrote it. Corrupt or version-mismatched files are still deleted.
func ReadCacheUnchecked(path string) (Cache, error) {
	if path == "" {
		return Cache{}, fmt.Errorf("bootstrap: cache path empty (DataDir unavailable?)")
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is paths.ServerInfoCachePath() under DataDir
	if err != nil {
		if os.IsNotExist(err) {
			return Cache{}, ErrCacheMissing
		}
		slog.Warn("bootstrap: read cache failed", "path", path, "err", err)
		return Cache{}, fmt.Errorf("bootstrap: read cache %s: %w", path, err)
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		slog.Warn("bootstrap: cache corrupt; deleting", "path", path, "err", err)
		_ = os.Remove(path)
		return Cache{}, fmt.Errorf("bootstrap: parse cache %s: %w", path, err)
	}
	if c.Version != cacheCurrentVersion {
		slog.Warn("bootstrap: cache version mismatch; deleting", "path", path, "version", c.Version)
		_ = os.Remove(path)
		return Cache{}, fmt.Errorf("bootstrap: cache version mismatch (got %d, want %d)", c.Version, cacheCurrentVersion)
	}
	return c, nil
}
