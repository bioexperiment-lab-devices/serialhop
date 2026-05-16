package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

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
	if err := ValidateHost(c.LabBridge.Host); err != nil {
		return err
	}
	if c.LabBridge.User == "" {
		return fmt.Errorf("lab_bridge.user must be non-empty")
	}
	if c.LabBridge.Pass == "" {
		return fmt.Errorf("lab_bridge.pass must be non-empty")
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
	if c.Flashing.KeepN < 0 {
		return fmt.Errorf("flashing.keep_n must be >= 0 (got %d)", c.Flashing.KeepN)
	}
	if c.Flashing.Enabled && c.Flashing.BackupDir != "" && !filepath.IsAbs(c.Flashing.BackupDir) {
		return fmt.Errorf("flashing.backup_dir must be absolute when flashing.enabled (got %q)", c.Flashing.BackupDir)
	}
	return nil
}

// ValidateHost reports whether s is a valid IPv4 address or RFC 1123 hostname.
// Returns nil on success, otherwise an error explaining why. IPv6 is rejected
// because the value is interpolated into URL strings ("https://" + host) and
// passed to chisel without bracketing.
func ValidateHost(s string) error {
	if s == "" {
		return fmt.Errorf("lab_bridge.host must be non-empty")
	}
	if ip := net.ParseIP(s); ip != nil {
		if ip.To4() != nil {
			return nil
		}
		return fmt.Errorf("lab_bridge.host: IPv6 is not supported (got %q)", s)
	}
	// If the string is digits and dots only, the user almost certainly intended
	// an IPv4 address — reject as malformed rather than passing the all-numeric
	// labels through to the hostname check (which would accept them silently).
	if onlyDigitsAndDots(s) {
		return fmt.Errorf("lab_bridge.host: %q is not a valid IPv4 address", s)
	}
	if len(s) > 253 {
		return fmt.Errorf("lab_bridge.host: hostname must be at most 253 characters (got %d)", len(s))
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" {
			return fmt.Errorf("lab_bridge.host: empty label in %q", s)
		}
		if len(label) > 63 {
			return fmt.Errorf("lab_bridge.host: label %q must be at most 63 characters", label)
		}
		if !isValidHostLabel(label) {
			return fmt.Errorf("lab_bridge.host: %q is not a valid hostname or IPv4 address", s)
		}
	}
	return nil
}

func isValidHostLabel(label string) bool {
	if !isAlphaNum(label[0]) || !isAlphaNum(label[len(label)-1]) {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		if !isAlphaNum(c) && c != '-' {
			return false
		}
	}
	return true
}

func isAlphaNum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func onlyDigitsAndDots(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || c == '.') {
			return false
		}
	}
	return true
}
