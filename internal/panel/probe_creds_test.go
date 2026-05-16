package panel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

func writeYAML(t *testing.T, path, host, user, pass string) {
	t.Helper()
	body := "lab_bridge:\n" +
		"  host: " + host + "\n" +
		"  user: " + user + "\n" +
		"  pass: " + pass + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
}

func TestProbeCreds_NotInstalled_UsesYAML(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	cfgPath := filepath.Join(dir, "config.yaml")
	writeYAML(t, cfgPath, "yaml.example", "yaml-user", "yaml-pw")
	if err := bootstrap.WriteCache(cachePath, bootstrap.Cache{
		Version: 1, FetchedAt: "2026-05-13T00:00:00Z",
		Host: "cache.example", User: "cache-user", Pass: "cache-pw",
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	ls := &lampState{}
	ls.setService(serviceLamp{state: winsvc.StateNotInstalled})
	host, user, pass := ls.probeCreds(cachePath, cfgPath)
	if host != "yaml.example" || user != "yaml-user" || pass != "yaml-pw" {
		t.Errorf("got (%q,%q,%q), want YAML triple", host, user, pass)
	}
}

func TestProbeCreds_Running_UsesCacheWhenHostPresent(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	cfgPath := filepath.Join(dir, "config.yaml")
	writeYAML(t, cfgPath, "yaml.example", "yaml-user", "yaml-pw")
	if err := bootstrap.WriteCache(cachePath, bootstrap.Cache{
		Version: 1, FetchedAt: "2026-05-13T00:00:00Z",
		Host: "cache.example", User: "cache-user", Pass: "cache-pw",
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	ls := &lampState{}
	ls.setService(serviceLamp{state: winsvc.StateRunning})
	host, user, pass := ls.probeCreds(cachePath, cfgPath)
	if host != "cache.example" || user != "cache-user" || pass != "cache-pw" {
		t.Errorf("got (%q,%q,%q), want cache triple", host, user, pass)
	}
}

func TestProbeCreds_Running_LegacyCacheFallsBackToYAML(t *testing.T) {
	// Simulates a v1 cache file written before this fix: Host/Pass empty.
	// The service hasn't yet been restarted post-upgrade, so probeCreds
	// falls back to YAML one time to avoid lamps appearing broken.
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	cfgPath := filepath.Join(dir, "config.yaml")
	writeYAML(t, cfgPath, "yaml.example", "yaml-user", "yaml-pw")
	if err := bootstrap.WriteCache(cachePath, bootstrap.Cache{
		Version: 1, FetchedAt: "2026-05-13T00:00:00Z",
		User: "cache-user", // Host/Pass intentionally empty
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	ls := &lampState{}
	ls.setService(serviceLamp{state: winsvc.StateRunning})
	host, user, pass := ls.probeCreds(cachePath, cfgPath)
	if host != "yaml.example" || user != "yaml-user" || pass != "yaml-pw" {
		t.Errorf("got (%q,%q,%q), want YAML triple (legacy fallback)", host, user, pass)
	}
}

func TestProbeCreds_Running_CacheMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "missing.json")
	cfgPath := filepath.Join(dir, "config.yaml")
	writeYAML(t, cfgPath, "yaml.example", "yaml-user", "yaml-pw")
	ls := &lampState{}
	ls.setService(serviceLamp{state: winsvc.StateRunning})
	host, user, pass := ls.probeCreds(cachePath, cfgPath)
	if host != "" || user != "" || pass != "" {
		t.Errorf("got (%q,%q,%q), want empty triple (cache missing while installed)", host, user, pass)
	}
}

func TestProbeCreds_Stopped_StillUsesCache(t *testing.T) {
	// Stopped is "installed but not running" — we don't fall back to YAML
	// in that state; we want lamps to reflect what the (now-stopped)
	// service was using.
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	cfgPath := filepath.Join(dir, "config.yaml")
	writeYAML(t, cfgPath, "yaml.example", "yaml-user", "yaml-pw")
	if err := bootstrap.WriteCache(cachePath, bootstrap.Cache{
		Version: 1, FetchedAt: "2026-05-13T00:00:00Z",
		Host: "cache.example", User: "cache-user", Pass: "cache-pw",
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	ls := &lampState{}
	ls.setService(serviceLamp{state: winsvc.StateStopped})
	host, user, pass := ls.probeCreds(cachePath, cfgPath)
	if host != "cache.example" || user != "cache-user" || pass != "cache-pw" {
		t.Errorf("got (%q,%q,%q), want cache triple (stopped state)", host, user, pass)
	}
}
