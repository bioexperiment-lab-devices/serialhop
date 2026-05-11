# Status Lamps Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add three at-a-glance status lamps (Service / Server / Tunnel) to the SerialHop control panel, in a dedicated `Status` group; introduce a breaking config schema change moving `host` / `user` / `pass` into a new top-level `lab_bridge` section; fix the existing service-lamp color not rendering.

**Architecture:** New `internal/labbridge` package (stateless HTTP client) is called from two probe goroutines spawned by the panel on a 10 s tick; results are written into a mutex-guarded `lampState` consumed by the existing 1 s `refresh()` paint loop. `lampState`, the `lampKind` enum, the three per-lamp presentation functions, and the probe-result-mapping helpers all live outside `//go:build windows` so they're exercised on the macOS test runner.

**Tech Stack:** Go 1.24 stdlib (`net/http`, `httptest`), `gopkg.in/yaml.v3`, `github.com/lxn/walk` (windows-only panel layout), existing `internal/config` and `internal/winsvc` packages.

**Spec:** `docs/superpowers/specs/2026-05-11-status-lamps-design.md`

---

## File map

**New files:**

| Path | Purpose | Build tag |
|---|---|---|
| `internal/labbridge/client.go` | `FetchHealth`, `FetchClient`, sentinel errors | none |
| `internal/labbridge/client_test.go` | `httptest.NewServer`-driven unit tests | none |
| `internal/panel/lampstate.go` | `lampKind`, `netLamp`, `serviceLamp`, `lampState`, three presentation functions, helpers that map labbridge results → `lampKind` | none |
| `internal/panel/lampstate_test.go` | Presentation table-driven tests; mapping tests | none |
| `internal/panel/probe.go` | `runServerProbe`, `runTunnelProbe`, `probeLoop` | none |
| `internal/panel/probe_test.go` | Probe-function tests using fake config loader + fake HTTP server | none |

**Modified files:**

| Path | What changes |
|---|---|
| `internal/config/config.go` | Add `LabBridgeConfig`; drop `Server`/`User`/`Pass` from `ChiselConfig`; add `Port` to `ChiselConfig`; update `Default()`; update embedded scaffold YAML |
| `internal/config/config_test.go` | Field-name updates in `TestDefaultConfig` and `TestWriteScaffold_RoundTrip` |
| `internal/config/load.go` | `Validate()` replaces `chisel.server` check with `lab_bridge.host` non-empty check; adds `chisel.port` range check |
| `internal/config/load_test.go` | Update YAML bodies and field references throughout |
| `internal/app/app.go` | Compose `chisel.Config.Server` from `cfg.LabBridge.Host` + `cfg.Chisel.Port`; pass `cfg.LabBridge.User/Pass` |
| `internal/winsvc/worker.go` | Line 61: `cfg.Chisel.User` → `cfg.LabBridge.User` |
| `internal/panel/panel.go` | Replace top status row with `GroupBox` holding three lamps; spawn probe goroutines; update `refresh()` to read from `lampState`; compose host:port for display; apply color-fix attempt (`Invalidate()` after `SetTextColor`) |
| `internal/panel/state.go` | Unchanged (existing `StatusIndicator` / `ComputeButtons` still used; reused by `serviceLampPresentation`) |

`internal/chisel/client.go` is intentionally **not** modified — its internal `Config.Server` field remains a `host:port` string; only its caller in `app.go` changes.

---

## Task 1: Config schema migration (breaking, single commit)

This task is one logical change: introduce `LabBridgeConfig`, drop the old `chisel.server`/`user`/`pass` fields, add `chisel.port`, and update every call-site so `go build ./...` and `go test ./...` stay green. Because the field rename breaks compilation, everything lands together.

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/load.go`
- Modify: `internal/config/load_test.go`
- Modify: `internal/app/app.go`
- Modify: `internal/winsvc/worker.go`
- Modify: `internal/panel/panel.go`

- [ ] **Step 1.1: Update the Config types in `internal/config/config.go`**

Replace the current contents of `internal/config/config.go` with:

```go
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
```

- [ ] **Step 1.2: Update `internal/config/load.go` validator**

Replace the body of `Validate()` so it checks the new fields. Final content of `load.go`:

```go
package config

