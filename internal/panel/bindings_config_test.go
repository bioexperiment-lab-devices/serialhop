//go:build windows

package panel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
)

func TestSaveConfig_WritesYAMLAndReadsBack(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.yaml")
	t.Setenv("SERIALHOP_TEST_CONFIG_PATH", cfgPath)

	app := NewApp()
	cfg := config.Default()
	cfg.LabBridge.User = "alice"
	cfg.LabBridge.Pass = "pw"
	cfg.LabBridge.Host = "h.example"

	res := app.SaveConfig(cfg)
	if !res.OK {
		t.Fatalf("SaveConfig: %+v", res)
	}

	got, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LabBridge.User != "alice" || got.LabBridge.Pass != "pw" || got.LabBridge.Host != "h.example" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestSaveConfig_ValidationFailureSurfaces(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.yaml")
	t.Setenv("SERIALHOP_TEST_CONFIG_PATH", cfgPath)

	app := NewApp()
	cfg := config.Default() // user/pass empty → validation fails
	res := app.SaveConfig(cfg)
	if res.OK {
		t.Errorf("expected save to fail validation")
	}
	if len(res.FieldErrors) == 0 {
		t.Errorf("expected field errors, got none")
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Errorf("yaml should not have been written on validation failure")
	}
}

func TestValidateConfig_HappyPath(t *testing.T) {
	app := NewApp()
	cfg := config.Default()
	cfg.LabBridge.User = "alice"
	cfg.LabBridge.Pass = "pw"
	if errs := app.ValidateConfig(cfg); len(errs) != 0 {
		t.Errorf("expected no errors, got %+v", errs)
	}
}

func TestLoadConfigFromDisk_ReturnsDefaultWhenMissing(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nope.yaml")
	t.Setenv("SERIALHOP_TEST_CONFIG_PATH", cfgPath)

	got := NewApp().LoadConfigFromDisk()
	if got.LabBridge.Host != config.Default().LabBridge.Host {
		t.Errorf("expected default; got %+v", got)
	}
}

func TestVerifyCredentials_UnchangedSkipsVerify(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.yaml")
	t.Setenv("SERIALHOP_TEST_CONFIG_PATH", cfgPath)

	app := NewApp()
	cfg := config.Default()
	cfg.LabBridge.Host = "h.example"
	cfg.LabBridge.User = "alice"
	cfg.LabBridge.Pass = "pw"
	if !app.SaveConfig(cfg).OK {
		t.Fatalf("seed SaveConfig failed")
	}

	res := app.VerifyCredentials("h.example", "alice", "pw")
	if res.Outcome != "skipped" {
		t.Errorf("Outcome: got %q, want skipped", res.Outcome)
	}
}
