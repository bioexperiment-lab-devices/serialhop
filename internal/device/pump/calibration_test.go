package pump_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/pump"
)

// runCalibration drives a start_calibration job to success and returns its id.
// 20000 steps at the default 50 % (→ 400 µs half-period): estimate 16 s.
func runCalibration(t *testing.T, f *fixture) string {
	t.Helper()
	resp := f.exec("start_calibration", `{"speed_pct":50}`)
	if resp.Status != "ok" {
		t.Fatalf("start_calibration: %+v", resp)
	}
	id := f.resultMap(resp)["job"].(map[string]any)["job_id"].(string)
	if st := f.resultMap(f.exec("status", ""))["state"]; st != "calibrating" {
		t.Fatalf("state = %v", st)
	}
	// completion reply: 15,800,000 µs = 0x00F116C0, then disarm ping reply
	f.port.Feed([]byte{0x00, 0xF1, 0x16, 0xC0})
	f.port.Feed([]byte{10, 26, 25, 1})
	waitFor(t, "calibration completes", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
	return id
}

func TestStartCalibrationFramesAndResult(t *testing.T) {
	f := newFixture(t) // works UNCALIBRATED — that's the point
	id := runCalibration(t, f)
	fr := f.frames()
	// find the config + opcode-18 frames it sent (before the disarm ping):
	// 50 % → 400 µs → [1 4]; 20000 steps = 0x00004E20
	n := len(fr)
	if !frameEq(fr[n-3], 10, 0, 1, 4, 0) || !frameEq(fr[n-2], 18, 0, 0, 78, 32) {
		t.Fatalf("calibration frames: %v", fr)
	}
	res := jobState(t, f, id)["result"].(map[string]any)
	if res["steps"] != float64(20000) || res["duration_s"] != 15.8 {
		t.Fatalf("calibration result: %v", res)
	}
}

func TestSetCalibrationFromJob(t *testing.T) {
	f := newFixture(t)
	id := runCalibration(t, f)
	// measured 10.0 ml over 20000 steps → 0.0005 ml/step
	// mirror v = 0.0005 × 1e8 = 50000 = 0x00C350 → frame [13 0 0 195 80],
	// then identify returns the same bytes for the mirror verify.
	f.port.Feed([]byte{10, 0, 195, 80})
	resp := f.exec("set_calibration", `{"job_id":"`+id+`","measured_volume_ml":10.0}`)
	if resp.Status != "ok" {
		t.Fatalf("set_calibration: %+v", resp)
	}
	if f.resultMap(resp)["ml_per_step"] != 0.0005 {
		t.Fatalf("result: %v", f.resultMap(resp))
	}
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 13, 0, 0, 195, 80) || !frameEq(fr[n-1], 1, 2, 3, 4, 181) {
		t.Fatalf("mirror frames: %v", fr[n-2:])
	}
	// capabilities refreshed: identify now reports speed limits
	caps := f.resultMap(f.exec("identify", ""))["capabilities"].(map[string]any)
	if caps["speed_ml_min"] == nil {
		t.Fatalf("capabilities not refreshed: %v", caps)
	}
	// metered dispensing is now available
	if resp := f.exec("dispense", `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`); resp.Status != "ok" {
		t.Fatalf("dispense after calibration: %+v", resp)
	}
}

