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
	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

//go:embed all:frontend/dist
var assets embed.FS

// App is the Wails application. Bindings methods are defined in
// bindings.go (later tasks); event emission lives in events.go.
// The struct itself holds the long-lived collaborators (probe
// goroutines, log tailer, service-cli) initialized in startup.
type App struct {
	ctx           context.Context
	updateCh      *updateCtl
	hc            *http.Client
	logTail       *logTailController
	svc           *ServiceCli
	lamps         *lampState
	serverTrigger chan struct{}
	tunnelTrigger chan struct{}
	lastService   winsvc.ServiceState // last-known SCM state for stickiness
}

func newApp() *App {
	return &App{
		updateCh: &updateCtl{},
		hc:       &http.Client{}, // no global timeout; per-request ctx applied in the update helpers
		lamps: &lampState{
			server: netLamp{kind: lampChecking},
			tunnel: netLamp{kind: lampChecking},
		},
		serverTrigger: make(chan struct{}, 1),
		tunnelTrigger: make(chan struct{}, 1),
		lastService:   winsvc.StateNotInstalled,
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	cfg, _ := config.LoadPartial(paths.ConfigPath())
	a.svc = NewServiceCli(paths.ServerInfoCachePath(), cfg.LabBridge.User)
	if cfg.AutoUpdate.Enabled {
		go func() {
			time.Sleep(500 * time.Millisecond)
			runUpdateCheckEvent(a)
		}()
		go a.updateRecheckLoop(ctx)
	}

	// Probe loops — emit status:lamp events on tone-or-label change.
	probeHC := &http.Client{Timeout: 30 * time.Second}
	userAgent := "SerialHop/" + version.Base() + " (status-probe)"
	go probeLoop(ctx, 30*time.Second, a.serverTrigger, func(ctx context.Context) {
		c, _ := config.LoadPartial(paths.ConfigPath())
		base := ""
		if c.LabBridge.Host != "" {
			base = "https://" + c.LabBridge.Host
		}
		runServerProbe(ctx, probeHC, base, userAgent, a.lamps)
		a.emitServerLamp()
	})
	go probeLoop(ctx, 30*time.Second, a.tunnelTrigger, func(ctx context.Context) {
		c, _ := config.LoadPartial(paths.ConfigPath())
		base := ""
		if c.LabBridge.Host != "" {
			base = "https://" + c.LabBridge.Host
		}
		runTunnelProbe(ctx, probeHC, base, c.LabBridge.User, c.LabBridge.Pass, userAgent, a.lamps)
		a.emitTunnelLamp()
	})
	go a.scmPollLoop(ctx)
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
	if a.logTail != nil {
		a.logTail.stop()
	}
}

func (a *App) emitServerLamp() {
	_, srv, _ := a.lamps.snapshot()
	color, text := serverLampPresentation(srv)
	a.emitEvent("status:lamp", map[string]string{
		"which": "server",
		"tone":  toneString(color),
		"label": text,
	})
}

func (a *App) emitTunnelLamp() {
	_, _, tun := a.lamps.snapshot()
	color, text := tunnelLampPresentation(tun)
	a.emitEvent("status:lamp", map[string]string{
		"which": "tunnel",
		"tone":  toneString(color),
		"label": text,
	})
}

func (a *App) emitServiceLamp() {
	svc, _, _ := a.lamps.snapshot()
	color, text := serviceLampPresentation(svc)
	a.emitEvent("status:lamp", map[string]string{
		"which": "service",
		"tone":  toneString(color),
		"label": text,
	})
}

func (a *App) emitButtonState(s serviceLamp) {
	btns := ComputeButtons(s.state, s.cfgValid)
	a.emitEvent("buttons:state", ButtonStateDTO{
		Install:   btns.Install,
		Uninstall: btns.Uninstall,
		Restart:   btns.Restart,
	})
}

func toneString(c StatusColor) string {
	switch c {
	case ColorGreen:
		return "green"
	case ColorYellow:
		return "yellow"
	case ColorRed:
		return "red"
	}
	return "grey"
}

func (a *App) scmPollLoop(ctx context.Context) {
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	first := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		scmState, ok := queryServiceState()
		if !ok {
			scmState = a.lastService
		} else {
			a.lastService = scmState
		}
		cfg, cfgErr := config.LoadPartial(paths.ConfigPath())
		_ = cfg
		newSvc := serviceLamp{state: scmState, cfgValid: cfgErr == nil}
		oldSvc, _, _ := a.lamps.snapshot()
		a.lamps.setService(newSvc)
		changed := oldSvc.state != newSvc.state || oldSvc.cfgValid != newSvc.cfgValid
		if first || changed {
			a.emitServiceLamp()
			a.emitButtonState(newSvc)
			first = false
		}
		// Warn header tracking — emit on every tick so the SPA stays current.
		if cfgErr != nil {
			a.emitWarn("⚠ " + cfgErr.Error())
		} else {
			a.clearWarn()
		}
	}
}

// Run is the panel-mode entry point invoked from main.go.
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
		// EnableDefaultContextMenu turns on the WebView2 right-click menu in
		// production. Operators get cut+copy+paste; we get a fighting chance
		// at diagnostics — `Inspect` is included, which opens DevTools.
		EnableDefaultContextMenu: true,
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
