package config

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Load reads, parses, and validates a config file at path.
// Returns os.IsNotExist-compatible error if the file is missing.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is user-supplied config file; intentional
	if err != nil {
		return Config{}, err
	}
	c := Default()
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := Validate(&c); err != nil {
		return Config{}, fmt.Errorf("validate %s: %w", path, err)
	}
	return c, nil
}

// LoadPartial parses path and returns whatever fields were populated, plus
// the first validation error (or nil if valid). Distinct from Load, which
// returns a zero Config on validation failure. Used by the GUI panel to
// display current config values alongside any validation warning.
func LoadPartial(path string) (Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is user-supplied config file; intentional
	if err != nil {
		return Default(), err
	}
	c := Default()
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Default(), fmt.Errorf("parse %s: %w", path, err)
	}
	return c, Validate(&c)
}

// Validate checks the config for invariants documented in the spec.
func Validate(c *Config) error {
	if c.Chisel.Server == "" {
		return fmt.Errorf("chisel.server must be non-empty")
	}
	host, port, err := net.SplitHostPort(c.Chisel.Server)
	if err != nil || host == "" {
		return fmt.Errorf("chisel.server must be host:port (got %q)", c.Chisel.Server)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("chisel.server must be host:port (got %q)", c.Chisel.Server)
	}
	if c.Chisel.RemotePort < 1 || c.Chisel.RemotePort > 65535 {
		return fmt.Errorf("chisel.remote_port must be in 1..65535 (got %d)", c.Chisel.RemotePort)
	}
	if c.Rest.Port < 0 || c.Rest.Port > 65535 {
		return fmt.Errorf("rest.port must be in 0..65535 (got %d)", c.Rest.Port)
	}
	if len(c.Discovery.Include) > 0 && len(c.Discovery.Exclude) > 0 {
		return fmt.Errorf("discovery.include and discovery.exclude are mutually exclusive")
	}
	if c.Discovery.PostOpenSettleMs < 0 {
		return fmt.Errorf("discovery.post_open_settle_ms must be >= 0 (got %d)", c.Discovery.PostOpenSettleMs)
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level must be one of debug|info|warn|error (got %q)", c.Log.Level)
	}
	return nil
}