import (
	"fmt"
	"os"

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
	if c.LabBridge.Host == "" {
		return fmt.Errorf("lab_bridge.host must be non-empty")
	}
	if c.Chisel.Port < 1 || c.Chisel.Port > 65535 {
		return fmt.Errorf("chisel.port must be in 1..65535 (got %d)", c.Chisel.Port)
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
```

(The `net` and `strconv` imports are removed; they are no longer needed.)

- [ ] **Step 1.3: Update `internal/config/config_test.go`**

Replace the contents with:

```go
package config

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultConfig(t *testing.T) {
	c := Default()
	if c.LabBridge.Host != "111.88.145.138" {
		t.Errorf("lab_bridge.host: got %q, want %q", c.LabBridge.Host, "111.88.145.138")
	}
	if c.LabBridge.User != "devices_coordinator" {
		t.Errorf("lab_bridge.user: got %q, want %q", c.LabBridge.User, "devices_coordinator")
	}
	if c.Chisel.Port != 7000 {
		t.Errorf("chisel.port: got %d, want 7000", c.Chisel.Port)
	}
	if c.Chisel.RemotePort != 8081 {
		t.Errorf("chisel.remote_port: got %d, want 8081", c.Chisel.RemotePort)
	}
	if c.Rest.Port != 0 {
		t.Errorf("rest.port: got %d, want 0", c.Rest.Port)
	}
	if c.Log.Level != "info" {
		t.Errorf("log.level: got %q, want info", c.Log.Level)
	}
}

func TestDefaultConfig_RawSerialDisabled(t *testing.T) {
	c := Default()
	if c.RawSerial.Enabled {
		t.Errorf("raw_serial.enabled: got true, want false (must default off)")
	}
}

func TestDefaultConfig_PostOpenSettle(t *testing.T) {
	c := Default()
	if c.Discovery.PostOpenSettleMs != 2000 {
		t.Errorf("discovery.post_open_settle_ms: got %d, want 2000", c.Discovery.PostOpenSettleMs)
	}
}

func TestWriteScaffold_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteScaffold(&buf); err != nil {
		t.Fatalf("WriteScaffold: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "111.88.145.138") {
		t.Errorf("scaffold missing default host; got:\n%s", out)
	}
	if !strings.Contains(out, "devices_coordinator") {
		t.Errorf("scaffold missing default user; got:\n%s", out)
	}
	// Scaffold must parse back into the default config.
	var parsed Config
	if err := yaml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("scaffold did not parse as YAML: %v\n%s", err, out)
	}
	def := Default()
	if parsed.LabBridge.Host != def.LabBridge.Host {
		t.Errorf("round-trip lab_bridge.host: got %q, want %q", parsed.LabBridge.Host, def.LabBridge.Host)
	}
	if parsed.Chisel.Port != def.Chisel.Port {
		t.Errorf("round-trip chisel.port: got %d, want %d", parsed.Chisel.Port, def.Chisel.Port)
	}
	if parsed.Chisel.RemotePort != def.Chisel.RemotePort {
		t.Errorf("round-trip chisel.remote_port: got %d, want %d", parsed.Chisel.RemotePort, def.Chisel.RemotePort)
	}
	if parsed.RawSerial.Enabled {
		t.Errorf("round-trip raw_serial.enabled: got true, want false (default)")
	}
	if parsed.Discovery.PostOpenSettleMs != def.Discovery.PostOpenSettleMs {
		t.Errorf("round-trip discovery.post_open_settle_ms: got %d, want %d",
			parsed.Discovery.PostOpenSettleMs, def.Discovery.PostOpenSettleMs)
	}
}
```

- [ ] **Step 1.4: Update `internal/config/load_test.go`**

Replace the file contents with:

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestLoad_Success(t *testing.T) {
	dir := t.TempDir()
	body := `
lab_bridge:
  host: "10.0.0.1"
  user: "u"
  pass: "p"
chisel:
  port: 7000
  remote_port: 9000
rest:
  port: 8080
discovery:
  include: ["COM3"]
log:
  level: "debug"
`
	p := writeFile(t, dir, "cfg.yaml", body)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.LabBridge.Host != "10.0.0.1" {
		t.Errorf("lab_bridge.host: got %q", c.LabBridge.Host)
	}
	if c.LabBridge.User != "u" {
		t.Errorf("lab_bridge.user: got %q", c.LabBridge.User)
	}
	if c.Chisel.Port != 7000 {
		t.Errorf("chisel.port: got %d", c.Chisel.Port)
	}
	if c.Chisel.RemotePort != 9000 {
		t.Errorf("remote_port: got %d", c.Chisel.RemotePort)
	}
	if len(c.Discovery.Include) != 1 || c.Discovery.Include[0] != "COM3" {
		t.Errorf("include: got %v", c.Discovery.Include)
	}
}

func TestLoad_FileMissing(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if !os.IsNotExist(err) {
		t.Errorf("expected os.IsNotExist, got %v", err)
	}
}

func TestValidate_Cases(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(*Config)
		wantErr string
	}{
		{"host empty", func(c *Config) { c.LabBridge.Host = "" }, "lab_bridge.host"},
		{"chisel.port low", func(c *Config) { c.Chisel.Port = 0 }, "chisel.port"},
		{"chisel.port high", func(c *Config) { c.Chisel.Port = 70000 }, "chisel.port"},
		{"remote_port low", func(c *Config) { c.Chisel.RemotePort = 0 }, "remote_port"},
		{"remote_port high", func(c *Config) { c.Chisel.RemotePort = 70000 }, "remote_port"},
		{"include+exclude both set", func(c *Config) {
			c.Discovery.Include = []string{"COM1"}
			c.Discovery.Exclude = []string{"COM2"}
		}, "mutually exclusive"},
		{"log.level invalid", func(c *Config) { c.Log.Level = "verbose" }, "log.level"},
		{"post_open_settle_ms negative", func(c *Config) {
			c.Discovery.PostOpenSettleMs = -1
		}, "post_open_settle_ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.mut(&c)
			err := Validate(&c)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidate_DefaultIsValid(t *testing.T) {
	c := Default()
	if err := Validate(&c); err != nil {
		t.Errorf("default config should validate, got %v", err)
	}
}

func TestLoadPartial_Valid(t *testing.T) {
	dir := t.TempDir()
	body := `
lab_bridge:
  host: "10.0.0.1"
  user: "u"
  pass: "p"
chisel:
  port: 7000
  remote_port: 9001
rest:
  port: 8080
discovery:
  include: ["COM3"]
log:
  level: "debug"
`
	p := writeFile(t, dir, "cfg.yaml", body)
	cfg, err := LoadPartial(p)
	if err != nil {
		t.Fatalf("LoadPartial err: %v", err)
	}
	if cfg.LabBridge.Host != "10.0.0.1" {
		t.Errorf("host: got %q", cfg.LabBridge.Host)
	}
	if cfg.Chisel.RemotePort != 9001 {
		t.Errorf("remote_port: got %d", cfg.Chisel.RemotePort)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("level: got %q", cfg.Log.Level)
	}
}

func TestLoadPartial_InvalidValidationReturnsParsedFields(t *testing.T) {
	dir := t.TempDir()
	body := `
lab_bridge:
  host: ""
chisel:
  port: 7000
  remote_port: 9001
log:
  level: "info"
`
	p := writeFile(t, dir, "cfg.yaml", body)
	cfg, err := LoadPartial(p)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "lab_bridge.host must be non-empty") {
		t.Errorf("unexpected err: %v", err)
	}
	if cfg.Chisel.RemotePort != 9001 {
		t.Errorf("remote_port should still be parsed: got %d", cfg.Chisel.RemotePort)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("log level should still be parsed: got %q", cfg.Log.Level)
	}
}

func TestLoadPartial_MalformedYAMLReturnsDefault(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "cfg.yaml", "::: not yaml :::")
	cfg, err := LoadPartial(p)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	def := Default()
	if cfg.LabBridge.Host != def.LabBridge.Host {
		t.Errorf("on parse failure, expected Default()-host %q, got %q", def.LabBridge.Host, cfg.LabBridge.Host)
	}
}

func TestLoad_PostOpenSettleCustom(t *testing.T) {
	dir := t.TempDir()
	body := `
lab_bridge:
  host: "10.0.0.1"
chisel:
  port: 7000
  remote_port: 9000
rest:
  port: 0
discovery:
  post_open_settle_ms: 500
log:
  level: "info"
`
	p := writeFile(t, dir, "cfg.yaml", body)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Discovery.PostOpenSettleMs != 500 {
		t.Errorf("post_open_settle_ms: got %d, want 500", c.Discovery.PostOpenSettleMs)
	}
}

func TestLoad_RawSerialEnabled(t *testing.T) {
	dir := t.TempDir()
	body := `
lab_bridge:
  host: "10.0.0.1"
chisel:
  port: 7000
  remote_port: 9000
rest:
  port: 0
log:
  level: "info"
raw_serial:
  enabled: true
`
	p := writeFile(t, dir, "cfg.yaml", body)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.RawSerial.Enabled {
		t.Errorf("raw_serial.enabled: got false, want true")
	}
}

func TestLoadPartial_MissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope.yaml")
	cfg, err := LoadPartial(p)
	if !os.IsNotExist(err) {
		t.Errorf("expected os.IsNotExist, got %v", err)
	}
	def := Default()
	if cfg.LabBridge.Host != def.LabBridge.Host {
		t.Errorf("on missing file, expected Default()-host %q, got %q", def.LabBridge.Host, cfg.LabBridge.Host)
	}
}

func TestLoad_AutoUpdateDisabled(t *testing.T) {
	dir := t.TempDir()
	body := `
lab_bridge:
  host: "10.0.0.1"
chisel:
  port: 7000
  remote_port: 9000
rest:
  port: 0
log:
  level: "info"
auto_update:
  enabled: false
`
	p := writeFile(t, dir, "cfg.yaml", body)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.AutoUpdate.Enabled {
		t.Errorf("auto_update.enabled: got true, want false")
	}
}

func TestLoad_AutoUpdateDefaultsToTrue(t *testing.T) {
	// A config file written by an older binary has no auto_update section.
	dir := t.TempDir()
	body := `
lab_bridge:
  host: "10.0.0.1"
chisel:
  port: 7000
  remote_port: 9000
rest:
  port: 0
log:
  level: "info"
`
	p := writeFile(t, dir, "cfg.yaml", body)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.AutoUpdate.Enabled {
		t.Errorf("auto_update.enabled: got false, want true (default)")
	}
}

func TestLoad_OldSchemaIsRejected(t *testing.T) {
	// Old configs with chisel.server / chisel.user / chisel.pass should
	// surface as a clear "lab_bridge.host must be non-empty" error since
	// yaml.v3 silently ignores unknown fields.
	dir := t.TempDir()
	body := `
chisel:
  server: "10.0.0.1:7000"
  remote_port: 9000
  user: "u"
  pass: "p"
log:
  level: "info"
`
	p := writeFile(t, dir, "cfg.yaml", body)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "lab_bridge.host") {
		t.Errorf("expected lab_bridge.host error, got %v", err)
	}
}
```

- [ ] **Step 1.5: Update `internal/app/app.go`**

Replace lines 22-23 of the `slog.Info("serialhop starting", ...)` call and lines 56-64 of the `chisel.Run` invocation. Final relevant section:

```go
	slog.Info("serialhop starting",
		"chisel_host", cfg.LabBridge.Host,
		"chisel_port", cfg.Chisel.Port,
		"remote_port", cfg.Chisel.RemotePort,
		"rest_port", cfg.Rest.Port,
		"discovery_include", cfg.Discovery.Include,
		"discovery_exclude", cfg.Discovery.Exclude,
		"discovery_post_open_settle_ms", cfg.Discovery.PostOpenSettleMs,
	)
```

And later:

```go
	chiselDone := make(chan error, 1)
	go func() {
		chiselDone <- chisel.Run(ctx, chisel.Config{
			Server:     net.JoinHostPort(cfg.LabBridge.Host, strconv.Itoa(cfg.Chisel.Port)),
			User:       cfg.LabBridge.User,
			Pass:       cfg.LabBridge.Pass,
			RemotePort: cfg.Chisel.RemotePort,
			LocalPort:  localPort,
		})
	}()
```

Add `"net"` and `"strconv"` to the import block.

- [ ] **Step 1.6: Update `internal/winsvc/worker.go`**

Find the line (currently line 61):
```go
	h.manager.StartShipper(cfg.Chisel.User)
```

Replace with:
```go
	h.manager.StartShipper(cfg.LabBridge.User)
```

- [ ] **Step 1.7: Update `internal/panel/panel.go` config-display lines**

The current `refresh()` at line 125 uses `cfg.Chisel.Server`. Update those two lines to compose host:port:

```go
		serverLbl.SetText("Chisel server:    " + net.JoinHostPort(cfg.LabBridge.Host, strconv.Itoa(cfg.Chisel.Port)))
		remotePort.SetText(fmt.Sprintf("Remote port:      %d", cfg.Chisel.RemotePort))
```

Add `"net"` and `"strconv"` to the import block.

- [ ] **Step 1.8: Run cross-platform tests**

```bash
go test ./internal/config/... ./internal/app/... ./internal/winsvc/... -race -count=1
```

Expected: PASS. The `winsvc` package has `//go:build windows` files; on macOS the `Execute` function won't compile-test, but the package itself has no other broken references.

- [ ] **Step 1.9: Cross-compile for Windows to catch any panel.go references missed**

```bash
GOOS=windows GOARCH=amd64 go build ./...
```

Expected: build succeeds. If a compile error mentions `cfg.Chisel.Server`, `cfg.Chisel.User`, or `cfg.Chisel.Pass`, fix the remaining reference and re-run.

- [ ] **Step 1.10: Run linter**

```bash
golangci-lint run ./...
```

Expected: no findings.

- [ ] **Step 1.11: Commit**

```bash
git add internal/config internal/app internal/winsvc internal/panel
git commit -m "$(cat <<'EOF'
refactor!: move host/user/pass to lab_bridge config section

Breaking config schema change: chisel.server -> lab_bridge.host + chisel.port; chisel.user/pass -> lab_bridge.user/pass. Prepares for the new lab-bridge public API client which shares host and bearer-token credentials with chisel.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: `internal/labbridge` package — types and `FetchHealth`

**Files:**
- Create: `internal/labbridge/client.go`
- Create: `internal/labbridge/client_test.go`

- [ ] **Step 2.1: Write failing test for `FetchHealth` happy path**

Create `internal/labbridge/client_test.go`:

```go
package labbridge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testUserAgent = "labbridge-test/1.0"

func TestFetchHealth_ChiselOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/public/health" {
			t.Errorf("path: got %q, want /api/public/health", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"chisel":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := FetchHealth(ctx, srv.Client(), srv.URL, testUserAgent)
	if err != nil {
		t.Fatalf("FetchHealth: %v", err)
	}
	if !got.ChiselOK {
		t.Errorf("ChiselOK: got false, want true")
	}
	if got.Detail != "" {
		t.Errorf("Detail: got %q, want empty", got.Detail)
	}
}
```

- [ ] **Step 2.2: Run the test to verify it fails**

```bash
go test ./internal/labbridge/ -run TestFetchHealth_ChiselOK -v
```

Expected: FAIL with `undefined: FetchHealth` (or similar).

- [ ] **Step 2.3: Create `internal/labbridge/client.go` with minimal implementation**

```go
// Package labbridge is the HTTP client for the lab-bridge VPS's public
// API (see docs/superpowers/specs/2026-05-11-status-lamps-design.md).
// Stateless: callers supply *http.Client and context.Context; per-call
// timeouts live at the call site.
package labbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const (
	healthPath   = "/api/public/health"
	clientsPath  = "/api/public/clients/"
	maxBodyBytes = 64 << 10 // 64 KB; both responses are tiny
)

// ErrUnauthorized is returned by FetchClient on HTTP 401. The lab-bridge
// spec intentionally makes "unknown user", "wrong token", "missing
// Authorization header", and "non-Bearer scheme" indistinguishable; this
// package does not try to disambiguate them.
var ErrUnauthorized = errors.New("labbridge: unauthorized")

// ErrServerError is wrapped (via fmt.Errorf "...: %w") by FetchClient
// and FetchHealth on HTTP 5xx responses.
var ErrServerError = errors.New("labbridge: server error")

// Health is the parsed result of GET /api/public/health. The endpoint
// always returns HTTP 200; the up/down signal is in the JSON body.
type Health struct {
	ChiselOK bool
	Detail   string
}

type healthBody struct {
	Chisel string `json:"chisel"`
	Error  string `json:"error,omitempty"`
}

// FetchHealth probes the chisel-server liveness endpoint.
func FetchHealth(ctx context.Context, hc *http.Client, base, userAgent string) (Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+healthPath, nil)
	if err != nil {
		return Health{}, fmt.Errorf("labbridge: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return Health{}, fmt.Errorf("labbridge: do: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 500 {
		return Health{}, fmt.Errorf("labbridge: health: %w (status %d)", ErrServerError, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return Health{}, fmt.Errorf("labbridge: health: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return Health{}, fmt.Errorf("labbridge: read health body: %w", err)
	}
	var hb healthBody
	if err := json.Unmarshal(body, &hb); err != nil {
		return Health{}, fmt.Errorf("labbridge: parse health body: %w", err)
	}
	return Health{ChiselOK: hb.Chisel == "ok", Detail: hb.Error}, nil
}

// ClientInfo and FetchClient land in Task 3.

var _ = url.PathEscape // silence the import until Task 3 uses it
```

(Remove the `var _ = url.PathEscape` placeholder once `FetchClient` adds a real use of `url.PathEscape` in Task 3.)

- [ ] **Step 2.4: Run the test to verify it passes**

```bash
go test ./internal/labbridge/ -run TestFetchHealth_ChiselOK -v
```

Expected: PASS.

- [ ] **Step 2.5: Add failing tests for the other `FetchHealth` cases**

Append to `internal/labbridge/client_test.go`:

```go
func TestFetchHealth_ChiselDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"chisel":"down","error":"connection refused"}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := FetchHealth(ctx, srv.Client(), srv.URL, testUserAgent)
	if err != nil {
		t.Fatalf("FetchHealth: %v", err)
	}
	if got.ChiselOK {
		t.Errorf("ChiselOK: got true, want false")
	}
	if got.Detail != "connection refused" {
		t.Errorf("Detail: got %q, want %q", got.Detail, "connection refused")
	}
}

func TestFetchHealth_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	_, err := FetchHealth(ctx, srv.Client(), srv.URL, testUserAgent)
	if err == nil || !strings.Contains(err.Error(), "parse health body") {
		t.Fatalf("expected parse-body error, got %v", err)
	}
}

func TestFetchHealth_5xxWrapsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	_, err := FetchHealth(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if !errors.Is(err, ErrServerError) {
		t.Fatalf("expected ErrServerError, got %v", err)
	}
}

func TestFetchHealth_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway - 100) // 402
	}))
	t.Cleanup(srv.Close)

	_, err := FetchHealth(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err == nil || !strings.Contains(err.Error(), "unexpected status 402") {
		t.Fatalf("expected unexpected-status error, got %v", err)
	}
}

