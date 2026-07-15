package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultConfig(t *testing.T) {
	c := Default()
	if c.LabBridge.Host != "111.88.145.138" {
		t.Errorf("lab_bridge.host: got %q, want %q", c.LabBridge.Host, "111.88.145.138")
	}
	if c.LabBridge.User != "" {
		t.Errorf("lab_bridge.user: got %q, want empty (no default identity)", c.LabBridge.User)
	}
	if c.LabBridge.Pass != "" {
		t.Errorf("lab_bridge.pass: got %q, want empty", c.LabBridge.Pass)
	}
	if c.Rest.Port != 0 {
		t.Errorf("rest.port: got %d, want 0", c.Rest.Port)
	}
	if c.Log.Level != "info" {
		t.Errorf("log.level: got %q, want info", c.Log.Level)
	}
}

func TestDefaultConfig_PostOpenSettle(t *testing.T) {
	c := Default()
	if c.Discovery.PostOpenSettleMs != 2000 {
		t.Errorf("discovery.post_open_settle_ms: got %d, want 2000", c.Discovery.PostOpenSettleMs)
	}
}

func TestDefaultConfig_FlashingDefaults(t *testing.T) {
	c := Default()
	if c.Flashing.Enabled {
		t.Errorf("flashing.enabled: got true, want false")
	}
	if c.Flashing.BackupDir != "" {
		t.Errorf("flashing.backup_dir: got %q, want \"\"", c.Flashing.BackupDir)
	}
	if c.Flashing.KeepN != 10 {
		t.Errorf("flashing.keep_n: got %d, want 10", c.Flashing.KeepN)
	}
}

func TestDefaultRawSerialDisabled(t *testing.T) {
	c := Default()
	if c.RawSerial.Enabled {
		t.Errorf("RawSerial.Enabled default = true, want false")
	}
	if c.RawSerial.IdleTimeoutMs != 900000 {
		t.Errorf("RawSerial.IdleTimeoutMs default = %d, want 900000", c.RawSerial.IdleTimeoutMs)
	}
}

func TestWriteScaffold_GoldenSnapshot(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteScaffold(&buf); err != nil {
		t.Fatalf("WriteScaffold: %v", err)
	}
	got := buf.String()

	wantPath := filepath.Join("testdata", "scaffold.golden.yaml")
	want, err := os.ReadFile(wantPath) //nolint:gosec // wantPath is a literal under testdata/
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(want) != got {
		t.Errorf("scaffold output drifted from golden file %s.\nGOT:\n%s\nWANT:\n%s", wantPath, got, string(want))
	}

	// Sanity: scaffold must parse as YAML and round-trip into a usable
	// Config (after filling creds, since they default to "").
	var parsed Config
	if err := yaml.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("scaffold did not parse as YAML: %v\n%s", err, got)
	}
	parsed.LabBridge.User = "u"
	parsed.LabBridge.Pass = "p"
	if err := Validate(&parsed); err != nil {
		t.Errorf("scaffold + creds should validate, got %v", err)
	}
	if parsed.Flashing.Enabled {
		t.Errorf("round-trip flashing.enabled: got true, want false (default)")
	}
	if parsed.Flashing.BackupDir != "" {
		t.Errorf("round-trip flashing.backup_dir: got %q, want \"\"", parsed.Flashing.BackupDir)
	}
	if parsed.Flashing.KeepN != 10 {
		t.Errorf("round-trip flashing.keep_n: got %d, want 10", parsed.Flashing.KeepN)
	}
}
