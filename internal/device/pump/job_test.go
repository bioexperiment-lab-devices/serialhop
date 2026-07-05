package pump_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/pump"
)

// startDispense issues a dispense and returns the job_id.
func startDispense(t *testing.T, f *fixture, params string) string {
	t.Helper()
	resp := f.exec("dispense", params)
	if resp.Status != "ok" {
		t.Fatalf("dispense: %+v", resp)
	}
	job := f.resultMap(resp)["job"].(map[string]any)
	if job["state"] != "running" {
		t.Fatalf("job: %v", job)
	}
	return job["job_id"].(string)
}

func jobState(t *testing.T, f *fixture, id string) map[string]any {
	t.Helper()
	resp := f.exec("get_job", `{"job_id":"`+id+`"}`)
	if resp.Status != "ok" {
		t.Fatalf("get_job: %+v", resp)
	}
	return f.resultMap(resp)
}

func TestDispenseReverseTimerCompletion(t *testing.T) {
	f := newCalibratedFixture(t)
	// 1 ml reverse @ 3 ml/min → opcode 16, 2000 steps, estimate 20 s
	id := startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 10, 0, 1, 50, 0) {
		t.Fatalf("config frame: %v", fr[n-2])
	}
	if !frameEq(fr[n-1], 16, 0, 0, 7, 208) { // be32(2000) = 0 0 7 208
		t.Fatalf("motion frame: %v", fr[n-1])
	}
	st := f.resultMap(f.exec("status", ""))
	if st["state"] != "dispensing" || *jsonStr(st["direction"]) != "reverse" {
		t.Fatalf("status: %v", st)
	}

	f.port.Feed([]byte{10, 26, 25, 1}) // panel-disarm ping reply
	f.clock.Advance(20*time.Second + pump.TimerGrace)
	waitFor(t, "job success", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
	js := jobState(t, f, id)
	res := js["result"].(map[string]any)
	if res["dispensed_ml"] != 1.0 || res["suckback_ml"] != 0.0 {
		t.Fatalf("result: %v", res)
	}
	if res["mean_speed_ml_min"].(float64) < 2.8 || res["mean_speed_ml_min"].(float64) > 3.2 {
		t.Fatalf("mean speed: %v", res)
	}
	// the disarm ping (serial-number frame) must have been sent
	fr = f.frames()
	if !frameEq(fr[len(fr)-1], 11, 2, 3, 4, 5) {
		t.Fatalf("panel-disarm ping missing: %v", fr[len(fr)-1])
	}
	if f.resultMap(f.exec("status", ""))["state"] != "idle" {
		t.Fatal("must return to idle")
	}
}

func jsonStr(v any) *string {
	if v == nil {
		return nil
	}
	s := v.(string)
	return &s
}

func TestDispenseSuckbackInflatesSteps(t *testing.T) {
	f := newCalibratedFixture(t)
	// 1 ml + 0.12 ml suckback → dropMult 2, steps 2000+200=2200, opcode 17
	startDispense(t, f,
		`{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0,"drop_suckback_ml":0.12}`)
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 10, 2, 1, 50, 0) { // dropMult rides N2 of the config frame
		t.Fatalf("config frame: %v", fr[n-2])
	}
	if !frameEq(fr[n-1], 17, 0, 0, 8, 152) { // be32(2200) = 0 0 8 152
		t.Fatalf("motion frame: %v", fr[n-1])
	}
	// estimate = (2×2200 + 400×2) × 5000 µs + 0.1 s = 26.1 s
	st := f.resultMap(f.exec("status", ""))
	job := st["job"].(map[string]any)
	if job["estimated_duration_s"] != 26.1 {
		t.Fatalf("estimate: %v", job)
	}
}

func TestDispenseSuckbackCompletionEchoesActual(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f,
		`{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0,"drop_suckback_ml":0.12}`)
	f.port.Feed([]byte{10, 26, 25, 1})
	f.clock.Advance(26100*time.Millisecond + pump.TimerGrace)
	waitFor(t, "job success", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
	res := jobState(t, f, id)["result"].(map[string]any)
	if res["suckback_ml"] != 0.1 { // quantized actual, not the requested 0.12
		t.Fatalf("suckback_ml: %v", res)
	}
	if res["dispensed_ml"] != 1.0 { // net delivered volume, drop excluded
		t.Fatalf("dispensed_ml: %v", res)
	}
}

