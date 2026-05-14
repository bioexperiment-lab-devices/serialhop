package app

import (
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
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

// TestWriteActualRestPort_UserMismatchStillWrites ensures the cache's
// user-anchor does NOT block the port write. Previously this no-op'd
// when cache.User != cfg.User (e.g. the operator edited
// lab_bridge.user but hadn't restarted the service yet), leaving the
// cache with ActualRestPort=0 — and the panel's Devices/Ports tabs
// permanently rendering "Can't reach the local service."
func TestWriteActualRestPort_UserMismatchStillWrites(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	seed := bootstrap.Cache{
		Version:        1,
		FetchedAt:      "2026-05-13T00:00:00Z",
		User:           "alice", // cache was written by an older alice-anchored service
		ServerInfo:     labbridge.ServerInfo{ChiselListenPort: 7000},
		RemotePort:     8089,
		ActualRestPort: 0,
	}
	if err := bootstrap.WriteCache(cachePath, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// cfg has been rewritten to a different user (or hasn't been read yet).
	if err := writeActualRestPort(cachePath, "bob", 52111); err != nil {
		t.Fatalf("writeActualRestPort: %v", err)
	}
	got, err := bootstrap.ReadCacheUnchecked(cachePath)
	if err != nil {
		t.Fatalf("ReadCacheUnchecked: %v", err)
	}
	if got.ActualRestPort != 52111 {
		t.Errorf("ActualRestPort: got %d, want 52111 (user-anchor must NOT block the write)", got.ActualRestPort)
	}
	if got.User != "alice" {
		t.Errorf("User clobbered: got %q, want %q (the write should preserve the existing anchor)", got.User, "alice")
	}
}
