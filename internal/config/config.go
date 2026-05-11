package config

import (
	"fmt"
	"io"
)

type Config struct {
	LabBridge  LabBridgeConfig  `yaml:"lab_bridge"`
	Chisel     ChiselConfig     `yaml:"chisel"`
	Rest       RestConfig       `yaml:"rest"`
	Discovery  DiscoveryConfig  `yaml:"discovery"`
	Log        LogConfig        `yaml:"log"`
	RawSerial  RawSerialConfig  `yaml:"raw_serial"`
	AutoUpdate AutoUpdateConfig `yaml:"auto_update"`
}

type LabBridgeConfig struct {
	Host string `yaml:"host"`
	User string `yaml:"user"`
	Pass string `yaml:"pass"`
}

type ChiselConfig struct {
	Port       int `yaml:"port"`
	RemotePort int `yaml:"remote_port"`
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
		LabBridge: LabBridgeConfig{
			Host: "111.88.145.138",
			User: "devices_coordinator",
			Pass: "",
		},
		Chisel: ChiselConfig{
			Port:       7000,
			RemotePort: 8081,
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

lab_bridge:
  host: "111.88.145.138"          # lab-bridge VPS host (used for chisel + public HTTPS API)
  user: "devices_coordinator"     # chisel auth user; also bearer-token identity for the public API
  pass: ""                        # chisel password; also bearer token for /api/public/clients/{user}

chisel:
  port: 7000                      # chisel server port on the lab-bridge host
  remote_port: 8081               # REQUIRED — reverse-tunnel port assigned to this agent

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
