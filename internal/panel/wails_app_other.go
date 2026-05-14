//go:build !windows

// Package panel ships only on Windows. The Wails entry point used to
// live here as a no-op stub on non-Windows so the package would compile
// for CI. With the entry point moved to package main (Windows-only file),
// the panel package no longer exposes any callable from non-Windows code,
// and this file exists solely to keep the package compilable in
// macOS/Linux CI builds that pull it in transitively.
package panel
