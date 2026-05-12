//go:build windows

package panel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
	"github.com/bioexperiment-lab-devices/serialhop/internal/updater"
	"github.com/bioexperiment-lab-devices/serialhop/internal/version"
	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

const pollInterval = 1 * time.Second

// updateCtl holds the state machine and current release info for the update
// row. All fields are guarded by mu except where noted.
type updateCtl struct {
	mu       sync.Mutex
	state    UpdateState
	release  updater.Release
	exeAsset *updater.Asset
	exeFile  string // full path to the staged .exe (when ready)
	dlCancel context.CancelFunc
}

// Run opens the control-panel window and blocks until the user closes it.
func Run() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	installDir := filepath.Dir(exePath)
	pathsErr := paths.EnsureDirs() // non-fatal: surfaced via warn label and disabled file buttons

	cfgPath := paths.ConfigPath()
	if pathsErr == nil {
		state := readFirstRunState(cfgPath)
		if decideFirstRun(state) == FirstRunShowDialog {
			_ = runCredsDialog(cfgPath, state.Cfg)
			// Whether the user saved or cancelled, fall through. On cancel,
			// the panel opens with empty creds and the existing
			// validation-warning label surfaces the missing-fields error.
		}
	}

	// Per-lamp current color. Mutated by paintLamp; read by the per-lamp
	// Paint closure registered with each CustomWidget.
	serviceDotColor := walk.RGB(128, 128, 128)
	serverDotColor := walk.RGB(128, 128, 128)
	tunnelDotColor := walk.RGB(128, 128, 128)

	makeCirclePaint := func(colorPtr *walk.Color) walk.PaintFunc {
		return func(canvas *walk.Canvas, _ walk.Rectangle) error {
			brush, err := walk.NewSolidColorBrush(*colorPtr)
			if err != nil {
				return err
			}
			defer brush.Dispose()
			// Centered 12x12 circle inside the 16x16 widget.
			return canvas.FillEllipsePixels(brush, walk.Rectangle{X: 2, Y: 2, Width: 12, Height: 12})
		}
	}

	var (
		mw           *walk.MainWindow
		serviceDot   *walk.CustomWidget
		serviceLabel *walk.Label
		serverDot    *walk.CustomWidget
		serverState  *walk.Label // lamp state text — distinct from serverLbl which shows the configured host:port
		tunnelDot    *walk.CustomWidget
		tunnelLabel  *walk.Label
		warnLabel    *walk.Label
		statusBar    *walk.Label

		serverLbl    *walk.Label
		remotePort   *walk.Label
		restPort     *walk.Label
		discoveryLbl *walk.Label
		logLevel     *walk.Label
		rawSerialLbl *walk.Label

		btnInstall   *walk.PushButton
		btnUninstall *walk.PushButton
		btnRestart   *walk.PushButton
		btnOpenCfg   *walk.PushButton
		btnOpenLogs  *walk.PushButton

		updateRow   *walk.Composite
		updateLabel *walk.Label
		btnDownload *walk.PushButton
		btnInstall2 *walk.PushButton
		btnRelease  *walk.PushButton
		btnRetry    *walk.PushButton
		btnCancelDL *walk.PushButton
	)

	// `View error` button from spec §10 is deliberately omitted in v1: the
	// status-bar label already shows the failure detail, and plumbing the
	// elevated-child's temp error file path (cleaned up in elevate.go's defer)
	// to a UI handler is non-trivial. Add later if operators ask for it.

	ctl := &updateCtl{}

	httpClient := &http.Client{} // timeouts applied via per-request ctx
	updateUA := "SerialHop/" + version.Base() + " (auto-update; +https://github.com/bioexperiment-lab-devices/serialhop)"
	userAgent := "SerialHop/" + version.Base() + " (status-probe)"

	cfg, _ := config.LoadPartial(cfgPath)
	autoUpdateEnabled := cfg.AutoUpdate.Enabled

	// Last-known SCM state. On transient SCM errors we keep showing this
	// instead of blinking to "Not installed".
	lastState := winsvc.StateNotInstalled

	state := &lampState{
		server: netLamp{kind: lampChecking},
		tunnel: netLamp{kind: lampChecking},
	}

	paintLamp := func(dot *walk.CustomWidget, colorPtr *walk.Color, label *walk.Label, color StatusColor, text string) {
		_ = label.SetText(text)
		switch color {
		case ColorGreen:
			*colorPtr = walk.RGB(0, 160, 0)
		case ColorYellow:
			*colorPtr = walk.RGB(200, 160, 0)
		case ColorRed:
			*colorPtr = walk.RGB(192, 0, 0)
		default:
			*colorPtr = walk.RGB(128, 128, 128)
		}
		dot.Invalidate()
	}

	repaintLamps := func() {
		svc, srv, tun := state.snapshot()
		sc, st := serviceLampPresentation(svc)
		paintLamp(serviceDot, &serviceDotColor, serviceLabel, sc, st)
		sec, set := serverLampPresentation(srv)
		paintLamp(serverDot, &serverDotColor, serverState, sec, set)
		tc, tt := tunnelLampPresentation(tun)
		paintLamp(tunnelDot, &tunnelDotColor, tunnelLabel, tc, tt)
	}

	refresh := func() {
		scmState, ok := queryServiceState()
		if !ok {
			scmState = lastState
		} else {
			lastState = scmState
		}
		cfg, cfgErr := config.LoadPartial(cfgPath)

		state.setService(serviceLamp{state: scmState, cfgValid: cfgErr == nil})

		repaintLamps()

		serverDisplay, remoteDisplay := readCacheDisplay(cfg.LabBridge.Host, cfg.LabBridge.User)
		serverLbl.SetText("Chisel server:    " + serverDisplay)
		remotePort.SetText("Remote port:      " + remoteDisplay)
		restPort.SetText(fmt.Sprintf("REST port:        %d", cfg.Rest.Port))
		discoveryLbl.SetText(fmt.Sprintf("Discovery:        include=%v, exclude=%v", cfg.Discovery.Include, cfg.Discovery.Exclude))
		rawSerialState := "disabled"
		if cfg.RawSerial.Enabled {
			rawSerialState = "enabled"
		}
		rawSerialLbl.SetText("Raw serial:       " + rawSerialState)
		logLevel.SetText("Log level:        " + cfg.Log.Level)

		switch {
		case pathsErr != nil:
			warnLabel.SetText("⚠ " + pathsErr.Error())
			warnLabel.SetVisible(true)
		case cfgErr != nil:
			warnLabel.SetText("⚠ " + cfgErr.Error())
			warnLabel.SetVisible(true)
		default:
			warnLabel.SetText("")
			warnLabel.SetVisible(false)
		}

		btns := ComputeButtons(scmState, cfgErr == nil)
		btnInstall.SetEnabled(btns.Install)
		btnUninstall.SetEnabled(btns.Uninstall)
		btnRestart.SetEnabled(btns.Restart)
		btnOpenCfg.SetEnabled(pathsErr == nil)
		btnOpenLogs.SetEnabled(pathsErr == nil)
	}

	performAdmin := func(action, successMsg string) {
		btnInstall.SetEnabled(false)
		btnUninstall.SetEnabled(false)
		btnRestart.SetEnabled(false)
		statusBar.SetText("Working…")

		errMsg, err := RunElevatedAdminAction(action)
		switch {
		case errors.Is(err, ErrUserCancelled):
			statusBar.SetText("Cancelled.")
		case err != nil:
			walk.MsgBox(mw, "Error", err.Error(), walk.MsgBoxIconError)
			statusBar.SetText("Failed.")
		case errMsg != "":
			walk.MsgBox(mw, "Error", errMsg, walk.MsgBoxIconError)
			statusBar.SetText("Failed.")
		default:
			statusBar.SetText(successMsg + " at " + time.Now().Format("15:04:05"))
		}
		refresh()
	}

	if err := (MainWindow{
		AssignTo: &mw,
		Title:    "SerialHop v" + version.Base(),
		Size:     Size{Width: 480, Height: 420},
		MinSize:  Size{Width: 480, Height: 420},
		Layout:   VBox{},
		Children: []Widget{
			GroupBox{
				Title:  "Status",
				Layout: Grid{Columns: 3},
				Children: []Widget{
					Label{Text: "Service:"},
					CustomWidget{
						AssignTo:         &serviceDot,
						MinSize:          Size{Width: 16, Height: 16},
						MaxSize:          Size{Width: 16, Height: 16},
						ClearsBackground: true,
						Paint:            makeCirclePaint(&serviceDotColor),
					},
					Label{AssignTo: &serviceLabel, Text: "…"},

					Label{Text: "Server:"},
					CustomWidget{
						AssignTo:         &serverDot,
						MinSize:          Size{Width: 16, Height: 16},
						MaxSize:          Size{Width: 16, Height: 16},
						ClearsBackground: true,
						Paint:            makeCirclePaint(&serverDotColor),
					},
					Label{AssignTo: &serverState, Text: "Checking…"},

					Label{Text: "Tunnel:"},
					CustomWidget{
						AssignTo:         &tunnelDot,
						MinSize:          Size{Width: 16, Height: 16},
						MaxSize:          Size{Width: 16, Height: 16},
						ClearsBackground: true,
						Paint:            makeCirclePaint(&tunnelDotColor),
					},
					Label{AssignTo: &tunnelLabel, Text: "Checking…"},
				},
			},
			Label{Text: "─── Configuration ─────────────────────────────"},
			Label{AssignTo: &serverLbl},
			Label{AssignTo: &remotePort},
			Label{AssignTo: &restPort},
			Label{AssignTo: &discoveryLbl},
			Label{AssignTo: &rawSerialLbl},
			Label{AssignTo: &logLevel},
			Label{AssignTo: &warnLabel, TextColor: walk.RGB(192, 0, 0)},
			Composite{
				AssignTo: &updateRow,
				Layout:   HBox{MarginsZero: true},
				Visible:  false,
				Children: []Widget{
					Label{AssignTo: &updateLabel, Text: ""},
					PushButton{AssignTo: &btnDownload, Text: "Download", Visible: false, OnClicked: func() {
						go ctlDownload(mw, ctl, httpClient, updateUA, installDir, statusBar,
							applyUpdateRow(mw, ctl, updateRow, updateLabel, btnDownload, btnInstall2, btnRelease, btnRetry, btnCancelDL))
					}},
					PushButton{AssignTo: &btnInstall2, Text: "Install update", Visible: false, OnClicked: func() {
						go ctlInstall(mw, ctl, statusBar,
							applyUpdateRow(mw, ctl, updateRow, updateLabel, btnDownload, btnInstall2, btnRelease, btnRetry, btnCancelDL))
					}},
					PushButton{AssignTo: &btnRelease, Text: "Release notes", Visible: false, OnClicked: func() {
						ctl.mu.Lock()
						url := ctl.release.HTMLURL
						ctl.mu.Unlock()
						if url != "" {
							_ = OpenWithDefaultApp(url)
						}
					}},
					PushButton{AssignTo: &btnRetry, Text: "Retry", Visible: false, OnClicked: func() {
						applyUpdateRow(mw, ctl, updateRow, updateLabel, btnDownload, btnInstall2, btnRelease, btnRetry, btnCancelDL)(EvRetry)
					}},
					PushButton{AssignTo: &btnCancelDL, Text: "Cancel", Visible: false, OnClicked: func() {
						ctl.mu.Lock()
						cancel := ctl.dlCancel
						ctl.mu.Unlock()
						if cancel != nil {
							cancel()
						}
					}},
				},
			},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					PushButton{AssignTo: &btnInstall, Text: "Install", OnClicked: func() { performAdmin("install", "Service installed") }},
					PushButton{AssignTo: &btnUninstall, Text: "Uninstall", OnClicked: func() { performAdmin("uninstall", "Service uninstalled") }},
					PushButton{AssignTo: &btnRestart, Text: "Restart", OnClicked: func() { performAdmin("restart", "Service restarted") }},
				},
			},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					PushButton{AssignTo: &btnOpenCfg, Text: "Open config file", OnClicked: func() {
						if err := OpenWithDefaultApp(paths.ConfigPath()); err != nil {
							walk.MsgBox(mw, "Error", err.Error(), walk.MsgBoxIconError)
						}
					}},
					PushButton{AssignTo: &btnOpenLogs, Text: "Open logs folder", OnClicked: func() {
						if err := OpenWithDefaultApp(paths.LogsDir()); err != nil {
							walk.MsgBox(mw, "Error", err.Error(), walk.MsgBoxIconError)
						}
					}},
				},
			},
			Label{AssignTo: &statusBar, Text: ""},
		},
	}).Create(); err != nil {
		return err
	}

	probeCtx, probeCancel := context.WithCancel(context.Background())
	defer probeCancel()
	mw.Closing().Attach(func(_ *bool, _ walk.CloseReason) { probeCancel() })

	probeHC := &http.Client{Timeout: 30 * time.Second} // fallback; per-call 5s ctx in probe.go still primary

	serverTrigger := make(chan struct{}, 1)
	tunnelTrigger := make(chan struct{}, 1)

	// kickProbes flips the affected lamps to "Checking…" and wakes their
	// probe goroutines. Must be called from the UI thread (it mutates
	// lamp state and repaints). Non-blocking — safe to call from button
	// handlers and from inside performAdmin.
	kickProbes := func(server, tunnel bool) {
		if server {
			state.setServer(netLamp{kind: lampChecking})
			trySend(serverTrigger)
		}
		if tunnel {
			state.setTunnel(netLamp{kind: lampChecking})
			trySend(tunnelTrigger)
		}
		repaintLamps()
	}
	_ = kickProbes // Silenced until Task 5 wires the first call site.

	go probeLoop(probeCtx, probeInterval, serverTrigger, func(ctx context.Context) {
		c, _ := config.LoadPartial(cfgPath)
		base := ""
		if c.LabBridge.Host != "" {
			base = "https://" + c.LabBridge.Host
		}
		runServerProbe(ctx, probeHC, base, userAgent, state)
		mw.Synchronize(repaintLamps)
	})
	go probeLoop(probeCtx, probeInterval, tunnelTrigger, func(ctx context.Context) {
		c, _ := config.LoadPartial(cfgPath)
		base := ""
		if c.LabBridge.Host != "" {
			base = "https://" + c.LabBridge.Host
		}
		runTunnelProbe(ctx, probeHC, base, c.LabBridge.User, c.LabBridge.Pass, userAgent, state)
		mw.Synchronize(repaintLamps)
	})

	timer, err := newTickTimer(mw, pollInterval, refresh)
	if err != nil {
		return err
	}
	defer timer.Dispose()

	// on-launch cleanup of old binary stub — unconditional, no network needed.
	_ = os.Remove(filepath.Join(installDir, "SerialHop.exe.old"))

	if autoUpdateEnabled {
		go func() {
			// Small delay so the panel paints first.
			time.Sleep(500 * time.Millisecond)
			runUpdateCheck(mw, ctl, httpClient, updateUA, installDir,
				applyUpdateRow(mw, ctl, updateRow, updateLabel, btnDownload, btnInstall2, btnRelease, btnRetry, btnCancelDL))
		}()

		// Periodic recheck (6 h).
		updateTicker, err := newTickTimer(mw, 6*time.Hour, func() {
			go runUpdateCheck(mw, ctl, httpClient, updateUA, installDir,
				applyUpdateRow(mw, ctl, updateRow, updateLabel, btnDownload, btnInstall2, btnRelease, btnRetry, btnCancelDL))
		})
		if err != nil {
			return err
		}
		defer updateTicker.Dispose()
	}

	refresh()
	mw.Run()
	return nil
}

