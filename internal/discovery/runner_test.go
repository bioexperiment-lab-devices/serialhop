package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

func newOpener(t *testing.T, ports map[string][]byte) *serial.FakeOpener {
	t.Helper()
	o := serial.NewFakeOpener()
	for name, reply := range ports {
		fp := serial.NewFakePort(name)
		// Feed response after drain completes but during read timeout
		// (drain is 200ms, probe bytes add ~50ms, so 300ms total)
		go func(port *serial.FakePort, data []byte) {
			time.Sleep(300 * time.Millisecond)
			port.Feed(data)
		}(fp, reply)
		o.Add(fp)
	}
	return o
}

func TestRun_AssignsSequentialIDs(t *testing.T) {
	o := newOpener(t, map[string][]byte{
		"COM3": {10, 1, 2, 3},
		"COM4": {10, 4, 5, 6},
		"COM5": {30, 1, 1, 6},
	})
	devs, err := Run(context.Background(), o, []string{"COM3", "COM4", "COM5"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r := registry.New()
	r.Replace(devs)
	got := r.List()
	if len(got) != 3 {
		t.Fatalf("List len: got %d", len(got))
	}
	wantIDs := []string{"pump_1", "pump_2", "valve_1"}
	for i, w := range wantIDs {
		if got[i].ID != w {
			t.Errorf("got[%d].ID=%q, want %q", i, got[i].ID, w)
		}
	}
}

func TestRun_SkipsUnknownAndPartial(t *testing.T) {
	o := newOpener(t, map[string][]byte{
		"COM3": {10, 1, 2, 3},
		"COM4": {99, 1, 2, 3}, // unknown type byte
		"COM5": {30, 1, 1},    // only 3 bytes
	})
	devs, err := Run(context.Background(), o, []string{"COM3", "COM4", "COM5"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(devs) != 1 {
		t.Errorf("got %d devices, want 1 (only the pump)", len(devs))
	}
}

func TestRun_EmptyPortList(t *testing.T) {
	o := serial.NewFakeOpener()
	devs, err := Run(context.Background(), o, []string{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(devs) != 0 {
		t.Errorf("expected no devices, got %d", len(devs))
	}
}

func TestFilterPorts_Include(t *testing.T) {
	enumerated := []string{"COM1", "COM3", "COM4"}
	got := FilterPorts(enumerated, []string{"COM3", "COM5"}, nil)
	if len(got) != 1 || got[0] != "COM3" {
		t.Errorf("include: got %v, want [COM3]", got)
	}
}

func TestFilterPorts_Exclude(t *testing.T) {
	enumerated := []string{"COM1", "COM3", "COM4"}
	got := FilterPorts(enumerated, nil, []string{"COM1"})
	if len(got) != 2 || got[0] != "COM3" || got[1] != "COM4" {
		t.Errorf("exclude: got %v, want [COM3 COM4]", got)
	}
}

func TestFilterPorts_NoFilter(t *testing.T) {
	enumerated := []string{"COM1", "COM3"}
	got := FilterPorts(enumerated, nil, nil)
	if len(got) != 2 {
		t.Errorf("no filter: got %v, want 2 entries", got)
	}
}

// TestRun_DebugLogsSentAndReplyPerPort verifies the per-port discovery debug
// log records what was sent and what the port answered, as integer arrays
// (matching the convention for /devices/{id}/command logging). Covers the
// matched-device, no-reply, and unknown-type-byte paths.
func TestRun_DebugLogsSentAndReplyPerPort(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	o := serial.NewFakeOpener()

	pump := serial.NewFakePort("COM3")
	go func() {
		time.Sleep(300 * time.Millisecond)
		pump.Feed([]byte{10, 1, 2, 3})
	}()
	o.Add(pump)

	unknown := serial.NewFakePort("COM4")
	go func() {
		time.Sleep(300 * time.Millisecond)
		unknown.Feed([]byte{99, 1, 2, 3})
	}()
	o.Add(unknown)

	silent := serial.NewFakePort("COM5") // no Feed → no reply
	o.Add(silent)

	if _, err := Run(context.Background(), o, []string{"COM3", "COM4", "COM5"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	type logLine struct {
		Msg   string `json:"msg"`
		Port  string `json:"port"`
		Sent  any    `json:"sent"`
		Reply any    `json:"reply"`
	}
	byPort := map[string]logLine{}
	for _, raw := range bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var l logLine
		if err := json.Unmarshal(raw, &l); err != nil {
			t.Fatalf("log line is not JSON: %s", raw)
		}
		if l.Port == "" {
			continue
		}
		byPort[l.Port] = l
	}

	wantSent := []any{1.0, 2.0, 3.0, 4.0, 0.0}
	for _, port := range []string{"COM3", "COM4", "COM5"} {
		l, ok := byPort[port]
		if !ok {
			t.Errorf("no per-port debug line for %s; buf=%s", port, buf.String())
			continue
		}
		gotSent, ok := l.Sent.([]any)
		if !ok {
			t.Errorf("%s: sent: got %T (%v), want JSON number array", port, l.Sent, l.Sent)
			continue
		}
		if !equalNums(gotSent, wantSent) {
			t.Errorf("%s: sent: got %v, want %v", port, gotSent, wantSent)
		}
		if _, ok := l.Reply.([]any); !ok {
			t.Errorf("%s: reply: got %T (%v), want JSON number array", port, l.Reply, l.Reply)
		}
	}

	if got := byPort["COM3"].Msg; got != "discovery: matched device" {
		t.Errorf("COM3: msg=%q, want %q", got, "discovery: matched device")
	}
	if got := byPort["COM3"].Reply.([]any); !equalNums(got, []any{10.0, 1.0, 2.0, 3.0}) {
		t.Errorf("COM3: reply=%v, want [10 1 2 3]", got)
	}
	if got := byPort["COM4"].Msg; got != "discovery: no device on port" {
		t.Errorf("COM4: msg=%q, want %q", got, "discovery: no device on port")
	}
	if got := byPort["COM4"].Reply.([]any); !equalNums(got, []any{99.0, 1.0, 2.0, 3.0}) {
		t.Errorf("COM4: reply=%v, want [99 1 2 3]", got)
	}
	if got := byPort["COM5"].Msg; got != "discovery: no device on port" {
		t.Errorf("COM5: msg=%q, want %q", got, "discovery: no device on port")
	}
	if got := byPort["COM5"].Reply.([]any); len(got) != 0 {
		t.Errorf("COM5: reply=%v, want empty (no reply)", got)
	}
}

func equalNums(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		af, aok := a[i].(float64)
		bf, bok := b[i].(float64)
		if !aok || !bok || af != bf {
			return false
		}
	}
	return true
}
