//go:build !windows

package main

import "errors"

type shortcutOpts struct {
	Path         string
	Target       string
	WorkingDir   string
	IconLocation string
	Description  string
}

// writeShortcut is a Windows-only operation; the cross-platform stub lets the
// installer package compile and run its dispatch tests on macOS/Linux. Real
// shortcut creation happens via COM IShellLinkW in shortcut_windows.go.
func writeShortcut(_ shortcutOpts) error {
	return errors.New("writeShortcut: only supported on Windows")
}

type realShortcutWriter struct{}

func (realShortcutWriter) Write(opts shortcutOpts) error {
	return writeShortcut(opts)
}
