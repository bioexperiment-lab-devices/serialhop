package valve

import (
	"log/slog"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// applyBelief reconciles a freshly read position counter with the tracked
// belief — TRANSLATION.md §2 CHECK_BELIEF steps 2–4. Callers guarantee no
// move is in flight.
//
// pos==0 with a nonzero belief is the reboot signature: the firmware
// assumed position 0 at power-up and lost its RAM-only config. No move was
// interrupted, so the rotor did not actually turn — belief resets to 0, the
// config is re-pushed, and the virtual-homing offset math absorbs the reset
// (homed and physical_position stand). Any other mismatch means a lost
// command or a foreign host on the port: position knowledge is void →
// unhomed + alarm log.
func (d *Driver) applyBelief(pos int) {
	d.lastCheck = d.s.Now()
	switch {
	case pos == d.deviceBelief:
		// consistent
	case pos == 0 && d.deviceBelief != 0:
		slog.Warn("valve: device reboot detected while idle — auto-recovering",
			"port", d.s.PortName(), "belief", d.deviceBelief)
		d.deviceBelief = 0
		_ = d.pushConfig() // a failure here trips the session's unreachable handling
	default:
		slog.Error("valve: position counter mismatch — valve is now unhomed",
			"port", d.s.PortName(), "reported", pos, "belief", d.deviceBelief)
		d.deviceBelief = pos
		d.homed = false
	}
}

// checkBelief runs the consistency check (TRANSLATION.md §2): query the
// position counter and reconcile. Returns a CmdError only for serial
// failures; belief mismatches are absorbed into driver state (callers that
// need homed must re-check it afterwards).
func (d *Driver) checkBelief() *device.CmdError {
	reply, err := d.s.Transact(queryPosFrame, 4, replyTimeout)
	if err != nil {
		return device.ErrHardware("position query: " + err.Error())
	}
	if reply[0] != TypeCode {
		return device.ErrHardware("position query: unexpected reply")
	}
	d.applyBelief(int(reply[3]))
	return nil
}
