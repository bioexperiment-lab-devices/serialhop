//go:build windows

package main

import (
	"context"
	"embed"
	"time"

	"github.com/wailsapp/wails/v2"
	wailsopts "github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var frontendAssets embed.FS

// InstallerApp is the Wails-bound singleton exposed to JS. JS calls into
// these methods via the auto-generated bindings at
// window.go.main.InstallerApp.*. Method names must start with an uppercase
// letter to be bindable.
type InstallerApp struct {
	ctx  context.Context
	opts *options // CLI-parsed defaults (install dir, allow-downgrade, etc.)
}

// OnStartup is wired into options.App.OnStartup; Wails calls it once after
// the WebView2 control is initialized. We capture the context so later
// methods can emit events and call runtime.Quit.
func (a *InstallerApp) OnStartup(ctx context.Context) {
	a.ctx = ctx
}

// InitialPath returns the install directory the dialog should pre-fill —
// the CLI --dir flag's value (or the default `C:\Program Files\SerialHop`).
func (a *InstallerApp) InitialPath() string {
	return a.opts.InstallDir
}

// BrowseFolder opens the system folder picker rooted at `current` (or the
// default if empty) and returns the chosen path. Returns "" if the
// operator cancelled the picker.
func (a *InstallerApp) BrowseFolder(current string) string {
	dlg, err := wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title:                "Choose install directory",
		DefaultDirectory:     current,
		CanCreateDirectories: true,
	})
	if err != nil {
		return ""
	}
	return dlg
}

// Install runs the install flow with the operator's chosen install
// directory, then (on success) schedules a 1500ms auto-close so the
// installer window vanishes after the panel is launched. Returns the
// Result so JS can render the final status before the close timer fires.
func (a *InstallerApp) Install(installDir string) Result {
	runOpts := *a.opts
	runOpts.InstallDir = installDir
	r := newProductionRunner()
	res := r.Run(runOpts)

	// Auto-close on success only. Failure paths leave the window open
	// so the operator can see the error and retry (or close manually).
	// Same-version "already installed" is treated as success — the
	// installer launches the existing panel and exits.
	if res.Err == nil && !runOpts.NoLaunch && !runOpts.Silent {
		go func(ctx context.Context) {
			time.Sleep(1500 * time.Millisecond)
			wailsruntime.Quit(ctx)
		}(a.ctx)
	}
	return res
}

// Cancel closes the installer window. Same effect as the system close
// button. If an install is in flight, Cancel still fires Quit — Wails
// shuts down the WebView2 control and the Go process exits. We do not
// abort the underlying install (rare for the operator to want a
// mid-rename abort, and the rollback machinery handles a SIGTERM-during-
// rename poorly).
func (a *InstallerApp) Cancel() {
	wailsruntime.Quit(a.ctx)
}

// runDialog opens the Wails-based installer window. Returns the process
// exit code (0 on user-driven close, 1 on Wails startup error).
func runDialog(opts *options) int {
	app := &InstallerApp{opts: opts}

	err := wails.Run(&wailsopts.App{
		Title:         "SerialHop Installer",
		Width:         480,
		Height:        300,
		MinWidth:      480,
		MinHeight:     300,
		MaxWidth:      480,
		MaxHeight:     300,
		DisableResize: true,
		// Frameless: the SPA owns the title bar (close button + drag region)
		// to match the panel's chrome. The HTML at frontend/dist/index.html
		// renders a .shp-titlebar that mirrors the panel's TitleBar component.
		Frameless:        true,
		BackgroundColour: &wailsopts.RGBA{R: 0xEC, G: 0xE9, B: 0xE0, A: 0xFF}, // --bg-page
		AssetServer: &assetserver.Options{
			Assets: frontendAssets,
		},
		Bind:      []any{app},
		OnStartup: app.OnStartup,
	})
	if err != nil {
		// We're on the windowsgui subsystem; stderr goes to nowhere unless
		// reattached. The error is also captured in the structured log file
		// at %TEMP%\SerialHop-installer-<version>.log via slog (from main.go).
		return 1
	}
	return 0
}
