package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/api"
	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
	"github.com/bioexperiment-lab-devices/serialhop/internal/chisel"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/discovery"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

func Run(ctx context.Context, cfg config.Config, resolved bootstrap.Resolved) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	slog.Info("serialhop starting",
		"chisel_host", cfg.LabBridge.Host,
		"chisel_port", resolved.ServerInfo.ChiselListenPort,
		"remote_port", resolved.RemotePort,
		"rest_port", cfg.Rest.Port,
		"discovery_include", cfg.Discovery.Include,
		"discovery_exclude", cfg.Discovery.Exclude,
		"discovery_post_open_settle_ms", cfg.Discovery.PostOpenSettleMs,
		"forward_tunnels", len(resolved.ServerInfo.ForwardTunnels),
	)

	listener, localPort, err := api.Listen(cfg.Rest.Port)
	if err != nil {
		return fmt.Errorf("bind rest: %w", err)
	}
	slog.Info("rest listening", "addr", listener.Addr().String())

	discovery.PostOpenSettle = time.Duration(cfg.Discovery.PostOpenSettleMs) * time.Millisecond

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

	srv := api.New(reg, discoverFn, opener, cfg.RawSerial.Enabled, nil, false)

	chiselDone := make(chan error, 1)
	go func() {
		chiselDone <- chisel.Run(ctx, chisel.Config{
			Server:         net.JoinHostPort(cfg.LabBridge.Host, strconv.Itoa(resolved.ServerInfo.ChiselListenPort)),
			User:           cfg.LabBridge.User,
			Pass:           cfg.LabBridge.Pass,
			RemotePort:     resolved.RemotePort,
			LocalPort:      localPort,
			ForwardTunnels: resolved.ServerInfo.ForwardTunnels,
		})
	}()

	apiDone := make(chan error, 1)
	go func() {
		apiDone <- api.Serve(ctx, listener, srv.Handler())
	}()

	var runErr error
	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case err := <-chiselDone:
		slog.Error("chisel exited", "err", err)
		if err != nil {
			runErr = fmt.Errorf("chisel: %w", err)
		}
		cancel()
	case err := <-apiDone:
		slog.Error("rest server exited", "err", err)
		if err != nil {
			runErr = fmt.Errorf("rest: %w", err)
		}
		cancel()
	}

	<-chiselDone
	<-apiDone

	reg.Replace(nil)
	slog.Info("shutdown complete")
	return runErr
}
