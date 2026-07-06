package densitometer_test

import (
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/densitometer"
)

// buildArrayBytes renders a 20-record intensity array (test-side mirror of the
// package helper) for feeding the port.
func buildArrayBytes(fn func(i int) int) []byte {
	buf := make([]byte, 0, 80)
	for i := 1; i <= 20; i++ {
		v := fn(i)
		buf = append(buf, 105, byte(i), byte(v%256), byte(v/256)) // #nosec G115 -- test data; i and derived low/high bytes are bounded to 0..255 by construction
	}
	return buf
}

// feedSweepCompletion feeds the completion chain: liveness reply, 80-byte
// array, temperature reply — in read order.
func feedSweepCompletion(f *fixture, slopePerLevel int, tInt, tFrac byte) {
	f.port.Feed([]byte{70, 5, tInt, tFrac})                                    // liveness (71 2 3 4)
	f.port.Feed(buildArrayBytes(func(i int) int { return slopePerLevel * i })) // 79 1 0
	f.port.Feed([]byte{5, 5, tInt, tFrac})                                     // temperature (76 0)
}

func jobResult(t *testing.T, f *fixture, id string) map[string]any {
	t.Helper()
	resp := f.exec("get_job", `{"job_id":"`+id+`"}`)
	if resp.Status != "ok" {
		t.Fatalf("get_job: %+v", resp)
	}
	return f.resultMap(resp)
}

func startJob(t *testing.T, f *fixture, cmd, params string) string {
	t.Helper()
	resp := f.exec(cmd, params)
	if resp.Status != "ok" {
		t.Fatalf("%s: %+v", cmd, resp)
	}
	job := f.resultMap(resp)["job"].(map[string]any)
	return job["job_id"].(string)
}

func TestMeasureBlankHappyPath(t *testing.T) {
	dir := t.TempDir()
	f := newFixture(t, withStateDir(dir))
	id := startJob(t, f, "measure_blank", "")
	// trigger frame 78 3 0 0 0 must have fired
	if !frameEq(f.frames()[len(f.frames())-1], 78, 3, 0, 0, 0) {
		t.Fatalf("blank trigger: %v", f.frames())
	}
	feedSweepCompletion(f, 100, 27, 45) // slope 100, 27.45 °C
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "blank success", func() bool {
		return jobResult(t, f, id)["state"] == "succeeded"
	})
	res := jobResult(t, f, id)["result"].(map[string]any)
	if res["slope"].(float64) < 99 || res["slope"].(float64) > 101 {
		t.Fatalf("slope = %v, want ~100", res["slope"])
	}
	if res["temperature_c"].(float64) < 27.4 || res["temperature_c"].(float64) > 27.5 {
		t.Fatalf("temperature_c = %v", res["temperature_c"])
	}
	if sweep, ok := res["sweep"].([]any); !ok || len(sweep) != 20 {
		t.Fatalf("blank result must include the 20-point sweep: %v", res["sweep"])
	}
	// blank persisted
	st := device.NewStore(dir, "densitometer-25-006")
	var ps struct {
		Blank *struct {
			Slope float64 `json:"slope"`
		} `json:"blank"`
	}
	if _, err := st.Load(&ps); err != nil || ps.Blank == nil {
		t.Fatalf("blank not persisted: %+v err=%v", ps, err)
	}
}

func TestSweepBusyFailFast(t *testing.T) {
	f := newFixture(t)
	startJob(t, f, "measure_blank", "")
	// mid-sweep (busy_until in the future): a serial-touching command fails fast.
	// ping is gated by serialGate; set_led's mid-sweep busy path is covered in
	// Task 7, where set_led is wired into dispatch.
	if resp := f.exec("ping", ""); resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("ping mid-sweep must be busy: %+v", resp)
	}
	// a second sweep is rejected by the active-job guard
	if resp := f.exec("measure_blank", ""); resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("second blank must be busy: %+v", resp)
	}
}

func TestSweepLivenessRetry(t *testing.T) {
	f := newFixture(t)
	id := startJob(t, f, "measure_blank", "")
	// No liveness reply yet: first soft attempt fails, schedules a retry.
	f.clock.Advance(densitometer.SweepWait)
	// still running (device "finishing")
	if jobResult(t, f, id)["state"] != "running" {
		t.Fatalf("job must still be running after failed liveness")
	}
	// now the device answers; the retry succeeds and the sweep completes
	feedSweepCompletion(f, 100, 27, 45)
	f.clock.Advance(densitometer.LivenessSpacing)
	waitFor(t, "blank success after retry", func() bool {
		return jobResult(t, f, id)["state"] == "succeeded"
	})
}

func TestSweepUnusableDetectorFailsJob(t *testing.T) {
	f := newFixture(t)
	id := startJob(t, f, "measure_blank", "")
	// liveness ok, array all-zero (dark), temperature ok → slope error
	f.port.Feed([]byte{70, 5, 27, 45})
	f.port.Feed(buildArrayBytes(func(i int) int { return 0 }))
	f.port.Feed([]byte{5, 5, 27, 45})
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "blank failed", func() bool {
		return jobResult(t, f, id)["state"] == "failed"
	})
	js := jobResult(t, f, id)
	if js["error"].(map[string]any)["code"] != "hardware_error" {
		t.Fatalf("unusable sweep must fail with hardware_error: %v", js["error"])
	}
}

