package config

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultConfig(t *testing.T) {
	c := Default()
	if c.Chisel.Server != "111.88.145.138:7000" {
		t.Errorf("chisel.server: got %q, want %q", c.Chisel.Server, "111.88.145.138:7000")
	}
	if c.Chisel.RemotePort != 8081 {
		t.Errorf("chisel.remote_port: got %d, want 8081", c.Chisel.RemotePort)
	}
	if c.Chisel.User != "devices_coordinator" {
		t.Errorf("chisel.user: got %q, want %q", c.Chisel.User, "devices_coordinator")
	}
	if c.Rest.Port != 0 {
		t.Errorf("rest.port: got %d, want 0", c.Rest.Port)
	}
	if c.Log.Level != "info" {
		t.Errorf("log.level: got %q, want info", c.Log.Level)
	}
}

func TestWriteScaffold_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteScaffold(&buf); err != nil {
		t.Fatalf("WriteScaffold: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "111.88.145.138:7000") {
		t.Errorf("scaffold missing default server; got:\n%s", out)
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
	if parsed.Chisel.Server != def.Chisel.Server {
		t.Errorf("round-trip chisel.server: got %q, want %q", parsed.Chisel.Server, def.Chisel.Server)
	}
	if parsed.Chisel.RemotePort != def.Chisel.RemotePort {
		t.Errorf("round-trip chisel.remote_port: got %d, want %d", parsed.Chisel.RemotePort, def.Chisel.RemotePort)
	}
}
