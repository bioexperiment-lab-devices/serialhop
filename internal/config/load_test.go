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
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
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
