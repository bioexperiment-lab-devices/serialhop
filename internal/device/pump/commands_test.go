package pump_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/pump"
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

func countFrames(f *fixture, opcode byte) int {
	n := 0
	for _, fr := range f.frames() {
		if fr[0] == opcode {
			n++
		}
	}
	return n
}

func TestPauseFreezesJobClock(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.clock.Advance(10 * time.Second) // halfway through the 20 s estimate

	resp := f.exec("pause", "")
	if resp.Status != "ok" {
		t.Fatalf("pause: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["state"] != "paused" || m["job_id"] != id {
		t.Fatalf("pause result: %v", m)
	}
	if m["dispensed_ml"].(float64) < 0.45 || m["dispensed_ml"].(float64) > 0.55 {
		t.Fatalf("dispensed estimate: %v", m)
	}
	if countFrames(f, 19) != 1 {
		t.Fatalf("pause must send exactly one cmd-19 frame: %v", f.frames())
	}

	// while paused, elapsed and progress are frozen and the job survives
	// well past its estimate
	f.clock.Advance(time.Minute)
	js := jobState(t, f, id)
	if js["state"] != "paused" || js["elapsed_s"] != 10.0 {
		t.Fatalf("paused job: %v", js)
	}

	// resume unfreezes; the timer path completes after the REMAINING time
	resp = f.exec("resume", "")
	if resp.Status != "ok" || f.resultMap(resp)["state"] != "dispensing" {
		t.Fatalf("resume: %+v", resp)
	}
	if countFrames(f, 19) != 2 {
		t.Fatalf("resume must send exactly one more cmd-19 frame")
	}
	f.port.Feed([]byte{10, 26, 25, 1}) // disarm ping reply
	f.clock.Advance(10*time.Second + pump.TimerGrace)
	waitFor(t, "job success after resume", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
}

func TestPauseWhileRotating(t *testing.T) {
	f := newCalibratedFixture(t)
	f.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	resp := f.exec("pause", "")
	if resp.Status != "ok" || f.resultMap(resp)["state"] != "paused" {
		t.Fatalf("pause while rotating: %+v", resp)
	}
	resp = f.exec("resume", "")
	if resp.Status != "ok" || f.resultMap(resp)["state"] != "rotating" {
		t.Fatalf("resume back to rotating: %+v", resp)
	}
}

func TestPauseIdleIsBusy(t *testing.T) {
	f := newCalibratedFixture(t)
	resp := f.exec("pause", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("pause idle: %+v", resp)
	}
	m, _ := resp.Error.Details.(map[string]any)
	if m["state"] != "idle" {
		t.Fatalf("details: %#v", resp.Error.Details)
	}
}

func TestPauseTwiceRejected(t *testing.T) {
	f := newCalibratedFixture(t)
	startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.exec("pause", "")
	resp := f.exec("pause", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("double pause would double-toggle cmd 19: %+v", resp)
	}
	if countFrames(f, 19) != 1 {
		t.Fatal("second pause must not send a frame")
	}
	resp = f.exec("resume", "")
	if resp.Status != "ok" {
		t.Fatalf("resume: %+v", resp)
	}
	resp = f.exec("resume", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("double resume: %+v", resp)
	}
}

func TestStopCancelsWatcherJob(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.clock.Advance(5 * time.Second) // a quarter through the 20 s estimate

	f.port.Feed([]byte{10, 0, 0, 0}) // post-stop verification reply
	resp := f.exec("stop", "")
	if resp.Status != "ok" {
		t.Fatalf("stop: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["state"] != "idle" || m["cancelled_job_id"] != id {
		t.Fatalf("stop result: %v", m)
	}
	if got := m["dispensed_ml"].(float64); got < 0.2 || got > 0.3 {
		t.Fatalf("dispensed estimate: %v", got)
	}
	// frame order: ... [10 0 0 0 0] halt, then [1 2 3 0 0] verification
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 10, 0, 0, 0, 0) || !frameEq(fr[n-1], 1, 2, 3, 0, 0) {
		t.Fatalf("stop frames: %v", fr[n-2:])
	}
	if js := jobState(t, f, id); js["state"] != "cancelled" {
		t.Fatalf("job: %v", js)
	}
	// watcher fully torn down: a fresh dispense works and completes
	id2 := startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.port.Feed([]byte{0x01, 0x28, 0x0A, 0x40})
	f.port.Feed([]byte{10, 26, 25, 1})
	waitFor(t, "second dispense completes", func() bool {
		return jobState(t, f, id2)["state"] == "succeeded"
	})
}

func TestStopEndsRotation(t *testing.T) {
	f := newCalibratedFixture(t)
	f.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	f.port.Feed([]byte{10, 0, 0, 0})
	resp := f.exec("stop", "")
	m := f.resultMap(resp)
	if resp.Status != "ok" || m["state"] != "idle" {
		t.Fatalf("stop: %+v", resp)
	}
	if _, has := m["cancelled_job_id"]; has {
		t.Fatalf("no job to cancel when rotating: %v", m)
	}
	if f.resultMap(f.exec("status", ""))["state"] != "idle" {
		t.Fatal("status must be idle")
	}
}

func TestStopWhilePausedCancels(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.exec("pause", "")
	f.port.Feed([]byte{10, 0, 0, 0})
	resp := f.exec("stop", "")
	if resp.Status != "ok" || f.resultMap(resp)["cancelled_job_id"] != id {
		t.Fatalf("stop while paused: %+v", resp)
	}
	if js := jobState(t, f, id); js["state"] != "cancelled" {
		t.Fatalf("job: %v", js)
	}
}

func TestStopIdleSucceeds(t *testing.T) {
	f := newCalibratedFixture(t)
	f.port.Feed([]byte{10, 0, 0, 0})
	resp := f.exec("stop", "")
	if resp.Status != "ok" || f.resultMap(resp)["state"] != "idle" {
		t.Fatalf("idle stop must succeed: %+v", resp)
	}
}

func TestStopVerificationFailure(t *testing.T) {
	f := newCalibratedFixture(t)
	startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	// no verification reply → hardware_error and unreachable
	resp := f.exec("stop", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("stop without verification reply: %+v", resp)
	}
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })
}