func TestDispenseGradient(t *testing.T) {
	f := newCalibratedFixture(t)
	resp := f.exec("dispense",
		`{"direction":"forward","volume_ml":1.0,"speed_profile":{"start_ml_min":0.5,"end_ml_min":5.0,"shape":"linear"}}`)
	if resp.Status != "ok" {
		t.Fatalf("gradient dispense: %+v", resp)
	}
	fr := f.frames()
	n := len(fr)
	// increasing profile → grad flag 12; speed bytes inert (firmware ramp)
	if !frameEq(fr[n-2], 10, 0, 0, 0, 12) {
		t.Fatalf("config frame: %v", fr[n-2])
	}
	if fr[n-1][0] != 15 { // gradient runs must use opcode 15
		t.Fatalf("motion frame: %v", fr[n-1])
	}
	m := f.resultMap(resp)
	prof := m["speed_profile"].(map[string]any)
	if prof["applied"] != "hardware-fixed quadratic ramp" ||
		prof["start_ml_min"] != nil || prof["end_ml_min"] != nil {
		t.Fatalf("profile echo: %v", prof)
	}
}

func TestDispenseGradientDecreasingFlag(t *testing.T) {
	f := newCalibratedFixture(t)
	f.exec("dispense",
		`{"direction":"forward","volume_ml":1.0,"speed_profile":{"start_ml_min":5.0,"end_ml_min":0.5,"shape":"linear"}}`)
	fr := f.frames()
	if fr[len(fr)-2][4] != 21 {
		t.Fatalf("decreasing profile must arm grad flag 21: %v", fr[len(fr)-2])
	}
}

func TestDispenseRejections(t *testing.T) {
	f := newCalibratedFixture(t)
	cases := []struct {
		name, params, code string
	}{
		{"gradient+reverse", `{"direction":"reverse","volume_ml":1,"speed_profile":{"start_ml_min":1,"end_ml_min":2,"shape":"linear"}}`, "invalid_params"},
		{"gradient+suckback", `{"direction":"forward","volume_ml":1,"drop_suckback_ml":0.1,"speed_profile":{"start_ml_min":1,"end_ml_min":2,"shape":"linear"}}`, "invalid_params"},
		{"gradient flat", `{"direction":"forward","volume_ml":1,"speed_profile":{"start_ml_min":2,"end_ml_min":2,"shape":"linear"}}`, "invalid_params"},
		{"suckback+reverse", `{"direction":"reverse","volume_ml":1,"speed_ml_min":3,"drop_suckback_ml":0.1}`, "invalid_params"},
		{"zero volume", `{"direction":"forward","volume_ml":0,"speed_ml_min":3}`, "invalid_params"},
		{"no speed", `{"direction":"forward","volume_ml":1}`, "invalid_params"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := f.exec("dispense", c.params)
			if resp.Status != "error" || resp.Error.Code != c.code {
				t.Fatalf("%+v", resp)
			}
		})
	}
}

func TestDispenseBusyWhileJobActive(t *testing.T) {
	f := newCalibratedFixture(t)
	startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	resp := f.exec("dispense", `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("second dispense: %+v", resp)
	}
	resp = f.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("rotate during job: %+v", resp)
	}
}

func TestDispenseBusyWhileRotating(t *testing.T) {
	f := newCalibratedFixture(t)
	f.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	resp := f.exec("dispense", `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("dispense while rotating: %+v", resp)
	}
}

func TestDispenseUncalibrated(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("dispense", `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeNotCalibrated {
		t.Fatalf("%+v", resp)
	}
}

// TestDispenseVerificationFailureFailsJob: the end-of-job disarm ping gets no
// reply → transaction double-fails → session flips unreachable and fails the
// job; the completion handler must tolerate that (PR-1 decision 2).
func TestDispenseVerificationFailureFailsJob(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	// feed nothing: the disarm ping will time out twice
	f.clock.Advance(20*time.Second + pump.TimerGrace)
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })
	// job failed, not completed; reattach and inspect it
	f.port.Feed([]byte{10, 26, 25, 1}) // next attach's serial reply
	f.clock.Advance(device.ReattachBase)
	waitFor(t, "reattach", f.s.Connected)
	if st := jobState(t, f, id); st["state"] != "failed" {
		t.Fatalf("job after failed verification: %v", st)
	}
}
