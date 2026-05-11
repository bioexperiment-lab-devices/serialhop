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
chisel:
  server: "10.0.0.1:7000"
  remote_port: 9000
  user: "u"
  pass: "p"
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
	if c.Chisel.Server != "10.0.0.1:7000" {
		t.Errorf("server: got %q", c.Chisel.Server)
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
		{"server empty", func(c *Config) { c.Chisel.Server = "" }, "chisel.server"},
		{"server no port", func(c *Config) { c.Chisel.Server = "host" }, "host:port"},
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
chisel:
  server: "10.0.0.1:7000"
  remote_port: 9001
  user: "u"
  pass: "p"
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
	if cfg.Chisel.Server != "10.0.0.1:7000" {
		t.Errorf("server: got %q", cfg.Chisel.Server)
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
chisel:
  server: ""
  remote_port: 9001
log:
  level: "info"
`
	p := writeFile(t, dir, "cfg.yaml", body)
	cfg, err := LoadPartial(p)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "chisel.server must be non-empty") {
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
	if cfg.Chisel.Server != def.Chisel.Server {
		t.Errorf("on parse failure, expected Default()-server %q, got %q", def.Chisel.Server, cfg.Chisel.Server)
	}
}

func TestLoad_PostOpenSettleCustom(t *testing.T) {
	dir := t.TempDir()
	body := `
chisel:
  server: "10.0.0.1:7000"
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
chisel:
  server: "10.0.0.1:7000"
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
	if cfg.Chisel.Server != def.Chisel.Server {
		t.Errorf("on missing file, expected Default()-server %q, got %q", def.Chisel.Server, cfg.Chisel.Server)
	}
}

func TestLoad_AutoUpdateDisabled(t *testing.T) {
	dir := t.TempDir()
	body := `
chisel:
  server: "10.0.0.1:7000"
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
chisel:
  server: "10.0.0.1:7000"
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
