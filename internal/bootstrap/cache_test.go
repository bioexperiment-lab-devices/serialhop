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

func TestWriteCache_AndReadCache_RoundTripActualRestPort(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	in := sampleCache()
	in.ActualRestPort = 49283
	if err := WriteCache(p, in); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	got, err := ReadCache(p, "alice")
	if err != nil {
		t.Fatalf("ReadCache: %v", err)
	}
	if got.ActualRestPort != 49283 {
		t.Errorf("ActualRestPort: got %d, want 49283", got.ActualRestPort)
	}
}

func TestWriteCache_ActualRestPortJSONKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	in := sampleCache()
	in.ActualRestPort = 49283
	if err := WriteCache(p, in); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	data, err := os.ReadFile(p) //nolint:gosec // p is t.TempDir() + literal filename
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), `"actual_rest_port": 49283`) {
		t.Errorf("missing actual_rest_port key; body:\n%s", data)
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
		`"host"`,
		`"user"`,
		`"pass"`,
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

func TestWriteCache_AndReadCache_RoundTripIdentity(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	in := sampleCache()
	in.Host = "lab-bridge.example.com"
	in.Pass = "s3cret"
	if err := WriteCache(p, in); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	got, err := ReadCache(p, "alice")
	if err != nil {
		t.Fatalf("ReadCache: %v", err)
	}
	if got.Host != "lab-bridge.example.com" {
		t.Errorf("Host: got %q, want %q", got.Host, "lab-bridge.example.com")
	}
	if got.User != "alice" {
		t.Errorf("User: got %q, want %q", got.User, "alice")
	}
	if got.Pass != "s3cret" {
		t.Errorf("Pass: got %q, want %q", got.Pass, "s3cret")
	}
}

func TestWriteCache_HostAndPassJSONKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	in := sampleCache()
	in.Host = "lab-bridge.example.com"
	in.Pass = "s3cret"
	if err := WriteCache(p, in); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	data, err := os.ReadFile(p) //nolint:gosec // p is t.TempDir() + literal filename
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	body := string(data)
	for _, want := range []string{`"host": "lab-bridge.example.com"`, `"pass": "s3cret"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing key/value %s; body:\n%s", want, body)
		}
	}
}

func TestReadCacheRaw_ReturnsCacheRegardlessOfUser(t *testing.T) {
	// Cache is anchored to "alice". Confirm by contrast that ReadCache
	// rejects a mismatched user — then confirm ReadCacheRaw still returns
	// the same cache. The contrast is the whole point of ReadCacheRaw.
	p := filepath.Join(t.TempDir(), "cache.json")
	in := sampleCache() // User: "alice"
	if err := WriteCache(p, in); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	if _, err := ReadCache(p, "bob"); !errors.Is(err, ErrCacheMissing) {
		t.Fatalf("ReadCache with mismatched user: want ErrCacheMissing, got %v", err)
	}
	got, err := ReadCacheRaw(p)
	if err != nil {
		t.Fatalf("ReadCacheRaw: %v", err)
	}
	if got.User != "alice" {
		t.Errorf("User: got %q, want %q", got.User, "alice")
	}
	if got.RemotePort != in.RemotePort {
		t.Errorf("RemotePort: got %d, want %d", got.RemotePort, in.RemotePort)
	}
}

func TestReadCacheRaw_MissingFileReturnsErrCacheMissing(t *testing.T) {
	_, err := ReadCacheRaw(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, ErrCacheMissing) {
		t.Errorf("expected ErrCacheMissing, got %v", err)
	}
}

func TestReadCacheRaw_VersionMismatchDeletesAndReturnsMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(p, []byte(`{"version":99,"user":"alice"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := ReadCacheRaw(p)
	if !errors.Is(err, ErrCacheMissing) {
		t.Errorf("expected ErrCacheMissing on version mismatch, got %v", err)
	}
	if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
		t.Errorf("expected version-mismatch cache file to be deleted; stat err = %v", statErr)
	}
}

func TestReadCacheRaw_CorruptJSONDeletesAndReturnsMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(p, []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := ReadCacheRaw(p)
	if !errors.Is(err, ErrCacheMissing) {
		t.Errorf("expected ErrCacheMissing on corrupt JSON, got %v", err)
	}
	if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
		t.Errorf("expected corrupt cache file to be deleted; stat err = %v", statErr)
	}
}

func TestWriteReadPanelEndpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel-endpoint.json")
	in := PanelEndpoint{
		Host:      "127.0.0.1",
		Port:      49217,
		PID:       12345,
		StartedAt: "2026-05-24T13:45:00Z",
	}
	if err := WritePanelEndpoint(path, in); err != nil {
		t.Fatalf("WritePanelEndpoint: %v", err)
	}
	out, err := ReadPanelEndpoint(path)
	if err != nil {
		t.Fatalf("ReadPanelEndpoint: %v", err)
	}
	if out.Port != 49217 || out.PID != 12345 || out.Host != "127.0.0.1" {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestReadPanelEndpoint_Missing(t *testing.T) {
	_, err := ReadPanelEndpoint(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, ErrPanelEndpointMissing) {
		t.Fatalf("want ErrPanelEndpointMissing, got %v", err)
	}
}

func TestReadPanelEndpoint_VersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel-endpoint.json")
	if err := os.WriteFile(path, []byte(`{"version": 99, "host":"127.0.0.1","port":1,"pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadPanelEndpoint(path)
	if !errors.Is(err, ErrPanelEndpointMissing) {
		t.Fatalf("want ErrPanelEndpointMissing for version mismatch, got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("version-mismatched file should have been deleted, stat err = %v", err)
	}
}

func TestReadPanelEndpoint_CorruptJSONDeletesAndReturnsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel-endpoint.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadPanelEndpoint(path)
	if !errors.Is(err, ErrPanelEndpointMissing) {
		t.Fatalf("want ErrPanelEndpointMissing, got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("corrupt file should have been deleted, stat err = %v", err)
	}
}

func TestDeletePanelEndpoint_RemovesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel-endpoint.json")
	if err := WritePanelEndpoint(path, PanelEndpoint{Host: "127.0.0.1", Port: 1, PID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := DeletePanelEndpoint(path); err != nil {
		t.Fatalf("DeletePanelEndpoint: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file should be gone, stat err = %v", err)
	}
}

func TestDeletePanelEndpoint_MissingFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")
	if err := DeletePanelEndpoint(path); err != nil {
		t.Fatalf("DeletePanelEndpoint on missing file: %v", err)
	}
}

func TestReadCache_LegacyV1FileHasEmptyHostAndPass(t *testing.T) {
	// Simulates a v1 cache written before this change: no host/pass keys.
	p := filepath.Join(t.TempDir(), "cache.json")
	legacy := `{
		"version": 1,
		"fetched_at": "2026-05-13T00:00:00Z",
		"user": "alice",
		"server_info": {"chisel_listen_port": 7000, "loki_push_url": "", "forward_tunnels": null},
		"remote_port": 8089,
		"actual_rest_port": 49283
	}`
	if err := os.WriteFile(p, []byte(legacy), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := ReadCache(p, "alice")
	if err != nil {
		t.Fatalf("ReadCache: %v", err)
	}
	if got.Host != "" {
		t.Errorf("Host: got %q, want empty", got.Host)
	}
	if got.Pass != "" {
		t.Errorf("Pass: got %q, want empty", got.Pass)
	}
	if got.ActualRestPort != 49283 {
		t.Errorf("ActualRestPort: got %d, want 49283", got.ActualRestPort)
	}
}
