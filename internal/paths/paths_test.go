package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDataDirUsesOverrideWhenSet(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "/custom/root")
	t.Setenv("ProgramData", "/should/be/ignored")
	if got := DataDir(); got != "/custom/root" {
		t.Errorf("DataDir() = %q, want /custom/root", got)
	}
}

func TestDataDirFallsBackToProgramData(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", `C:\ProgramData`)
	want := filepath.Join(`C:\ProgramData`, "SerialHop")
	if got := DataDir(); got != want {
		t.Errorf("DataDir() = %q, want %q", got, want)
	}
}

func TestDataDirReturnsEmptyWhenNeitherSet(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	if got := DataDir(); got != "" {
		t.Errorf("DataDir() = %q, want empty", got)
	}
}

func TestComposedPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SERIALHOP_DATA_DIR", root)

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"ConfigPath", ConfigPath(), filepath.Join(root, "SerialHop_config.yaml")},
		{"LogsDir", LogsDir(), filepath.Join(root, "logs")},
		{"ServiceLogPath", ServiceLogPath(), filepath.Join(root, "logs", "SerialHop.log")},
		{"StderrLogPath", StderrLogPath(), filepath.Join(root, "logs", "SerialHop_stderr.log")},
		{"PanelErrorLogPath", PanelErrorLogPath(), filepath.Join(root, "logs", "SerialHop_panel_error.log")},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestComposedPathsAreEmptyWhenDataDirIsEmpty(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	if got := ConfigPath(); got != "" {
		t.Errorf("ConfigPath() = %q, want empty", got)
	}
	if got := LogsDir(); got != "" {
		t.Errorf("LogsDir() = %q, want empty", got)
	}
	if got := ServiceLogPath(); got != "" {
		t.Errorf("ServiceLogPath() = %q, want empty", got)
	}
	if got := StderrLogPath(); got != "" {
		t.Errorf("StderrLogPath() = %q, want empty", got)
	}
	if got := PanelErrorLogPath(); got != "" {
		t.Errorf("PanelErrorLogPath() = %q, want empty", got)
	}
}

func TestEnsureDirsCreatesBothLevels(t *testing.T) {
	root := filepath.Join(t.TempDir(), "SerialHop")
	t.Setenv("SERIALHOP_DATA_DIR", root)

	if err := EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	for _, p := range []string{root, filepath.Join(root, "logs"), filepath.Join(root, "state")} {
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("stat %q: %v", p, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", p)
		}
	}
}

func TestEnsureDirsIsIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "SerialHop")
	t.Setenv("SERIALHOP_DATA_DIR", root)

	if err := EnsureDirs(); err != nil {
		t.Fatalf("first EnsureDirs: %v", err)
	}
	if err := EnsureDirs(); err != nil {
		t.Fatalf("second EnsureDirs: %v", err)
	}
}

func TestEnsureDirsErrorsWhenDataDirEmpty(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	if err := EnsureDirs(); err == nil {
		t.Fatal("EnsureDirs returned nil, want error")
	}
}

func TestServerInfoCachePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SERIALHOP_DATA_DIR", dir)
	want := filepath.Join(dir, "server-info.cache.json")
	if got := ServerInfoCachePath(); got != want {
		t.Errorf("ServerInfoCachePath: got %q, want %q", got, want)
	}
}

func TestServerInfoCachePath_EmptyWhenDataDirUnavailable(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	if got := ServerInfoCachePath(); got != "" {
		t.Errorf("ServerInfoCachePath: got %q, want empty", got)
	}
}

func TestBackupsDir_UnderDataDir(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "/tmp/sh")
	got := BackupsDir()
	want := filepath.Join("/tmp/sh", "backups")
	if got != want {
		t.Errorf("BackupsDir: got %q, want %q", got, want)
	}
}

func TestBackupsDir_EmptyWhenNoDataDir(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	got := BackupsDir()
	if got != "" {
		t.Errorf("BackupsDir: got %q, want \"\"", got)
	}
}

func TestLocalDataDirUsesOverrideWhenSet(t *testing.T) {
	t.Setenv("SERIALHOP_LOCAL_DATA_DIR", "/custom/local")
	t.Setenv("LOCALAPPDATA", "/should/be/ignored")
	if got := LocalDataDir(); got != "/custom/local" {
		t.Errorf("LocalDataDir() = %q, want /custom/local", got)
	}
}

