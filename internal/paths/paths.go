// Package paths owns the on-disk layout for the SerialHop client.
//
// Three base directories, three distinct purposes:
//
//   - InstallDir resolves to the directory of the running executable —
//     the install dir on Windows (typically C:\Program Files\SerialHop).
//     Shipped binaries the installer placed alongside SerialHop.exe
//     (notably ffmpeg.exe) live here. Read-only at runtime by
//     convention. The SERIALHOP_INSTALL_DIR env var overrides for
//     tests.
//
//   - DataDir resolves to %ProgramData%\SerialHop. Operator/runtime
//     data that must survive upgrades — config, logs,
//     panel-endpoint.json, armed-cameras.json. The
//     SERIALHOP_DATA_DIR environment variable, if set, overrides this
//     — tests use t.Setenv("SERIALHOP_DATA_DIR", t.TempDir()) for
//     isolation.
//
//   - LocalDataDir resolves to %LOCALAPPDATA%\SerialHop — the per-user
//     equivalent, used for things the desktop user must be able to
//     write without elevation (notably the auto-update staging dir,
//     since %ProgramData% and the Program Files install dir aren't
//     user-writable in a default install). The
//     SERIALHOP_LOCAL_DATA_DIR env var overrides.
//
// All composed-path getters (ConfigPath, LogsDir, ServiceLogPath,
// StderrLogPath, PanelErrorLogPath, PanelUpdateStagingDir, PanelLogPath,
// StateDir, PanelLogOffsetPath) return "" when their base directory
// returns "" (i.e., the underlying env var is unset and no test override
// is in effect). Callers can detect "no directory available" with a
// single empty-string check.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	ConfigFileName        = "SerialHop_config.yaml"
	ServiceLogFileName    = "SerialHop.log"
	StderrLogFileName     = "SerialHop_stderr.log"
	PanelErrorLogFileName = "SerialHop_panel_error.log"
	// PanelCrashJournalFileName is the on-disk name of the JSON-lines
	// crash journal the panel writes via RecordFrontendCrash. One line
	// per caught JS-side error; the file is capped at ~64 KiB by
	// appendCapped in internal/panel/crash_journal.go.
	PanelCrashJournalFileName = "SerialHop_panel_crash.log"
	ServerInfoCacheFileName   = "server-info.cache.json"
	PanelLogFileName          = "SerialHop_panel.log"
	// PanelLogOffsetFileName is the on-disk name of the byte-offset
	// tracking file used by internal/logship's panel-log file tailer.
	// It lives under StateDir (not LogsDir) — it is state, not a log.
	PanelLogOffsetFileName = "panel-log.offset"
)

// DataDir returns the SerialHop root data directory.
// SERIALHOP_DATA_DIR overrides %ProgramData% for tests. Returns ""
// when neither is set.
func DataDir() string {
	if v := os.Getenv("SERIALHOP_DATA_DIR"); v != "" {
		return v
	}
	if pd := os.Getenv("ProgramData"); pd != "" {
		return filepath.Join(pd, "SerialHop")
	}
	return ""
}

// LogsDir returns <DataDir>/logs, or "" if DataDir is empty.
func LogsDir() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "logs")
}

// BackupsDir returns <DataDir>/backups, or "" if DataDir is empty.
func BackupsDir() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "backups")
}

// ConfigPath returns <DataDir>/SerialHop_config.yaml, or "" if DataDir is empty.
func ConfigPath() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, ConfigFileName)
}

// ServiceLogPath returns <LogsDir>/SerialHop.log, or "" if LogsDir is empty.
func ServiceLogPath() string {
	d := LogsDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, ServiceLogFileName)
}

// StderrLogPath returns <LogsDir>/SerialHop_stderr.log, or "" if LogsDir is empty.
func StderrLogPath() string {
	d := LogsDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, StderrLogFileName)
}

// PanelErrorLogPath returns <LogsDir>/SerialHop_panel_error.log,
// or "" if LogsDir is empty.
func PanelErrorLogPath() string {
	d := LogsDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, PanelErrorLogFileName)
}

// PanelCrashJournalPath returns <LogsDir>/SerialHop_panel_crash.log,
// or "" if LogsDir is empty. Callers must treat "" as "no journal here"
// and silently no-op — the binding swallows write errors anyway, so a
// missing DataDir must not surface as an exception.
func PanelCrashJournalPath() string {
	d := LogsDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, PanelCrashJournalFileName)
}

// PanelLogPath returns <LogsDir>/SerialHop_panel.log, or "" if LogsDir
// is empty. This is the structured slog destination written by the
// panel process and tailed by the service-side logship file tailer.
func PanelLogPath() string {
	d := LogsDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, PanelLogFileName)
}

// StateDir returns <DataDir>/state, or "" if DataDir is empty.
// Holds small per-host state files (e.g., panel-log.offset).
func StateDir() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "state")
}

