//go:build windows

package main

import "github.com/bioexperiment-lab-devices/serialhop/internal/panel"

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

// runPanel is the panel-mode entrypoint. main.go's default flag branch
// calls it; the real wails.Run lives in internal/panel/RunWithBindings.
func runPanel() error {
	app := panel.NewApp()
	return panel.RunWithBindings(app, []interface{}{&App{App: app}})
}
