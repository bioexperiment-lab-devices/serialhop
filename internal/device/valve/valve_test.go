package valve_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/valve"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

type fixture struct {
	t     *testing.T
	s     *device.Session
	clock *device.FakeClock
	port  *serial.FakePort
	dir   string
}

type fixtureOpt func(*device.SessionConfig)

func withStateDir(dir string) fixtureOpt {
	return func(cfg *device.SessionConfig) { cfg.StateDir = dir }
}

func withProbeReply(r []byte) fixtureOpt {
	return func(cfg *device.SessionConfig) { cfg.ProbeReply = r }
}

func shrinkTimeouts(t *testing.T) {
	t.Helper()
	oldPB, oldDW := device.PerByteTimeout, device.DrainWindow
	device.PerByteTimeout, device.DrainWindow = 10*time.Millisecond, 0
	t.Cleanup(func() { device.PerByteTimeout, device.DrainWindow = oldPB, oldDW })
}

// newFixture boots a real Session hosting the valve driver. Attach consumes
// one position-query transaction; devicePos is the position counter the
// device reports there (DrainWindow is 0, so the pre-fed reply survives the
// transaction's drain step). The valve driver has no watcher goroutines, so
// pre-feeding replies is race-free.
func newFixture(t *testing.T, devicePos byte, opts ...fixtureOpt) *fixture {
	t.Helper()
	shrinkTimeouts(t)
	clock := device.NewFakeClock(time.Unix(1000, 0))
	port := serial.NewFakePort("COM9")
	opener := serial.NewFakeOpener()
	opener.Add(port)
	conn, err := opener.Open("COM9")
	if err != nil {
		t.Fatal(err)
	}
	cfg := device.SessionConfig{
		ID: "valve_1", Type: "valve", TypeCode: valve.TypeCode, PortName: "COM9",
		Conn: conn, Opener: opener, Clock: clock, StateDir: t.TempDir(),
		Factory:    valve.New,
		ProbeReply: []byte{30, 1, 1, 6}, // radial-6 build
		Reprobe:    func(p serial.Port) ([]byte, error) { return []byte{30, 1, 1, 6}, nil },
	}
	for _, o := range opts {
		o(&cfg)
	}
	port.Feed([]byte{30, 1, 1, devicePos}) // Attach's position-query reply
	s := device.NewSession(cfg)
	s.Start(context.Background())
	t.Cleanup(s.Close)
	f := &fixture{t: t, s: s, clock: clock, port: port, dir: cfg.StateDir}
	waitFor(t, "attach", s.Connected)
	return f
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (f *fixture) exec(cmd, params string) device.Response {
	f.t.Helper()
	req := device.Request{ID: "t-" + cmd, Cmd: cmd}
	if params != "" {
		req.Params = json.RawMessage(params)
	}
	return f.s.Execute(context.Background(), req)
}

// frames splits everything written to the port into 5-byte command frames.
func (f *fixture) frames() [][]byte {
	tx := f.port.Written()
	var out [][]byte
	for i := 0; i+5 <= len(tx); i += 5 {
		out = append(out, tx[i:i+5])
	}
	return out
}

func frameEq(a []byte, b ...byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// resultMap round-trips a Result through JSON for shape assertions.
func (f *fixture) resultMap(resp device.Response) map[string]any {
	f.t.Helper()
	b, err := json.Marshal(resp.Result)
	if err != nil {
		f.t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		f.t.Fatal(err)
	}
	return m
}

// readState decodes the port-keyed persistent state file.
func readState(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "valve-COM9.json")) // #nosec G304 -- dir is t.TempDir(), filename is a fixed literal
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAttachQueriesPositionAndPushesConfig(t *testing.T) {
	f := newFixture(t, 0)
	fr := f.frames()
	// TRANSLATION §3: position query, then the RAM-only config mirror —
	// default shortest (code 3) and hold OFF (N3=1: inverted encoding)
	if len(fr) != 3 || !frameEq(fr[0], 33, 1, 0, 0, 0) ||
		!frameEq(fr[1], 35, 1, 3, 0, 0) || !frameEq(fr[2], 35, 2, 1, 0, 0) {
		t.Fatalf("attach frames: %v", fr)
	}
}

func TestIdentifyOmitsSerialAndDerivesCapabilities(t *testing.T) {
	f := newFixture(t, 0)
	resp := f.exec("identify", "")
	if resp.Status != "ok" {
		t.Fatalf("identify: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["device_type"] != "distribution_valve" || m["model"] != "radial-6" ||
		m["firmware_version"] != "legacy" || m["protocol_version"] != "1.0" {
		t.Fatalf("identify result: %v", m)
	}
	if _, ok := m["serial"]; ok {
		t.Fatalf("valve identify must omit serial (no serial command): %v", m)
	}
	caps := m["capabilities"].(map[string]any)
	if caps["positions"] != 6.0 || caps["seconds_per_position"] != 0.92 {
		t.Fatalf("capabilities: %v", caps)
	}
	modes := caps["rotation_modes"].([]any)
	if len(modes) != 3 || modes[0] != "shortest" || modes[1] != "direct" || modes[2] != "wrap" {
		t.Fatalf("rotation_modes: %v", caps)
	}
}

// TestAttachTwoPositionBuild: the position count comes from probeReply[3],
// not a constant — a 2-output build reports positions 2, model radial-2.
func TestAttachTwoPositionBuild(t *testing.T) {
	f := newFixture(t, 0, withProbeReply([]byte{30, 1, 1, 2}))
	m := f.resultMap(f.exec("identify", ""))
	caps := m["capabilities"].(map[string]any)
	if m["model"] != "radial-2" || caps["positions"] != 2.0 {
		t.Fatalf("2-position build: %v", m)
	}
}

func TestUnknownCommand(t *testing.T) {
	f := newFixture(t, 0)
	resp := f.exec("frobnicate", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeUnknownCommand {
		t.Fatalf("resp: %+v", resp)
	}
}

func TestRegister(t *testing.T) {
	valve.Register()
	name, factory, ok := device.LookupDriver(valve.TypeCode)
	if !ok || name != "valve" || factory == nil {
		t.Fatalf("LookupDriver(30) = %q %v %v", name, factory, ok)
	}
}

// TestDetachPersistsIdleState: Detach persists {physical_position,
// device_belief_at_shutdown, config} — and needs NO serial I/O.
func TestDetachPersistsIdleState(t *testing.T) {
	f := newFixture(t, 5)
	n := len(f.port.Written())
	f.s.Close()
	m := readState(t, f.dir)
	if m["schema_version"] != 1.0 || m["physical_position"] != nil ||
		m["device_belief_at_shutdown"] != 5.0 {
		t.Fatalf("persisted state: %v", m)
	}
	cfg := m["config"].(map[string]any)
	if cfg["default_rotation"] != "shortest" || cfg["hold_torque"] != false {
		t.Fatalf("persisted config: %v", cfg)
	}
	if len(f.port.Written()) != n {
		t.Fatal("Detach must not write to the serial port")
	}
}
