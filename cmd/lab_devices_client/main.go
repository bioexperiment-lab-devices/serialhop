package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/khamitovdr/lab_devices_client/internal/app"
	"github.com/khamitovdr/lab_devices_client/internal/config"
)

const configFileName = "lab_devices_client_config.yaml"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
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

	configureLogger(cfg.Log.Level)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return app.Run(ctx, cfg)
}

func configureLogger(level string) {
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