func TestFetchHealth_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"chisel":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := FetchHealth(ctx, srv.Client(), srv.URL, testUserAgent)
	if err == nil {
		t.Fatal("expected ctx error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ctx.DeadlineExceeded, got %v", err)
	}
}

func TestFetchHealth_SendsUserAgent(t *testing.T) {
	gotUA := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"chisel":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	_, err := FetchHealth(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err != nil {
		t.Fatalf("FetchHealth: %v", err)
	}
	if gotUA != testUserAgent {
		t.Errorf("User-Agent: got %q, want %q", gotUA, testUserAgent)
	}
}
```

- [ ] **Step 2.6: Run all FetchHealth tests to verify they pass**

```bash
go test ./internal/labbridge/ -run TestFetchHealth -v
```

Expected: all PASS.

- [ ] **Step 2.7: Commit**

```bash
git add internal/labbridge
git commit -m "$(cat <<'EOF'
feat(labbridge): FetchHealth client for /api/public/health

Cross-platform HTTP client returning Health{ChiselOK, Detail}; wraps ErrServerError on 5xx; unit-tested via httptest.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: `internal/labbridge` — `FetchClient`

**Files:**
- Modify: `internal/labbridge/client.go`
- Modify: `internal/labbridge/client_test.go`

- [ ] **Step 3.1: Write failing tests for `FetchClient`**

Append to `internal/labbridge/client_test.go`:

```go
func TestFetchClient_200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/public/clients/devices_coordinator" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer s3cret" {
			t.Errorf("auth: got %q, want %q", got, "Bearer s3cret")
		}
		_, _ = w.Write([]byte(`{"port":8089,"connected":true}`))
	}))
	t.Cleanup(srv.Close)

	got, err := FetchClient(context.Background(), srv.Client(), srv.URL, "devices_coordinator", "s3cret", testUserAgent)
	if err != nil {
		t.Fatalf("FetchClient: %v", err)
	}
	if got.Port != 8089 || !got.Connected {
		t.Errorf("got %+v, want {Port:8089 Connected:true}", got)
	}
}

func TestFetchClient_401_WrapsErrUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"unauthorized"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	_, err := FetchClient(context.Background(), srv.Client(), srv.URL, "u", "p", testUserAgent)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestFetchClient_500_WrapsErrServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "roster broken", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	_, err := FetchClient(context.Background(), srv.Client(), srv.URL, "u", "p", testUserAgent)
	if !errors.Is(err, ErrServerError) {
		t.Fatalf("expected ErrServerError, got %v", err)
	}
}

func TestFetchClient_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	t.Cleanup(srv.Close)

	_, err := FetchClient(context.Background(), srv.Client(), srv.URL, "u", "p", testUserAgent)
	if err == nil || !strings.Contains(err.Error(), "parse client body") {
		t.Fatalf("expected parse-body error, got %v", err)
	}
}

func TestFetchClient_UsernameURLEscaped(t *testing.T) {
	gotPath := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"port":1,"connected":false}`))
	}))
	t.Cleanup(srv.Close)

	_, err := FetchClient(context.Background(), srv.Client(), srv.URL, "foo bar", "p", testUserAgent)
	if err != nil {
		t.Fatalf("FetchClient: %v", err)
	}
	if gotPath != "/api/public/clients/foo%20bar" {
		t.Errorf("escaped path: got %q, want %q", gotPath, "/api/public/clients/foo%20bar")
	}
}

