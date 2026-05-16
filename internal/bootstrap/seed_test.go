package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

func TestSeedCache_MissingFileCreatesFreshCache(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := SeedCache(p, "host.example", "alice", "pw"); err != nil {
		t.Fatalf("SeedCache: %v", err)
	}
	got, err := ReadCacheRaw(p)
	if err != nil {
		t.Fatalf("ReadCacheRaw: %v", err)
	}
	if got.Version != cacheCurrentVersion {
		t.Errorf("Version: got %d, want %d", got.Version, cacheCurrentVersion)
	}
	if got.Host != "host.example" || got.User != "alice" || got.Pass != "pw" {
		t.Errorf("identity triple: got (%q,%q,%q), want (host.example,alice,pw)", got.Host, got.User, got.Pass)
	}
	if got.FetchedAt == "" {
		t.Errorf("FetchedAt should be set")
	}
}

func TestSeedCache_PreservesExistingNonIdentityFields(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	prior := Cache{
		Version:        cacheCurrentVersion,
		FetchedAt:      "2026-05-01T00:00:00Z",
		Host:           "old.example",
		User:           "old-user",
		Pass:           "old-pw",
		ServerInfo:     labbridge.ServerInfo{ChiselListenPort: 7000, LokiPushURL: "http://loki:3100"},
		RemotePort:     8089,
		ActualRestPort: 49283,
	}
	if err := WriteCache(p, prior); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	if err := SeedCache(p, "new.example", "new-user", "new-pw"); err != nil {
		t.Fatalf("SeedCache: %v", err)
	}
	got, err := ReadCacheRaw(p)
	if err != nil {
		t.Fatalf("ReadCacheRaw: %v", err)
	}
	if got.Host != "new.example" || got.User != "new-user" || got.Pass != "new-pw" {
		t.Errorf("identity not overwritten: got (%q,%q,%q)", got.Host, got.User, got.Pass)
	}
	if got.ServerInfo.ChiselListenPort != 7000 {
		t.Errorf("ServerInfo.ChiselListenPort clobbered: got %d", got.ServerInfo.ChiselListenPort)
	}
	if got.RemotePort != 8089 {
		t.Errorf("RemotePort clobbered: got %d", got.RemotePort)
	}
	if got.ActualRestPort != 49283 {
		t.Errorf("ActualRestPort clobbered: got %d", got.ActualRestPort)
	}
}

func TestSeedCache_CorruptCacheGetsReplaced(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(p, []byte("garbage"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := SeedCache(p, "h", "u", "p"); err != nil {
		t.Fatalf("SeedCache: %v", err)
	}
	got, err := ReadCacheRaw(p)
	if err != nil {
		t.Fatalf("ReadCacheRaw: %v", err)
	}
	if got.Host != "h" || got.User != "u" || got.Pass != "p" {
		t.Errorf("identity: got (%q,%q,%q)", got.Host, got.User, got.Pass)
	}
}

func TestSeedCache_IsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := SeedCache(p, "h", "u", "pw"); err != nil {
		t.Fatalf("SeedCache #1: %v", err)
	}
	if err := SeedCache(p, "h", "u", "pw"); err != nil {
		t.Fatalf("SeedCache #2: %v", err)
	}
	got, err := ReadCacheRaw(p)
	if err != nil {
		t.Fatalf("ReadCacheRaw: %v", err)
	}
	if got.Host != "h" || got.User != "u" || got.Pass != "pw" {
		t.Errorf("identity: got (%q,%q,%q)", got.Host, got.User, got.Pass)
	}
}
