package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

func sampleCache() Cache {
	return Cache{
		Version:   cacheCurrentVersion,
		FetchedAt: "2026-05-11T14:32:01Z",
		User:      "alice",
		ServerInfo: labbridge.ServerInfo{
			ChiselListenPort: 7000,
			LokiPushURL:      "http://127.0.0.1:3100/loki/api/v1/push",
			ForwardTunnels: []labbridge.ForwardTunnel{
				{Name: "loki", Local: "127.0.0.1:3100", Remote: "loki:3100"},
			},
		},
		RemotePort: 8089,
	}
}

func TestWriteCache_AndReadCache_RoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	in := sampleCache()
	if err := WriteCache(p, in); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	got, err := ReadCache(p, "alice")
	if err != nil {
		t.Fatalf("ReadCache: %v", err)
	}
	if got.RemotePort != in.RemotePort || got.ServerInfo.ChiselListenPort != in.ServerInfo.ChiselListenPort {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, in)
	}
}

func TestReadCache_MissingFileReturnsErrCacheMissing(t *testing.T) {
	_, err := ReadCache(filepath.Join(t.TempDir(), "nope.json"), "alice")
	if !errors.Is(err, ErrCacheMissing) {
		t.Errorf("expected ErrCacheMissing, got %v", err)
	}
}

func TestReadCache_UserMismatchTreatedAsMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := WriteCache(p, sampleCache()); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	_, err := ReadCache(p, "bob")
	if !errors.Is(err, ErrCacheMissing) {
		t.Errorf("expected ErrCacheMissing on user mismatch, got %v", err)
	}
}

func TestReadCache_CorruptJSONDeletesAndReturnsMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(p, []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := ReadCache(p, "alice")
	if !errors.Is(err, ErrCacheMissing) {
		t.Errorf("expected ErrCacheMissing on corrupt JSON, got %v", err)
	}
	if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
		t.Errorf("expected corrupt cache file to be deleted; stat err = %v", statErr)
	}
}

func TestReadCache_VersionMismatchDeletesAndReturnsMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(p, []byte(`{"version":99,"user":"alice"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := ReadCache(p, "alice")
	if !errors.Is(err, ErrCacheMissing) {
		t.Errorf("expected ErrCacheMissing on version mismatch, got %v", err)
	}
	if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
		t.Errorf("expected version-mismatch cache file to be deleted; stat err = %v", statErr)
	}
}

func TestWriteCache_IsAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cache.json")
	if err := WriteCache(p, sampleCache()); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "cache.json" {
			t.Errorf("unexpected leftover file: %s", e.Name())
		}
	}
}

func TestWriteCache_JSONKeysAreSnakeCase(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := WriteCache(p, sampleCache()); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	data, err := os.ReadFile(p) //nolint:gosec // p is t.TempDir() + literal filename
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		`"chisel_listen_port"`,
		`"loki_push_url"`,
		`"forward_tunnels"`,
		`"server_info"`,
		`"remote_port"`,
		`"fetched_at"`,
		`"user"`,
		`"version"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("cache JSON missing key %s; body:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{
		`"ChiselListenPort"`,
		`"LokiPushURL"`,
		`"ForwardTunnels"`,
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("cache JSON contains Go-CamelCase key %s; body:\n%s", unwanted, body)
		}
	}
}
