package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfig_JSONUsesSnakeCaseTags(t *testing.T) {
	c := Default()
	c.LabBridge.User = "alice"
	c.LabBridge.Pass = "pw"
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		`"lab_bridge"`,
		`"host"`,
		`"user"`,
		`"pass"`,
		`"rest"`,
		`"port"`,
		`"discovery"`,
		`"include"`,
		`"exclude"`,
		`"post_open_settle_ms"`,
		`"log"`,
		`"level"`,
		`"raw_serial"`,
		`"auto_update"`,
		`"flashing"`,
		`"enabled"`,
		`"backup_dir"`,
		`"keep_n"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("JSON missing key %s; body:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{
		`"LabBridge"`,
		`"Host"`,
		`"User"`,
		`"PostOpenSettleMs"`,
		`"BackupDir"`,
		`"KeepN"`,
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("JSON contains Go-CamelCase key %s; body:\n%s", unwanted, body)
		}
	}
}

func TestConfig_JSONRoundTripPreservesAllFields(t *testing.T) {
	in := Default()
	in.LabBridge.Host = "h.example"
	in.LabBridge.User = "alice"
	in.LabBridge.Pass = "pw"
	in.Rest.Port = 49283
	in.Discovery.Include = []string{"COM5", "COM6"}
	in.Discovery.PostOpenSettleMs = 1500
	in.Log.Level = "debug"
	in.RawSerial.Enabled = true
	in.AutoUpdate.Enabled = false
	in.Flashing.Enabled = true
	in.Flashing.BackupDir = `C:\Backups`
	in.Flashing.KeepN = 7

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Config
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.LabBridge != in.LabBridge {
		t.Errorf("LabBridge: got %+v, want %+v", out.LabBridge, in.LabBridge)
	}
	if out.Rest != in.Rest {
		t.Errorf("Rest: got %+v, want %+v", out.Rest, in.Rest)
	}
	if out.Log != in.Log || out.RawSerial != in.RawSerial || out.AutoUpdate != in.AutoUpdate || out.Flashing != in.Flashing {
		t.Errorf("non-LabBridge fields: got %+v, want %+v", out, in)
	}
	// Slice equality:
	if len(out.Discovery.Include) != len(in.Discovery.Include) || (len(out.Discovery.Include) > 0 && out.Discovery.Include[0] != in.Discovery.Include[0]) {
		t.Errorf("Discovery.Include round-trip failed: got %v, want %v", out.Discovery.Include, in.Discovery.Include)
	}
}