// queryServiceState returns the current SCM state plus an "ok" flag.
// ok=false signals a transient SCM error (Connect failure, Query failure,
// etc.) — the panel should keep displaying the last-known state in that
// case to avoid blinking the indicator. ok=true with state==StateNotInstalled
// is the legitimate "service is not registered" reading; the SCM call itself
// succeeded. Uses DialSCMReadOnly so the panel can poll without admin
// elevation; install/uninstall/restart go through the elevated subprocess.
// succeeded.
func queryServiceState() (winsvc.ServiceState, bool) {
	scm, err := winsvc.DialSCMReadOnly()
	if err != nil {
		return winsvc.StateNotInstalled, false
	}
	defer scm.Disconnect() //nolint:errcheck // best-effort disconnect; error cannot be handled in defer
	s, err := scm.OpenService(winsvc.ServiceName)
	if err != nil {
		if errors.Is(err, winsvc.ErrServiceMissing) {
			return winsvc.StateNotInstalled, true
		}
		return winsvc.StateNotInstalled, false
	}
	defer s.Close() //nolint:errcheck // best-effort cleanup; error cannot be handled in defer
	st, err := s.Query()
	if err != nil {
		return winsvc.StateStopped, false
	}
	return st, true
}

// applyUpdateRow returns a function the caller invokes with a transition
// event. It marshals onto the GUI thread via mw.Synchronize, runs the state
// machine, and updates label text and button visibility. This is the single
// point where the panel UI reflects update state.
func applyUpdateRow(
	mw *walk.MainWindow,
	ctl *updateCtl,
	row *walk.Composite,
	label *walk.Label,
	btnDownload, btnInstall, btnRelease, btnRetry, btnCancel *walk.PushButton,
) func(ev UpdateEvent) {
	return func(ev UpdateEvent) {
		mw.Synchronize(func() {
			ctl.mu.Lock()
			ctl.state = nextUpdateState(ctl.state, ev)
			st := ctl.state
			tag := ctl.release.TagName
			ctl.mu.Unlock()

			row.SetVisible(st != UpdateIdle)
			// Hide every action button by default; the cases below opt-in.
			for _, b := range []*walk.PushButton{btnDownload, btnInstall, btnRelease, btnRetry, btnCancel} {
				b.SetVisible(false)
			}
			switch st {
			case UpdateAvailable:
				_ = label.SetText("Update: " + tag + " available")
				label.SetTextColor(walk.RGB(0, 0, 0))
				btnDownload.SetVisible(true)
				btnRelease.SetVisible(true)
			case UpdateDownloading:
				_ = label.SetText("Update: " + tag + " — downloading…")
				label.SetTextColor(walk.RGB(0, 0, 0))
				btnCancel.SetVisible(true)
			case UpdateDownloadFailed:
				_ = label.SetText("Update: " + tag + " — download failed")
				label.SetTextColor(walk.RGB(192, 0, 0))
				btnRetry.SetVisible(true)
			case UpdateReady:
				_ = label.SetText("Update: " + tag + " — ready to install")
				label.SetTextColor(walk.RGB(0, 0, 0))
				btnInstall.SetVisible(true)
				btnRelease.SetVisible(true)
			case UpdateInstalling:
				_ = label.SetText("Update: installing…")
				label.SetTextColor(walk.RGB(0, 0, 0))
			case UpdateInstalled:
				_ = label.SetText("Updated to " + tag + ". Close and reopen this window to load the new panel.")
				label.SetTextColor(walk.RGB(0, 128, 0))
			case UpdateInstallFailed:
				_ = label.SetText("Update failed — service restored to previous version.")
				label.SetTextColor(walk.RGB(192, 0, 0))
				btnRetry.SetVisible(true)
			}
		})
	}
}

