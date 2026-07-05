package pump_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

func TestPingIdleUsesIdentifyFrameOnly(t *testing.T) {
	f := newFixture(t)
	before := len(f.frames())
	f.port.Feed([]byte{10, 0, 0, 0}) // identify reply
	resp := f.exec("ping", "")
	if resp.Status != "ok" {
		t.Fatalf("ping: %+v", resp)
	}
	fr := f.frames()
	if len(fr) != before+1 || !frameEq(fr[len(fr)-1], 1, 2, 3, 0, 0) {
		t.Fatalf("idle ping must send exactly one identify frame (EEPROM-safe): %v", fr[before:])
	}
	if _, ok := f.resultMap(resp)["uptime_ms"]; !ok {
		t.Fatalf("ping result: %v", f.resultMap(resp))
	}
}

func TestPingUptimeTracksClock(t *testing.T) {
	f := newFixture(t)
	f.clock.Advance(8 * time.Second)
	f.port.Feed([]byte{10, 0, 0, 0})
	m := f.resultMap(f.exec("ping", ""))
	if m["uptime_ms"] != float64(8000) {
		t.Fatalf("uptime_ms = %v, want 8000", m["uptime_ms"])
	}
}

func TestPingFailureGoesUnreachable(t *testing.T) {
	f := newFixture(t)
	// no reply fed → both transaction attempts time out
	resp := f.exec("ping", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("ping: %+v", resp)
	}
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })
}

func TestStatusIdleShape(t *testing.T) {
	f := newCalibratedFixture(t)
	m := f.resultMap(f.exec("status", ""))
	if m["state"] != "idle" || m["job"] != nil || m["direction"] != nil ||
		m["speed_ml_min"] != nil || m["dispensed_ml"] != nil {
		t.Fatalf("idle status: %v", m)
	}
	cal, ok := m["calibration"].(map[string]any)
	if !ok || cal["ml_per_step"] != 0.0005 {
		t.Fatalf("calibration block: %v", m["calibration"])
	}
	// calibration was persisted before this connection → clamped to 0
	if cal["set_at_uptime_ms"] != float64(0) {
		t.Fatalf("set_at_uptime_ms: %v", cal)
	}
}

func TestStatusUncalibrated(t *testing.T) {
	f := newFixture(t)
	m := f.resultMap(f.exec("status", ""))
	if m["calibration"] != nil {
		t.Fatalf("uncalibrated status must have null calibration: %v", m)
	}
}

func TestGetCalibration(t *testing.T) {
	f := newCalibratedFixture(t)
	m := f.resultMap(f.exec("get_calibration", ""))
	if m["ml_per_step"] != 0.0005 {
		t.Fatalf("get_calibration: %v", m)
	}
	f2 := newFixture(t)
	resp := f2.exec("get_calibration", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeNotCalibrated {
		t.Fatalf("uncalibrated get_calibration: %+v", resp)
	}
}

func TestRotateSendsArmingThenMotionFrame(t *testing.T) {
	f := newCalibratedFixture(t)
	resp := f.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	if resp.Status != "ok" {
		t.Fatalf("rotate: %+v", resp)
	}
	fr := f.frames()
	n := len(fr)
	// TRANSLATION §4 rotate steps 4–5: cmd-10 arming frame (forces the pause
	// toggle to "running"), then the 11/12 motion frame. 3 ml/min → [1 50].
	if !frameEq(fr[n-2], 10, 0, 1, 50, 0) || !frameEq(fr[n-1], 11, 0, 1, 50, 0) {
		t.Fatalf("frames: %v", fr[n-2:])
	}
	m := f.resultMap(resp)
	if m["state"] != "rotating" || m["direction"] != "forward" || m["speed_ml_min"] != 3.0 {
		t.Fatalf("result: %v", m)
	}
	if st := f.resultMap(f.exec("status", "")); st["state"] != "rotating" {
		t.Fatalf("status: %v", st)
	}
}

func TestRotateEchoesQuantizedSpeed(t *testing.T) {
	f := newCalibratedFixture(t)
	m := f.resultMap(f.exec("rotate", `{"direction":"reverse","speed_ml_min":2.9}`))
	// 2.9 → 5200 µs → actual = 15000/5200 ≈ 2.8846: echo ACTUAL, not requested
	want := 30_000_000 * 0.0005 / 5200
	if m["speed_ml_min"] != want {
		t.Fatalf("speed_ml_min = %v, want %v", m["speed_ml_min"], want)
	}
	fr := f.frames()
	if fr[len(fr)-1][0] != 12 {
		t.Fatalf("reverse must use opcode 12: %v", fr[len(fr)-1])
	}
}

func TestRotateRetargetsWhileRotating(t *testing.T) {
	f := newCalibratedFixture(t)
	f.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	resp := f.exec("rotate", `{"direction":"reverse","speed_ml_min":3.0}`)
	if resp.Status != "ok" {
		t.Fatalf("retarget must be allowed while rotating: %+v", resp)
	}
}

func TestRotateRequiresVerifiedCalibration(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeNotCalibrated {
		t.Fatalf("uncalibrated rotate: %+v", resp)
	}
	fu := newFixture(t, withProbeReply([]byte{10, 0, 195, 80})) // unverified mirror
	resp = fu.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeNotCalibrated {
		t.Fatalf("unverified rotate: %+v", resp)
	}
	m, _ := resp.Error.Details.(map[string]any)
	if m["reason"] != "unverified_mirror" {
		t.Fatalf("details: %#v", resp.Error.Details)
	}
}

func TestRotateInvalidParams(t *testing.T) {
	f := newCalibratedFixture(t)
	for _, params := range []string{
		`{"direction":"sideways","speed_ml_min":3.0}`,
		`{"direction":"forward","speed_ml_min":0}`,
		`{"direction":"forward","speed_ml_min":40}`, // > 37.5 max at this calibration
		`not json`,
	} {
		resp := f.exec("rotate", params)
		if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
			t.Fatalf("params %s: %+v", params, resp)
		}
	}
}

func TestRotateRawBypassesCalibration(t *testing.T) {
	f := newFixture(t) // uncalibrated
	resp := f.exec("rotate_raw", `{"direction":"forward","speed_pct":50}`)
	if resp.Status != "ok" {
		t.Fatalf("rotate_raw: %+v", resp)
	}
	fr := f.frames()
	n := len(fr)
	// 50% → 200 µs clamped to 400 → P=4 → [1 4]
	if !frameEq(fr[n-2], 10, 0, 1, 4, 0) || !frameEq(fr[n-1], 11, 0, 1, 4, 0) {
		t.Fatalf("frames: %v", fr[n-2:])
	}
	m := f.resultMap(resp)
	if m["state"] != "rotating" || m["speed_pct"] != float64(50) {
		t.Fatalf("result: %v", m)
	}
}

func TestRotateRawValidatesPct(t *testing.T) {
	f := newFixture(t)
	for _, params := range []string{`{"direction":"forward","speed_pct":0}`,
		`{"direction":"forward","speed_pct":101}`} {
		resp := f.exec("rotate_raw", params)
		if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
			t.Fatalf("params %s: %+v", params, resp)
		}
	}
}
