package panel

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
)

func TestDecideFirstRun_MissingFile(t *testing.T) {
	s := FirstRunState{Exists: false, ParseErr: nil, Cfg: config.Default()}
	if got := decideFirstRun(s); got != FirstRunShowDialog {
		t.Errorf("missing file: got %v, want ShowDialog", got)
	}
}

func TestDecideFirstRun_BothSet(t *testing.T) {
	c := config.Default()
	c.LabBridge.User = "u"
	c.LabBridge.Pass = "p"
	s := FirstRunState{Exists: true, ParseErr: nil, Cfg: c}
	if got := decideFirstRun(s); got != FirstRunOpenPanel {
		t.Errorf("both set: got %v, want OpenPanel", got)
	}
}

func TestDecideFirstRun_UserBlank(t *testing.T) {
	c := config.Default()
	c.LabBridge.User = ""
	c.LabBridge.Pass = "p"
	s := FirstRunState{Exists: true, ParseErr: nil, Cfg: c}
	if got := decideFirstRun(s); got != FirstRunShowDialog {
		t.Errorf("user blank: got %v, want ShowDialog", got)
	}
}

func TestDecideFirstRun_PassBlank(t *testing.T) {
	c := config.Default()
	c.LabBridge.User = "u"
	c.LabBridge.Pass = ""
	s := FirstRunState{Exists: true, ParseErr: nil, Cfg: c}
	if got := decideFirstRun(s); got != FirstRunShowDialog {
		t.Errorf("pass blank: got %v, want ShowDialog", got)
	}
}

func TestDecideFirstRun_YAMLParseErrorOpensPanel(t *testing.T) {
	s := FirstRunState{Exists: true, ParseErr: errors.New("yaml: invalid"), Cfg: config.Default()}
	if got := decideFirstRun(s); got != FirstRunOpenPanel {
		t.Errorf("parse error: got %v, want OpenPanel (existing warning surfaces it)", got)
	}
}

func TestReadFirstRunState_MissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope.yaml")
	s := readFirstRunState(p)
	if s.Exists {
		t.Errorf("Exists: got true, want false")
	}
	if s.ParseErr != nil {
		t.Errorf("ParseErr: got %v, want nil", s.ParseErr)
	}
	if s.Cfg.LabBridge.Host == "" {
		t.Errorf("Cfg: expected Default() values, got %+v", s.Cfg)
	}
}

func TestReadFirstRunState_PresentParses(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte(`lab_bridge: {host: "10.0.0.1", user: "u", pass: "p"}
rest: {port: 0}
log: {level: "info"}
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := readFirstRunState(p)
	if !s.Exists {
		t.Errorf("Exists: got false, want true")
	}
	if s.ParseErr != nil {
		t.Errorf("ParseErr: got %v, want nil (validation errors are not ParseErr)", s.ParseErr)
	}
	if s.Cfg.LabBridge.User != "u" {
		t.Errorf("user: got %q, want u", s.Cfg.LabBridge.User)
	}
}

func TestReadFirstRunState_MalformedYAMLSetsParseErr(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte("::: not yaml :::"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := readFirstRunState(p)
	if !s.Exists {
		t.Errorf("Exists: got false, want true")
	}
	if s.ParseErr == nil {
		t.Errorf("ParseErr: expected non-nil on malformed YAML")
	}
}
