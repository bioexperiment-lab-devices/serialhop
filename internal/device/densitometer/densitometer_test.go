package densitometer_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/densitometer"
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
	return func(cfg *device.SessionConfig) {
		cfg.ProbeReply = r
		cfg.Reprobe = func(p serial.Port) ([]byte, error) { return r, nil }
	}
}

// shrinkTimeouts collapses every real-time and clock knob so tests run fast.
func shrinkTimeouts(t *testing.T) {
	t.Helper()
	oldPB, oldDW := device.PerByteTimeout, device.DrainWindow
	oldTS, oldLS := densitometer.ThermoSettle, densitometer.LivenessSpacing
	device.PerByteTimeout, device.DrainWindow = 10*time.Millisecond, 0
	densitometer.ThermoSettle, densitometer.LivenessSpacing = 5*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() {
		device.PerByteTimeout, device.DrainWindow = oldPB, oldDW
		densitometer.ThermoSettle, densitometer.LivenessSpacing = oldTS, oldLS
	})
}

// attachReplies feeds the three reply-bearing frames of first-contact Attach:
// serial number, channel-1 descriptor, thermostat readback (defaults to 10.00,
// a fresh-boot value that first-contact ignores).
func feedAttach(port *serial.FakePort, thermReadback byte) {
	port.Feed([]byte{5, 7, 25, 6})            // 71 0 0 5 → serial 25-006
	port.Feed([]byte{1, 2, 6, 0})             // 71 0 0 1 → wavelength 600
	port.Feed([]byte{5, 5, thermReadback, 0}) // 76 2     → device set-point
}

func newFixture(t *testing.T, opts ...fixtureOpt) *fixture {
	t.Helper()
	shrinkTimeouts(t)
	clock := device.NewFakeClock(time.Unix(1000, 0))
	port := serial.NewFakePort("COM8")
	opener := serial.NewFakeOpener()
	opener.Add(port)
	conn, err := opener.Open("COM8")
	if err != nil {
		t.Fatal(err)
	}
	cfg := device.SessionConfig{
		ID: "densitometer_1", Type: "densitometer", TypeCode: densitometer.TypeCode,
		PortName: "COM8", Conn: conn, Opener: opener, Clock: clock, StateDir: t.TempDir(),
		Factory:    densitometer.New,
		ProbeReply: []byte{70, 0, 0, 2},
		Reprobe:    func(p serial.Port) ([]byte, error) { return []byte{70, 0, 0, 2}, nil },
	}
	for _, o := range opts {
		o(&cfg)
	}
	feedAttach(port, 10) // first-contact: readback ignored, so 10 is fine
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

func TestAttachReadsSerialWavelengthAndForcesTubeCorrection(t *testing.T) {
	f := newFixture(t)
	fr := f.frames()
	// Attach order: serial read, channel-1 read, force-tube (75 3 0 0 0),
	// thermostat read (76 2), first-contact disable (75 2 0 0 0).
	if !frameEq(fr[0], 71, 0, 0, 5, 0) {
		t.Fatalf("frame 0 must be serial read: %v", fr[0])
	}
	if !frameEq(fr[1], 71, 0, 0, 1, 0) {
		t.Fatalf("frame 1 must be channel-1 read: %v", fr[1])
	}
	if !frameEq(fr[2], 75, 3, 0, 0, 0) {
		t.Fatalf("frame 2 must force tube correction to 1.0: %v", fr[2])
	}
	if !frameEq(fr[3], 76, 2, 0, 0, 0) {
		t.Fatalf("frame 3 must read thermostat set-point: %v", fr[3])
	}
	if !frameEq(fr[4], 75, 2, 0, 0, 0) {
		t.Fatalf("frame 4 must disable thermostat on first contact: %v", fr[4])
	}
}

func TestAttachServesIdentify(t *testing.T) {
	f := newFixture(t)
	m := f.resultMap(f.exec("identify", ""))
	if m["device_type"] != "densitometer" || m["serial"] != "25-006" ||
		m["model"] != "TDS909A-wide" || m["firmware_version"] != "legacy" ||
		m["protocol_version"] != "1.0" {
		t.Fatalf("identify: %v", m)
	}
	caps := m["capabilities"].(map[string]any)
	if caps["wavelength_nm"] != float64(600) || caps["brightness_levels"] != float64(20) ||
		caps["temperature_sensor"] != "DS18B20" {
		t.Fatalf("capabilities: %v", caps)
	}
	th := caps["thermostat"].(map[string]any)
	if th["min_c"] != 20.0 || th["max_c"] != 45.0 {
		t.Fatalf("thermostat caps: %v", th)
	}
}

func TestAttachPersistsFirstContactMirror(t *testing.T) {
	dir := t.TempDir()
	f := newFixture(t, withStateDir(dir))
	st := device.NewStore(dir, "densitometer-25-006")
	var ps struct {
		SchemaVersion  int     `json:"schema_version"`
		TubeCorrection float64 `json:"tube_correction"`
		Thermostat     struct {
			Enabled bool    `json:"enabled"`
			TargetC float64 `json:"target_c"`
		} `json:"thermostat"`
	}
	found, err := st.Load(&ps)
	if err != nil || !found {
		t.Fatalf("state not persisted: found=%v err=%v", found, err)
	}
	if ps.SchemaVersion != 1 || ps.TubeCorrection != 1.0 || ps.Thermostat.Enabled {
		t.Fatalf("first-contact state: %+v", ps)
	}
	_ = f
}

func TestUnknownCommand(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("frobnicate", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeUnknownCommand {
		t.Fatalf("resp: %+v", resp)
	}
}

func TestRegister(t *testing.T) {
	densitometer.Register()
	name, factory, ok := device.LookupDriver(densitometer.TypeCode)
	if !ok || name != "densitometer" || factory == nil {
		t.Fatalf("LookupDriver(70) = %q %v %v", name, factory, ok)
	}
}
