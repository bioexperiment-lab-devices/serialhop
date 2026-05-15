//go:build !windows

package main

// runDialog is unreachable from cross-platform tests because main() dispatches
// to it only under //go:build windows. A panic here would make debugging an
// accidental invocation obvious.
func runDialog() int {
	panic("runDialog: only supported on Windows")
}
