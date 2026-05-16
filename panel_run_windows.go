//go:build windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/logship"
	"github.com/bioexperiment-lab-devices/serialhop/internal/panel"
	"github.com/bioexperiment-lab-devices/serialhop/internal/panellog"
	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
	"github.com/bioexperiment-lab-devices/serialhop/internal/version"
)

// App is the binding target passed to Wails. Methods are inherited
// from the embedded *panel.App via Go's method-promotion rules; Wails'
// reflector sees them all as methods of *main.App. The struct must
// live in package main because Wails namespaces bindings by Go package
// path — the SPA imports from window.go.main.App and would not find
// the methods if they were bound at window.go["...internal/panel"].App
// (which is what happened in v0.14.x — empty panel window).
type App struct {
	*panel.App
}

func runPanel() error {
	if err := paths.EnsureDirs(); err != nil {
		return fmt.Errorf("paths setup: %w", err)
	}

	level := slog.LevelInfo
	if cfg, err := config.LoadPartial(paths.ConfigPath()); err == nil {
		level = logship.ParseLogLevel(cfg.Log.Level)
	}

	panelMgr, plErr := panellog.Init(version.Version, level)
	if plErr != nil {
		// Best-effort fallback: continue without slog → panel log routing.
		// writePanelStartupError captures this breadcrumb for the operator.
		writePanelStartupError(fmt.Errorf("panellog.Init: %w", plErr))
	}

	app := panel.NewApp()
	if panelMgr != nil {
		app.SetPanelLog(panelMgr)
	}

	err := panel.RunWithBindings(app, []interface{}{&App{App: app}})

	if panelMgr != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = panelMgr.Shutdown(shutdownCtx)
		cancel()
	}
	return err
}
