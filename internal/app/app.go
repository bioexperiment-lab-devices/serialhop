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
	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/densitometer"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/pump"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/valve"
	"github.com/bioexperiment-lab-devices/serialhop/internal/discovery"
	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher"
	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
	"github.com/bioexperiment-lab-devices/serialhop/internal/power"
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
	if err := writeActualRestPort(paths.ServerInfoCachePath(), cfg.LabBridge.User, localPort); err != nil {
		slog.Warn("failed to write actual rest port to cache", "err", err)
	}

	discovery.PostOpenSettle = time.Duration(cfg.Discovery.PostOpenSettleMs) * time.Millisecond

	// Driver registration happens here, at wiring time — never in init()
	// (spec §2.4), so tests can register fakes under unused type codes.
	pump.Register()
	densitometer.Register()
	valve.Register()

	reg := registry.New()
	opener := labserial.NewRealOpener()
	include := append([]string(nil), cfg.Discovery.Include...)
	exclude := append([]string(nil), cfg.Discovery.Exclude...)

	stateDir := paths.DeviceStateDir()
	if stateDir == "" {
		slog.Warn("device state: no data dir available; state files will land in the working directory")
	}
	// reprobe re-identifies a device during background reattach. Opening a
	// port pulses DTR and reboots Arduino-class boards (see
	// discovery.PostOpenSettle) — a reattach reopens the port, so probing
	// before the settle would hit the bootloader window on every retry.
	reprobe := func(p labserial.Port) ([]byte, error) {
		time.Sleep(discovery.PostOpenSettle)
		reply, _, err := discovery.Probe(p)
		return reply, err
	}
	discoverFn := func(reqCtx context.Context) ([]*device.Session, error) {
		all, err := opener.List()
		if err != nil {
			return nil, fmt.Errorf("list ports: %w", err)
		}
		ports := discovery.FilterPorts(all, include, exclude)
		slog.Info("discovery: starting", "candidates", ports)
		matches, err := discovery.Run(reqCtx, opener, ports)
		if err != nil {
			return nil, err
		}
		sessions := make([]*device.Session, 0, len(matches))
		for _, m := range matches {
			name, factory, ok := device.LookupDriver(m.TypeCode)
			if !ok {
				// The probe classified it, so a missing factory is a wiring bug.
				slog.Error("discovery: no driver registered", "type_code", int(m.TypeCode), "port", m.Port)
				_ = m.Conn.Close()
				continue
			}
			sess := device.NewSession(device.SessionConfig{
				ID: m.ID, Type: name, TypeCode: m.TypeCode, PortName: m.Port,
				Conn: m.Conn, Opener: opener, StateDir: stateDir,
				Factory: factory, ProbeReply: m.Reply, Reprobe: reprobe,
			})
			sess.Start(ctx) // app-lifetime ctx: sessions die on shutdown
			sessions = append(sessions, sess)
		}
		return sessions, nil
	}

	backupDir := cfg.Flashing.BackupDir
	if backupDir == "" {
		backupDir = paths.BackupsDir()
	}
	if backupDir == "" {
		slog.Warn("flashing: no backup dir available; flashing forced off")
	}
	var fl flasher.Flasher
	if backupDir != "" {
		var err error
		fl, err = flasher.New(opener, backupDir, cfg.Flashing.KeepN, discovery.PostOpenSettle)
		if err != nil {
			return fmt.Errorf("flasher init: %w", err)
		}
	}
	flashingEnabled := cfg.Flashing.Enabled && fl != nil
	keepAwake, err := power.New()
	if err != nil {
		return fmt.Errorf("power.New: %w", err)
	}
	defer func() { _ = keepAwake.Close() }()
	srv := api.New(reg, discoverFn, opener, fl, flashingEnabled, keepAwake,
		cfg.RawSerial.Enabled, time.Duration(cfg.RawSerial.IdleTimeoutMs)*time.Millisecond)

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

	// Close every session: each driver detaches gracefully (persisting its
	// state) before its port is closed.
	reg.CloseAll()
	slog.Info("shutdown complete")
	return runErr
}

// writeActualRestPort updates the bootstrap cache with the port the local
// REST listener actually bound to. Called once after api.Listen returns.
// Silently no-ops if the cache is missing or anchored to a different user;
// the panel falls back to its "service unreachable" empty state in that case.
func writeActualRestPort(cachePath, user string, port int) error {
	c, err := bootstrap.ReadCache(cachePath, user)
	if err != nil {
		// ErrCacheMissing or anchored-to-other-user: silently skip.
		return nil
	}
	c.ActualRestPort = port
	return bootstrap.WriteCache(cachePath, c)
}
