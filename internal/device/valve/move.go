package valve

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// home declares the rotor's current physical position (TRANSLATION.md §4
// home). No motion frame is sent — homing is purely a translator-side
// declaration; all future moves are computed relative to it. The device's
// counter is re-read first so the belief↔physical offset is anchored to
// reality, then both are persisted for restart recovery.
func (d *Driver) home(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Position *int `json:"position"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if j := d.s.Jobs().Active(); j != nil {
		return nil, device.ErrBusy("a move is in progress", map[string]any{"job_id": j.ID})
	}
	if p.Position == nil || *p.Position < 0 || *p.Position > d.positions {
		return nil, device.ErrInvalidParams("position", p.Position,
			fmt.Sprintf("position must be between 0 and %d", d.positions))
	}
	reply, err := d.s.Transact(queryPosFrame, 4, replyTimeout)
	if err != nil {
		return nil, device.ErrHardware("position query: " + err.Error())
	}
	if reply[0] != TypeCode {
		return nil, device.ErrHardware("position query: unexpected reply")
	}
	d.deviceBelief = int(reply[3])
	d.lastCheck = d.s.Now()
	d.physicalPos = *p.Position
	d.homed = true
	if err := d.persistNow(); err != nil {
		return nil, device.ErrInternal("persist home: " + err.Error())
	}
	return map[string]any{"homed": true, "position": *p.Position}, nil
}

// moveResult is the completed-move job result (JSON_PROTOCOL.md §4). A
// no-motion move (target == current) reports duration 0 and omits
// direction — neither "increasing" nor "decreasing" happened (flagged
// deviation: the JSON doc defines no direction for a degenerate move).
type moveResult struct {
	Position     int     `json:"position"`
	FromPosition int     `json:"from_position"`
	Direction    string  `json:"direction,omitempty"`
	DurationS    float64 `json:"duration_s"`
}

// setPosition implements TRANSLATION.md §4 set_position. Safety rules, in
// order: the not_homed gate, the single-job gate (no mid-move retargeting —
// the firmware would compute from its already-advanced counter while the
// rotor is between detents), parameter validation, CHECK_BELIEF, and the
// Δ=0 guard (in wrap mode the firmware interprets "move to the current
// position" as a full 360° revolution — that frame must never go out).
func (d *Driver) setPosition(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Position *int   `json:"position"`
		Rotation string `json:"rotation"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if !d.homed {
		return nil, device.ErrNotHomed("position is unknown — home the valve first")
	}
	if j := d.s.Jobs().Active(); j != nil {
		return nil, device.ErrBusy("a move is in progress", map[string]any{"job_id": j.ID})
	}
	if p.Position == nil || *p.Position < 0 || *p.Position > d.positions {
		return nil, device.ErrInvalidParams("position", p.Position,
			fmt.Sprintf("position must be between 0 and %d", d.positions))
	}
	mode := d.config.DefaultRotation
	if p.Rotation != "" {
		if _, ok := rotationCode(p.Rotation); !ok {
			return nil, device.ErrInvalidParams("rotation", p.Rotation,
				`rotation must be "shortest", "direct" or "wrap"`)
		}
		mode = p.Rotation
	}

	if cerr := d.checkBelief(); cerr != nil {
		return nil, cerr
	}
	if !d.homed { // the check itself may have just unhomed us
		return nil, device.ErrNotHomed("position counter mismatch — home the valve again")
	}

	target := *p.Position
	if target == d.physicalPos {
		// Already there: succeed without motion (the wrap-mode Δ=0 guard).
		if _, cerr := d.s.Jobs().Start("move", 0); cerr != nil {
			return nil, cerr
		}
		done := d.s.Jobs().Complete(moveResult{Position: target, FromPosition: target})
		d.lastJobID = done.ID
		return map[string]any{"job": *done}, nil
	}

	if mode != d.lastPushed {
		code, _ := rotationCode(mode)
		if _, err := d.s.Transact(rotationFrame(code), 0, time.Second); err != nil {
			return nil, device.ErrHardware("rotation mode frame: " + err.Error())
		}
		d.lastPushed = mode
	}

	plan := planMove(target, d.physicalPos, d.deviceBelief, d.slots(), mode)
	// #nosec G115 -- targetDevice ∈ [0, slots) and slots ≤ 256 (probe byte + 1)
	if _, err := d.s.Transact(moveFrame(byte(plan.targetDevice)), 0, time.Second); err != nil {
		// The write failed; whether the firmware parsed the frame first is
		// unknowable → position knowledge is void (TRANSLATION.md §5).
		d.homed = false
		return nil, device.ErrHardware("move frame: " + err.Error())
	}

	job, cerr := d.s.Jobs().Start("move", plan.estimate)
	if cerr != nil {
		return nil, cerr // unreachable: the busy gate ran above
	}
	d.moveJob = &moveJob{
		id: job.ID, fromPhysical: d.physicalPos, targetPhysical: target,
		targetDevice: plan.targetDevice, direction: plan.direction, estimate: plan.estimate,
	}
	d.lastJobID = job.ID
	// Optimistic belief update: the firmware bumps its counter the moment
	// it parses the frame (TRANSLATION.md §4 step 9).
	d.deviceBelief = plan.targetDevice
	d.jobGen++
	gen := d.jobGen
	d.s.After(plan.estimate, func() { d.moveComplete(gen) })
	return map[string]any{"job": job}, nil
}

