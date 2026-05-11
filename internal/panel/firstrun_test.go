package panel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

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

func TestVerifyCredentials_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"port":8089,"connected":true}`))
	}))
	t.Cleanup(srv.Close)
	got := verifyCredentials(context.Background(), srv.Client(), srv.URL, "u", "p", "test/1")
	if got.Kind != CredsOK {
		t.Errorf("Kind: got %v, want CredsOK (detail=%q)", got.Kind, got.Detail)
	}
}

func TestVerifyCredentials_401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", 401)
	}))
	t.Cleanup(srv.Close)
	got := verifyCredentials(context.Background(), srv.Client(), srv.URL, "u", "wrong", "test/1")
	if got.Kind != CredsUnauthorized {
		t.Errorf("Kind: got %v, want CredsUnauthorized", got.Kind)
	}
}

func TestVerifyCredentials_500NeedsConfirm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "roster broken", 500)
	}))
	t.Cleanup(srv.Close)
	got := verifyCredentials(context.Background(), srv.Client(), srv.URL, "u", "p", "test/1")
	if got.Kind != CredsNeedsConfirm {
		t.Errorf("Kind: got %v, want CredsNeedsConfirm", got.Kind)
	}
	if got.Detail == "" {
		t.Errorf("Detail should describe the error")
	}
}

func TestVerifyCredentials_NetworkNeedsConfirm(t *testing.T) {
	// Point at a closed port.
	got := verifyCredentials(context.Background(), &http.Client{Timeout: 100 * time.Millisecond},
		"http://127.0.0.1:1", "u", "p", "test/1")
	if got.Kind != CredsNeedsConfirm {
		t.Errorf("Kind: got %v, want CredsNeedsConfirm", got.Kind)
	}
}

func TestPatchCredentials_ReplacesUserAndPass(t *testing.T) {
	in := []byte(`# top comment
lab_bridge:
  host: "10.0.0.1"   # host comment
  user: ""           # user comment
  pass: ""           # pass comment

rest:
  port: 0
`)
	got, err := patchCredentials(in, "alice", "s3cret")
	if err != nil {
		t.Fatalf("patchCredentials: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, `user: "alice"`) {
		t.Errorf("user not replaced:\n%s", s)
	}
	if !strings.Contains(s, `pass: "s3cret"`) {
		t.Errorf("pass not replaced:\n%s", s)
	}
	if !strings.Contains(s, `host: "10.0.0.1"`) {
		t.Errorf("host should be preserved:\n%s", s)
	}
	if !strings.Contains(s, "# top comment") {
		t.Errorf("top comment dropped:\n%s", s)
	}
	if !strings.Contains(s, "# host comment") {
		t.Errorf("inline host comment dropped:\n%s", s)
	}
}

func TestPatchCredentials_PreservesUnrelatedFields(t *testing.T) {
	in := []byte(`lab_bridge:
  host: "h"
  user: ""
  pass: ""
discovery:
  include: ["COM3", "COM4"]
log:
  level: "debug"
`)
	got, err := patchCredentials(in, "alice", "s3cret")
	if err != nil {
		t.Fatalf("patchCredentials: %v", err)
	}
	var c config.Config
	if err := yaml.Unmarshal(got, &c); err != nil {
		t.Fatalf("unmarshal patched: %v", err)
	}
	if c.LabBridge.User != "alice" || c.LabBridge.Pass != "s3cret" {
		t.Errorf("creds: got user=%q pass=%q", c.LabBridge.User, c.LabBridge.Pass)
	}
	if c.Log.Level != "debug" {
		t.Errorf("log.level: got %q, want debug", c.Log.Level)
	}
	if len(c.Discovery.Include) != 2 || c.Discovery.Include[0] != "COM3" {
		t.Errorf("discovery.include not preserved: %+v", c.Discovery.Include)
	}
}

func TestPatchCredentials_AppendsLabBridgeWhenAbsent(t *testing.T) {
	in := []byte(`rest:
  port: 0
log:
  level: "info"
`)
	got, err := patchCredentials(in, "alice", "s3cret")
	if err != nil {
		t.Fatalf("patchCredentials: %v", err)
	}
	var c config.Config
	if err := yaml.Unmarshal(got, &c); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	if c.LabBridge.User != "alice" || c.LabBridge.Pass != "s3cret" {
		t.Errorf("creds: got user=%q pass=%q", c.LabBridge.User, c.LabBridge.Pass)
	}
}

func TestPatchCredentials_AddsKeysWhenLabBridgePresentButCredsMissing(t *testing.T) {
	in := []byte(`lab_bridge:
  host: "h"
rest:
  port: 0
`)
	got, err := patchCredentials(in, "alice", "s3cret")
	if err != nil {
		t.Fatalf("patchCredentials: %v", err)
	}
	var c config.Config
	if err := yaml.Unmarshal(got, &c); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	if c.LabBridge.Host != "h" {
		t.Errorf("host: got %q", c.LabBridge.Host)
	}
	if c.LabBridge.User != "alice" || c.LabBridge.Pass != "s3cret" {
		t.Errorf("creds: got user=%q pass=%q", c.LabBridge.User, c.LabBridge.Pass)
	}
}

func TestPatchCredentials_RejectsMalformedYAML(t *testing.T) {
	_, err := patchCredentials([]byte("::: not yaml :::"), "u", "p")
	if err == nil {
		t.Errorf("expected error on malformed YAML, got nil")
	}
}

func TestWriteOrPatchCreds_CreatesScaffoldWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	if err := writeOrPatchCreds(p, "alice", "s3cret"); err != nil {
		t.Fatalf("writeOrPatchCreds: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `user: "alice"`) {
		t.Errorf("scaffold missing user:\n%s", data)
	}
	if !strings.Contains(string(data), `pass: "s3cret"`) {
		t.Errorf("scaffold missing pass:\n%s", data)
	}
	// File must validate end-to-end (post-creds).
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.LabBridge.User != "alice" {
		t.Errorf("loaded user: got %q", c.LabBridge.User)
	}
}

func TestWriteOrPatchCreds_PatchesWhenPresent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte(`lab_bridge:
  host: "10.0.0.1"
  user: ""
  pass: ""
log:
  level: "debug"
`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := writeOrPatchCreds(p, "alice", "s3cret"); err != nil {
		t.Fatalf("writeOrPatchCreds: %v", err)
	}
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.LabBridge.User != "alice" || c.LabBridge.Pass != "s3cret" {
		t.Errorf("creds: got %+v", c.LabBridge)
	}
	if c.Log.Level != "debug" {
		t.Errorf("log.level not preserved: got %q", c.Log.Level)
	}
}
