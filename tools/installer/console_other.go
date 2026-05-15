//go:build !windows

package main

// attachParentConsole is a no-op on non-Windows (the installer's silent
// path is exercised cross-platform during tests).
func attachParentConsole() {}
