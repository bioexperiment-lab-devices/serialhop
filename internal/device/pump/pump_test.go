package pump_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/pump"
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

func withProbeReply(r []byte) fixtureOpt {
	return func(cfg *device.SessionConfig) { cfg.ProbeReply = r }
}

func withStateDir(dir string) fixtureOpt {
	return func(cfg *device.SessionConfig) { cfg.StateDir = dir }
}

func shrinkTimeouts(t *testing.T) {
	t.Helper()
	oldPB, oldDW, oldWP := device.PerByteTimeout, device.DrainWindow, pump.WatchPoll
	device.PerByteTimeout, device.DrainWindow, pump.WatchPoll =
		10*time.Millisecond, 0, 5*time.Millisecond
	t.Cleanup(func() {
		device.PerByteTimeout, device.DrainWindow, pump.WatchPoll = oldPB, oldDW, oldWP
	})
}

// newFixture boots a real Session hosting the pump driver. Attach consumes
// one serial-number transaction, so its reply is pre-fed (DrainWindow is 0,
// so pre-fed RX survives the transaction's drain step).
func newFixture(t *testing.T, opts ...fixtureOpt) *fixture {
	t.Helper()
	shrinkTimeouts(t)
	clock := device.NewFakeClock(time.Unix(1000, 0))
	port := serial.NewFakePort("COM7")
	opener := serial.NewFakeOpener()
	opener.Add(port)
	conn, err := opener.Open("COM7")
	if err != nil {
		t.Fatal(err)
	}
	cfg := device.SessionConfig{
		ID: "pump_1", Type: "pump", TypeCode: pump.TypeCode, PortName: "COM7",
		Conn: conn, Opener: opener, Clock: clock, StateDir: t.TempDir(),
		Factory:    pump.New,
		ProbeReply: []byte{10, 0, 0, 0}, // no calibration mirror by default
		Reprobe:    func(p serial.Port) ([]byte, error) { return []byte{10, 0, 0, 0}, nil },
	}
	for _, o := range opts {
		o(&cfg)
	}
	port.Feed([]byte{10, 26, 25, 1}) // Attach's serial-number reply
	s := device.NewSession(cfg)
	s.Start(context.Background())
	t.Cleanup(s.Close)
	f := &fixture{t: t, s: s, clock: clock, port: port, dir: cfg.StateDir}
	waitFor(t, "attach", s.Connected)
	return f
}

