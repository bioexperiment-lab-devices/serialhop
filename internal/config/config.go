package config

import (
	"fmt"
	"io"
)

type Config struct {
	LabBridge  LabBridgeConfig  `yaml:"lab_bridge"`
	Rest       RestConfig       `yaml:"rest"`
	Discovery  DiscoveryConfig  `yaml:"discovery"`
	Log        LogConfig        `yaml:"log"`
	RawSerial  RawSerialConfig  `yaml:"raw_serial"`
	AutoUpdate AutoUpdateConfig `yaml:"auto_update"`
	Flashing   FlashingConfig   `yaml:"flashing"`
}

type LabBridgeConfig struct {
	Host string `yaml:"host"`
	User string `yaml:"user"`
	Pass string `yaml:"pass"`
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

type FlashingConfig struct {
	Enabled   bool   `yaml:"enabled"`
	BackupDir string `yaml:"backup_dir"`
	KeepN     int    `yaml:"keep_n"`
}

func Default() Config {
	return Config{
		LabBridge: LabBridgeConfig{
			Host: "111.88.145.138",
			User: "",
			Pass: "",
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
		Flashing:   FlashingConfig{Enabled: false, BackupDir: "", KeepN: 10},
	}
}

const scaffoldTemplate = `# SerialHop_config.yaml
# Auto-generated scaffold. Site values are filled in via the panel's
# first-run dialog (username + password). Other fields are optional —
# edit only if you need to change defaults.

lab_bridge:
  host: "111.88.145.138"   # lab-bridge VPS host (chisel + public HTTPS API).
                           # change only when pointing at a different deployment.
  user: ""                 # REQUIRED — chisel auth user; also Bearer-token
                           # identity for the public API. No default.
  pass: ""                 # REQUIRED — chisel password; also Bearer token
                           # for /api/public/clients/{user}. No default.

rest:
  port: 0                  # local REST port; 0 = OS picks a free one.

discovery:
  include: []              # optional: only probe these COM ports, e.g. ["COM3", "COM4"]
  exclude: []              # optional: skip these COM ports, e.g. ["COM1"]
  post_open_settle_ms: 2000  # wait after opening a port before probing. covers the
                             # Arduino auto-reset bootloader window (~1-2 s). lower
                             # if your boards don't reset on DTR; 0 to disable.

log:
  level: "info"            # debug | info | warn | error

raw_serial:
  enabled: false           # set true to allow GET /serial/ports and
                           # POST /serial/ports/{port}/command. bypasses
                           # device classification — leave off unless diagnosing.

auto_update:
  enabled: true            # check GitHub Releases for newer versions
                           # and offer to install them from the panel.
                           # set to false on air-gapped lab boxes.

flashing:
  enabled: false                  # allow POST /flash/{port}. higher risk than
                                  # raw_serial — a bad .hex bricks the board
                                  # (ISP recovery required). independent of
                                  # raw_serial.enabled.
  backup_dir: ""                  # absolute path for pre-flash backups.
                                  # empty -> %ProgramData%\SerialHop\backups
  keep_n: 10                      # retain this many backups per COM port;
                                  # oldest pruned after each completed flash.
                                  # 0 = keep all.
`

func WriteScaffold(w io.Writer) error {
	if _, err := fmt.Fprint(w, scaffoldTemplate); err != nil {
		return fmt.Errorf("write scaffold: %w", err)
	}
	return nil
}
