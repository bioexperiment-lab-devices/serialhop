//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/app"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/panel"
	internalversion "github.com/bioexperiment-lab-devices/serialhop/internal/version"
	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

func init() {
	// walk requires the GUI thread to be locked to a single OS thread for
	// the lifetime of the process. main goroutine drives the UI in panel mode.
	runtime.LockOSThread()
}

const configFileName = "SerialHop_config.yaml"

var (
	flagAdminAction = flag.String("admin-action", "", "internal: install|uninstall|restart|update (used by the GUI)")
	flagErrorFile   = flag.String("error-file", "", "internal: path the elevated child writes its error message to")
	flagUpdateSrc   = flag.String("update-src", "", "internal: path to the staged update .exe (used by --admin-action=update)")
	flagForeground  = flag.Bool("foreground", false, "run the device-client logic in the console (developer mode)")
	flagVersion     = flag.Bool("version", false, "print version and exit")
)

func main() {
	flag.Parse()

	if *flagVersion {
		fmt.Println(internalversion.Version)
		return
	}

	if flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "fatal: unexpected positional arguments:", flag.Args())
		os.Exit(2)
	}

	isService, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal: detect SCM context:", err)
		os.Exit(1)
	}

	switch {
	case isService:
		if err := winsvc.RunWorker(); err != nil {
			slog.Error("RunWorker failed", "err", err)
			os.Exit(1)
		}
	case *flagAdminAction != "":
		os.Exit(winsvc.RunAdminAction(*flagAdminAction, *flagErrorFile, *flagUpdateSrc))
	case *flagForeground:
		attachParentConsole()
		if err := runForeground(); err != nil {
			fmt.Fprintln(os.Stderr, "fatal:", err)
			os.Exit(1)
		}
	default:
		if err := panel.Run(); err != nil {
			writePanelStartupError(err)
			fmt.Fprintln(os.Stderr, "fatal:", err)
			os.Exit(1)
		}
	}
}

// writePanelStartupError records a panel startup failure to a file next to the
// .exe so the operator can see what went wrong. Stderr is `/dev/null` under
// the windowsgui subsystem, so without this the failure is invisible.
func writePanelStartupError(panelErr error) {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	logPath := filepath.Join(filepath.Dir(exePath), "SerialHop_panel_error.log")
	line := fmt.Sprintf("%s panel startup failed: %v\n", time.Now().Format(time.RFC3339), panelErr)
	_ = os.WriteFile(logPath, []byte(line), 0o600)
}

func runForeground() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	cfgPath := filepath.Join(filepath.Dir(exePath), configFileName)

	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		f, err := os.Create(cfgPath)
		if err != nil {
			return fmt.Errorf("create scaffold: %w", err)
		}
		if writeErr := config.WriteScaffold(f); writeErr != nil {
			_ = f.Close()
			return fmt.Errorf("write scaffold: %w", writeErr)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close scaffold: %w", err)
		}
		fmt.Printf("Config file created at %s. Please review and edit it, then run again.\n", cfgPath)
		return errors.New("config scaffold generated; please edit and rerun")
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	configureStdoutLogger(cfg.Log.Level)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return app.Run(ctx, cfg)
}

func attachParentConsole() {
	modKernel32 := windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole := modKernel32.NewProc("AttachConsole")
	procAttachConsole.Call(uintptr(^uint32(0)))
}

func configureStdoutLogger(level string) {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "info":
		l = slog.LevelInfo
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	slog.SetDefault(slog.New(h))
}