func TestFetchClient_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"port":1,"connected":true}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := FetchClient(ctx, srv.Client(), srv.URL, "u", "p", testUserAgent)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ctx.DeadlineExceeded, got %v", err)
	}
}
```

- [ ] **Step 3.2: Run tests to verify they fail**

```bash
go test ./internal/labbridge/ -run TestFetchClient -v
```

Expected: FAIL with `undefined: FetchClient` (or similar).

- [ ] **Step 3.3: Implement `FetchClient` in `internal/labbridge/client.go`**

Delete the placeholder `var _ = url.PathEscape` line; append at the end of the file:

```go
// ClientInfo is the parsed result of GET /api/public/clients/{user}.
type ClientInfo struct {
	Port      int
	Connected bool
}

type clientBody struct {
	Port      int  `json:"port"`
	Connected bool `json:"connected"`
}

// FetchClient looks up the agent's reverse-tunnel port and the server's
// view of whether its tunnel is currently connected.
//
// Returns wrapped ErrUnauthorized on HTTP 401 (intentionally
// indistinguishable from "unknown user" per spec); wrapped ErrServerError
// on HTTP 5xx; plain error for network failures, unexpected status codes,
// and JSON parse errors.
func FetchClient(ctx context.Context, hc *http.Client, base, user, pass, userAgent string) (ClientInfo, error) {
	endpoint := base + clientsPath + url.PathEscape(user)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ClientInfo{}, fmt.Errorf("labbridge: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+pass)

	resp, err := hc.Do(req)
	if err != nil {
		return ClientInfo{}, fmt.Errorf("labbridge: do: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ClientInfo{}, fmt.Errorf("labbridge: client: %w", ErrUnauthorized)
	case resp.StatusCode >= 500:
		return ClientInfo{}, fmt.Errorf("labbridge: client: %w (status %d)", ErrServerError, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return ClientInfo{}, fmt.Errorf("labbridge: client: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return ClientInfo{}, fmt.Errorf("labbridge: read client body: %w", err)
	}
	var cb clientBody
	if err := json.Unmarshal(body, &cb); err != nil {
		return ClientInfo{}, fmt.Errorf("labbridge: parse client body: %w", err)
	}
	return ClientInfo{Port: cb.Port, Connected: cb.Connected}, nil
}
```

- [ ] **Step 3.4: Run all labbridge tests**

```bash
go test ./internal/labbridge/ -race -count=1 -v
```

Expected: all PASS.

- [ ] **Step 3.5: Commit**

```bash
git add internal/labbridge
git commit -m "$(cat <<'EOF'
feat(labbridge): FetchClient with bearer auth and sentinel errors

GET /api/public/clients/{user} returning {Port, Connected}; wraps ErrUnauthorized on 401 and ErrServerError on 5xx; username URL-path-escaped.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Lamp state types and presentation functions

**Files:**
- Create: `internal/panel/lampstate.go`
- Create: `internal/panel/lampstate_test.go`

- [ ] **Step 4.1: Write the failing presentation test**

Create `internal/panel/lampstate_test.go`:

```go
package panel

import (
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

func TestServerLampPresentation(t *testing.T) {
	cases := []struct {
		kind      lampKind
		wantColor StatusColor
		wantText  string
	}{
		{lampChecking, ColorGrey, "Checking…"},
		{lampOK, ColorGreen, "Up"},
		{lampChiselDown, ColorRed, "Chisel down"},
		{lampUnreachable, ColorGrey, "Unreachable"},
	}
	for _, tc := range cases {
		t.Run(tc.wantText, func(t *testing.T) {
			color, text := serverLampPresentation(netLamp{kind: tc.kind})
			if color != tc.wantColor {
				t.Errorf("color: got %v, want %v", color, tc.wantColor)
			}
			if text != tc.wantText {
				t.Errorf("text: got %q, want %q", text, tc.wantText)
			}
		})
	}
}

func TestTunnelLampPresentation(t *testing.T) {
	cases := []struct {
		kind      lampKind
		wantColor StatusColor
		wantText  string
	}{
		{lampChecking, ColorGrey, "Checking…"},
		{lampOK, ColorGreen, "Connected"},
		{lampDisconnected, ColorRed, "Disconnected"},
		{lampAuthFailed, ColorRed, "Auth failed"},
		{lampServerError, ColorYellow, "Server error"},
		{lampUnreachable, ColorGrey, "Unreachable"},
		{lampNotConfigured, ColorGrey, "Not configured"},
	}
	for _, tc := range cases {
		t.Run(tc.wantText, func(t *testing.T) {
			color, text := tunnelLampPresentation(netLamp{kind: tc.kind})
			if color != tc.wantColor {
				t.Errorf("color: got %v, want %v", color, tc.wantColor)
			}
			if text != tc.wantText {
				t.Errorf("text: got %q, want %q", text, tc.wantText)
			}
		})
	}
}

func TestServiceLampPresentation(t *testing.T) {
	cases := []struct {
		name      string
		state     winsvc.ServiceState
		cfgValid  bool
		wantColor StatusColor
		wantText  string
	}{
		{"running", winsvc.StateRunning, true, ColorGreen, "Running"},
		{"stopped", winsvc.StateStopped, true, ColorGrey, "Stopped"},
		{"start pending", winsvc.StateStartPending, true, ColorYellow, "Starting…"},
		{"stop pending", winsvc.StateStopPending, true, ColorYellow, "Stopping…"},
		{"not installed cfg valid", winsvc.StateNotInstalled, true, ColorGrey, "Not installed"},
		{"not installed cfg invalid", winsvc.StateNotInstalled, false, ColorRed, "Not installed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			color, text := serviceLampPresentation(serviceLamp{state: tc.state, cfgValid: tc.cfgValid})
			if color != tc.wantColor {
				t.Errorf("color: got %v, want %v", color, tc.wantColor)
			}
			if text != tc.wantText {
				t.Errorf("text: got %q, want %q", text, tc.wantText)
			}
		})
	}
}
```

- [ ] **Step 4.2: Run the test to verify it fails**

```bash
go test ./internal/panel/ -run 'TestServerLampPresentation|TestTunnelLampPresentation|TestServiceLampPresentation' -v
```

Expected: FAIL with undefined identifiers.

- [ ] **Step 4.3: Create `internal/panel/lampstate.go`**

```go
package panel

import (
	"sync"

	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

// lampKind enumerates the abstract states the two network lamps can be in.
// Per-lamp text (e.g. "Up" vs "Connected" for lampOK) is resolved by the
// per-lamp presentation functions below.
type lampKind int

const (
	lampChecking lampKind = iota
	lampOK
	lampDisconnected
	lampAuthFailed
	lampServerError
	lampUnreachable
	lampNotConfigured
	lampChiselDown
)

// netLamp is the state of a network-probed lamp (Server or Tunnel).
type netLamp struct {
	kind   lampKind
	detail string // optional human-readable extra info (currently unused; reserved for tooltip)
}

// serviceLamp is the state of the local-service lamp.
type serviceLamp struct {
	state    winsvc.ServiceState
	cfgValid bool
}

// lampState is the panel's shared mutable view of all three lamps.
// All access goes through mu.
type lampState struct {
	mu      sync.Mutex
	service serviceLamp
	server  netLamp
	tunnel  netLamp
}

// snapshot returns a copy of the current lamp triple under the mutex.
// Used by the GUI paint loop.
func (s *lampState) snapshot() (serviceLamp, netLamp, netLamp) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.service, s.server, s.tunnel
}

// setService writes only the service-lamp slot.
func (s *lampState) setService(v serviceLamp) {
	s.mu.Lock()
	s.service = v
	s.mu.Unlock()
}

// setServer writes only the server-lamp slot.
func (s *lampState) setServer(v netLamp) {
	s.mu.Lock()
	s.server = v
	s.mu.Unlock()
}

// setTunnel writes only the tunnel-lamp slot.
func (s *lampState) setTunnel(v netLamp) {
	s.mu.Lock()
	s.tunnel = v
	s.mu.Unlock()
}

// serverLampPresentation maps a netLamp to the color and label text shown
// in the Server row.
func serverLampPresentation(v netLamp) (StatusColor, string) {
	switch v.kind {
	case lampChecking:
		return ColorGrey, "Checking…"
	case lampOK:
		return ColorGreen, "Up"
	case lampChiselDown:
		return ColorRed, "Chisel down"
	case lampUnreachable:
		return ColorGrey, "Unreachable"
	default:
		// lampDisconnected / lampAuthFailed / lampServerError / lampNotConfigured
		// are tunnel-only states; fall back to "Unreachable" if we somehow get
		// them in a server context.
		return ColorGrey, "Unreachable"
	}
}

// tunnelLampPresentation maps a netLamp to the color and label text shown
// in the Tunnel row.
func tunnelLampPresentation(v netLamp) (StatusColor, string) {
	switch v.kind {
	case lampChecking:
		return ColorGrey, "Checking…"
	case lampOK:
		return ColorGreen, "Connected"
	case lampDisconnected:
		return ColorRed, "Disconnected"
	case lampAuthFailed:
		return ColorRed, "Auth failed"
	case lampServerError:
		return ColorYellow, "Server error"
	case lampUnreachable:
		return ColorGrey, "Unreachable"
	case lampNotConfigured:
		return ColorGrey, "Not configured"
	default:
		return ColorGrey, "Unreachable"
	}
}

// serviceLampPresentation maps a serviceLamp to the color and label text
// shown in the Service row. Reuses StatusIndicator() for the color
// (which already encodes "red iff not-installed-and-config-invalid").
func serviceLampPresentation(v serviceLamp) (StatusColor, string) {
	color := StatusIndicator(v.state, v.cfgValid)
	var text string
	switch v.state {
	case winsvc.StateRunning:
		text = "Running"
	case winsvc.StateStartPending:
		text = "Starting…"
	case winsvc.StateStopPending:
		text = "Stopping…"
	case winsvc.StateStopped:
		text = "Stopped"
	case winsvc.StateNotInstalled:
		text = "Not installed"
	default:
		text = v.state.String()
	}
	return color, text
}
```

- [ ] **Step 4.4: Run the tests to verify they pass**

```bash
go test ./internal/panel/ -run 'TestServerLampPresentation|TestTunnelLampPresentation|TestServiceLampPresentation' -v
```

Expected: all PASS.

- [ ] **Step 4.5: Confirm cross-platform build still works**

```bash
go build ./...
GOOS=windows GOARCH=amd64 go build ./...
```

Expected: both succeed.

- [ ] **Step 4.6: Commit**

```bash
git add internal/panel/lampstate.go internal/panel/lampstate_test.go
git commit -m "$(cat <<'EOF'
feat(panel): lamp state types and per-lamp presentation

Cross-platform (no //go:build windows): lampKind enum, lampState mutex-guarded holder, and serviceLampPresentation/serverLampPresentation/tunnelLampPresentation mappings.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Probe functions and loop

**Files:**
- Create: `internal/panel/probe.go`
- Create: `internal/panel/probe_test.go`

- [ ] **Step 5.1: Write failing tests for `mapServerResult` and `mapTunnelResult` (the pure-function part of probe.go)**

Create `internal/panel/probe_test.go`:

```go
package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

func TestMapServerResult(t *testing.T) {
	cases := []struct {
		name string
		h    labbridge.Health
		err  error
		want lampKind
	}{
		{"chisel ok", labbridge.Health{ChiselOK: true}, nil, lampOK},
		{"chisel down", labbridge.Health{ChiselOK: false, Detail: "connection refused"}, nil, lampChiselDown},
		{"server error", labbridge.Health{}, fmt.Errorf("wrap: %w", labbridge.ErrServerError), lampUnreachable},
		{"network error", labbridge.Health{}, errors.New("dial: connection refused"), lampUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapServerResult(tc.h, tc.err)
			if got.kind != tc.want {
				t.Errorf("kind: got %v, want %v", got.kind, tc.want)
			}
		})
	}
}

func TestMapTunnelResult(t *testing.T) {
	cases := []struct {
		name string
		info labbridge.ClientInfo
		err  error
		want lampKind
	}{
		{"connected", labbridge.ClientInfo{Port: 8089, Connected: true}, nil, lampOK},
		{"disconnected", labbridge.ClientInfo{Port: 8089, Connected: false}, nil, lampDisconnected},
		{"unauthorized", labbridge.ClientInfo{}, fmt.Errorf("wrap: %w", labbridge.ErrUnauthorized), lampAuthFailed},
		{"server error", labbridge.ClientInfo{}, fmt.Errorf("wrap: %w", labbridge.ErrServerError), lampServerError},
		{"network error", labbridge.ClientInfo{}, errors.New("dial: connection refused"), lampUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapTunnelResult(tc.info, tc.err)
			if got.kind != tc.want {
				t.Errorf("kind: got %v, want %v", got.kind, tc.want)
			}
		})
	}
}

func TestRunServerProbe_WritesState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"chisel":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	state := &lampState{}
	runServerProbe(context.Background(), srv.Client(), srv.URL, "ua/1", state)

	_, serverLamp, _ := state.snapshot()
	if serverLamp.kind != lampOK {
		t.Errorf("server lamp kind: got %v, want lampOK", serverLamp.kind)
	}
}

func TestRunServerProbe_EmptyBaseSetsUnreachable(t *testing.T) {
	state := &lampState{}
	runServerProbe(context.Background(), http.DefaultClient, "", "ua/1", state)

	_, serverLamp, _ := state.snapshot()
	if serverLamp.kind != lampUnreachable {
		t.Errorf("kind: got %v, want lampUnreachable", serverLamp.kind)
	}
}

func TestRunTunnelProbe_EmptyPassShortCircuits(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	t.Cleanup(srv.Close)

	state := &lampState{}
	runTunnelProbe(context.Background(), srv.Client(), srv.URL, "u", "", "ua/1", state)

	if called {
		t.Error("HTTP server was hit despite empty pass; expected short-circuit")
	}
	_, _, tunnel := state.snapshot()
	if tunnel.kind != lampNotConfigured {
		t.Errorf("kind: got %v, want lampNotConfigured", tunnel.kind)
	}
}

func TestRunTunnelProbe_EmptyBaseSetsUnreachable(t *testing.T) {
	state := &lampState{}
	runTunnelProbe(context.Background(), http.DefaultClient, "", "u", "p", "ua/1", state)

	_, _, tunnel := state.snapshot()
	if tunnel.kind != lampUnreachable {
		t.Errorf("kind: got %v, want lampUnreachable", tunnel.kind)
	}
}

func TestRunTunnelProbe_WritesConnected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"port":8089,"connected":true}`))
	}))
	t.Cleanup(srv.Close)

	state := &lampState{}
	runTunnelProbe(context.Background(), srv.Client(), srv.URL, "u", "p", "ua/1", state)

	_, _, tunnel := state.snapshot()
	if tunnel.kind != lampOK {
		t.Errorf("kind: got %v, want lampOK", tunnel.kind)
	}
}

func TestProbeLoop_RunsImmediatelyAndOnTick(t *testing.T) {
	var calls atomicCounter
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		probeLoop(ctx, 20*time.Millisecond, func(context.Context) {
			calls.inc()
		})
		close(done)
	}()

	// Wait long enough for the immediate call + ~3 ticks.
	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done

	if got := calls.load(); got < 2 {
		t.Errorf("probeLoop calls: got %d, want >=2 (immediate + at least one tick)", got)
	}
}

// atomicCounter is a tiny goroutine-safe counter, scoped to this test file.
type atomicCounter struct {
	mu sync.Mutex
	n  int
}

func (c *atomicCounter) inc() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *atomicCounter) load() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
```