// moveComplete is the clock-driven completion callback (s.After). Stale
// generations are ignored: stop, an unreachable episode, or a reattach may
// already have settled the job.
func (d *Driver) moveComplete(gen int) {
	if gen != d.jobGen || d.moveJob == nil {
		return
	}
	_ = d.verifyMove(false)
}

// verifyMove runs TRANSLATION.md §4 set_position step 10 — the post-motion
// readback. cancelled marks the job cancelled instead of succeeded (stop's
// settle-and-report path). The readback proves the firmware is alive and
// processed the move; it CANNOT prove the rotor physically arrived (no
// encoder) — a stalled motor is undetectable. Likewise, a mid-move reboot
// is invisible when the target is device-frame 0 — the reset counter
// equals the target, so the readback passes. Inherent hardware gaps.
func (d *Driver) verifyMove(cancelled bool) *device.CmdError {
	mj := d.moveJob
	d.moveJob = nil
	d.jobGen++ // invalidate the pending completion timer (stop path)
	reply, err := d.s.Transact(queryPosFrame, 4, replyTimeout)
	if err != nil {
		// Double failure: the session went unreachable and failed the job.
		// The move's outcome is unknown → position knowledge is void.
		d.homed = false
		return device.ErrHardware("post-move readback: " + err.Error())
	}
	pos := int(reply[3])
	if reply[0] != TypeCode || pos != mj.targetDevice {
		d.homed = false
		d.deviceBelief = pos
		if pos == 0 {
			// Reboot signature: the firmware also lost its RAM-only config.
			_ = d.pushConfig()
		}
		cerr := device.ErrHardware(fmt.Sprintf(
			"position readback %d after move to %d — device rebooted or lost the command; valve is unhomed",
			pos, mj.targetDevice))
		d.s.Jobs().Fail(cerr)
		return cerr
	}
	d.physicalPos = mj.targetPhysical
	d.lastCheck = d.s.Now() // the readback doubles as a consistency check
	if err := d.persistNow(); err != nil {
		// The move itself succeeded — log rather than fail the job over disk.
		slog.Warn("valve: persist after move failed", "port", d.s.PortName(), "err", err)
	}
	if cancelled {
		d.s.Jobs().Cancel()
	} else {
		d.s.Jobs().Complete(moveResult{
			Position: mj.targetPhysical, FromPosition: mj.fromPhysical,
			Direction: mj.direction, DurationS: mj.estimate.Seconds(),
		})
	}
	return nil
}

// stop implements the documented spec deviation (TRANSLATION.md §4 stop;
// JSON_PROTOCOL.md §3 stop's MAY clause; spec §8.4): the firmware has NO
// abort command — motion always runs to completion (worst case
// ≈ N × SlotDuration ≈ 5.5 s). stop therefore WAITS OUT the remaining
// motion, deliberately blocking this session's loop (accepted per spec §3;
// queued commands stall behind it, within single-client semantics), then
// runs the usual post-motion verification. Position knowledge is preserved;
// the job is marked cancelled to record intent even though the motion
// physically completed. Callers must treat stop as "settle and report",
// latency ≤ ~6 s.
func (d *Driver) stop() (any, *device.CmdError) {
	if d.moveJob == nil {
		return map[string]any{"state": d.stateName()}, nil
	}
	id := d.moveJob.id
	if a := d.s.Jobs().Active(); a != nil {
		if remaining := d.moveJob.estimate - elapsedOf(a); remaining > 0 {
			d.s.Sleep(remaining)
		}
	}
	if cerr := d.verifyMove(true); cerr != nil {
		return nil, cerr
	}
	return map[string]any{"state": d.stateName(), "cancelled_job_id": id}, nil
}

func elapsedOf(j *device.Job) time.Duration {
	return time.Duration(j.ElapsedS * float64(time.Second))
}
