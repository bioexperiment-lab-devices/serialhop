package config

import (
	"fmt"
	"io"
)

type Config struct {
	Chisel    ChiselConfig    `yaml:"chisel"`
	Rest      RestConfig      `yaml:"rest"`
	Discovery DiscoveryConfig `yaml:"discovery"`
	Log       LogConfig       `yaml:"log"`
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
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

func Default() Config {
	return Config{
		Chisel: ChiselConfig{
			Server:     "111.88.145.138:7000",
			RemotePort: 8081,
			User:       "devices_coordinator",
			Pass:       "",
		},
		Rest:      RestConfig{Port: 0},
		Discovery: DiscoveryConfig{Include: []string{}, Exclude: []string{}},
		Log:       LogConfig{Level: "info"},
	}
}

const scaffoldTemplate = `# lab_devices_client_config.yaml
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

log:
  level: "info"                   # debug | info | warn | error
`

func WriteScaffold(w io.Writer) error {
	if _, err := fmt.Fprint(w, scaffoldTemplate); err != nil {
		return fmt.Errorf("write scaffold: %w", err)
	}
	return nil
}
