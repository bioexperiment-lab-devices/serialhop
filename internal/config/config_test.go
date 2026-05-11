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
