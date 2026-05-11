// Package paths owns the on-disk layout for the SerialHop client.
//
// In production, DataDir resolves to %ProgramData%\SerialHop. The
// SERIALHOP_DATA_DIR environment variable, if set, overrides this —
// tests use t.Setenv("SERIALHOP_DATA_DIR", t.TempDir()) for isolation.
//
// All composed-path getters (ConfigPath, LogsDir, ServiceLogPath,
// StderrLogPath, PanelErrorLogPath) return "" when DataDir() returns ""
// (i.e., %ProgramData% is unset and no test override is in effect).
// Callers can detect "no data dir available" with a single empty-string
// check.
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

// EnsureDirs creates DataDir and LogsDir with os.MkdirAll (0o755).
// Idempotent. Returns an error if DataDir() is empty or MkdirAll fails.
func EnsureDirs() error {
	d := DataDir()
	if d == "" {
		return errors.New("paths: data directory unavailable (%ProgramData% not set)")
	}
	logs := filepath.Join(d, "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return fmt.Errorf("paths: create %s: %w", logs, err)
	}
	return nil
}
