package pump

import (
	"encoding/json"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

type speedProfile struct {
	StartMlMin float64 `json:"start_ml_min"`
	EndMlMin   float64 `json:"end_ml_min"`
	Shape      string  `json:"shape"`
}

type dispenseParams struct {
	Direction      string        `json:"direction"`
	VolumeMl       float64       `json:"volume_ml"`
	SpeedMlMin     float64       `json:"speed_ml_min"`
	DropSuckbackMl float64       `json:"drop_suckback_ml"`
	SpeedProfile   *speedProfile `json:"speed_profile"`
}

type dispenseJobResult struct {
	DispensedMl    float64 `json:"dispensed_ml"`
	DurationS      float64 `json:"duration_s"`
	MeanSpeedMlMin float64 `json:"mean_speed_ml_min"`
	SuckbackMl     float64 `json:"suckback_ml"`
}

type calibrationRunResult struct {
	Steps     int64   `json:"steps"`
	DurationS float64 `json:"duration_s"`
}

// gradientEcho is the response's speed_profile block: only the ramp
// direction is honored by hardware; endpoints are echoed as null
// (TRANSLATION §4 dispense step 5, gap table).
type gradientEcho struct {
	Applied    string   `json:"applied"`
	StartMlMin *float64 `json:"start_ml_min"`
	EndMlMin   *float64 `json:"end_ml_min"`
}

// dispensePlan is the fully-resolved byte-level plan for one motion job.
type dispensePlan struct {
	opcode   byte
	dropMult int
	gradFlag byte
	n3, n4   byte
	job      motionJob
}

// planDispense implements TRANSLATION §4 dispense steps 1–8 (the pure part:
// validation, conversion, opcode selection, estimate). No I/O.
func (d *Driver) planDispense(p dispenseParams) (*dispensePlan, *device.CmdError) {
	if _, cerr := parseDirection(p.Direction); cerr != nil {
		return nil, cerr
	}
	steps, cerr := volumeToSteps(p.VolumeMl, d.mlPerStep)
	if cerr != nil {
		return nil, cerr
	}
	plan := &dispensePlan{job: motionJob{
		kind: "dispense", direction: p.Direction, volumeML: p.VolumeMl,
	}}

	if p.SpeedProfile != nil {
		// Gradient: firmware computes its fixed ramp only for opcode 15 —
		// forward, no suckback (TRANSLATION §4 dispense step 5).
		if p.Direction != "forward" || p.DropSuckbackMl > 0 {
			return nil, device.ErrInvalidParams("speed_profile", nil,
				"gradient unsupported with reverse/suckback")
		}
		switch {
		case p.SpeedProfile.StartMlMin < p.SpeedProfile.EndMlMin:
			plan.gradFlag = 12
		case p.SpeedProfile.StartMlMin > p.SpeedProfile.EndMlMin:
			plan.gradFlag = 21
		default:
			return nil, device.ErrInvalidParams("speed_profile", nil,
				"start_ml_min and end_ml_min must differ")
		}
		plan.opcode = 15
		plan.job.gradient = true
		plan.job.steps = steps
		plan.job.estimate = gradientEstimate(steps)
		// n3/n4 stay 0: the firmware overrides speed with its fixed ramp.
		return plan, nil
	}

	n3, n4, actualUs, cerr := speedToBytes(p.SpeedMlMin, d.mlPerStep)
	if cerr != nil {
		return nil, cerr
	}
	plan.n3, plan.n4 = n3, n4
	plan.job.delTimeUs = actualUs
	plan.job.speedML = actualSpeedMlMin(d.mlPerStep, actualUs)

	if p.DropSuckbackMl > 0 {
		// The firmware's forward leg equals the COMMANDED count, then it
		// retracts the drop, netting (commanded − drop): inflating the
		// count by the drop makes net delivery equal volume_ml.
		if p.Direction != "forward" {
			return nil, device.ErrInvalidParams("drop_suckback_ml", p.DropSuckbackMl,
				"drop_suckback requires direction=forward")
		}
		dropMult, actualMl := quantizeSuckback(p.DropSuckbackMl, d.mlPerStep)
		steps += int64(100 * dropMult)
		if steps > 2_000_000_000 {
			return nil, device.ErrInvalidParams("volume_ml", p.VolumeMl, "volume out of range")
		}
		plan.dropMult = dropMult
		plan.job.suckbackML = actualMl
		plan.job.steps = steps
		plan.job.estimate = suckbackEstimate(steps, dropMult, actualUs)
		plan.opcode = 17
		return plan, nil
	}

	plan.job.steps = steps
	plan.job.estimate = plainEstimate(steps, actualUs)
	if p.Direction == "reverse" {
		plan.opcode = 16
	} else {
		plan.opcode = 18 // completion reply available — the opcode-18 trick
	}
	return plan, nil
}

// launchMotion performs TRANSLATION §4 dispense steps 6–8 (and the
// start_calibration analog): config frame, motion frame, job start.
func (d *Driver) launchMotion(plan *dispensePlan) (device.Job, *device.CmdError) {
	cfg := []byte{10, byte(plan.dropMult), plan.n3, plan.n4, plan.gradFlag}
	if _, err := d.s.Transact(cfg, 0, time.Second); err != nil {
		return device.Job{}, device.ErrHardware("configuration frame: " + err.Error())
	}
	d.pauseAssumed = false // cmd 10 forces the firmware toggle to "running"

	steps := be32(plan.job.steps)
	motion := []byte{plan.opcode, steps[0], steps[1], steps[2], steps[3]}
	if _, err := d.s.Transact(motion, 0, time.Second); err != nil {
		return device.Job{}, device.ErrHardware("motion frame: " + err.Error())
	}

	job, cerr := d.s.Jobs().Start(plan.job.kind, plan.job.estimate)
	if cerr != nil {
		return device.Job{}, cerr // unreachable: busyGuard ran first
	}
	mj := plan.job
	mj.id = job.ID
	d.job = &mj
	d.jobGen++
	d.lastJobID, d.lastJobKind = job.ID, mj.kind
	if mj.kind == "dispense" {
		d.lastVolumeML = mj.volumeML
	}
	return job, nil
}

func (d *Driver) dispense(params json.RawMessage) (any, *device.CmdError) {
	var p dispenseParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if cerr := d.busyGuard(); cerr != nil {
		return nil, cerr
	}
	if d.state == stateRotating {
		return nil, device.ErrBusy("device is rotating — stop first",
			map[string]any{"state": string(stateRotating)})
	}
	if cerr := d.requireCalibration(); cerr != nil {
		return nil, cerr
	}
	plan, cerr := d.planDispense(p)
	if cerr != nil {
		return nil, cerr
	}
	job, cerr := d.launchMotion(plan)
	if cerr != nil {
		return nil, cerr
	}
	d.state = stateDispensing
	gen := d.jobGen
	if plan.opcode == 18 {
		d.startWatch(gen, plan.job.estimate)
	} else {
		d.armTimer(gen)
	}

	result := map[string]any{"job": job}
	if plan.job.gradient {
		result["speed_profile"] = gradientEcho{Applied: "hardware-fixed quadratic ramp"}
	}
	if plan.dropMult > 0 {
		result["suckback_ml"] = d.job.suckbackML
	}
	return result, nil
}

// armTimer schedules the clock-simulated completion (TRANSLATION §4 dispense
// step 9, non-18 opcodes): fire at estimate + grace of ACTIVE time. Pauses
// freeze the job clock, so timerFire re-arms for the remainder when it wakes
// early. One timer is outstanding per arm — no unbounded Posts.
func (d *Driver) armTimer(gen int) {
	if d.job == nil {
		return
	}
	remaining := d.job.estimate + TimerGrace
	if a := d.s.Jobs().Active(); a != nil {
		remaining = d.job.estimate - elapsedOf(a) + TimerGrace
	}
	d.s.After(remaining, func() { d.timerFire(gen) })
}

func elapsedOf(j *device.Job) time.Duration {
	return time.Duration(j.ElapsedS * float64(time.Second))
}

func (d *Driver) timerFire(gen int) {
	if gen != d.jobGen || d.job == nil {
		return // stale timer: job already finished/cancelled/replaced
	}
	active := d.s.Jobs().Active()
	if active == nil {
		return // failed by an unreachable transition — tolerate (decision 2)
	}
	if elapsedOf(active) < d.job.estimate {
		d.armTimer(gen) // a pause froze the clock; wait out the remainder
		return
	}
	d.finishJob(gen, elapsedOf(active))
}

// finishJob runs TRANSLATION §4 dispense steps 10–11: end-of-job
// verification + panel disarm, then job completion. The serial-number ping
// (a) confirms the device is alive after the run and (b) overwrites the
// EEPROM "last command" so a physical START press replays a harmless ping
// instead of re-running the dispense.
func (d *Driver) finishJob(gen int, dur time.Duration) {
	if gen != d.jobGen || d.job == nil || d.s.Jobs().Active() == nil {
		return
	}
	reply, err := d.s.Transact(serialFrame, 4, replyTimeout)
	if err != nil {
		// Transact's double failure flipped the session unreachable and
		// failed the job (decision 2) — nothing left to do here.
		return
	}
	if reply[0] != TypeCode {
		d.s.Jobs().Fail(device.ErrHardware("post-job verification: unexpected reply"))
		d.clearJob()
		return
	}
	j := d.job
	var result any
	if j.kind == "calibration" {
		result = calibrationRunResult{Steps: j.steps, DurationS: dur.Seconds()}
	} else {
		durS := dur.Seconds()
		result = dispenseJobResult{
			DispensedMl:    j.volumeML,
			DurationS:      durS,
			MeanSpeedMlMin: j.volumeML / durS * 60,
			SuckbackMl:     j.suckbackML,
		}
	}
	d.s.Jobs().Complete(result)
	d.clearJob()
}

func (d *Driver) clearJob() {
	d.job = nil
	d.state = stateIdle
	d.jobGen++
}