// TestCalibrationSurvivesOnlyViaDeviceMirror proves persistence now lives
// entirely on the device: a fresh session recovers calibration only when the
// probe reply carries the mirror bytes a prior set_calibration wrote to
// EEPROM. There is no local file store to fall back on anymore — Attach no
// longer reads or writes one (see Attach's doc comment in pump.go).
func TestCalibrationSurvivesOnlyViaDeviceMirror(t *testing.T) {
	f := newFixture(t)
	id := runCalibration(t, f)
	f.port.Feed([]byte{10, 0, 195, 80})
	if resp := f.exec("set_calibration", `{"job_id":"`+id+`","measured_volume_ml":10.0}`); resp.Status != "ok" {
		t.Fatalf("set_calibration: %+v", resp)
	}
	f.s.Close()

	// fresh session, device now echoes the mirror set_calibration wrote:
	// calibration recovers straight from the probe reply.
	f2 := newFixture(t, withProbeReply([]byte{10, 0, 195, 80}))
	m := f2.resultMap(f2.exec("get_calibration", ""))
	if m["ml_per_step"] != 0.0005 {
		t.Fatalf("recovered calibration: %v", m)
	}

	// fresh session, device reports no mirror (e.g. a different device on
	// this port): nothing carries over from the earlier session.
	f3 := newFixture(t)
	resp := f3.exec("get_calibration", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeNotCalibrated {
		t.Fatalf("uncalibrated get_calibration must not recover from a prior session: %+v", resp)
	}
}

func TestSetCalibrationDirect(t *testing.T) {
	f := newFixture(t)
	f.port.Feed([]byte{10, 0, 195, 80}) // mirror verify reply
	resp := f.exec("set_calibration", `{"ml_per_step":0.0005}`)
	if resp.Status != "ok" {
		t.Fatalf("direct set_calibration: %+v", resp)
	}
}

func TestSetCalibrationConfirmsUnverifiedMirror(t *testing.T) {
	f := newFixture(t, withProbeReply([]byte{10, 0, 195, 80})) // unverified 0.0005
	f.port.Feed([]byte{10, 0, 195, 80})
	if resp := f.exec("set_calibration", `{"ml_per_step":0.0005}`); resp.Status != "ok" {
		t.Fatalf("confirming set_calibration: %+v", resp)
	}
	caps := f.resultMap(f.exec("identify", ""))["capabilities"].(map[string]any)
	if caps["calibration_unverified"] != nil || caps["speed_ml_min"] == nil {
		t.Fatalf("must be verified now: %v", caps)
	}
}

func TestSetCalibrationMirrorMismatch(t *testing.T) {
	f := newFixture(t)
	f.port.Feed([]byte{10, 9, 9, 9}) // device echoes WRONG mirror bytes
	resp := f.exec("set_calibration", `{"ml_per_step":0.0005}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("mirror mismatch: %+v", resp)
	}
}

func TestSetCalibrationValidation(t *testing.T) {
	f := newFixture(t)
	for _, params := range []string{
		`{}`,                   // neither variant
		`{"ml_per_step":0.5}`,  // > 0.1 sanity bound
		`{"ml_per_step":1e-9}`, // < 1e-6 sanity bound
		`{"job_id":"j-99","measured_volume_ml":10}`,                     // unknown job
		`{"job_id":"j-1","measured_volume_ml":10,"ml_per_step":0.0005}`, // both variants
	} {
		resp := f.exec("set_calibration", params)
		if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
			t.Fatalf("params %s: %+v", params, resp)
		}
	}
}

func TestSetCalibrationFromDispenseJobRejected(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.port.Feed([]byte{10, 26, 25, 1})
	f.clock.Advance(20*time.Second + pump.TimerGrace)
	waitFor(t, "dispense done", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
	resp := f.exec("set_calibration", `{"job_id":"`+id+`","measured_volume_ml":10}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
		t.Fatalf("dispense job must not calibrate: %+v", resp)
	}
}

func TestCalibrationCommandsBusyMidJob(t *testing.T) {
	f := newCalibratedFixture(t)
	startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	for _, cmd := range []string{"start_calibration", "set_calibration"} {
		resp := f.exec(cmd, `{"ml_per_step":0.0005,"speed_pct":50}`)
		if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
			t.Fatalf("%s mid-job: %+v", cmd, resp)
		}
	}
}

func TestStartCalibrationValidatesPct(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("start_calibration", `{"speed_pct":101}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
		t.Fatalf("%+v", resp)
	}
}
