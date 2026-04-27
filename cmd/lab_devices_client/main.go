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
	"syscall"

	"github.com/khamitovdr/lab_devices_client/internal/app"
	"github.com/khamitovdr/lab_devices_client/internal/config"
	"github.com/khamitovdr/lab_devices_client/internal/panel"
	"github.com/khamitovdr/lab_devices_client/internal/winsvc"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

const configFileName = "lab_devices_client_config.yaml"

var (
	flagAdminAction = flag.String("admin-action", "", "internal: install|uninstall|restart (used by the GUI)")
	flagErrorFile   = flag.String("error-file", "", "internal: path the elevated child writes its error message to")
	flagForeground  = flag.Bool("foreground", false, "run the device-client logic in the console (developer mode)")
)

func main() {
	flag.Parse()

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
		os.Exit(winsvc.RunAdminAction(*flagAdminAction, *flagErrorFile))
	case *flagForeground:
		attachParentConsole()
		if err := runForeground(); err != nil {
			fmt.Fprintln(os.Stderr, "fatal:", err)
			os.Exit(1)
		}
	default:
		if err := panel.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "fatal:", err)
			os.Exit(1)
		}
	}
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
		if err := config.WriteScaffold(f); err != nil {
			f.Close()
			return fmt.Errorf("write scaffold: %w", err)
		}
		f.Close()
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
