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
		{"user empty", func(c *Config) { c.LabBridge.User = "" }, "lab_bridge.user"},
		{"pass empty", func(c *Config) { c.LabBridge.Pass = "" }, "lab_bridge.pass"},
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
			c.LabBridge.User = "u"
			c.LabBridge.Pass = "p"
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

func TestValidate_DefaultIsInvalidUntilCredsSet(t *testing.T) {
	c := Default()
	if err := Validate(&c); err == nil {
		t.Fatalf("Default() should fail validation (empty user/pass)")
	}
}

func TestValidate_DefaultWithCredsIsValid(t *testing.T) {
	c := Default()
	c.LabBridge.User = "u"
	c.LabBridge.Pass = "p"
	if err := Validate(&c); err != nil {
		t.Fatalf("Default()+creds should validate, got %v", err)
	}
}

func TestLoadPartial_Valid(t *testing.T) {
	dir := t.TempDir()
	body := `
lab_bridge:
  host: "10.0.0.1"
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
	if cfg.LabBridge.Host != "10.0.0.1" {
		t.Errorf("host: got %q", cfg.LabBridge.Host)
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
  user: "u"
  pass: "p"
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
  user: "u"
  pass: "p"
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
  user: "u"
  pass: "p"
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
  user: "u"
  pass: "p"
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
	// surface as a clear "lab_bridge.host must be non-empty" error.
	// yaml.v3 silently ignores unknown fields, and Load() seeds from
	// Default() so AutoUpdate.Enabled defaults to true for older configs.
	// To exercise the validation path on an old config, we explicitly clear
	// lab_bridge.host (the only field with a non-empty default that the new
	// schema requires the operator to set).
	dir := t.TempDir()
	body := `
lab_bridge:
  host: ""
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

func TestValidate_FlashingRejectsRelativeBackupDir(t *testing.T) {
	c := Default()
	c.LabBridge.Host = "h"
	c.LabBridge.User = "u"
	c.LabBridge.Pass = "p"
	c.Flashing.Enabled = true
	c.Flashing.BackupDir = "relative/path"
	err := Validate(&c)
	if err == nil {
		t.Fatal("expected error for relative backup_dir, got nil")
	}
	if !strings.Contains(err.Error(), "backup_dir") {
		t.Errorf("error message %q must mention backup_dir", err)
	}
}

func TestValidate_FlashingRejectsNegativeKeepN(t *testing.T) {
	c := Default()
	c.LabBridge.Host = "h"
	c.LabBridge.User = "u"
	c.LabBridge.Pass = "p"
	c.Flashing.KeepN = -1
	err := Validate(&c)
	if err == nil {
		t.Fatal("expected error for negative keep_n, got nil")
	}
	if !strings.Contains(err.Error(), "keep_n") {
		t.Errorf("error message %q must mention keep_n", err)
	}
}

func TestValidate_FlashingAcceptsEmptyBackupDirWhenDisabled(t *testing.T) {
	c := Default()
	c.LabBridge.Host = "h"
	c.LabBridge.User = "u"
	c.LabBridge.Pass = "p"
	c.Flashing.Enabled = false
	c.Flashing.BackupDir = "" // empty + disabled = fine
	if err := Validate(&c); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestLoad_LegacyChiselBlockIgnoredWhenCredsValid(t *testing.T) {
	// Migration path: an existing config file written by a pre-cleanup
	// binary may still contain a `chisel:` block. yaml.v3 silently ignores
	// unknown fields, so the file should still load cleanly as long as the
	// new required fields (lab_bridge.user, lab_bridge.pass) are present.
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
  port: 0
log:
  level: "info"
`
	p := writeFile(t, dir, "cfg.yaml", body)
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.LabBridge.User != "u" || c.LabBridge.Pass != "p" {
		t.Errorf("creds: got %+v", c.LabBridge)
	}
}
