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