Add the import `"sync"` at the top of `probe_test.go` for `atomicCounter`.

- [ ] **Step 5.2: Run tests to verify they fail**

```bash
go test ./internal/panel/ -run 'TestMapServerResult|TestMapTunnelResult|TestRunServerProbe|TestRunTunnelProbe|TestProbeLoop' -v
```

Expected: FAIL with undefined identifiers.

- [ ] **Step 5.3: Create `internal/panel/probe.go`**

```go
package panel

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

// probeTimeout is the per-call deadline applied inside runServerProbe /
// runTunnelProbe. Independent of the probe-loop tick interval.
const probeTimeout = 5 * time.Second

// mapServerResult turns a (Health, error) pair from labbridge.FetchHealth
// into a netLamp for the Server row.
func mapServerResult(h labbridge.Health, err error) netLamp {
	if err != nil {
		// All error classes (network, 5xx, parse) collapse to Unreachable
		// for the Server lamp — the operator just needs to know the server
		// is not responding usefully right now.
		return netLamp{kind: lampUnreachable, detail: err.Error()}
	}
	if h.ChiselOK {
		return netLamp{kind: lampOK}
	}
	return netLamp{kind: lampChiselDown, detail: h.Detail}
}

// mapTunnelResult turns a (ClientInfo, error) pair from labbridge.FetchClient
// into a netLamp for the Tunnel row.
func mapTunnelResult(info labbridge.ClientInfo, err error) netLamp {
	switch {
	case errors.Is(err, labbridge.ErrUnauthorized):
		return netLamp{kind: lampAuthFailed}
	case errors.Is(err, labbridge.ErrServerError):
		return netLamp{kind: lampServerError}
	case err != nil:
		return netLamp{kind: lampUnreachable, detail: err.Error()}
	case info.Connected:
		return netLamp{kind: lampOK}
	default:
		return netLamp{kind: lampDisconnected}
	}
}

// runServerProbe performs one /api/public/health request (or short-circuits
// if base is empty) and writes the resulting netLamp into state.
func runServerProbe(ctx context.Context, hc *http.Client, base, userAgent string, state *lampState) {
	if base == "" {
		state.setServer(netLamp{kind: lampUnreachable})
		return
	}
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	h, err := labbridge.FetchHealth(cctx, hc, base, userAgent)
	state.setServer(mapServerResult(h, err))
}

// runTunnelProbe performs one /api/public/clients/{user} request, or
// short-circuits to Unreachable / NotConfigured when its inputs are
// missing, and writes the resulting netLamp into state.
func runTunnelProbe(ctx context.Context, hc *http.Client, base, user, pass, userAgent string, state *lampState) {
	if base == "" {
		state.setTunnel(netLamp{kind: lampUnreachable})
		return
	}
	if pass == "" {
		state.setTunnel(netLamp{kind: lampNotConfigured})
		return
	}
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	info, err := labbridge.FetchClient(cctx, hc, base, user, pass, userAgent)
	state.setTunnel(mapTunnelResult(info, err))
}

// probeLoop runs fn(ctx) immediately, then again on every tick of a
// time.Ticker(interval), until ctx is canceled. fn is expected to be
// short-running (a single HTTP request with its own timeout); if fn
// outlasts a tick, the next tick simply waits — no concurrent invocations.
// A defer/recover wraps each call so a panic in net/http or JSON parsing
// doesn't kill the panel; panics are reported via writePanelDebugLog.
func probeLoop(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	call := func() {
		defer func() {
			if r := recover(); r != nil {
				writePanelDebugLog("probe_panic", errors.New(panicString(r)))
			}
		}()
		fn(ctx)
	}
	call()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			call()
		}
	}
}

func panicString(r any) string {
	switch v := r.(type) {
	case string:
		return v
	case error:
		return v.Error()
	default:
		return "non-string, non-error panic"
	}
}
```

