package densitometer_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

func TestPingReturnsUptime(t *testing.T) {
	f := newFixture(t)
	f.clock.Advance(3 * time.Second)
	f.port.Feed([]byte{70, 5, 27, 45}) // 71 2 3 4 0 → 70 5 T_int T_frac
	resp := f.exec("ping", "")
	if resp.Status != "ok" {
		t.Fatalf("ping: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["uptime_ms"].(float64) < 2900 || m["uptime_ms"].(float64) > 3100 {
		t.Fatalf("uptime_ms = %v, want ~3000", m["uptime_ms"])
	}
	if !frameEq(f.frames()[len(f.frames())-1], 71, 2, 3, 4, 0) {
		t.Fatalf("ping frame missing: %v", f.frames())
	}
}

func TestStatusIdleReadsLiveTemperature(t *testing.T) {
	f := newFixture(t)
	f.port.Feed([]byte{5, 5, 36, 98}) // 76 0 → temperature 36.98
	f.port.Feed([]byte{5, 5, 0, 0})   // 76 2 → thermostat set-point 0 (disabled, in sync)
	m := f.resultMap(f.exec("status", ""))
	if m["state"] != "idle" {
		t.Fatalf("state: %v", m)
	}
	if m["temperature_c"].(float64) < 36.9 || m["temperature_c"].(float64) > 37.05 {
		t.Fatalf("temperature_c = %v", m["temperature_c"])
	}
	th := m["thermostat"].(map[string]any)
	if th["enabled"] != false || th["heating"] != nil || th["cooling"] != nil {
		t.Fatalf("thermostat block: %v", th)
	}
	cal := m["calibration"].(map[string]any)
	if cal["blank"] != nil || cal["tube_correction"] != 1.0 {
		t.Fatalf("calibration block: %v", cal)
	}
	if m["last_measurement"] != nil {
		t.Fatalf("last_measurement must be null before any measurement: %v", m["last_measurement"])
	}
}

func TestStatusIdleServesCachedTemperatureWhenLiveReadFails(t *testing.T) {
	f := newFixture(t)
	f.port.Feed([]byte{5, 5, 30, 0}) // 76 0 → temperature 30.00 (primes cache)
	f.port.Feed([]byte{5, 5, 0, 0})  // 76 2 → thermostat set-point 0 (disabled, in sync)
	m := f.resultMap(f.exec("status", ""))
	if m["temperature_c"].(float64) != 30.0 {
		t.Fatalf("priming status temperature_c = %v, want 30.0", m["temperature_c"])
	}

	// No replies fed this time: both the temperature and thermostat reads
	// fail, so the idle branch must fall back to the cached temperature
	// rather than leaving temperature_c at its zero value.
	resp := f.exec("status", "")
	if resp.Status != "ok" {
		t.Fatalf("status: %+v", resp)
	}
	m2 := f.resultMap(resp)
	if m2["temperature_c"].(float64) != 30.0 {
		t.Fatalf("temperature_c = %v, want cached 30.0", m2["temperature_c"])
	}
}

func TestStatusThermostatEnabledMirror(t *testing.T) {
	f := newFixture(t)
	feedThermSet(f.port, 37)
	if resp := f.exec("set_thermostat", `{"enabled":true,"target_c":37}`); resp.Status != "ok" {
		t.Fatalf("set: %+v", resp)
	}
	f.port.Feed([]byte{5, 5, 36, 98}) // temperature
	f.port.Feed([]byte{5, 5, 37, 0})  // thermostat set-point 37 (in sync)
	th := f.resultMap(f.exec("status", ""))["thermostat"].(map[string]any)
	if th["enabled"] != true || th["target_c"] != 37.0 {
		t.Fatalf("enabled mirror: %v", th)
	}
}

func TestPingLivenessTransactFails(t *testing.T) {
	f := newFixture(t)
	// No reply fed: the liveness Transact (71 2 3 4 0) fails outright.
	resp := f.exec("ping", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("ping: %+v", resp)
	}
}

func TestPingUnexpectedReplyTypeCode(t *testing.T) {
	f := newFixture(t)
	f.port.Feed([]byte{99, 5, 27, 45}) // first byte != TypeCode(70)
	resp := f.exec("ping", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("ping: %+v", resp)
	}
}

func TestSetTubeCorrection(t *testing.T) {
	dir := t.TempDir()
	f := newFixture(t, withStateDir(dir))
	m := f.resultMap(f.exec("set_tube_correction", `{"factor":1.03}`))
	if m["tube_correction"] != 1.03 {
		t.Fatalf("result: %v", m)
	}
	st := device.NewStore(dir, "densitometer-25-006")
	var ps struct {
		TubeCorrection float64 `json:"tube_correction"`
	}
	if _, err := st.Load(&ps); err != nil || ps.TubeCorrection != 1.03 {
		t.Fatalf("tube correction not persisted: %v err=%v", ps.TubeCorrection, err)
	}
}

func TestSetTubeCorrectionRange(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{`{"factor":0.4}`, `{"factor":2.1}`} {
		resp := f.exec("set_tube_correction", p)
		if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
			t.Fatalf("%s: %+v", p, resp)
		}
	}
}

func TestCalibrateTubeNoMeasurement(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("calibrate_tube", `{"reference_absorbance":0.5}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeNotCalibrated {
		t.Fatalf("calibrate_tube without measurement: %+v", resp)
	}
}

func TestCalibrateTubeFromLastMeasurement(t *testing.T) {
	f := newFixture(t)
	// measure gives absorbance ~0.30103 at tube 1.0; reference 0.60206 → factor 2.0
	measureAfterBlank(t, f, "", 50, 27, 45)
	m := f.resultMap(f.exec("calibrate_tube", `{"reference_absorbance":0.60206}`))
	if m["tube_correction"].(float64) < 1.99 || m["tube_correction"].(float64) > 2.01 {
		t.Fatalf("tube_correction = %v, want ~2.0", m["tube_correction"])
	}
	if m["based_on_seq"].(float64) != 1 {
		t.Fatalf("based_on_seq = %v", m["based_on_seq"])
	}
}
