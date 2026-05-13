//go:build windows

package panel

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
	"github.com/bioexperiment-lab-devices/serialhop/internal/version"
)

//go:embed all:frontend/dist
var assets embed.FS

// App is the Wails application. Bindings methods are defined in
// bindings.go (later tasks); event emission lives in events.go.
// The struct itself holds the long-lived collaborators (probe
// goroutines, log tailer, service-cli) initialized in startup.
type App struct {
	ctx      context.Context
	updateCh *updateCtl
	hc       *http.Client
}

func newApp() *App {
	return &App{
		updateCh: &updateCtl{},
		hc:       &http.Client{}, // no global timeout; per-request ctx applied in the update helpers
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	cfg, _ := config.LoadPartial(paths.ConfigPath())
	if cfg.AutoUpdate.Enabled {
		go func() {
			time.Sleep(500 * time.Millisecond)
			runUpdateCheckEvent(a)
		}()
		go a.updateRecheckLoop(ctx)
	}
}

func (a *App) updateRecheckLoop(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runUpdateCheckEvent(a)
		}
	}
}

func (a *App) shutdown(_ context.Context) {
	// Wiring added by later tasks.
}

// Run is the panel-mode entry point invoked from cmd/serialhop/main.go.
// Replaces the walk-based panel from panel.go (kept as walkRun for now).
func Run() error {
	app := newApp()
	err := wails.Run(&options.App{
		Title:     "SerialHop v" + version.Base(),
		Width:     980,
		Height:    700,
		MinWidth:  860,
		MinHeight: 580,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind:       []interface{}{app},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
	})
	if err != nil {
		return fmt.Errorf("wails run: %w", err)
	}
	return nil
}