// PanelLogOffsetPath returns <StateDir>/panel-log.offset, or "" if
// StateDir is empty. The service-side logship file tailer atomically
// persists its byte offset here on every successful queue push.
func PanelLogOffsetPath() string {
	d := StateDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, PanelLogOffsetFileName)
}

// ServerInfoCachePath returns <DataDir>/server-info.cache.json, or ""
// if DataDir is empty.
func ServerInfoCachePath() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, ServerInfoCacheFileName)
}

const (
	FFmpegBinaryName      = "ffmpeg.exe"
	PanelEndpointFileName = "panel-endpoint.json"
	ArmedCamerasFileName  = "armed-cameras.json"
)

// InstallDir returns the directory holding the running SerialHop.exe
// — the install dir on Windows (typically `C:\Program Files\SerialHop`).
// Derived from os.Executable() so it follows wherever the operator
// chose to install. Returns "" if os.Executable() fails (this is a
// hard error in practice; callers treat empty as "ffmpeg unavailable"
// rather than crashing).
//
// The SERIALHOP_INSTALL_DIR environment variable overrides — tests use
// t.Setenv("SERIALHOP_INSTALL_DIR", t.TempDir()) to keep filesystem
// paths deterministic without spawning a binary at that path.
func InstallDir() string {
	if v := os.Getenv("SERIALHOP_INSTALL_DIR"); v != "" {
		return v
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// FFmpegPath returns <InstallDir>/ffmpeg.exe, or "" if InstallDir
// returns "". The binary is written by the installer (which puts it
// next to SerialHop.exe in the install dir) and read by the panel
// when launching streaming sessions.
//
// Note: ffmpeg.exe lives in the install dir (Program Files), NOT in
// %ProgramData% — it's a shipped binary, not a data file. The other
// path helpers in this file are correctly DataDir-based because they
// resolve runtime/operator data (config, logs, panel-endpoint.json,
// armed-cameras.json).
func FFmpegPath() string {
	d := InstallDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, FFmpegBinaryName)
}

// PanelEndpointPath returns <DataDir>/panel-endpoint.json, or "" if
// DataDir is empty. The panel writes this file on startup so the
// service can discover the panel's localhost listener address.
func PanelEndpointPath() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, PanelEndpointFileName)
}

// ArmedCamerasPath returns <DataDir>/armed-cameras.json, or "" if
// DataDir is empty. The panel reads/writes this file to remember
// which cameras the user has armed for streaming across restarts.
func ArmedCamerasPath() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, ArmedCamerasFileName)
}

// EnsureDirs creates DataDir, LogsDir, and StateDir with os.MkdirAll (0o750).
// Idempotent. Returns an error if DataDir() is empty or MkdirAll fails.
// On Windows the Unix mode bits are advisory; the actual ACL inherits
// from %ProgramData%.
func EnsureDirs() error {
	d := DataDir()
	if d == "" {
		return errors.New("paths: data directory unavailable (%ProgramData% not set)")
	}
	logs := filepath.Join(d, "logs")
	if err := os.MkdirAll(logs, 0o750); err != nil {
		return fmt.Errorf("paths: create %s: %w", logs, err)
	}
	state := StateDir()
	if err := os.MkdirAll(state, 0o750); err != nil {
		return fmt.Errorf("paths: create %s: %w", state, err)
	}
	return nil
}

// LocalDataDir returns the SerialHop per-user data directory.
// SERIALHOP_LOCAL_DATA_DIR overrides %LOCALAPPDATA% for tests. Returns ""
// when neither is set.
//
// Use this for things the desktop user must write without elevation —
// e.g., the auto-update staging dir, since the default install dir
// (C:\Program Files\SerialHop) is not user-writable.
func LocalDataDir() string {
	if v := os.Getenv("SERIALHOP_LOCAL_DATA_DIR"); v != "" {
		return v
	}
	if lad := os.Getenv("LOCALAPPDATA"); lad != "" {
		return filepath.Join(lad, "SerialHop")
	}
	return ""
}

// PanelUpdateStagingDir returns <LocalDataDir>/updates, or "" if
// LocalDataDir is empty. This is where the panel stages downloaded
// SerialHop-v*.exe binaries before the elevated install step copies
// them into the install dir.
func PanelUpdateStagingDir() string {
	d := LocalDataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "updates")
}

// EnsurePanelUpdateStagingDir creates the staging dir with os.MkdirAll
// (0o750) and returns its path. Idempotent. Returns an error if
// LocalDataDir() is empty or MkdirAll fails.
func EnsurePanelUpdateStagingDir() (string, error) {
	d := PanelUpdateStagingDir()
	if d == "" {
		return "", errors.New("paths: local data directory unavailable (%LOCALAPPDATA% not set)")
	}
	if err := os.MkdirAll(d, 0o750); err != nil {
		return "", fmt.Errorf("paths: create %s: %w", d, err)
	}
	return d, nil
}
