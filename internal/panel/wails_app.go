//go:build windows

package panel

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

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

// onDomReady runs once the WebView has parsed the HTML. We use it to:
//  1. Write a marker line to the wails log so we know the DOM was reached.
//  2. Inject diagnostic JS that writes runtime state / errors into
//     document.title — readable from outside the process via Win32
//     GetWindowText. This works even when window.go.main.App isn't
//     populated (the very condition we're trying to diagnose), unlike
//     a binding callback.
func (a *App) onDomReady(ctx context.Context) {
	wailsruntime.LogPrint(ctx, "[panel] DOM ready")
	wailsruntime.WindowExecJS(ctx, `
(function () {
  function setTitle(s) {
    try { document.title = String(s).substring(0, 250); } catch (_) {}
  }

  // Intercept early errors so the title surfaces them.
  window.addEventListener('error', function (e) {
    setTitle('ERR ' + (e.message || '?') + ' @ ' + (e.filename || '?') + ':' + (e.lineno || '?'));
  });
  window.addEventListener('unhandledrejection', function (e) {
    setTitle('REJ ' + String(e.reason && (e.reason.stack || e.reason.message || e.reason)));
  });

  function snapshot(tag) {
    var scripts = Array.prototype.map.call(
      document.querySelectorAll('script[src]'),
      function (s) { return s.getAttribute('src'); }
    ).join(',');
    var root = document.getElementById('root');
    setTitle(tag +
      ' go=' + (typeof window.go) +
      ' rt=' + (typeof window.runtime) +
      ' wb=' + (typeof window.wailsbindings) +
      ' rk=' + (root ? root.childElementCount : 'NO-ROOT') +
      ' bl=' + (document.body ? document.body.innerHTML.length : 0) +
      ' s=' + scripts);
  }

  snapshot('READY');
  // Re-snapshot after the bundle has had a chance to mount React.
  setTimeout(function () { snapshot('T1500'); }, 1500);
  setTimeout(function () { snapshot('T4000'); }, 4000);
})();
`)
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

	// Make sure %ProgramData%\SerialHop\logs exists before we try to point a
	// FileLogger at it. We swallow the error here because the panel works
	// without a log file too — it's only diagnostic.
	_ = paths.EnsureDirs()

	// Wails internal log → file in our logs dir. The panel runs as a
	// windowsgui binary so stdout/stderr are /dev/null; without this,
	// every diagnostic Wails emits (asset 404s, binding generation
	// problems, WebView2 init errors) is silently discarded.
	wailsLogPath := ""
	if dir := paths.LogsDir(); dir != "" {
		wailsLogPath = filepath.Join(dir, "SerialHop_wails.log")
	}
	var wailsLogger logger.Logger
	if wailsLogPath != "" {
		wailsLogger = logger.NewFileLogger(wailsLogPath)
	}

	err := wails.Run(&options.App{
		Title:     "SerialHop v" + version.Base(),
		Width:     980,
		Height:    700,
		MinWidth:  860,
		MinHeight: 580,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		Logger:             wailsLogger,
		LogLevel:           logger.DEBUG,
		LogLevelProduction: logger.DEBUG,
		OnStartup:          app.startup,
		OnShutdown:         app.shutdown,
		// OnDomReady fires once the WebView has parsed the served HTML and
		// the document is interactable. If this never fires we know the page
		// itself never loaded; if it fires but the React tree never mounts
		// we know the bundle JS errored after parse. Either is invaluable
		// for diagnosing empty-window failures we can't reproduce locally.
		OnDomReady: app.onDomReady,
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