func TestLocalDataDirFallsBackToLocalAppData(t *testing.T) {
	t.Setenv("SERIALHOP_LOCAL_DATA_DIR", "")
	t.Setenv("LOCALAPPDATA", `C:\Users\bob\AppData\Local`)
	want := filepath.Join(`C:\Users\bob\AppData\Local`, "SerialHop")
	if got := LocalDataDir(); got != want {
		t.Errorf("LocalDataDir() = %q, want %q", got, want)
	}
}

func TestLocalDataDirReturnsEmptyWhenNeitherSet(t *testing.T) {
	t.Setenv("SERIALHOP_LOCAL_DATA_DIR", "")
	t.Setenv("LOCALAPPDATA", "")
	if got := LocalDataDir(); got != "" {
		t.Errorf("LocalDataDir() = %q, want empty", got)
	}
}

func TestPanelUpdateStagingDir_UnderLocalDataDir(t *testing.T) {
	t.Setenv("SERIALHOP_LOCAL_DATA_DIR", "/tmp/sh-local")
	got := PanelUpdateStagingDir()
	want := filepath.Join("/tmp/sh-local", "updates")
	if got != want {
		t.Errorf("PanelUpdateStagingDir: got %q, want %q", got, want)
	}
}

func TestPanelUpdateStagingDir_EmptyWhenNoLocalDataDir(t *testing.T) {
	t.Setenv("SERIALHOP_LOCAL_DATA_DIR", "")
	t.Setenv("LOCALAPPDATA", "")
	if got := PanelUpdateStagingDir(); got != "" {
		t.Errorf("PanelUpdateStagingDir: got %q, want empty", got)
	}
}

func TestEnsurePanelUpdateStagingDir_Creates(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Local")
	t.Setenv("SERIALHOP_LOCAL_DATA_DIR", filepath.Join(root, "SerialHop"))

	got, err := EnsurePanelUpdateStagingDir()
	if err != nil {
		t.Fatalf("EnsurePanelUpdateStagingDir: %v", err)
	}
	want := filepath.Join(root, "SerialHop", "updates")
	if got != want {
		t.Errorf("path: got %q, want %q", got, want)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat %q: %v", got, err)
	}
	if !info.IsDir() {
		t.Errorf("%q is not a directory", got)
	}
}

func TestEnsurePanelUpdateStagingDir_Idempotent(t *testing.T) {
	t.Setenv("SERIALHOP_LOCAL_DATA_DIR", filepath.Join(t.TempDir(), "SerialHop"))
	if _, err := EnsurePanelUpdateStagingDir(); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := EnsurePanelUpdateStagingDir(); err != nil {
		t.Fatalf("second: %v", err)
	}
}

func TestEnsurePanelUpdateStagingDir_ErrorsWhenEmpty(t *testing.T) {
	t.Setenv("SERIALHOP_LOCAL_DATA_DIR", "")
	t.Setenv("LOCALAPPDATA", "")
	if _, err := EnsurePanelUpdateStagingDir(); err == nil {
		t.Fatal("EnsurePanelUpdateStagingDir returned nil, want error")
	}
}

func TestPanelLogPaths(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SERIALHOP_DATA_DIR", dir)
	t.Setenv("ProgramData", "") // ignore real ProgramData

	if got, want := PanelLogPath(), filepath.Join(dir, "logs", "SerialHop_panel.log"); got != want {
		t.Errorf("PanelLogPath() = %q, want %q", got, want)
	}
	if got, want := StateDir(), filepath.Join(dir, "state"); got != want {
		t.Errorf("StateDir() = %q, want %q", got, want)
	}
	if got, want := PanelLogOffsetPath(), filepath.Join(dir, "state", "panel-log.offset"); got != want {
		t.Errorf("PanelLogOffsetPath() = %q, want %q", got, want)
	}
}

func TestPanelLogPaths_Empty(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	if got := PanelLogPath(); got != "" {
		t.Errorf("PanelLogPath() = %q, want empty", got)
	}
	if got := StateDir(); got != "" {
		t.Errorf("StateDir() = %q, want empty", got)
	}
	if got := PanelLogOffsetPath(); got != "" {
		t.Errorf("PanelLogOffsetPath() = %q, want empty", got)
	}
}
