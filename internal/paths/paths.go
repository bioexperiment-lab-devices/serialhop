// Package paths owns the on-disk layout for the SerialHop client.
//
// In production, DataDir resolves to %ProgramData%\SerialHop. The
// SERIALHOP_DATA_DIR environment variable, if set, overrides this —
// tests use t.Setenv("SERIALHOP_DATA_DIR", t.TempDir()) for isolation.
//
// LocalDataDir resolves to %LOCALAPPDATA%\SerialHop — the per-user
// equivalent, used for things the desktop user must be able to write
// without elevation (notably the auto-update staging dir, since
// %ProgramData% and the Program Files install dir aren't user-writable
// in a default install). The SERIALHOP_LOCAL_DATA_DIR env var overrides.
//
// All composed-path getters (ConfigPath, LogsDir, ServiceLogPath,
// StderrLogPath, PanelErrorLogPath, PanelUpdateStagingDir, PanelLogPath,
// StateDir, DeviceStateDir, PanelLogOffsetPath) return "" when their base directory
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

// DeviceStateDir returns the directory for per-device persistent state
// (devicestate/pump-26-025.json, devicestate/valve-COM7.json). Empty when
// no data dir is available.
func DeviceStateDir() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "devicestate")
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

// EnsureDirs creates DataDir, LogsDir, StateDir, and DeviceStateDir with os.MkdirAll (0o750).
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
	deviceState := DeviceStateDir()
	if err := os.MkdirAll(deviceState, 0o750); err != nil {
		return fmt.Errorf("paths: create %s: %w", deviceState, err)
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

// ServiceUpdateStagingDir is where the LocalSystem service stages a downloaded
// remote-update binary (SerialHop-v*.exe) before spawning the elevated swap
// child. It lives under %ProgramData% (not %LOCALAPPDATA%, whose LocalSystem
// expansion is the awkward systemprofile path). Returns "" if DataDir is empty.
func ServiceUpdateStagingDir() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "updates")
}

// EnsureServiceUpdateStagingDir creates ServiceUpdateStagingDir (0o750) and
// returns it. Returns an error if DataDir() is empty or MkdirAll fails.
func EnsureServiceUpdateStagingDir() (string, error) {
	d := ServiceUpdateStagingDir()
	if d == "" {
		return "", errors.New("paths: data directory unavailable (%ProgramData% not set)")
	}
	if err := os.MkdirAll(d, 0o750); err != nil {
		return "", fmt.Errorf("paths: create %s: %w", d, err)
	}
	return d, nil
}

// UpdateResultPath is the JSON file recording the last remote-update outcome,
// read by GET /agent/update/status. Written by both the service (progress) and
// the elevated child (terminal state). Returns "" if DataDir is empty.
func UpdateResultPath() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "update_result.json")
}
