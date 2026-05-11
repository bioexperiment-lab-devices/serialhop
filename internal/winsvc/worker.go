//go:build windows

package winsvc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/app"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/logship"
	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
	"github.com/bioexperiment-lab-devices/serialhop/internal/version"

	"golang.org/x/sys/windows/svc"
)

const (
	workerStopGracePeriod = 30 * time.Second
	logshipShutdown       = 2 * time.Second
)

// RunWorker is the service-mode entry point. It must only be called when
// svc.IsWindowsService() returns true. It initializes log streaming
// before svc.Run so that even a config-load failure is captured both
// on disk and (if a previous successful run cached chisel auth) in
// Loki on the next push.
func RunWorker() error {
	if err := paths.EnsureDirs(); err != nil {
		return fmt.Errorf("paths setup: %w", err)
	}

	manager, err := logship.Init(version.Version, slog.LevelInfo)
	if err != nil {
		return fmt.Errorf("logship init: %w", err)
	}

	return svc.Run(ServiceName, &handler{manager: manager})
}

type handler struct {
	manager *logship.Manager
}

func (h *handler) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}

	cfgPath := paths.ConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("config load failed", "path", cfgPath, "err", err)
		h.shutdownLogship()
		changes <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
		return false, 1
	}
	h.manager.SetLevel(logship.ParseLogLevel(cfg.Log.Level))
	h.manager.StartShipper(cfg.LabBridge.User)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	appDone := make(chan error, 1)
	go func() {
		appDone <- app.Run(ctx, cfg)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: accepts}

	for {
		select {
		case req := <-r:
			switch req.Cmd {
			case svc.Interrogate:
				changes <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				slog.Info("service stop requested")
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case err := <-appDone:
					if err != nil {
						slog.Error("app exited with error during stop", "err", err)
					}
				case <-time.After(workerStopGracePeriod):
					slog.Error("app did not exit within grace period; forcing stop", "grace", workerStopGracePeriod)
				}
				h.shutdownLogship()
				changes <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		case err := <-appDone:
			if err != nil {
				slog.Error("app exited unexpectedly", "err", err)
				h.shutdownLogship()
				changes <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
				return false, 1
			}
			slog.Info("app exited cleanly")
			h.shutdownLogship()
			changes <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

func (h *handler) shutdownLogship() {
	ctx, cancel := context.WithTimeout(context.Background(), logshipShutdown)
	defer cancel()
	h.manager.Shutdown(ctx)
}