// runUpdateCheck fetches the latest release, compares against the current
// version, and emits the appropriate event. Called from a goroutine; uses
// apply (which marshals onto the GUI thread).
func runUpdateCheck(
	mw *walk.MainWindow,
	ctl *updateCtl,
	hc *http.Client,
	userAgent, installDir string,
	apply func(UpdateEvent),
) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rel, err := updater.LatestRelease(ctx, hc, updater.DefaultReleasesURL, userAgent)
	if err != nil {
		writePanelDebugLog("update_check_failed", err)
		return
	}
	newer, err := updater.IsNewer(rel.TagName, version.Version)
	if err != nil {
		writePanelDebugLog("update_check_parse_failed", err)
		return
	}
	if !newer {
		return
	}
	// Locate the asset for this Windows binary.
	var exeAsset *updater.Asset
	for i := range rel.Assets {
		name := rel.Assets[i].Name
		if strings.HasPrefix(name, "SerialHop-v") && strings.HasSuffix(name, ".exe") {
			exeAsset = &rel.Assets[i]
			break
		}
	}
	if exeAsset == nil {
		writePanelDebugLog("update_check_no_asset", fmt.Errorf("no SerialHop-v*.exe asset on release %s", rel.TagName))
		return
	}

	// Resume-from-disk: if a staged file under <installDir>/<assetName>
	// already exists, re-verify it against the current sums file. If it
	// matches, jump straight to UpdateReady.
	stagedPath := filepath.Join(installDir, exeAsset.Name)
	if _, err := os.Stat(stagedPath); err == nil {
		sumsAsset := rel.AssetByName("SHA256SUMS.txt")
		if sumsAsset != nil {
			body, ferr := fetchSums(hc, userAgent, sumsAsset.BrowserDownloadURL)
			if ferr == nil && updater.VerifyFile(stagedPath, body, exeAsset.Name) == nil {
				ctl.mu.Lock()
				ctl.release = rel
				ctl.exeAsset = exeAsset
				ctl.exeFile = stagedPath
				ctl.mu.Unlock()
				apply(EvUpdateAvailable)
				apply(EvDownloadStart)
				apply(EvDownloadOK)
				cleanupStaleStagedFiles(installDir, exeAsset.Name)
				return
			}
		}
		// Stale or unverifiable staged file: delete it.
		_ = os.Remove(stagedPath)
	}

	cleanupStaleStagedFiles(installDir, exeAsset.Name)

	ctl.mu.Lock()
	ctl.release = rel
	ctl.exeAsset = exeAsset
	ctl.mu.Unlock()
	apply(EvUpdateAvailable)
}

