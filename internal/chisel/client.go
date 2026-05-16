package chisel

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	chclient "github.com/jpillora/chisel/client"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

// Config is the subset of chisel client options this app exposes.
type Config struct {
	Server         string // host:port (no scheme)
	User           string // empty = no auth
	Pass           string
	RemotePort     int
	LocalPort      int
	ForwardTunnels []labbridge.ForwardTunnel
}

// buildRemotes returns the list of chisel route strings for cfg. The
// reverse route exposes the local REST server; each ForwardTunnel is
// rendered as <local>:<remote>.
func buildRemotes(cfg Config) []string {
	out := []string{fmt.Sprintf("R:%d:127.0.0.1:%d", cfg.RemotePort, cfg.LocalPort)}
	for _, t := range cfg.ForwardTunnels {
		out = append(out, fmt.Sprintf("%s:%s", t.Local, t.Remote))
	}
	return out
}

func Run(ctx context.Context, cfg Config) error {
	if _, _, err := net.SplitHostPort(cfg.Server); err != nil {
		slog.Error("chisel run starting", "err", err.Error())
		return fmt.Errorf("invalid server %q: %w", cfg.Server, err)
	}
	auth := ""
	if cfg.User != "" {
		auth = cfg.User + ":" + cfg.Pass
	}
	remotes := buildRemotes(cfg)
	c, err := chclient.NewClient(&chclient.Config{
		Server:           "http://" + cfg.Server,
		Auth:             auth,
		Remotes:          remotes,
		KeepAlive:        25 * time.Second,
		MaxRetryInterval: 5 * time.Minute,
		MaxRetryCount:    -1, // unbounded; default 0 means "give up after first failed attempt"
	})
	if err != nil {
		return fmt.Errorf("new chisel client: %w", err)
	}
	// Chisel's internal logger writes to stderr, which is /dev/null under the
	// windowsgui subsystem. The service worker re-points os.Stderr at a pipe
	// (see logship.installStderrTap) so chisel's state-change logs are
	// captured to SerialHop_stderr.log and shipped to Loki.
	c.Info = true
	c.Debug = false
	slog.Info("chisel run starting",
		"server", cfg.Server,
		"remote_port", cfg.RemotePort,
		"local_port", cfg.LocalPort,
		"auth", cfg.User != "",
		"routes_count", len(remotes),
		"forward_tunnels", len(cfg.ForwardTunnels))
	slog.Debug("chisel routes", "routes", remotes)
	if err := c.Start(ctx); err != nil {
		return fmt.Errorf("start chisel client: %w", err)
	}
	slog.Info("chisel run waiting (session live)")
	if err := c.Wait(); err != nil {
		if ctx.Err() != nil {
			slog.Info("chisel session ended", "reason", "context cancelled")
		} else {
			slog.Warn("chisel session lost", "reason", err.Error())
		}
		return fmt.Errorf("chisel client: %w", err)
	}
	slog.Info("chisel run exiting", "err", "")
	return nil
}
