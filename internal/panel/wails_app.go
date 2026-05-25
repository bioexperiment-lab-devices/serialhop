//go:build windows

package panel

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/panellog"
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
	probeDedup    *probeDedup
	panelLog      *panellog.Manager
	streaming     *StreamingLifecycle
}

// SetPanelLog wires the panellog manager so SaveConfig can update the
// live log level when cfg.Log.Level changes. Called once at startup by
// the panel-mode entry point.
func (a *App) SetPanelLog(m *panellog.Manager) {
	a.panelLog = m
}

// NewApp constructs a panel App. Exported so package main can wrap it
// in its own type — Wails namespaces bindings by Go package path, so
// the binding target must live in package main for the SPA's
// `window.go.main.App` import to resolve.
func NewApp() *App { return newAppInternal() }

func newAppInternal() *App {
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
		probeDedup:    newProbeDedup(5 * time.Minute),
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	cfg, _ := config.LoadPartial(paths.ConfigPath())
	a.svc = NewServiceCli(paths.ServerInfoCachePath())
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
		host, _, _ := a.lamps.probeCreds(paths.ServerInfoCachePath(), paths.ConfigPath())
		base := ""
		if host != "" {
			base = "https://" + host
		}
		runServerProbe(ctx, probeHC, base, userAgent, a.lamps)
		a.emitServerLamp()
		_, srv, _ := a.lamps.snapshot()
		switch srv.kind {
		case lampOK:
			a.probeDedup.reset("server")
		default:
			reason := srv.detail
			if reason == "" {
				reason = fmt.Sprintf("kind=%d", srv.kind)
			}
			if a.probeDedup.shouldLog("server", reason, time.Now()) {
				slog.Warn("server probe failed", "reason", reason)
			}
		}
	})
	go probeLoop(ctx, 30*time.Second, a.tunnelTrigger, func(ctx context.Context) {
		host, user, pass := a.lamps.probeCreds(paths.ServerInfoCachePath(), paths.ConfigPath())
		base := ""
		if host != "" {
			base = "https://" + host
		}
		runTunnelProbe(ctx, probeHC, base, user, pass, userAgent, a.lamps)
		a.emitTunnelLamp()
		_, _, tun := a.lamps.snapshot()
		switch tun.kind {
		case lampOK:
			a.probeDedup.reset("tunnel")
		default:
			reason := tun.detail
			if reason == "" {
				reason = fmt.Sprintf("kind=%d", tun.kind)
			}
			if a.probeDedup.shouldLog("tunnel", reason, time.Now()) {
				slog.Warn("tunnel probe failed", "reason", reason)
			}
		}
	})
	go a.scmPollLoop(ctx)

	// Streaming subsystem is gated behind experimental.camera_streaming
	// in the YAML config — defaults to false so the Cameras tab stays
	// hidden and the localhost listener / panel-endpoint.json never
	// gets created on lab machines that haven't opted in.
	if cfg.Experimental.CameraStreaming {
		a.streaming = NewStreamingLifecycle(
			paths.PanelEndpointPath(),
			paths.ArmedCamerasPath(),
			paths.FFmpegPath(),
			"-authorization",
		)
		if err := a.streaming.Start(ctx); err != nil {
			slog.Error("streaming subsystem failed to start", "err", err)
		}
	} else {
		// If a previous panel session left a stale endpoint file behind
		// (e.g. the user toggled the flag off and crashed), clean it up
		// so the service-side proxy doesn't spend 5 seconds per request
		// trying to reach a dead port.
		_ = bootstrap.DeletePanelEndpoint(paths.PanelEndpointPath())
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

func (a *App) shutdown(ctx context.Context) {
	if a.logTail != nil {
		a.logTail.stop()
	}
	if a.streaming != nil {
		_ = a.streaming.Stop(ctx)
	}
}

func (a *App) emitServerLamp() {
	_, srv, _ := a.lamps.snapshot()
	color, text, sub := serverLampPresentation(srv)
	a.emitLamp("server", color, text, sub)
}

func (a *App) emitTunnelLamp() {
	_, _, tun := a.lamps.snapshot()
	color, text, sub := tunnelLampPresentation(tun)
	a.emitLamp("tunnel", color, text, sub)
}

func (a *App) emitServiceLamp() {
	svc, _, _ := a.lamps.snapshot()
	color, text, sub := serviceLampPresentation(svc)
	a.emitLamp("service", color, text, sub)
}

// emitLamp is the single status:lamp emitter. `sub` is omitted from the
// payload when empty so the SPA's `sub && <span>` render path stays clean.
func (a *App) emitLamp(which string, color StatusColor, label, sub string) {
	payload := map[string]string{
		"which": which,
		"tone":  toneString(color),
		"label": label,
	}
	if sub != "" {
		payload["sub"] = sub
	}
	a.emitEvent("status:lamp", payload)
}

// markNetProbesChecking flips both network lamps to "Checking…" and emits.
// Used right when a user action starts so the lamps don't keep showing
// stale "Connected"/"Up" while the action runs.
func (a *App) markNetProbesChecking() {
	a.lamps.setServer(netLamp{kind: lampChecking})
	a.lamps.setTunnel(netLamp{kind: lampChecking})
	a.emitServerLamp()
	a.emitTunnelLamp()
}

// kickNetProbes wakes the server and tunnel probe goroutines so they
// re-run now instead of waiting for the next 30 s tick. Pairs with
// markNetProbesChecking — the probe writes the real result into the
// lamp state when it returns. trySend coalesces, so callers may invoke
// freely from action handlers.
func (a *App) kickNetProbes() {
	a.markNetProbesChecking()
	trySend(a.serverTrigger)
	trySend(a.tunnelTrigger)
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
			if changed {
				slog.Info("scm state change",
					"from", oldSvc.state, "to", newSvc.state,
					"cfg_valid", newSvc.cfgValid)
			}
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

// RunWithBindings is the panel-mode entry point. The caller (package main)
// owns binding-target construction: Wails reflects over the bound values
// and registers their methods under the Go package path of the value's
// type. The SPA imports from `window.go.main.App`, so the binding target
// must be a type defined in package main — typically a thin struct that
// embeds *panel.App to inherit its methods. See `panel_run_windows.go`.
func RunWithBindings(app *App, bindings []interface{}) error {
	err := wails.Run(&options.App{
		Title:     "SerialHop v" + version.Base(),
		Width:     980,
		Height:    700,
		MinWidth:  720,
		MinHeight: 480,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind:       bindings,
		// Frameless removes the OS-drawn caption (title text + system buttons).
		// The SPA's TitleBar component supplies the chrome instead, including
		// minimise / close buttons that call WindowMinimise / Quit. Window
		// dragging is enabled per-element via `--wails-draggable: drag`.
		// Trade-off: Win11 Snap Layouts (the maximize-button hover menu) are
		// not reachable without a custom hit-test bridge — acceptable for an
		// operator panel that doesn't expose maximize anyway.
		Frameless: true,
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
