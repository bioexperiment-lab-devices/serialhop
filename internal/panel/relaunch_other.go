//go:build !windows

// File intentionally empty on non-Windows. relaunchPanelExe and
// (*App).requestQuit are defined alongside the bindings.go App type,
// both of which are Windows-only; nothing in the cross-platform half
// of this package needs to reference them.
package panel
