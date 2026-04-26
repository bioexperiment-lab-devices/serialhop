package chisel

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	chclient "github.com/jpillora/chisel/client"
)

// Config is the subset of chisel client options this app exposes.
type Config struct {
	Server     string // host:port (no scheme)
	User       string // empty = no auth
	Pass       string
	RemotePort int
	LocalPort  int
}

// Run blocks until ctx is cancelled or the chisel client encounters a fatal
// error. Reconnect / backoff are handled internally by chisel (default
// KeepAlive=25s, MaxRetryInterval=5min, unbounded retries).
func Run(ctx context.Context, cfg Config) error {
	if _, _, err := net.SplitHostPort(cfg.Server); err != nil {
		return fmt.Errorf("invalid server %q: %w", cfg.Server, err)
	}
	auth := ""
	if cfg.User != "" {
		auth = cfg.User + ":" + cfg.Pass
	}
	remotes := []string{
		fmt.Sprintf("R:%d:127.0.0.1:%d", cfg.RemotePort, cfg.LocalPort),
	}
	c, err := chclient.NewClient(&chclient.Config{
		Server:           "http://" + cfg.Server,
		Auth:             auth,
		Remotes:          remotes,
		KeepAlive:        25 * time.Second,
		MaxRetryInterval: 5 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("new chisel client: %w", err)
	}
	c.Logger.Info = false   // we route logs through slog
	c.Logger.Debug = false
	slog.Info("chisel: starting",
		"server", cfg.Server,
		"remote_port", cfg.RemotePort,
		"local_port", cfg.LocalPort,
		"auth", cfg.User != "")
	if err := c.Start(ctx); err != nil {
		return fmt.Errorf("start chisel client: %w", err)
	}
	if err := c.Wait(); err != nil {
		return fmt.Errorf("chisel client: %w", err)
	}
	return nil
}
