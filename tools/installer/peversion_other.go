//go:build !windows

package main

import "errors"

// readPEFileVersion is a Windows-only operation. The cross-platform stub
// returns a sentinel error so cross-platform tests that exercise the install
// dispatch can substitute a fake reader instead of calling this directly.
func readPEFileVersion(_ string) (string, error) {
	return "", errors.New("readPEFileVersion: only supported on Windows")
}
