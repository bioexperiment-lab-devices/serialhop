//go:build !windows

package main

// Non-Windows builds are not user-facing (they exist only to satisfy
// cross-platform CI). Elevation check is a no-op.
func enforceElevation() error { return nil }
