package streamer

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

const armedCamerasCurrentVersion = 1

// Store persists the operator's "armed cameras" list as JSON on disk.
// All writes go through a temp file + rename so partial writes can never
// corrupt the live file.
type Store struct {
	path string
}

type armedFile struct {
	Version int           `json:"version"`
	Cameras []ArmedCamera `json:"cameras"`
}

// NewStore returns a Store backed by the given file path. The directory
// must already exist; bootstrap handles that on app startup.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Load returns the persisted list. A missing file, corrupt JSON, or
// version mismatch yields an empty list with no error — the operator
// just sees "no cameras armed" and can re-arm them.
func (s *Store) Load() ([]ArmedCamera, error) {
	data, err := os.ReadFile(s.path) //nolint:gosec // path is paths.ArmedCamerasPath()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		slog.Warn("streamer: read armed cameras failed", "path", s.path, "err", err)
		return nil, nil
	}
	var af armedFile
	if err := json.Unmarshal(data, &af); err != nil {
		slog.Warn("streamer: armed cameras corrupt; treating as empty", "path", s.path, "err", err)
		return nil, nil
	}
	if af.Version != armedCamerasCurrentVersion {
		slog.Warn("streamer: armed cameras version mismatch; treating as empty", "path", s.path, "version", af.Version)
		return nil, nil
	}
	return af.Cameras, nil
}

// Save atomically replaces the persisted list.
func (s *Store) Save(cams []ArmedCamera) error {
	af := armedFile{Version: armedCamerasCurrentVersion, Cameras: cams}
	data, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return fmt.Errorf("streamer: marshal armed cameras: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "armed-cameras.json.*.tmp")
	if err != nil {
		return fmt.Errorf("streamer: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("streamer: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("streamer: close temp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("streamer: chmod temp: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("streamer: rename temp: %w", err)
	}
	return nil
}
