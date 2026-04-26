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

	"github.com/khamitovdr/lab_devices_client/internal/api"
	"github.com/khamitovdr/lab_devices_client/internal/chisel"
	"github.com/khamitovdr/lab_devices_client/internal/config"
	"github.com/khamitovdr/lab_devices_client/internal/discovery"
	"github.com/khamitovdr/lab_devices_client/internal/registry"
	labserial "github.com/khamitovdr/lab_devices_client/internal/serial"
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
	slog.Info("lab_devices_client starting",
		"chisel_server", cfg.Chisel.Server,
		"remote_port", cfg.Chisel.RemotePort,
		"rest_port", cfg.Rest.Port,
		"discovery_include", cfg.Discovery.Include,
		"discovery_exclude", cfg.Discovery.Exclude,
	)

	listener, localPort, err := api.Listen(cfg.Rest.Port)
	if err != nil {
		return fmt.Errorf("bind rest: %w", err)
	}
	slog.Info("rest listening", "addr", listener.Addr().String())

	reg := registry.New()
	opener := labserial.NewRealOpener()
	include := append([]string(nil), cfg.Discovery.Include...)
	exclude := append([]string(nil), cfg.Discovery.Exclude...)

	discoverFn := func(ctx context.Context) ([]*registry.Device, error) {
		all, err := opener.List()
		if err != nil {
			return nil, fmt.Errorf("list ports: %w", err)
		}
		ports := discovery.FilterPorts(all, include, exclude)
		slog.Info("discovery: starting", "candidates", ports)
		return discovery.Run(ctx, opener, ports)
	}

	srv := api.New(reg, discoverFn)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	chiselDone := make(chan error, 1)
	go func() {
		chiselDone <- chisel.Run(ctx, chisel.Config{
			Server:     cfg.Chisel.Server,
			User:       cfg.Chisel.User,
			Pass:       cfg.Chisel.Pass,
			RemotePort: cfg.Chisel.RemotePort,
			LocalPort:  localPort,
		})
	}()

	apiDone := make(chan error, 1)
	go func() {
		apiDone <- api.Serve(ctx, listener, srv.Handler())
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case err := <-chiselDone:
		slog.Error("chisel exited", "err", err)
		cancel()
	case err := <-apiDone:
		slog.Error("rest server exited", "err", err)
		cancel()
	}

	// Drain remaining goroutines.
	<-chiselDone
	<-apiDone

	// Close any open serial ports.
	reg.Replace(nil)
	slog.Info("shutdown complete")
	return nil
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