// newCalibratedFixture pre-writes a verified calibration (0.0005 ml/step:
// 3 ml/min → [n3 n4] = [1 50], 1 ml → 2000 steps) into the state dir.
func newCalibratedFixture(t *testing.T, opts ...fixtureOpt) *fixture {
	t.Helper()
	dir := t.TempDir()
	st := device.NewStore(dir, "pump-26-025")
	err := st.Save(map[string]any{
		"schema_version": 1, "ml_per_step": 0.0005,
		"set_at": time.Unix(900, 0).UTC(), "serial": "26-025",
	})
	if err != nil {
		t.Fatal(err)
	}
	return newFixture(t, append([]fixtureOpt{withStateDir(dir)}, opts...)...)
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

func TestAttachReadsSerialAndServesIdentify(t *testing.T) {
	f := newFixture(t)
	if !frameEq(f.frames()[0], 11, 2, 3, 4, 5) {
		t.Fatalf("first frame must be the serial-number read: %v", f.frames())
	}
	resp := f.exec("identify", "")
	if resp.Status != "ok" {
		t.Fatalf("identify: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["serial"] != "26-025" || m["device_type"] != "pump" ||
		m["model"] != "peristaltic-1ch" || m["firmware_version"] != "legacy" ||
		m["protocol_version"] != "1.0" {
		t.Fatalf("identify result: %v", m)
	}
	caps := m["capabilities"].(map[string]any)
	if caps["channels"] != float64(1) || caps["supports_gradient"] != true ||
		caps["supports_drop_suckback"] != true {
		t.Fatalf("capabilities: %v", caps)
	}
	if caps["speed_ml_min"] != nil {
		t.Fatalf("uncalibrated pump must not report speed limits: %v", caps)
	}
}

func TestAttachRecoversVerifiedCalibration(t *testing.T) {
	f := newCalibratedFixture(t)
	caps := f.resultMap(f.exec("identify", ""))["capabilities"].(map[string]any)
	sr, ok := caps["speed_ml_min"].(map[string]any)
	if !ok {
		t.Fatalf("calibrated pump must report speed limits: %v", caps)
	}
	// max = 30e6 × 0.0005 / 400 = 37.5; min = 30e6 × 0.0005 / 6502500
	if sr["max"] != 37.5 {
		t.Fatalf("max speed: %v", sr)
	}
	if caps["calibration_unverified"] != nil {
		t.Fatalf("verified calibration must not be flagged: %v", caps)
	}
}

func TestAttachProposesUnverifiedMirrorCalibration(t *testing.T) {
	// mirror bytes encode 50000 → proposed ml_per_step = 50000/1e8 = 0.0005,
	// but unverified: no speed limits, capabilities flagged.
	f := newFixture(t, withProbeReply([]byte{10, 0, 195, 80}))
	caps := f.resultMap(f.exec("identify", ""))["capabilities"].(map[string]any)
	if caps["calibration_unverified"] != true {
		t.Fatalf("mirror recovery must be flagged unverified: %v", caps)
	}
	if caps["speed_ml_min"] != nil {
		t.Fatalf("unverified calibration must not report speed limits: %v", caps)
	}
}

// TestFinishJobDisarmsWithZeroStepRun proves the end-of-job frame is the
// zero-step opcode-18 run, not the absent opcode-11 ping. Real firmware never
// answers opcode 11, so using it failed every dispense at completion.
func TestFinishJobDisarmsWithZeroStepRun(t *testing.T) {
	f := newCalibratedFixture(t)

	// newCalibratedFixture's Attach already wrote its own opcode-11
	// serial-number-read frame before this test does anything (Task 3's job
	// to remove, not this one's) — snapshot the frame count so the ping
	// check below looks only at what THIS dispense/completion writes, not
	// at Attach's unrelated opcode-11 use.
	base := len(f.frames())

	// 1 ml at 0.0005 ml/step = 2000 steps. direction must be "forward" (opcode
	// 18 — the only motion opcode with a completion reply, so it is the one
	// that reaches finishJob via the watch path) and speed_ml_min must be
	// positive (speedToBytes rejects <=0); the brief's literal
	// {"volume_ml":1.0} omits both required params and fails validation
	// before ever reaching the disarm frame.
	if resp := f.exec("dispense", `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`); resp.Error != nil {
		t.Fatalf("dispense: %v", resp.Error)
	}
	f.port.Feed([]byte{0, 0, 0, 10}) // run completion (elapsed us)
	f.port.Feed([]byte{0, 0, 0, 10}) // disarm frame's elapsed-us reply
	// Jobs().Active() is loop-only (device/jobs.go); calling it from the test
	// goroutine races with the session loop under -race. HasActiveJob() is
	// the documented cross-goroutine-safe mirror (device/session.go).
	waitFor(t, "job done", func() bool { return !f.s.HasActiveJob() })

	var sawDisarm, sawPing bool
	for _, fr := range f.frames()[base:] {
		if frameEq(fr, 18, 0, 0, 0, 0) {
			sawDisarm = true
		}
		if frameEq(fr, 11, 2, 3, 4, 5) {
			sawPing = true
		}
	}
	if !sawDisarm {
		t.Errorf("no zero-step disarm frame in %v", f.frames()[base:])
	}
	if sawPing {
		t.Error("opcode-11 ping still sent; real firmware never answers it")
	}
}

func TestUnknownCommand(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("frobnicate", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeUnknownCommand {
		t.Fatalf("resp: %+v", resp)
	}
}

func TestRegister(t *testing.T) {
	pump.Register()
	name, factory, ok := device.LookupDriver(pump.TypeCode)
	if !ok || name != "pump" || factory == nil {
		t.Fatalf("LookupDriver(10) = %q %v %v", name, factory, ok)
	}
}
