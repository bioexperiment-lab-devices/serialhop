//go:build !windows

package panel

// The Wails app, its App struct, and RunWithBindings all live in
// wails_app.go behind //go:build windows. On non-Windows builds the
// package compiles only its pure-Go helpers (servicecli, filetail,
// credverify, lampstate, state, update_state, firstrun) so tests can
// run on macOS/Linux CI. There is no Wails entry point on these
// platforms; main.go's panel-mode dispatch is itself //go:build windows.
