package densitometer

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// pushThermostat commands the set-point (TRANSLATION §4 set_thermostat): write
// 75 2 t, wait ThermoSettle (the firmware blocks ~1 s redrawing the display),
// verify via 76 2, then update + persist the mirror. t = 0 disables. The
// settle is a bounded loop-block for a rare human-paced command (flagged
// deviation 3).
func (d *Driver) pushThermostat(enabled bool, targetC float64) *device.CmdError {
	t := 0
	if enabled {
		t = int(targetC)
	}
	if _, err := d.s.Transact([]byte{75, 2, byte(t), 0, 0}, 0, replyTimeout); err != nil { // #nosec G115 -- t is 0 or 20..45
		return device.ErrHardware("set_thermostat write: " + err.Error())
	}
	time.Sleep(ThermoSettle)
	reply, err := d.s.Transact(thermReadFrame, 4, replyTimeout)
	if err != nil {
		return device.ErrHardware("set_thermostat verify: " + err.Error())
	}
	if got := decodeFixedPoint(reply); math.Abs(got-float64(t)) > 0.01 {
		return device.ErrHardware(fmt.Sprintf(
			"set_thermostat verify: device echoed %.2f, want %d", got, t))
	}
	d.thermo = thermostatMirror{Enabled: enabled, TargetC: float64(t)}
	if err := d.persist(); err != nil {
		return device.ErrInternal("persist thermostat: " + err.Error())
	}
	return nil
}

type setThermostatResult struct {
	Enabled bool    `json:"enabled"`
	TargetC float64 `json:"target_c"`
}

func (d *Driver) setThermostat(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Enabled bool     `json:"enabled"`
		TargetC *float64 `json:"target_c"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if cerr := d.serialGate(); cerr != nil {
		return nil, cerr
	}
	var target float64
	if p.Enabled {
		if p.TargetC == nil {
			return nil, device.ErrInvalidParams("target_c", nil, "target_c is required when enabling")
		}
		target = math.Round(*p.TargetC) // firmware accepts whole °C only
		if target < thermoMinC || target > thermoMaxC {
			return nil, device.ErrInvalidParams("target_c", *p.TargetC,
				"target_c must be between 20 and 45")
		}
	}
	if cerr := d.pushThermostat(p.Enabled, target); cerr != nil {
		return nil, cerr
	}
	return setThermostatResult{Enabled: d.thermo.Enabled, TargetC: d.thermo.TargetC}, nil
}

// syncThermostat is Attach §3 step 5: read the device set-point and reconcile
// against the persisted mirror, arming the reboot canary. hasMirror is true
// when persistent state was recovered.
func (d *Driver) syncThermostat(hasMirror bool) error {
	reply, err := d.s.Transact(thermReadFrame, 4, replyTimeout)
	if err != nil {
		return fmt.Errorf("densitometer: thermostat read: %w", err)
	}
	readback := decodeFixedPoint(reply)
	if !hasMirror {
		// First-ever contact: disable and persist mirror {false}.
		if cerr := d.pushThermostat(false, 0); cerr != nil {
			return fmt.Errorf("densitometer: thermostat first-contact disable: %w", cerr)
		}
		return nil
	}
	if math.Abs(readback-d.thermo.mirrorValue()) <= 0.01 {
		return nil // in sync
	}
	// Mismatch (reboot ⇒ readback 10, or drift) — re-push the mirror. During
	// Attach there is no active job to fail and connected_since was just set.
	if math.Abs(readback-10.0) <= 0.01 {
		slog.Warn("densitometer: device rebooted (thermostat readback 10.00), re-pushing mirror",
			"device", d.serial, "mirror", d.thermo)
	} else {
		slog.Warn("densitometer: thermostat drift, re-pushing mirror",
			"device", d.serial, "readback", readback, "mirror", d.thermo)
	}
	if cerr := d.pushThermostat(d.thermo.Enabled, d.thermo.TargetC); cerr != nil {
		return fmt.Errorf("densitometer: thermostat re-push: %w", cerr)
	}
	return nil
}

// applyThermostatReadback is the canary shared by status step 3 and the idle
// Tick poll. readback is a decoded 76 2 value. When fromCanary and the device
// rebooted (readback 10.00) it fails any active job and resets connected_since
// before re-pushing.
func (d *Driver) applyThermostatReadback(readback float64, fromCanary bool) {
	if math.Abs(readback-d.thermo.mirrorValue()) <= 0.01 {
		return // in sync
	}
	rebooted := math.Abs(readback-10.0) <= 0.01
	if fromCanary && rebooted {
		slog.Warn("densitometer: reboot detected via canary — failing job, re-pushing mirror",
			"device", d.serial)
		if d.s.Jobs().Active() != nil {
			d.s.Jobs().Fail(device.ErrHardware("device rebooted mid-job (sweep data lost)"))
			d.clearSweep()
		}
		d.connectedSince = d.s.Now()
	} else {
		slog.Warn("densitometer: thermostat mismatch — re-pushing mirror",
			"device", d.serial, "readback", readback, "mirror", d.thermo)
	}
	if cerr := d.pushThermostat(d.thermo.Enabled, d.thermo.TargetC); cerr != nil {
		slog.Warn("densitometer: mirror re-push failed", "device", d.serial, "err", cerr)
	}
}