`writePanelDebugLog` is already defined in `internal/panel/panel.go`; it's a package-private helper so `probe.go` can call it directly. Note that `panel.go` has `//go:build windows`, so on non-Windows test runs the helper is not visible — this means `probe.go` won't compile on macOS as written. To keep `probe.go` cross-platform, move `writePanelDebugLog` out of the windows-only file in the next step.

- [ ] **Step 5.4: Relocate `writePanelDebugLog` to a build-tag-free file**

Create `internal/panel/debug_log.go`:

```go
package panel

import (
	"fmt"
	"os"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
)

// writePanelDebugLog appends a single line to SerialHop_panel_error.log
// inside %ProgramData%\SerialHop\logs\. Used for failures the operator
// might want to inspect post-mortem without surfacing a popup.
// Best-effort: if the target path is unreachable (paths.LogsDir() == ""),
// the entry is silently dropped.
func writePanelDebugLog(code string, err error) {
	target := paths.PanelErrorLogPath()
	if target == "" {
		return
	}
	line := fmt.Sprintf("%s %s: %v\n", time.Now().Format(time.RFC3339), code, err)
	f, ferr := os.OpenFile(target, //nolint:gosec // target is paths.PanelErrorLogPath(), not user-controlled
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close() //nolint:errcheck
	_, _ = f.WriteString(line)
}
```

Then delete the existing `writePanelDebugLog` definition from `internal/panel/panel.go` (currently lines 630-643 of the file as snapshotted in the spec).

- [ ] **Step 5.5: Run all panel tests**

```bash
go test ./internal/panel/ -race -count=1 -v
```

Expected: all PASS, including the existing `state_test.go` cases.

- [ ] **Step 5.6: Cross-compile for Windows**

```bash
GOOS=windows GOARCH=amd64 go build ./...
```

Expected: build succeeds.

- [ ] **Step 5.7: Commit**

```bash
git add internal/panel/probe.go internal/panel/probe_test.go internal/panel/debug_log.go internal/panel/panel.go
git commit -m "$(cat <<'EOF'
feat(panel): probe functions and ticker loop

runServerProbe and runTunnelProbe map labbridge results to lampKinds; probeLoop runs fn(ctx) immediately then on every tick with panic recovery. writePanelDebugLog moved out of the windows-only file so the cross-platform probe loop can call it.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Wire the Status group widget and probes into the panel

**Files:**
- Modify: `internal/panel/panel.go`

This task replaces the existing top-level `Status:` row with a three-row `GroupBox`, spawns probe goroutines, and updates `refresh()` to read from `lampState` for all three lamps. The walk color fix attempt (`Invalidate()` after `SetTextColor`) is included; a fallback to `CustomWidget` follows in Task 7 only if needed.

- [ ] **Step 6.1: Update imports in `internal/panel/panel.go`**

Replace the import block (currently lines 5-25) with:

```go
import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
	"github.com/bioexperiment-lab-devices/serialhop/internal/updater"
	"github.com/bioexperiment-lab-devices/serialhop/internal/version"
	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)
```

(`net` and `strconv` are new; the others were already present.)

- [ ] **Step 6.2: Replace the widget declarations and Status row inside the `MainWindow` builder**

Find the `var (...)` block at line 57-84 declaring `statusDot`, `statusLabel`, etc. Replace `statusDot` and `statusLabel` (the two top-level ones) with three pairs, one per lamp:

```go
	var (
		mw *walk.MainWindow

		serviceDot   *walk.Label
		serviceLabel *walk.Label
		serverDot    *walk.Label
		serverLbl2   *walk.Label // lamp state text — distinct from serverLbl which shows the configured host:port
		tunnelDot    *walk.Label
		tunnelLabel  *walk.Label

		warnLabel *walk.Label
		statusBar *walk.Label

		serverLbl    *walk.Label // existing: "Chisel server:    <host>:<port>"
		remotePort   *walk.Label
		restPort     *walk.Label
		discoveryLbl *walk.Label
		logLevel     *walk.Label
		rawSerialLbl *walk.Label

		btnInstall   *walk.PushButton
		btnUninstall *walk.PushButton
		btnRestart   *walk.PushButton
		btnOpenCfg   *walk.PushButton
		btnOpenLogs  *walk.PushButton

		updateRow   *walk.Composite
		updateLabel *walk.Label
		btnDownload *walk.PushButton
		btnInstall2 *walk.PushButton
		btnRelease  *walk.PushButton
		btnRetry    *walk.PushButton
		btnCancelDL *walk.PushButton
	)
