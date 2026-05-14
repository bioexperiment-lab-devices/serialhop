//go:build windows

package main

import (
	"path/filepath"
	"testing"
)

func TestPanelErrorPathPrefersDataDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SERIALHOP_DATA_DIR", root)

	got := panelErrorPath(`C:\Tools\SerialHop`)
	want := filepath.Join(root, "logs", "SerialHop_panel_error.log")
	if got != want {
		t.Errorf("panelErrorPath() = %q, want %q", got, want)
	}
}

func TestPanelErrorPathFallsBackToExeDirWhenDataDirEmpty(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")

	got := panelErrorPath(`C:\Tools\SerialHop`)
	want := filepath.Join(`C:\Tools\SerialHop`, "SerialHop_panel_error.log")
	if got != want {
		t.Errorf("panelErrorPath() = %q, want %q", got, want)
	}
}
