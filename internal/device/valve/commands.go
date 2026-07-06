package valve

import (
	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

type pingResult struct {
	UptimeMs int64 `json:"uptime_ms"`
}

// ping proves liveness with the side-effect-free ping frame and
// opportunistically feeds the reported position into the CHECK_BELIEF logic
// — but only while idle: a mid-move reply reflects the target the firmware
// is already counting from, not the rotor (TRANSLATION.md §4 ping).
// uptime_ms is connection age; true device uptime is unknowable.
func (d *Driver) ping() (any, *device.CmdError) {
	reply, err := d.s.Transact(pingFrame, 4, replyTimeout)
	if err != nil {
		if d.moveJob != nil {
			// The in-flight move's outcome is unknown (TRANSLATION.md §5);
			// the session has already failed the job.
			d.homed = false
			d.moveJob = nil
			d.jobGen++
		}
		return nil, device.ErrHardware("ping: " + err.Error())
	}
	if reply[0] != TypeCode {
		return nil, device.ErrHardware("ping: unexpected reply")
	}
	if d.moveJob == nil {
		d.applyBelief(int(reply[3]))
	}
	return pingResult{UptimeMs: d.s.Now().Sub(d.connectedSince).Milliseconds()}, nil
}

func (d *Driver) stateName() string {
	switch {
	case !d.homed:
		return "unhomed"
	case d.moveJob != nil:
		return "moving"
	default:
		return "idle"
	}
}

type statusResult struct {
	State          string      `json:"state"`
	Homed          bool        `json:"homed"`
	Position       *int        `json:"position"`
	TargetPosition *int        `json:"target_position"`
	Job            *device.Job `json:"job"`
	Config         configBlock `json:"config"`
}

// statusJob returns the active job, else the most recent one (JSON §2:
// "the active/last job is also embedded in status").
func (d *Driver) statusJob() *device.Job {
	if j := d.s.Jobs().Active(); j != nil {
		return j
	}
	if d.lastJobID != "" {
		return d.s.Jobs().Get(d.lastJobID)
	}
	return nil
}

// status (TRANSLATION.md §4): an idle status runs CHECK_BELIEF so reboots
// are caught even when nothing is happening; during a move it is served
// entirely from memory. position is reported only when the rotor is
// verifiably settled (homed and idle) — never the target of an in-flight
// move.
func (d *Driver) status() (any, *device.CmdError) {
	if d.moveJob == nil {
		if cerr := d.checkBelief(); cerr != nil {
			return nil, cerr
		}
	}
	res := statusResult{State: d.stateName(), Homed: d.homed, Config: d.config}
	if d.homed && d.moveJob == nil {
		pos := d.physicalPos // copy — never return a pointer into live driver state
		res.Position = &pos
	}
	if d.moveJob != nil {
		tp := d.moveJob.targetPhysical
		res.TargetPosition = &tp
	}
	res.Job = d.statusJob()
	return res, nil
}
