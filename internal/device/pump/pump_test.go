package pump_test

import (
	"context"
	"encoding/json"
	"math"
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
// no transactions — it derives everything from the probe reply — so nothing
// needs to be pre-fed to the port for attach to complete.
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
	s := device.NewSession(cfg)
	s.Start(context.Background())
	t.Cleanup(s.Close)
	f := &fixture{t: t, s: s, clock: clock, port: port, dir: cfg.StateDir}
	waitFor(t, "attach", s.Connected)
	return f
}

// newCalibratedFixture attaches against a probe reply carrying the
// calibration mirror (0.0005 ml/step: 3 ml/min → [n3 n4] = [1 50], 1 ml →
// 2000 steps) — this is the new source of truth; there is no on-disk store
// to pre-write anymore. 0.0005 ml/step × 1e8 = 50000 = 0x00C350.
func newCalibratedFixture(t *testing.T, opts ...fixtureOpt) *fixture {
	t.Helper()
	mirrorOpts := []fixtureOpt{
		withProbeReply([]byte{10, 0x00, 0xC3, 0x50}),
		func(cfg *device.SessionConfig) {
			cfg.Reprobe = func(p serial.Port) ([]byte, error) {
				return []byte{10, 0x00, 0xC3, 0x50}, nil
			}
		},
	}
	return newFixture(t, append(mirrorOpts, opts...)...)
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

func TestAttachServesIdentifyWithNoSerial(t *testing.T) {
	f := newFixture(t)
	// Attach transacts nothing (no real firmware answers opcode 11): the
	// port must be silent until the test itself talks to it.
	if fr := f.frames(); len(fr) != 0 {
		t.Fatalf("attach must send no frames: %v", fr)
	}
	resp := f.exec("identify", "")
	if resp.Status != "ok" {
		t.Fatalf("identify: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["serial"] != nil || m["device_type"] != "pump" ||
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

// TestAttachRecoversMirrorCalibration proves the EEPROM mirror in the probe
// reply is trusted immediately (no store, no "unverified" gate — see
// Attach's doc comment in pump.go).
func TestAttachRecoversMirrorCalibration(t *testing.T) {
	f := newCalibratedFixture(t)
	caps := f.resultMap(f.exec("identify", ""))["capabilities"].(map[string]any)
	sr, ok := caps["speed_ml_min"].(map[string]any)
	if !ok {
		t.Fatalf("mirror-calibrated pump must report speed limits: %v", caps)
	}
	// max = 30e6 × 0.0005 / 400 = 37.5; min = 30e6 × 0.0005 / 6502500
	if sr["max"] != 37.5 {
		t.Fatalf("max speed: %v", sr)
	}
}

// TestFinishJobDisarmsWithZeroStepRun proves the end-of-job frame is the
// zero-step opcode-18 run, not the absent opcode-11 ping. Real firmware never
// answers opcode 11, so using it failed every dispense at completion.
func TestFinishJobDisarmsWithZeroStepRun(t *testing.T) {
	f := newCalibratedFixture(t)

	// Attach sends no frames at all (it derives everything from the probe
	// reply), so base is 0 today; kept as a snapshot rather than a bare 0 so
	// this test stays correct if Attach or an earlier step in this test ever
	// starts writing to the port.
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

// TestAttachNeedsNoIdentityRead proves attach completes against firmware that
// answers ONLY the identify probe. No real pump implements opcode 11, so a
// mandatory identity read left every pump at connected=false.
func TestAttachNeedsNoIdentityRead(t *testing.T) {
	shrinkTimeouts(t)
	clock := device.NewFakeClock(time.Unix(1000, 0))
	port := serial.NewFakePort("COM7")
	opener := serial.NewFakeOpener()
	opener.Add(port)
	conn, err := opener.Open("COM7")
	if err != nil {
		t.Fatal(err)
	}
	// Calibration mirror 92000 = 0x016760, exactly what COM7 reports.
	s := device.NewSession(device.SessionConfig{
		ID: "pump_1", Type: "pump", TypeCode: pump.TypeCode, PortName: "COM7",
		Conn: conn, Opener: opener, Clock: clock, StateDir: t.TempDir(),
		Factory:    pump.New,
		ProbeReply: []byte{10, 0x01, 0x67, 0x60},
		Reprobe: func(p serial.Port) ([]byte, error) {
			return []byte{10, 0x01, 0x67, 0x60}, nil
		},
	})
	s.Start(context.Background())
	t.Cleanup(s.Close)

	// Nothing is fed to the port: any transaction during attach would hang.
	waitFor(t, "attach", s.Connected)

	// The brief's test used s.Info(), which does not exist on *device.Session
	// (only CachedInfo() (Info, bool) does) — fixed here per this package's
	// real API rather than weakening the assertion.
	info, ok := s.CachedInfo()
	if !ok {
		t.Fatal("CachedInfo: no info cached after attach")
	}
	if info.Serial != "" {
		t.Errorf("Serial = %q, want empty (no firmware serial exists)", info.Serial)
	}
}

// TestAttachTrustsEepromCalibration proves ml_per_step is taken from the
// device mirror on every attach and is immediately usable — for both a
// value read-back (get_calibration) and an actual metered command
// (dispense) with no confirmation step in between. The get_calibration
// check alone would pass even against the old store-first/mirror-as-
// unverified-fallback design, since d.mlPerStep is populated either way and
// get_calibration never consulted the (now-removed) unverified gate; only
// requireCalibration did, and only a command that reaches it (like
// dispense) can prove the gate is actually gone.
//
// direction "reverse" selects opcode 16 (timer-completed) rather than the
// opcode-18/background-watcher path "forward" would take; this test never
// feeds a completion reply, and a watcher goroutine left running past the
// test would race the next test's shrinkTimeouts mutation of the shared
// WatchPoll var.
func TestAttachTrustsEepromCalibration(t *testing.T) {
	f := newFixture(t, withProbeReply([]byte{10, 0x01, 0x67, 0x60}))
	resp := f.exec("get_calibration", "")
	if resp.Error != nil {
		t.Fatalf("get_calibration: %v", resp.Error)
	}
	// 92000 / 1e8 = 0.00092 ml/step
	got := f.resultMap(resp)["ml_per_step"].(float64)
	if math.Abs(got-0.00092) > 1e-12 {
		t.Errorf("ml_per_step = %v, want 0.00092", got)
	}
	if resp := f.exec("dispense", `{"direction":"reverse","volume_ml":0.1,"speed_ml_min":3.0}`); resp.Error != nil {
		t.Fatalf("dispense rejected on a mirror-calibrated pump: %v", resp.Error)
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