func TestMeasureRequiresBlank(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("measure", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeNotCalibrated {
		t.Fatalf("measure without blank must be not_calibrated: %+v", resp)
	}
}

// measureAfterBlank runs a blank (slope 100 @ 27.45) then a measure, returning
// the completed measure job result.
func measureAfterBlank(t *testing.T, f *fixture, params string, sampleSlope int, tInt, tFrac byte) map[string]any {
	t.Helper()
	bid := startJob(t, f, "measure_blank", "")
	feedSweepCompletion(f, 100, 27, 45)
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "blank done", func() bool { return jobResult(t, f, bid)["state"] == "succeeded" })

	mid := startJob(t, f, "measure", params)
	if !frameEq(f.frames()[len(f.frames())-1], 78, 4, 0, 0, 0) {
		t.Fatalf("measure trigger 78 4: %v", f.frames())
	}
	feedSweepCompletion(f, sampleSlope, tInt, tFrac)
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "measure done", func() bool { return jobResult(t, f, mid)["state"] == "succeeded" })
	return jobResult(t, f, mid)["result"].(map[string]any)
}

func TestMeasureAbsorbance(t *testing.T) {
	f := newFixture(t)
	// sample slope 50 vs blank 100, same temp → |log10(2)| ≈ 0.30103, tube 1.0
	res := measureAfterBlank(t, f, `{"include_raw":false}`, 50, 27, 45)
	if res["absorbance"].(float64) < 0.30 || res["absorbance"].(float64) > 0.302 {
		t.Fatalf("absorbance = %v, want ~0.30103", res["absorbance"])
	}
	if res["blank_slope"].(float64) < 99 || res["blank_slope"].(float64) > 101 {
		t.Fatalf("blank_slope = %v", res["blank_slope"])
	}
	if res["slope"].(float64) < 49 || res["slope"].(float64) > 51 {
		t.Fatalf("slope = %v", res["slope"])
	}
	if res["seq"].(float64) != 1 {
		t.Fatalf("seq = %v, want 1", res["seq"])
	}
	if res["raw"] != nil {
		t.Fatalf("raw must be null when include_raw=false: %v", res["raw"])
	}
}

func TestMeasureIncludeRaw(t *testing.T) {
	f := newFixture(t)
	res := measureAfterBlank(t, f, `{"include_raw":true}`, 50, 27, 45)
	if sw, ok := res["raw"].([]any); !ok || len(sw) != 20 {
		t.Fatalf("include_raw must attach the 20-point sweep: %v", res["raw"])
	}
}

func TestMeasureTemperatureCompensation(t *testing.T) {
	f := newFixture(t)
	// sample temp 37.45 vs blank 27.45 → +10 °C → +0.022 over raw
	res := measureAfterBlank(t, f, "", 50, 37, 45)
	if res["absorbance"].(float64) < 0.322 || res["absorbance"].(float64) > 0.324 {
		t.Fatalf("temperature-compensated absorbance = %v, want ~0.32303", res["absorbance"])
	}
}

func TestReadRawFullSweep(t *testing.T) {
	f := newFixture(t)
	id := startJob(t, f, "read_raw", `{"level":null}`)
	if !frameEq(f.frames()[len(f.frames())-1], 78, 4, 0, 0, 0) {
		t.Fatalf("full read_raw must trigger 78 4: %v", f.frames())
	}
	feedSweepCompletion(f, 100, 27, 45)
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "read_raw done", func() bool { return jobResult(t, f, id)["state"] == "succeeded" })
	res := jobResult(t, f, id)["result"].(map[string]any)
	if len(res["intensities"].([]any)) != 20 || len(res["levels"].([]any)) != 20 {
		t.Fatalf("full sweep must return 20 intensities+levels: %v", res)
	}
}

func TestReadRawSingleLevel(t *testing.T) {
	f := newFixture(t)
	id := startJob(t, f, "read_raw", `{"level":7}`)
	if !frameEq(f.frames()[len(f.frames())-1], 75, 1, 7, 0, 0) {
		t.Fatalf("single-level read_raw must trigger 75 1 7: %v", f.frames())
	}
	// firmware fills all 20 slots at brightness 7; feed a flat array of 500
	f.port.Feed([]byte{70, 5, 27, 45})
	f.port.Feed(buildArrayBytes(func(i int) int { return 500 }))
	f.port.Feed([]byte{5, 5, 27, 45})
	f.clock.Advance(densitometer.SingleLevelWait)
	waitFor(t, "single-level done", func() bool { return jobResult(t, f, id)["state"] == "succeeded" })
	res := jobResult(t, f, id)["result"].(map[string]any)
	ints := res["intensities"].([]any)
	levels := res["levels"].([]any)
	if len(ints) != 1 || ints[0].(float64) != 500 || len(levels) != 1 || levels[0].(float64) != 7 {
		t.Fatalf("single-level result: %v", res)
	}
}

func TestReadRawInvalidLevel(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("read_raw", `{"level":21}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
		t.Fatalf("level 21: %+v", resp)
	}
}