```

Then in the `MainWindow{...}.Children` slice, replace the existing top status `Composite{...}` (lines 185-192) with a `GroupBox`:

```go
			GroupBox{
				Title:  "Status",
				Layout: Grid{Columns: 3},
				Children: []Widget{
					Label{Text: "Service:"},
					Label{AssignTo: &serviceDot, Text: "●", MinSize: Size{Width: 16}},
					Label{AssignTo: &serviceLabel, Text: "…"},

					Label{Text: "Server:"},
					Label{AssignTo: &serverDot, Text: "●", MinSize: Size{Width: 16}},
					Label{AssignTo: &serverLbl2, Text: "Checking…"},

					Label{Text: "Tunnel:"},
					Label{AssignTo: &tunnelDot, Text: "●", MinSize: Size{Width: 16}},
					Label{AssignTo: &tunnelLabel, Text: "Checking…"},
				},
			},
```

Increase `MainWindow.Size.Height` and `MinSize.Height` from 360 to 420 to fit the extra rows.

- [ ] **Step 6.3: Compose host:port in the configuration-display block**

In `refresh()`, change the existing `serverLbl.SetText(...)` line to:

```go
		serverLbl.SetText("Chisel server:    " + net.JoinHostPort(cfg.LabBridge.Host, strconv.Itoa(cfg.Chisel.Port)))
```

(This was already done in Task 1 step 1.7; double-check it's still present after the import block change.)

- [ ] **Step 6.4: Introduce shared `lampState` and rewrite `refresh()` to use it**

At the top of `Run()`, after `cfg, _ := config.LoadPartial(cfgPath)`, declare and initialize `state`:

```go
	state := &lampState{
		server: netLamp{kind: lampChecking},
		tunnel: netLamp{kind: lampChecking},
	}
```

Rewrite `refresh` so it sources all three lamps' presentation values from `state`:

```go
	refresh := func() {
		scmState, ok := queryServiceState()
		if !ok {
			scmState = lastState
		} else {
			lastState = scmState
		}
		cfg, cfgErr := config.LoadPartial(cfgPath)

		// Update the service slot of the shared state. Network slots are
		// written by their goroutines.
		state.setService(serviceLamp{state: scmState, cfgValid: cfgErr == nil})

		svc, srv, tun := state.snapshot()
		paintLamp(serviceDot, serviceLabel, serviceLampPresentation(svc))
		paintLamp(serverDot, serverLbl2, serverLampPresentation(srv))
		paintLamp(tunnelDot, tunnelLabel, tunnelLampPresentation(tun))

		serverLbl.SetText("Chisel server:    " + net.JoinHostPort(cfg.LabBridge.Host, strconv.Itoa(cfg.Chisel.Port)))
		remotePort.SetText(fmt.Sprintf("Remote port:      %d", cfg.Chisel.RemotePort))
		restPort.SetText(fmt.Sprintf("REST port:        %d", cfg.Rest.Port))
		discoveryLbl.SetText(fmt.Sprintf("Discovery:        include=%v, exclude=%v", cfg.Discovery.Include, cfg.Discovery.Exclude))
		rawSerialState := "disabled"
		if cfg.RawSerial.Enabled {
			rawSerialState = "enabled"
		}
		rawSerialLbl.SetText("Raw serial:       " + rawSerialState)
		logLevel.SetText("Log level:        " + cfg.Log.Level)

		switch {
		case pathsErr != nil:
			warnLabel.SetText("⚠ " + pathsErr.Error())
			warnLabel.SetVisible(true)
		case cfgErr != nil:
			warnLabel.SetText("⚠ " + cfgErr.Error())
			warnLabel.SetVisible(true)
		default:
			warnLabel.SetText("")
			warnLabel.SetVisible(false)
		}

		btns := ComputeButtons(scmState, cfgErr == nil)
		btnInstall.SetEnabled(btns.Install)
		btnUninstall.SetEnabled(btns.Uninstall)
		btnRestart.SetEnabled(btns.Restart)
		btnOpenCfg.SetEnabled(pathsErr == nil)
		btnOpenLogs.SetEnabled(pathsErr == nil)
	}
```

Add a `paintLamp` helper inside `Run()` (or as a package-level windows-only function in `panel.go`):

```go
	paintLamp := func(dot, label *walk.Label, color StatusColor, text string) {
		_ = label.SetText(text)
		switch color {
		case ColorGreen:
			dot.SetTextColor(walk.RGB(0, 160, 0))
		case ColorYellow:
			dot.SetTextColor(walk.RGB(200, 160, 0))
		case ColorRed:
			dot.SetTextColor(walk.RGB(192, 0, 0))
		default:
			dot.SetTextColor(walk.RGB(128, 128, 128))
		}
		dot.Invalidate() // force the WM_PAINT that SetTextColor alone is not triggering.
	}
```

Note: `paintLamp` takes the `(StatusColor, string)` pair returned by the presentation function. Adjust the call sites in `refresh` to spread the return values:

```go
		c, t := serviceLampPresentation(svc); paintLamp(serviceDot, serviceLabel, c, t)
		c, t = serverLampPresentation(srv); paintLamp(serverDot, serverLbl2, c, t)
		c, t = tunnelLampPresentation(tun); paintLamp(tunnelDot, tunnelLabel, c, t)
```

Or rewrite `paintLamp` to take the tuple via a temp struct — pick whichever reads cleaner.

- [ ] **Step 6.5: Spawn the probe goroutines and wire context cancellation**

After `mw.Create()` succeeds and before `timer, err := newTickTimer(...)`, add:

```go
	probeCtx, probeCancel := context.WithCancel(context.Background())
	defer probeCancel()

	probeHC := &http.Client{} // per-call timeout via probeTimeout in probe.go
	userAgent := "SerialHop/" + version.Base() + " (status-probe)"

	go probeLoop(probeCtx, 10*time.Second, func(ctx context.Context) {
		c, _ := config.LoadPartial(cfgPath)
		base := ""
		if c.LabBridge.Host != "" {
			base = "https://" + c.LabBridge.Host
		}
		runServerProbe(ctx, probeHC, base, userAgent, state)
		mw.Synchronize(refresh)
	})
	go probeLoop(probeCtx, 10*time.Second, func(ctx context.Context) {
		c, _ := config.LoadPartial(cfgPath)
		base := ""
		if c.LabBridge.Host != "" {
			base = "https://" + c.LabBridge.Host
		}
		runTunnelProbe(ctx, probeHC, base, c.LabBridge.User, c.LabBridge.Pass, userAgent, state)
		mw.Synchronize(refresh)
	})
```

The `defer probeCancel()` placement ensures probes stop when `Run()` returns (i.e. after `mw.Run()` finishes). `mw.Synchronize(refresh)` makes the lamp update visible within the time it takes the GUI thread to dispatch — typically much faster than waiting for the next 1 s tick.

`userAgent` overrides the one previously declared near line 94 of `panel.go` (which was used by the update-check HTTP client). Rename one of them if both are needed; the existing update-check `userAgent` string is `"SerialHop/" + version.Base() + " (auto-update; +https://github.com/...)"`. Keep that one named `updateUA` and use `userAgent` for the status-probe only:

```go
	updateUA := "SerialHop/" + version.Base() + " (auto-update; +https://github.com/bioexperiment-lab-devices/serialhop)"
	userAgent := "SerialHop/" + version.Base() + " (status-probe)"
```

Update the existing calls that referenced `userAgent` for update-check purposes (`runUpdateCheck(...)`, `ctlDownload(...)`, `applyUpdateRow(...)` arguments) to use `updateUA` instead.

- [ ] **Step 6.6: Verify cross-compile**

```bash
GOOS=windows GOARCH=amd64 go build ./...
```

Expected: build succeeds.

- [ ] **Step 6.7: Run all tests**

```bash
go test ./... -race -count=1
```

Expected: PASS.

- [ ] **Step 6.8: Run linter**

```bash
golangci-lint run ./...
```

Expected: no findings.

- [ ] **Step 6.9: Commit**

```bash
git add internal/panel/panel.go
git commit -m "$(cat <<'EOF'
feat(panel): Status group with Service/Server/Tunnel lamps

