package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/khamitovdr/lab_devices_client/internal/api"
	"github.com/khamitovdr/lab_devices_client/internal/chisel"
	"github.com/khamitovdr/lab_devices_client/internal/config"
	"github.com/khamitovdr/lab_devices_client/internal/discovery"
	"github.com/khamitovdr/lab_devices_client/internal/registry"
	labserial "github.com/khamitovdr/lab_devices_client/internal/serial"
)

func Run(ctx context.Context, cfg config.Config) error {
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
	case err := <-apiDone:
		slog.Error("rest server exited", "err", err)
	}

	<-chiselDone
	<-apiDone

	reg.Replace(nil)
	slog.Info("shutdown complete")
	return nil
}
