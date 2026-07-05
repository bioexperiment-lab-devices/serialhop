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
