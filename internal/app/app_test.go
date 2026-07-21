package app

import (
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

func TestWriteActualRestPort_UpdatesCacheAtomically(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	seed := bootstrap.Cache{
		Version:    1,
		FetchedAt:  "2026-05-13T00:00:00Z",
		User:       "alice",
		ServerInfo: labbridge.ServerInfo{ChiselListenPort: 7000},
		RemotePort: 8089,
	}
	if err := bootstrap.WriteCache(cachePath, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := writeActualRestPort(cachePath, "alice", 49283); err != nil {
		t.Fatalf("writeActualRestPort: %v", err)
	}
	got, err := bootstrap.ReadCache(cachePath, "alice")
	if err != nil {
		t.Fatalf("ReadCache: %v", err)
	}
	if got.ActualRestPort != 49283 {
		t.Errorf("ActualRestPort: got %d, want 49283", got.ActualRestPort)
	}
	if got.RemotePort != 8089 {
		t.Errorf("RemotePort clobbered: got %d, want 8089", got.RemotePort)
	}
}

func TestWriteActualRestPort_NoCacheIsNotAnError(t *testing.T) {
	// If the cache doesn't exist yet (first launch racing chisel bootstrap)
	// we silently no-op. The next bootstrap.Resolve will rewrite the cache.
	if err := writeActualRestPort(filepath.Join(t.TempDir(), "nope.json"), "alice", 49283); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestBuildRemoteUpdateManager_DisabledIsNil(t *testing.T) {
	if m := buildRemoteUpdateManager(config.Config{}); m != nil {
		t.Error("disabled config should yield a nil manager")
	}
}

func TestBuildRemoteUpdateManager_EnabledNonNil(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", t.TempDir())
	cfg := config.Config{RemoteUpdate: config.RemoteUpdateConfig{Enabled: true}}
	m := buildRemoteUpdateManager(cfg)
	if m == nil || !m.Enabled() {
		t.Error("enabled config should yield an enabled manager")
	}
}
