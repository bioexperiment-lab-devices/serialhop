package densitometer_test

import (
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// feedThermostatSet feeds the verify readback of a set_thermostat: 76 2 → t.00.
func feedThermSet(port interface{ Feed([]byte) }, t byte) {
	port.Feed([]byte{5, 5, t, 0})
}

func TestSetThermostatEnable(t *testing.T) {
	f := newFixture(t)
	feedThermSet(f.port, 37)
	resp := f.exec("set_thermostat", `{"enabled":true,"target_c":37.0}`)
	if resp.Status != "ok" {
		t.Fatalf("set_thermostat: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["enabled"] != true || m["target_c"] != 37.0 {
		t.Fatalf("result: %v", m)
	}
	// The set frame 75 2 37 0 0 then the verify read 76 2 0 0 0 must both appear.
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 75, 2, 37, 0, 0) {
		t.Fatalf("set frame: %v", fr[n-2])
	}
	if !frameEq(fr[n-1], 76, 2, 0, 0, 0) {
		t.Fatalf("verify frame: %v", fr[n-1])
	}
}

func TestSetThermostatRoundsFractional(t *testing.T) {
	f := newFixture(t)
	feedThermSet(f.port, 37) // round(36.6) = 37
	m := f.resultMap(f.exec("set_thermostat", `{"enabled":true,"target_c":36.6}`))
	if m["target_c"] != 37.0 {
		t.Fatalf("fractional set-point must round to 37: %v", m)
	}
}

func TestSetThermostatDisable(t *testing.T) {
	f := newFixture(t)
	feedThermSet(f.port, 0) // disable verifies against 0
	m := f.resultMap(f.exec("set_thermostat", `{"enabled":false}`))
	if m["enabled"] != false || m["target_c"] != 0.0 {
		t.Fatalf("disable result: %v", m)
	}
	fr := f.frames()
	if !frameEq(fr[len(fr)-2], 75, 2, 0, 0, 0) {
		t.Fatalf("disable frame: %v", fr[len(fr)-2])
	}
}

func TestSetThermostatRangeRejected(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []string{`{"enabled":true,"target_c":19}`, `{"enabled":true,"target_c":46}`} {
		resp := f.exec("set_thermostat", tc)
		if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
			t.Fatalf("%s: %+v", tc, resp)
		}
	}
}

func TestSetThermostatVerifyMismatch(t *testing.T) {
	f := newFixture(t)
	feedThermSet(f.port, 30) // device echoes 30, we asked for 37 → hardware_error
	resp := f.exec("set_thermostat", `{"enabled":true,"target_c":37}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("verify mismatch must be hardware_error: %+v", resp)
	}
}

func TestSetThermostatPersistsMirror(t *testing.T) {
	dir := t.TempDir()
	f := newFixture(t, withStateDir(dir))
	feedThermSet(f.port, 37)
	if resp := f.exec("set_thermostat", `{"enabled":true,"target_c":37}`); resp.Status != "ok" {
		t.Fatalf("set: %+v", resp)
	}
	st := device.NewStore(dir, "densitometer-25-006")
	var ps struct {
		Thermostat struct {
			Enabled bool    `json:"enabled"`
			TargetC float64 `json:"target_c"`
		} `json:"thermostat"`
	}
	if _, err := st.Load(&ps); err != nil {
		t.Fatal(err)
	}
	if !ps.Thermostat.Enabled || ps.Thermostat.TargetC != 37 {
		t.Fatalf("mirror not persisted: %+v", ps.Thermostat)
	}
}

// TestAttachRebootCanaryRepushes: a persisted enabled mirror + a device that
// reads back 10.00 (fresh boot) must re-push the set-point during Attach.
func TestAttachRebootCanaryRepushes(t *testing.T) {
	dir := t.TempDir()
	// Seed a persisted enabled mirror at 37.
	st := device.NewStore(dir, "densitometer-25-006")
	if err := st.Save(map[string]any{
		"schema_version": 1, "tube_correction": 1.0,
		"thermostat": map[string]any{"enabled": true, "target_c": 37.0},
	}); err != nil {
		t.Fatal(err)
	}
	shrinkTimeouts(t)
	clock := device.NewFakeClock(timeUnix1000())
	port := newPort("COM8")
	opener := newOpener(port)
	mustOpen(t, opener, "COM8")
	// Attach reads: serial, wavelength, (force tube), thermostat=10 → reboot →
	// re-push: 75 2 37, then verify 76 2 → 37.
	port.Feed([]byte{5, 7, 25, 6})
	port.Feed([]byte{1, 2, 6, 0})
	port.Feed([]byte{5, 5, 10, 0}) // thermostat readback = 10.00 → rebooted
	port.Feed([]byte{5, 5, 37, 0}) // re-push verify readback
	f := startFixture(t, clock, port, opener, dir)
	// The re-push set frame must have been sent.
	sawRepush := false
	for _, fr := range f.frames() {
		if frameEq(fr, 75, 2, 37, 0, 0) {
			sawRepush = true
		}
	}
	if !sawRepush {
		t.Fatalf("reboot canary must re-push 75 2 37; frames=%v", f.frames())
	}
}
