package config

import (
	"fmt"
	"io"
)

type Config struct {
	Chisel     ChiselConfig     `yaml:"chisel"`
	Rest       RestConfig       `yaml:"rest"`
	Discovery  DiscoveryConfig  `yaml:"discovery"`
	Log        LogConfig        `yaml:"log"`
	RawSerial  RawSerialConfig  `yaml:"raw_serial"`
	AutoUpdate AutoUpdateConfig `yaml:"auto_update"`
}

type ChiselConfig struct {
	Server     string `yaml:"server"`
	RemotePort int    `yaml:"remote_port"`
	User       string `yaml:"user"`
	Pass       string `yaml:"pass"`
}

type RestConfig struct {
	Port int `yaml:"port"`
}

type DiscoveryConfig struct {
	Include          []string `yaml:"include"`
	Exclude          []string `yaml:"exclude"`
	PostOpenSettleMs int      `yaml:"post_open_settle_ms"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

type RawSerialConfig struct {
	Enabled bool `yaml:"enabled"`
}

type AutoUpdateConfig struct {
	Enabled bool `yaml:"enabled"`
}

func Default() Config {
	return Config{
		Chisel: ChiselConfig{
			Server:     "111.88.145.138:7000",
			RemotePort: 8081,
			User:       "devices_coordinator",
			Pass:       "",
		},
		Rest: RestConfig{Port: 0},
		Discovery: DiscoveryConfig{
			Include:          []string{},
			Exclude:          []string{},
			PostOpenSettleMs: 2000,
		},
		Log:        LogConfig{Level: "info"},
		RawSerial:  RawSerialConfig{Enabled: false},
		AutoUpdate: AutoUpdateConfig{Enabled: true},
	}
}

const scaffoldTemplate = `# SerialHop_config.yaml
# Auto-generated scaffold. Edit values then re-run the executable.

chisel:
  server: "111.88.145.138:7000"   # chisel server host:port
  remote_port: 8081               # REQUIRED — port to expose on the chisel server
  user: "devices_coordinator"     # default; override if your chisel server expects different
  pass: ""                        # optional

rest:
  port: 0                         # local REST port; 0 = OS picks a free one

discovery:
  include: []                     # optional: only probe these COM ports, e.g. ["COM3", "COM4"]
  exclude: []                     # optional: skip these COM ports, e.g. ["COM1"]
  post_open_settle_ms: 2000       # wait after opening a port before probing. covers the
                                  # Arduino auto-reset bootloader window (~1-2 s). lower
                                  # if your boards don't reset on DTR; 0 to disable.

log:
  level: "info"                   # debug | info | warn | error

raw_serial:
  enabled: false                  # set true to allow GET /serial/ports and
                                  # POST /serial/ports/{port}/command. bypasses
                                  # device classification — leave off unless diagnosing.

auto_update:
  enabled: true                   # check GitHub Releases for newer versions
                                  # and offer to install them from the panel.
                                  # set to false on air-gapped lab boxes.
`

func WriteScaffold(w io.Writer) error {
	if _, err := fmt.Fprint(w, scaffoldTemplate); err != nil {
		return fmt.Errorf("write scaffold: %w", err)
	}
	return nil
}