func ctlDownload(
	mw *walk.MainWindow,
	ctl *updateCtl,
	hc *http.Client,
	userAgent, installDir string,
	statusBar *walk.Label,
	apply func(UpdateEvent),
) {
	ctl.mu.Lock()
	rel := ctl.release
	asset := ctl.exeAsset
	ctl.mu.Unlock()
	if asset == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	ctl.mu.Lock()
	ctl.dlCancel = cancel
	ctl.mu.Unlock()
	defer func() {
		ctl.mu.Lock()
		ctl.dlCancel = nil
		ctl.mu.Unlock()
		cancel()
	}()

	apply(EvDownloadStart)

	dest := filepath.Join(installDir, asset.Name)
	var lastReport time.Time
	progress := func(received, total int64) {
		if time.Since(lastReport) < 200*time.Millisecond && (total <= 0 || received < total) {
			return
		}
		lastReport = time.Now()
		var msg string
		if total > 0 {
			pct := float64(received) / float64(total) * 100
			msg = fmt.Sprintf("Downloading %.0f%% (%.1f / %.1f MB)", pct, float64(received)/1e6, float64(total)/1e6)
		} else {
			msg = fmt.Sprintf("Downloading %.1f MB", float64(received)/1e6)
		}
		mw.Synchronize(func() { _ = statusBar.SetText(msg) })
	}
	if err := updater.Download(ctx, hc, asset.BrowserDownloadURL, dest, userAgent, progress); err != nil {
		if errors.Is(err, context.Canceled) {
			mw.Synchronize(func() { _ = statusBar.SetText("Download cancelled.") })
			apply(EvCancel)
			return
		}
		writePanelDebugLog("update_download_failed", err)
		apply(EvDownloadFail)
		return
	}

	sumsAsset := rel.AssetByName("SHA256SUMS.txt")
	if sumsAsset == nil {
		_ = os.Remove(dest)
		writePanelDebugLog("update_no_sums_asset", fmt.Errorf("release %s has no SHA256SUMS.txt", rel.TagName))
		apply(EvDownloadFail)
		return
	}
	body, err := fetchSums(hc, userAgent, sumsAsset.BrowserDownloadURL)
	if err != nil {
		_ = os.Remove(dest)
		writePanelDebugLog("update_fetch_sums_failed", err)
		apply(EvDownloadFail)
		return
	}
	if err := updater.VerifyFile(dest, body, asset.Name); err != nil {
		_ = os.Remove(dest)
		writePanelDebugLog("update_verify_failed", err)
		apply(EvDownloadFail)
		return
	}

	ctl.mu.Lock()
	ctl.exeFile = dest
	ctl.mu.Unlock()

	mw.Synchronize(func() { _ = statusBar.SetText("Download complete.") })
	apply(EvDownloadOK)
}