Replaces the single Status row with a dedicated GroupBox containing three lamps (Service / Server / Tunnel). Spawns two probe goroutines on a 10 s tick that call labbridge.FetchHealth and labbridge.FetchClient and update shared lampState; refresh() paints. Adds Invalidate() after SetTextColor as the first attempt at fixing the always-black dot.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Manual Windows verification + dot-color fallback

**Files (conditional):**
- Modify: `internal/panel/panel.go` (only if Task 6's Invalidate() does not produce color)

- [ ] **Step 7.1: Build a Windows binary**

```bash
task build
```

Expected: `dist/SerialHop.exe` produced.

- [ ] **Step 7.2: Manual test 1 — fresh install, valid config**

On a Windows test box (or VM), copy `dist/SerialHop.exe` over, set `lab_bridge.host`, `lab_bridge.user`, `lab_bridge.pass`, `chisel.port`, `chisel.remote_port` to known-good values, and click Install. Expected: within ~10 s, all three lamps show green dots — Service: Running, Server: Up, Tunnel: Connected.

**If the dots are still black**, go to Step 7.7.

- [ ] **Step 7.3: Manual test 2 — stop the service**

Click Restart, then Uninstall. Expected: Service lamp turns grey ("Not installed"); Server lamp stays green; Tunnel lamp turns red ("Disconnected") within 10 s as the VPS sees the tunnel drop.

- [ ] **Step 7.4: Manual test 3 — bad credentials**

Set `lab_bridge.pass` to a wrong value, save the config. Expected: within 10 s, Tunnel lamp shows red "Auth failed".

- [ ] **Step 7.5: Manual test 4 — empty pass**

Blank out `lab_bridge.pass`. Expected: within 10 s, Tunnel lamp shows grey "Not configured". The HTTP request must not be made (verify in Wireshark / by stopping the VPS — Tunnel lamp should stay grey).

- [ ] **Step 7.6: Manual test 5 — network drop**

Disconnect the test box from the internet. Expected: within ~15 s, both Server and Tunnel lamps turn grey ("Unreachable"); Service lamp is unaffected. Reconnect — both lamps recover to their previous green state within 10 s.

- [ ] **Step 7.7: Fallback — replace `●` Labels with painted-circle `CustomWidget`s (only if Step 7.2 reported black dots)**

Walk's declarative DSL exposes `CustomWidget` directly (see `github.com/lxn/walk/declarative/customwidget.go`), so this is a straightforward swap.

In `panel.go`, change the three lamp-dot widget declarations and the `paintLamp` helper. The dot variables become `*walk.CustomWidget` and each carries a per-widget `walk.Color` that the `Paint` closure reads:

```go
// Replace the three `*walk.Label` dot declarations with:
var (
	serviceDot   *walk.CustomWidget
	serverDot    *walk.CustomWidget
	tunnelDot    *walk.CustomWidget
)

// Per-lamp current color, mutated by paintLamp and read by the Paint closure.
serviceDotColor := walk.RGB(128, 128, 128)
serverDotColor := walk.RGB(128, 128, 128)
tunnelDotColor := walk.RGB(128, 128, 128)

paintCircle := func(colorPtr *walk.Color) walk.PaintFunc {
	return func(canvas *walk.Canvas, _ walk.Rectangle) error {
		brush, err := walk.NewSolidColorBrush(*colorPtr)
		if err != nil {
			return err
		}
		defer brush.Dispose()
		// Centered 12x12 circle inside the 16x16 widget.
		return canvas.FillEllipsePixels(brush, walk.Rectangle{X: 2, Y: 2, Width: 12, Height: 12})
	}
}
```

In the `GroupBox.Children` slice, replace each `Label{AssignTo: &xxxDot, Text: "●", MinSize: Size{Width: 16}}` with:

```go
CustomWidget{
	AssignTo:         &serviceDot,
	MinSize:          Size{Width: 16, Height: 16},
	MaxSize:          Size{Width: 16, Height: 16},
	ClearsBackground: true,
	Paint:            paintCircle(&serviceDotColor),
},
```

…and analogously for `serverDot`/`serverDotColor` and `tunnelDot`/`tunnelDotColor`.

Update `paintLamp` to mutate the right color variable and invalidate the right `CustomWidget`. Easiest is to make `paintLamp` take a pointer to the color and the widget:

```go
paintLamp := func(dot *walk.CustomWidget, colorPtr *walk.Color, label *walk.Label, color StatusColor, text string) {
	_ = label.SetText(text)
	switch color {
	case ColorGreen:
		*colorPtr = walk.RGB(0, 160, 0)
	case ColorYellow:
		*colorPtr = walk.RGB(200, 160, 0)
	case ColorRed:
		*colorPtr = walk.RGB(192, 0, 0)
	default:
		*colorPtr = walk.RGB(128, 128, 128)
	}
	dot.Invalidate()
}
```

Call sites in `refresh`:

```go
c, t := serviceLampPresentation(svc); paintLamp(serviceDot, &serviceDotColor, serviceLabel, c, t)
c, t = serverLampPresentation(srv); paintLamp(serverDot, &serverDotColor, serverLbl2, c, t)
c, t = tunnelLampPresentation(tun); paintLamp(tunnelDot, &tunnelDotColor, tunnelLabel, c, t)
```

Re-run Steps 7.2–7.6 with the new build. Commit:

```bash
git add internal/panel/panel.go
git commit -m "$(cat <<'EOF'
fix(panel): paint lamp dots via CustomWidget

walk.Label.SetTextColor + Invalidate() did not produce color on a real Windows build. Replaces the three Label-based dots with a small CustomWidget that paints a filled circle, which renders the intended color reliably.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

(If Step 7.2 already produced colored dots, **skip Step 7.7 entirely** — no commit needed.)

- [ ] **Step 7.8: Run `task test` and `task lint` one more time on the host**

```bash
gofmt -l .
go vet ./...
golangci-lint run
go test -race -count=1 ./...
govulncheck ./...
```

Expected: all clean.

---

## Task 8: Open the PR

- [ ] **Step 8.1: Push the branch and open a PR**

```bash
git push -u origin <branch-name>
gh pr create --title "feat: status lamps for service, lab-bridge server, and tunnel" --body "$(cat <<'EOF'
## Summary

- New `Status` group in the panel showing three lamps: **Service** (local SCM state), **Server** (chisel-server liveness via `GET /api/public/health`), **Tunnel** (server's view of this agent's reverse tunnel via `GET /api/public/clients/{user}`).
- Probe goroutines call the new `internal/labbridge` HTTP client on a 10 s tick; results flow into a mutex-guarded `lampState` consumed by the existing 1 s `refresh()` paint loop.
- Fixes the always-black service-status dot (`SetTextColor` + `Invalidate()`; `CustomWidget` fallback if needed).
- **Breaking config schema change:** `chisel.server` / `chisel.user` / `chisel.pass` move to a new top-level `lab_bridge` section as `host` / `user` / `pass`. `chisel.port` is added. No real users yet, so no migration path.

Design: `docs/superpowers/specs/2026-05-11-status-lamps-design.md`
Plan: `docs/superpowers/plans/2026-05-11-status-lamps.md`

## Test plan

- [ ] `gofmt -l .` clean
- [ ] `go vet ./...`
- [ ] `golangci-lint run`
- [ ] `go test -race -count=1 ./...`
- [ ] `govulncheck ./...`
- [ ] Manual Windows: fresh install → all three lamps green
- [ ] Manual Windows: uninstall service → Service grey, Server green, Tunnel red within 10 s
- [ ] Manual Windows: bad `lab_bridge.pass` → Tunnel red ("Auth failed") within 10 s
- [ ] Manual Windows: blank `lab_bridge.pass` → Tunnel grey ("Not configured")
- [ ] Manual Windows: disconnect network → Server + Tunnel grey ("Unreachable"); recover on reconnect

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: PR URL printed; CI runs `pr.yml` and posts `verify` status.

---

## Out-of-scope reminders

- **Dynamic reverse-tunnel port at service startup** — the lab-bridge `GET /api/public/clients/{user}` endpoint also returns the agent's assigned `port`, but using it in the chisel `-R <port>:…` argument is a service-worker change deferred to a follow-up plan.
- **Configurable HTTPS port for the public API** — hard-coded to 443; not configurable. Easy to add later if any deployment needs it.
- **Operator-facing migration tooling for the breaking config change** — none provided; no real users.
