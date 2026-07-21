package config

import (
	"fmt"
	"io"
)

// CurrentSchemaVersion is the config schema version the current binary
// expects. Bumped by +1 in the same PR that appends a migration to
// internal/config/migrations.go. Baseline is 1 (the shape at first ship).
const CurrentSchemaVersion = 2

type Config struct {
	SchemaVersion int                `yaml:"schema_version" json:"schema_version"`
	LabBridge     LabBridgeConfig    `yaml:"lab_bridge" json:"lab_bridge"`
	Rest          RestConfig         `yaml:"rest" json:"rest"`
	Discovery     DiscoveryConfig    `yaml:"discovery" json:"discovery"`
	Log           LogConfig          `yaml:"log" json:"log"`
	AutoUpdate    AutoUpdateConfig   `yaml:"auto_update" json:"auto_update"`
	Flashing      FlashingConfig     `yaml:"flashing" json:"flashing"`
	RawSerial     RawSerialConfig    `yaml:"raw_serial" json:"raw_serial"`
	RemoteUpdate  RemoteUpdateConfig `yaml:"remote_update" json:"remote_update"`
}

type LabBridgeConfig struct {
	Host string `yaml:"host" json:"host"`
	User string `yaml:"user" json:"user"`
	Pass string `yaml:"pass" json:"pass"`
}

type RestConfig struct {
	Port int `yaml:"port" json:"port"`
}

type DiscoveryConfig struct {
	Include          []string `yaml:"include" json:"include"`
	Exclude          []string `yaml:"exclude" json:"exclude"`
	PostOpenSettleMs int      `yaml:"post_open_settle_ms" json:"post_open_settle_ms"`
}

type LogConfig struct {
	Level string `yaml:"level" json:"level"`
}

type AutoUpdateConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
}

type FlashingConfig struct {
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	BackupDir string `yaml:"backup_dir" json:"backup_dir"`
	KeepN     int    `yaml:"keep_n" json:"keep_n"`
}

type RawSerialConfig struct {
	Enabled       bool `yaml:"enabled" json:"enabled"`
	IdleTimeoutMs int  `yaml:"idle_timeout_ms" json:"idle_timeout_ms"`
}

type RemoteUpdateConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
}

func Default() Config {
	return Config{
		SchemaVersion: CurrentSchemaVersion,
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
		Log:          LogConfig{Level: "info"},
		AutoUpdate:   AutoUpdateConfig{Enabled: true},
		Flashing:     FlashingConfig{Enabled: false, BackupDir: "", KeepN: 10},
		RawSerial:    RawSerialConfig{Enabled: false, IdleTimeoutMs: 900000},
		RemoteUpdate: RemoteUpdateConfig{Enabled: false},
	}
}

const scaffoldTemplate = `# SerialHop_config.yaml
# Auto-generated scaffold. Site values are filled in via the panel's
# first-run dialog (username + password). Other fields are optional —
# edit only if you need to change defaults.

schema_version: 2          # config schema version — managed automatically by
                           # SerialHop's migration tooling. Do not edit by hand.

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

auto_update:
  enabled: true            # check GitHub Releases for newer versions
                           # and offer to install them from the panel.
                           # set to false on air-gapped lab boxes.

flashing:
  enabled: false                  # allow POST /flash/{port}. a bad .hex bricks
                                  # the board (ISP recovery required) — leave
                                  # off unless you're actively flashing.
  backup_dir: ""                  # absolute path for pre-flash backups.
                                  # empty -> %ProgramData%\SerialHop\backups
  keep_n: 10                      # retain this many backups per COM port;
                                  # oldest pruned after each completed flash.
                                  # 0 = keep all.

raw_serial:
  enabled: false                  # allow GET /serial/ports/{port}/attach (raw
                                  # WebSocket byte + line-control stream). Only
                                  # ports with no discovered device are eligible.
                                  # off by default — turn on for bring-up / RE.
  idle_timeout_ms: 900000         # close a raw session after this many ms with
                                  # no traffic. 0 = never time out.

remote_update:
  enabled: false          # allow lab-bridge admins to push updates via
                          # POST /agent/update (admin-gated server-side, like
                          # /flash). the update installs with no operator
                          # action. off by default.
`

func WriteScaffold(w io.Writer) error {
	if _, err := fmt.Fprint(w, scaffoldTemplate); err != nil {
		return fmt.Errorf("write scaffold: %w", err)
	}
	return nil
}
