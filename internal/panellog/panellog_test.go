package panellog_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/panellog"
)

func setupDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SERIALHOP_DATA_DIR", dir)
	t.Setenv("ProgramData", "")
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o750); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	return dir
}

func readPanelLog(t *testing.T, dir string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "logs", "SerialHop_panel.log"))
	if err != nil {
		t.Fatalf("read panel log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	out := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("unmarshal %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func TestInit_WritesSessionStartRecord(t *testing.T) {
	dir := setupDataDir(t)
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	m, err := panellog.Init("1.2.3", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	recs := readPanelLog(t, dir)
	if len(recs) < 2 {
		t.Fatalf("want >=2 records (start + end), got %d", len(recs))
	}
	if recs[0]["msg"] != "panel session start" {
		t.Errorf("first record msg = %v, want %q", recs[0]["msg"], "panel session start")
	}
	if recs[0]["version"] != "1.2.3" {
		t.Errorf("version = %v, want %q", recs[0]["version"], "1.2.3")
	}
	if _, ok := recs[0]["panel"]; !ok {
		t.Errorf("missing panel group attrs in start record: %v", recs[0])
	}
}

func TestInit_SessionIDStableAcrossCalls(t *testing.T) {
	setupDataDir(t)
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	m, err := panellog.Init("v", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	id1 := m.SessionID()
	id2 := m.SessionID()
	if id1 == "" || id1 != id2 {
		t.Errorf("session id not stable: %q vs %q", id1, id2)
	}
	_ = m.Shutdown(context.Background())
}

func TestSetLevel_AffectsDebugEmission(t *testing.T) {
	dir := setupDataDir(t)
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	m, err := panellog.Init("v", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	slog.Debug("debug-info-level") // should be filtered
	m.SetLevel(slog.LevelDebug)
	slog.Debug("debug-debug-level") // should appear
	_ = m.Shutdown(context.Background())

	b, _ := os.ReadFile(filepath.Join(dir, "logs", "SerialHop_panel.log"))
	body := string(b)
	if strings.Contains(body, "debug-info-level") {
		t.Errorf("debug record leaked at info level: %s", body)
	}
	if !strings.Contains(body, "debug-debug-level") {
		t.Errorf("debug record missing at debug level: %s", body)
	}
}

func TestInit_DeletesLegacyPanelErrorLog(t *testing.T) {
	dir := setupDataDir(t)
	legacy := filepath.Join(dir, "logs", "SerialHop_panel_error.log")
	if err := os.WriteFile(legacy, []byte("old breadcrumb\n"), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	m, err := panellog.Init("v", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy file not deleted: stat err=%v", err)
	}
	_ = m.Shutdown(context.Background())
}

func TestInit_MissingDataDir(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	_, err := panellog.Init("v", slog.LevelInfo)
	if err == nil {
		t.Fatal("Init: want error, got nil")
	}
}

func TestShutdown_IsIdempotent(t *testing.T) {
	setupDataDir(t)
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	m, err := panellog.Init("v", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

var _ = time.Second // anchor import; remove if unused
