//go:build windows

package panel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/version"
	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

const (
	configFileName = "SerialHop_config.yaml"
	logFileName    = "SerialHop.log"
	pollInterval   = 1 * time.Second
)

// Run opens the control-panel window and blocks until the user closes it.
func Run() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	dir := filepath.Dir(exePath)
	cfgPath := filepath.Join(dir, configFileName)
	logPath := filepath.Join(dir, logFileName)

	if err := ensureScaffold(cfgPath); err != nil {
		// Non-fatal: the panel can still run; it'll show "config missing".
		_ = err
	}

	var (
		mw          *walk.MainWindow
		statusDot   *walk.Label
		statusLabel *walk.Label
		warnLabel   *walk.Label
		statusBar   *walk.Label

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
		btnOpenLog   *walk.PushButton
	)

	// Last-known SCM state. On transient SCM errors we keep showing this
	// instead of blinking to "Not installed".
	lastState := winsvc.StateNotInstalled

	refresh := func() {
		state, ok := queryServiceState()
		if !ok {
			state = lastState
		} else {
			lastState = state
		}
		cfg, cfgErr := config.LoadPartial(cfgPath)
		_, logStatErr := os.Stat(logPath)
		logExists := logStatErr == nil

		statusLabel.SetText(state.String())
		statusDot.SetText("●")
		switch StatusIndicator(state, cfgErr == nil) {
		case ColorGreen:
			statusDot.SetTextColor(walk.RGB(0, 160, 0))
		case ColorYellow:
			statusDot.SetTextColor(walk.RGB(200, 160, 0))
		case ColorRed:
			statusDot.SetTextColor(walk.RGB(192, 0, 0))
		default:
			statusDot.SetTextColor(walk.RGB(128, 128, 128))
		}

		serverLbl.SetText("Chisel server:    " + cfg.Chisel.Server)
		remotePort.SetText(fmt.Sprintf("Remote port:      %d", cfg.Chisel.RemotePort))
		restPort.SetText(fmt.Sprintf("REST port:        %d", cfg.Rest.Port))
		discoveryLbl.SetText(fmt.Sprintf("Discovery:        include=%v, exclude=%v", cfg.Discovery.Include, cfg.Discovery.Exclude))
		logLevel.SetText("Log level:        " + cfg.Log.Level)
		if cfg.RawSerial.Enabled {
			rawSerialLbl.SetText("Raw serial:       enabled")
		} else {
			rawSerialLbl.SetText("Raw serial:       disabled")
		}

		if cfgErr != nil {
			warnLabel.SetText("⚠ " + cfgErr.Error())
			warnLabel.SetVisible(true)
		} else {
			warnLabel.SetText("")
			warnLabel.SetVisible(false)
		}

		btns := ComputeButtons(state, cfgErr == nil, logExists)
		btnInstall.SetEnabled(btns.Install)
		btnUninstall.SetEnabled(btns.Uninstall)
		btnRestart.SetEnabled(btns.Restart)
		btnOpenLog.SetEnabled(btns.OpenLog)
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
		Size:     Size{Width: 480, Height: 360},
		MinSize:  Size{Width: 480, Height: 360},
		Layout:   VBox{},
		Children: []Widget{
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					Label{Text: "Status:"},
					Label{AssignTo: &statusDot, Text: "●", MinSize: Size{Width: 16}},
					Label{AssignTo: &statusLabel, Text: "…"},
				},
			},
			Label{Text: "─── Configuration ─────────────────────────────"},
			Label{AssignTo: &serverLbl},
			Label{AssignTo: &remotePort},
			Label{AssignTo: &restPort},
			Label{AssignTo: &discoveryLbl},
			Label{AssignTo: &logLevel},
			Label{AssignTo: &rawSerialLbl},
			Label{AssignTo: &warnLabel, TextColor: walk.RGB(192, 0, 0)},
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
						if err := OpenWithDefaultApp(cfgPath); err != nil {
							walk.MsgBox(mw, "Error", err.Error(), walk.MsgBoxIconError)
						}
					}},
					PushButton{AssignTo: &btnOpenLog, Text: "Open log file", OnClicked: func() {
						if err := OpenWithDefaultApp(logPath); err != nil {
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

	timer, err := newTickTimer(mw, pollInterval, refresh)
	if err != nil {
		return err
	}
	defer timer.Dispose()

	refresh()
	mw.Run()
	return nil
}

func ensureScaffold(cfgPath string) error {
	if _, err := os.Stat(cfgPath); err == nil {
		return nil
	}
	f, err := os.Create(cfgPath)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // best-effort cleanup; write errors returned by WriteScaffold are the priority
	return config.WriteScaffold(f)
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
