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

// ErrCacheMissing is returned by ReadCache and ReadCacheRaw when the
// cache file is absent, unparseable, or version-mismatched. ReadCache
// also returns it when the cache is anchored to a different user;
// ReadCacheRaw ignores the user anchor. Callers should treat all of
// these the same: fall back to a live fetch.
var ErrCacheMissing = errors.New("bootstrap: cache missing")

// Cache is the on-disk schema for server-info.cache.json. The User
// field anchors the cache to a specific identity so that changing
// lab_bridge.user in the YAML invalidates stale data automatically.
// Host/User/Pass record the lab-bridge identity the running service is
// using; they are written by SeedCache at service start (before
// bootstrap.Resolve) so the panel's status-badge probes always probe
// the credentials the service is actually using, not whatever the YAML
// currently says.
type Cache struct {
	Version        int                  `json:"version"`
	FetchedAt      string               `json:"fetched_at"`
	Host           string               `json:"host"`
	User           string               `json:"user"`
	Pass           string               `json:"pass"`
	ServerInfo     labbridge.ServerInfo `json:"server_info"`
	RemotePort     int                  `json:"remote_port"`
	ActualRestPort int                  `json:"actual_rest_port"`
}

// WriteCache atomically writes c to path. Any existing file at path is
// replaced. Permissions are 0600.
//
// The cache.Pass field intentionally serializes to JSON: the panel reads
// this file to learn the running service's lab-bridge credentials so
// status-lamp probes reflect what the service is actually using, not
// whatever the YAML currently says. The file is written 0600 in the
// same DataDir that already holds the plaintext lab_bridge.pass in
// config.yaml — net new exposure is zero. See spec
// 2026-05-16-cached-creds-for-status-badges-design.
func WriteCache(path string, c Cache) error {
	return writeJSONAtomic(path, c) //nolint:gosec // see WriteCache godoc
}

// writeJSONAtomic writes JSON to path via temp-file + chmod 0o600 + rename.
// The temp file is created in the same directory so rename is atomic on
// the same filesystem. On any error the temp file is removed; the
// destination is never partially written.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("bootstrap: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
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

// readCacheFile is the shared body of ReadCache / ReadCacheRaw: the
// I/O, JSON parse, and version check. Errors collapse to ErrCacheMissing
// per the cache contract; corrupt and version-mismatched files are
// deleted as a side effect so the next successful WriteCache starts
// from a clean slate.
func readCacheFile(path string) (Cache, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is paths.ServerInfoCachePath() under DataDir
	if err != nil {
		if os.IsNotExist(err) {
			return Cache{}, ErrCacheMissing
		}
		slog.Warn("bootstrap: read cache failed", "path", path, "err", err)
		return Cache{}, ErrCacheMissing
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		slog.Warn("bootstrap: cache corrupt; deleting", "path", path, "err", err)
		_ = os.Remove(path)
		return Cache{}, ErrCacheMissing
	}
	if c.Version != cacheCurrentVersion {
		slog.Warn("bootstrap: cache version mismatch; deleting", "path", path, "version", c.Version)
		_ = os.Remove(path)
		return Cache{}, ErrCacheMissing
	}
	return c, nil
}

// ReadCache reads the cache file at path and returns it if it is valid
// and anchored to user. Any failure (missing file, parse error, version
// mismatch, user mismatch) returns ErrCacheMissing; corrupt or
// version-mismatched files are deleted as a side effect.
func ReadCache(path, user string) (Cache, error) {
	c, err := readCacheFile(path)
	if err != nil {
		return Cache{}, err
	}
	if c.User != user {
		slog.Info("bootstrap: cache user mismatch; ignoring", "cache_user", c.User, "cfg_user", user)
		return Cache{}, ErrCacheMissing
	}
	return c, nil
}

// ReadCacheRaw reads the cache file at path without checking the user
// anchor. Same error contract and side effects as ReadCache (missing /
// corrupt / version-mismatched files return ErrCacheMissing; corrupt
// and version-mismatched files are also deleted). Used by panel code
// that wants whatever the running service wrote, regardless of whether
// the YAML's lab_bridge.user currently matches.
func ReadCacheRaw(path string) (Cache, error) {
	return readCacheFile(path)
}

const panelEndpointCurrentVersion = 1

// ErrPanelEndpointMissing is returned by ReadPanelEndpoint when the
// endpoint file is absent, unparseable, or version-mismatched. Callers
// should treat all of these the same: the panel is not running, or
// has not yet announced its listener address.
var ErrPanelEndpointMissing = errors.New("bootstrap: panel endpoint missing")

// PanelEndpoint is the on-disk schema for panel-endpoint.json. The
// panel writes this file once its localhost streaming listener is
// bound, so the service can proxy /v1/cam/* requests to it. The PID
// lets the service detect orphaned endpoint files left over from a
// crashed panel.
type PanelEndpoint struct {
	Version   int    `json:"version"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
}

// WritePanelEndpoint atomically writes e to path. Any existing file at
// path is replaced. Permissions are 0600. The Version field is set
// automatically.
func WritePanelEndpoint(path string, e PanelEndpoint) error {
	e.Version = panelEndpointCurrentVersion
	return writeJSONAtomic(path, e)
}

// ReadPanelEndpoint reads the panel-endpoint file at path. Any failure
// (missing file, parse error, version mismatch) returns
// ErrPanelEndpointMissing; corrupt and version-mismatched files are
// deleted as a side effect.
func ReadPanelEndpoint(path string) (PanelEndpoint, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is paths.PanelEndpointPath()
	if err != nil {
		if os.IsNotExist(err) {
			return PanelEndpoint{}, ErrPanelEndpointMissing
		}
		slog.Warn("bootstrap: read panel endpoint failed", "path", path, "err", err)
		return PanelEndpoint{}, ErrPanelEndpointMissing
	}
	var e PanelEndpoint
	if err := json.Unmarshal(data, &e); err != nil {
		slog.Warn("bootstrap: panel endpoint corrupt; deleting", "path", path, "err", err)
		_ = os.Remove(path)
		return PanelEndpoint{}, ErrPanelEndpointMissing
	}
	if e.Version != panelEndpointCurrentVersion {
		slog.Warn("bootstrap: panel endpoint version mismatch; deleting", "path", path, "version", e.Version)
		_ = os.Remove(path)
		return PanelEndpoint{}, ErrPanelEndpointMissing
	}
	return e, nil
}

// DeletePanelEndpoint removes the panel-endpoint file at path. A
// missing file is not an error — the goal state (file absent) is
// already achieved.
func DeletePanelEndpoint(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("bootstrap: delete panel endpoint: %w", err)
	}
	return nil
}
