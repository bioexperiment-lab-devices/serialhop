package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

func init() {
	PostOpenSettle = 0
}

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
	matches, err := Run(context.Background(), o, []string{"COM3", "COM4", "COM5"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(matches) != 3 {
		t.Fatalf("matches len: got %d", len(matches))
	}
	// Sorted by (TypeCode, Port): the two pumps come before the valve, so
	// ordinal IDs are assigned in that deterministic order.
	wantIDs := []string{"pump_1", "pump_2", "valve_1"}
	for i, w := range wantIDs {
		if matches[i].ID != w {
			t.Errorf("matches[%d].ID=%q, want %q", i, matches[i].ID, w)
		}
	}
	for _, m := range matches {
		_ = m.Conn.Close()
	}
}

func TestRun_SkipsUnknownPartialAndUnopenable(t *testing.T) {
	o := newOpener(t, map[string][]byte{
		"COM3": {10, 1, 2, 3},
		"COM4": {99, 1, 2, 3}, // unknown type byte → classified as no-match, closed
		"COM5": {30, 1, 1},    // only 3 bytes → no-match, closed
	})
	// COMX is a candidate the opener does not know about, so Open fails and
	// the port is skipped entirely.
	matches, err := Run(context.Background(), o, []string{"COM3", "COM4", "COM5", "COMX"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(matches) != 1 || matches[0].Type != "pump" {
		t.Errorf("got %+v, want 1 match (only the pump)", matches)
	}
	_ = matches[0].Conn.Close()
}

func TestRun_EmptyPortList(t *testing.T) {
	o := serial.NewFakeOpener()
	matches, err := Run(context.Background(), o, []string{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected no matches, got %d", len(matches))
	}
}

func TestRun_CapturesProbeReply(t *testing.T) {
	o := newOpener(t, map[string][]byte{
		"COM3": {10, 1, 2, 3},
	})
	matches, err := Run(context.Background(), o, []string{"COM3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches: %+v", matches)
	}
	m := matches[0]
	if m.ID != "pump_1" || m.Type != "pump" || m.TypeCode != 10 || m.Port != "COM3" {
		t.Fatalf("match fields: %+v", m)
	}
	if len(m.Reply) < 4 || m.Reply[0] != 10 {
		t.Fatalf("probe reply must be captured for SessionConfig.ProbeReply: %v", m.Reply)
	}
	if m.Conn == nil {
		t.Fatal("matched port must stay open")
	}
	_ = m.Conn.Close()
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
		Level string `json:"level"`
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
	if got := byPort["COM4"].Msg; got != "discovery: unknown device type" {
		t.Errorf("COM4: msg=%q, want %q", got, "discovery: unknown device type")
	}
	if got := byPort["COM4"].Level; got != "WARN" {
		t.Errorf("COM4: level=%q, want %q", got, "WARN")
	}
	if got := byPort["COM4"].Reply.([]any); !equalNums(got, []any{99.0, 1.0, 2.0, 3.0}) {
		t.Errorf("COM4: reply=%v, want [99 1 2 3]", got)
	}
	if got := byPort["COM5"].Msg; got != "discovery: no device on port" {
		t.Errorf("COM5: msg=%q, want %q", got, "discovery: no device on port")
	}
	if got := byPort["COM5"].Level; got != "DEBUG" {
		t.Errorf("COM5: level=%q, want %q", got, "DEBUG")
	}
	if got := byPort["COM5"].Reply.([]any); len(got) != 0 {
		t.Errorf("COM5: reply=%v, want empty (no reply)", got)
	}
}

// A port that only ever produces a partial frame must be logged at Warn,
// distinguishable from a silent port — a truncated device hiding in Debug
// logs is how the original bug went unnoticed for 30 days.
func TestRun_WarnsOnPartialReply(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	o := newOpener(t, map[string][]byte{
		"COM9": {30, 1}, // 2 bytes, then silence — retry also comes up empty
	})
	matches, err := Run(context.Background(), o, []string{"COM9"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no matches, got %+v", matches)
	}
	if rec.Find(slog.LevelWarn,
		"discovery: partial probe reply (device present, frame incomplete)",
		map[string]any{"port": "COM9", "reply": []int{30, 1}}) == nil {
		t.Errorf("missing partial-reply warn; records=%+v", rec.Records())
	}
	if rec.Find(slog.LevelDebug, "discovery: no device on port",
		map[string]any{"port": "COM9"}) != nil {
		t.Errorf("partial reply must not be logged as 'no device on port'")
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