func ctlInstall(
	mw *walk.MainWindow,
	ctl *updateCtl,
	statusBar *walk.Label,
	apply func(UpdateEvent),
) {
	ctl.mu.Lock()
	src := ctl.exeFile
	ctl.mu.Unlock()
	if src == "" {
		return
	}
	apply(EvInstallStart)
	mw.Synchronize(func() { _ = statusBar.SetText("Installing update…") })

	errMsg, err := RunElevatedAdminAction("update", "--update-src="+src)
	switch {
	case errors.Is(err, ErrUserCancelled):
		mw.Synchronize(func() { _ = statusBar.SetText("Cancelled.") })
		apply(EvCancel)
		return
	case err != nil:
		mw.Synchronize(func() { _ = statusBar.SetText("Failed: " + err.Error()) })
		apply(EvInstallFail)
		return
	case errMsg != "":
		mw.Synchronize(func() { _ = statusBar.SetText("Failed: " + errMsg) })
		apply(EvInstallFail)
		return
	}

	mw.Synchronize(func() { _ = statusBar.SetText("Update applied at " + time.Now().Format("15:04:05")) })
	apply(EvInstallOK)
}

func fetchSums(hc *http.Client, userAgent, url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("fetch sums: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB cap
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// cleanupStaleStagedFiles deletes any SerialHop-v*.exe inside installDir
// that doesn't match the current latest-release asset name. Best-effort.
func cleanupStaleStagedFiles(installDir, keep string) {
	matches, _ := filepath.Glob(filepath.Join(installDir, "SerialHop-v*.exe"))
	for _, m := range matches {
		if filepath.Base(m) == keep {
			continue
		}
		_ = os.Remove(m)
	}
}

// readCacheDisplay returns the strings used in the panel's
// "Chisel server" and "Remote port" labels. When the bootstrap cache
// exists and is anchored to user, the cached values are shown; otherwise
// host:<…> and "…". Errors are swallowed silently (lamps already surface
// connectivity problems).
func readCacheDisplay(host, user string) (server, remote string) {
	if host == "" {
		return "—", "—"
	}
	c, err := bootstrap.ReadCache(paths.ServerInfoCachePath(), user)
	if err != nil {
		return net.JoinHostPort(host, "…"), "…"
	}
	return net.JoinHostPort(host, strconv.Itoa(c.ServerInfo.ChiselListenPort)), strconv.Itoa(c.RemotePort)
}
