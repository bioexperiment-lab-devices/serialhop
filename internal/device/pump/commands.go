package pump

import (
	"encoding/json"
	"math"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

type pingResult struct {
	UptimeMs int64 `json:"uptime_ms"`
}

// ping (TRANSLATION §4): when idle, prove liveness with the identify frame —
// the only frame that writes nothing to EEPROM. In any other state it is
// answered from memory: mid-job serial traffic could interleave with an
// opcode-18 completion reply, and mid-rotate it would stall the motor
// ~100 ms. uptime_ms is connection age (true device uptime is unknowable).
func (d *Driver) ping() (any, *device.CmdError) {
	if d.state == stateIdle {
		reply, err := d.s.Transact(identifyFrame, 4, replyTimeout)
		if err != nil {
			return nil, device.ErrHardware("ping: " + err.Error())
		}
		if reply[0] != TypeCode {
			return nil, device.ErrHardware("ping: unexpected identify reply")
		}
	}
	return pingResult{UptimeMs: d.s.Now().Sub(d.connectedSince).Milliseconds()}, nil
}

type calibrationInfo struct {
	MlPerStep     float64 `json:"ml_per_step"`
	SetAtUptimeMs int64   `json:"set_at_uptime_ms"`
}

func (d *Driver) calibrationBlock() *calibrationInfo {
	if d.mlPerStep <= 0 {
		return nil
	}
	var upMs int64
	if !d.calSetAt.IsZero() {
		if ms := d.calSetAt.Sub(d.connectedSince).Milliseconds(); ms > 0 {
			upMs = ms // clamped ≥ 0: persisted calibration may predate this connection
		}
	}
	return &calibrationInfo{MlPerStep: d.mlPerStep, SetAtUptimeMs: upMs}
}

func (d *Driver) getCalibration() (any, *device.CmdError) {
	cal := d.calibrationBlock()
	if cal == nil {
		return nil, device.ErrNotCalibrated("no volume calibration stored")
	}
	return cal, nil
}

type statusResult struct {
	State       string           `json:"state"`
	Job         *device.Job      `json:"job"`
	Direction   *string          `json:"direction"`
	SpeedMlMin  *float64         `json:"speed_ml_min"`
	DispensedMl *float64         `json:"dispensed_ml"`
	Calibration *calibrationInfo `json:"calibration"`
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

// status (TRANSLATION §4) is served entirely from translator state — the
// firmware has no state-query command. Panel-button activity is invisible
// (documented gap).
func (d *Driver) status() (any, *device.CmdError) {
	res := statusResult{State: string(d.state), Calibration: d.calibrationBlock()}
	res.Job = d.statusJob()

	switch d.state {
	case stateRotating:
		dir := d.rotDirection
		res.Direction = &dir
		if d.rotSpeedML > 0 {
			v := d.rotSpeedML
			res.SpeedMlMin = &v
		}
	case stateDispensing, stateCalibrating, statePaused:
		if d.job != nil {
			dir := d.job.direction
			res.Direction = &dir
			if d.job.speedML > 0 {
				v := d.job.speedML
				res.SpeedMlMin = &v
			}
		} else if d.pausedFrom == stateRotating {
			dir := d.rotDirection
			res.Direction = &dir
			if d.rotSpeedML > 0 {
				v := d.rotSpeedML
				res.SpeedMlMin = &v
			}
		}
	}

	// dispensed_ml: progress × volume of the current/last dispense job
	// (clock-driven estimate; exact only on verified completion).
	if res.Job != nil {
		if d.job != nil && d.job.kind == "dispense" {
			v := res.Job.Progress * d.job.volumeML
			res.DispensedMl = &v
		} else if d.job == nil && d.lastJobKind == "dispense" && d.lastVolumeML > 0 {
			v := res.Job.Progress * d.lastVolumeML
			res.DispensedMl = &v
		}
	}
	return res, nil
}

// parseDirection maps the JSON direction to the run opcode. Polarity is
// fixed forward=11 / reverse=12 (per-installation configurability deferred).
func parseDirection(dir string) (opcode byte, cerr *device.CmdError) {
	switch dir {
	case "forward":
		return 11, nil
	case "reverse":
		return 12, nil
	default:
		return 0, device.ErrInvalidParams("direction", dir, `direction must be "forward" or "reverse"`)
	}
}

// busyGuard rejects motion-starting commands while a job is active or the
// device is paused (a bare rotating state may be retargeted freely).
func (d *Driver) busyGuard() *device.CmdError {
	if j := d.s.Jobs().Active(); j != nil {
		return device.ErrBusy("a job is running", map[string]any{"job_id": j.ID})
	}
	if d.state == statePaused {
		return device.ErrBusy("device is paused — resume or stop first",
			map[string]any{"state": string(statePaused)})
	}
	return nil
}

// startRotation sends the two-frame sequence (TRANSLATION §4 rotate steps
// 4–5): the cmd-10 arming frame is REQUIRED — 11/12 do not touch the
// firmware's pause toggle, and cmd 10 is the only command that forces it to
// "running" (it also clears leftover gradient mode).
func (d *Driver) startRotation(direction string, n3, n4 byte) *device.CmdError {
	if _, err := d.s.Transact([]byte{10, 0, n3, n4, 0}, 0, time.Second); err != nil {
		return device.ErrHardware("rotate arming frame: " + err.Error())
	}
	d.pauseAssumed = false
	opcode, cerr := parseDirection(direction)
	if cerr != nil {
		return cerr
	}
	if _, err := d.s.Transact([]byte{opcode, 0, n3, n4, 0}, 0, time.Second); err != nil {
		return device.ErrHardware("rotate motion frame: " + err.Error())
	}
	d.state = stateRotating
	d.rotDirection = direction
	return nil
}

type rotateResult struct {
	State      string  `json:"state"`
	Direction  string  `json:"direction"`
	SpeedMlMin float64 `json:"speed_ml_min"`
}

func (d *Driver) rotate(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Direction  string  `json:"direction"`
		SpeedMlMin float64 `json:"speed_ml_min"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if cerr := d.busyGuard(); cerr != nil {
		return nil, cerr
	}
	if cerr := d.requireCalibration(); cerr != nil {
		return nil, cerr
	}
	if _, cerr := parseDirection(p.Direction); cerr != nil {
		return nil, cerr
	}
	n3, n4, actualUs, cerr := speedToBytes(p.SpeedMlMin, d.mlPerStep)
	if cerr != nil {
		return nil, cerr
	}
	if cerr := d.startRotation(p.Direction, n3, n4); cerr != nil {
		return nil, cerr
	}
	d.rotSpeedML = actualSpeedMlMin(d.mlPerStep, actualUs)
	d.rotSpeedPct = 0
	return rotateResult{State: "rotating", Direction: p.Direction, SpeedMlMin: d.rotSpeedML}, nil
}

type rotateRawResult struct {
	State    string `json:"state"`
	SpeedPct int    `json:"speed_pct"`
}

func (d *Driver) rotateRaw(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Direction string `json:"direction"`
		SpeedPct  int    `json:"speed_pct"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if cerr := d.busyGuard(); cerr != nil {
		return nil, cerr
	}
	if p.SpeedPct < 1 || p.SpeedPct > 100 {
		return nil, device.ErrInvalidParams("speed_pct", p.SpeedPct, "speed_pct must be 1..100")
	}
	if _, cerr := parseDirection(p.Direction); cerr != nil {
		return nil, cerr
	}
	n3, n4, actualUs := factorDelTime(rawDelTimeUs(p.SpeedPct))
	if cerr := d.startRotation(p.Direction, n3, n4); cerr != nil {
		return nil, cerr
	}
	d.rotSpeedPct = p.SpeedPct
	d.rotSpeedML = 0
	if d.mlPerStep > 0 {
		d.rotSpeedML = actualSpeedMlMin(d.mlPerStep, actualUs)
	}
	return rotateRawResult{State: "rotating", SpeedPct: p.SpeedPct}, nil
}

type pauseResult struct {
	State       string   `json:"state"`
	JobID       string   `json:"job_id,omitempty"`
	DispensedMl *float64 `json:"dispensed_ml,omitempty"`
}

// pause / resume (TRANSLATION §4): cmd 19 is a blind toggle with no state
// query, so the frame goes out via WriteFrame — single write, no retry — a
// duplicate send would invert the toggle undetectably (PR-1 decision 4).
// pauseAssumed tracks our belief; every cmd-10 frame (dispense/rotate/stop)
// forces the firmware toggle to "running", so a desync from panel use never
// survives past the current job.
func (d *Driver) pause() (any, *device.CmdError) {
	switch d.state {
	case stateIdle:
		return nil, device.ErrBusy("nothing to pause", map[string]any{"state": "idle"})
	case statePaused:
		return nil, device.ErrBusy("already paused", map[string]any{"state": "paused"})
	}
	if err := d.s.WriteFrame(pauseFrame); err != nil {
		return nil, device.ErrHardware("pause: " + err.Error())
	}
	d.pauseAssumed = true
	d.s.Jobs().Freeze()
	d.pausedFrom = d.state
	d.state = statePaused

	res := pauseResult{State: "paused"}
	if a := d.s.Jobs().Active(); a != nil {
		res.JobID = a.ID
		if d.job != nil && d.job.kind == "dispense" {
			v := a.Progress * d.job.volumeML
			res.DispensedMl = &v
		}
	}
	return res, nil
}

func (d *Driver) resume() (any, *device.CmdError) {
	if d.state != statePaused {
		return nil, device.ErrBusy("not paused", map[string]any{"state": string(d.state)})
	}
	if err := d.s.WriteFrame(pauseFrame); err != nil {
		return nil, device.ErrHardware("resume: " + err.Error())
	}
	d.pauseAssumed = false
	d.s.Jobs().Unfreeze()
	d.state = d.pausedFrom
	res := pauseResult{State: string(d.state)}
	if a := d.s.Jobs().Active(); a != nil {
		res.JobID = a.ID
	}
	return res, nil
}

type stopResult struct {
	State          string   `json:"state"`
	CancelledJobID string   `json:"cancelled_job_id,omitempty"`
	DispensedMl    *float64 `json:"dispensed_ml,omitempty"`
}

// stop (TRANSLATION §4): the cmd-10 frame clears the remaining step count
// and takes effect within one step period. It also forces the firmware's
// pause toggle to "running" — stop doubles as the pause-belief resync point.
// An opcode-18 wait is abandoned (the firmware only replies if the run
// finishes on its own). Post-stop, the identify frame verifies the device
// is still responsive.
func (d *Driver) stop() (any, *device.CmdError) {
	if _, err := d.s.Transact(stopFrame, 0, time.Second); err != nil {
		return nil, device.ErrHardware("stop: " + err.Error())
	}
	d.pauseAssumed = false
	d.abandonWatch()

	res := stopResult{State: "idle"}
	if a := d.s.Jobs().Active(); a != nil {
		cancelled := d.s.Jobs().Cancel()
		res.CancelledJobID = cancelled.ID
		if d.job != nil && d.job.kind == "dispense" {
			v := cancelled.Progress * d.job.volumeML
			res.DispensedMl = &v
		}
	}
	d.job = nil
	d.jobGen++ // invalidate any in-flight timer/watchdog callbacks
	d.state = stateIdle
	d.pausedFrom = ""
	d.rotDirection, d.rotSpeedML, d.rotSpeedPct = "", 0, 0

	reply, err := d.s.Transact(identifyFrame, 4, replyTimeout)
	if err != nil {
		return nil, device.ErrHardware("post-stop verification: " + err.Error())
	}
	if reply[0] != TypeCode {
		return nil, device.ErrHardware("post-stop verification: unexpected reply")
	}
	return res, nil
}

// setCalibration (TRANSLATION §4): variant A computes ml_per_step from a
// succeeded calibration job; variant B restores a known value directly.
// Either way the value is written to the device's 3 EEPROM calibration
// bytes (cmd 13 — the device is the only store; there is no serial number
// and no on-disk file), and the mirror is read back for verification via
// the identify frame.
func (d *Driver) setCalibration(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		JobID            string  `json:"job_id"`
		MeasuredVolumeMl float64 `json:"measured_volume_ml"`
		MlPerStep        float64 `json:"ml_per_step"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if cerr := d.busyGuard(); cerr != nil {
		return nil, cerr
	}
	if d.state != stateIdle {
		return nil, device.ErrBusy("device is moving — stop first",
			map[string]any{"state": string(d.state)})
	}

	var mlPerStep float64
	switch {
	case p.JobID != "" && p.MlPerStep != 0:
		return nil, device.ErrInvalidParams("ml_per_step", p.MlPerStep,
			"provide either job_id+measured_volume_ml or ml_per_step, not both")
	case p.JobID != "":
		if p.MeasuredVolumeMl <= 0 {
			return nil, device.ErrInvalidParams("measured_volume_ml", p.MeasuredVolumeMl,
				"measured_volume_ml must be positive")
		}
		job := d.s.Jobs().Get(p.JobID)
		if job == nil || job.State != device.JobSucceeded || job.Kind != "calibration" {
			return nil, device.ErrInvalidParams("job_id", p.JobID,
				"job_id must reference a succeeded calibration job")
		}
		res, ok := job.Result.(calibrationRunResult)
		if !ok || res.Steps <= 0 {
			return nil, device.ErrInternal("calibration job has no step count")
		}
		mlPerStep = p.MeasuredVolumeMl / float64(res.Steps)
	case p.MlPerStep != 0:
		mlPerStep = p.MlPerStep
	default:
		return nil, device.ErrInvalidParams("ml_per_step", nil,
			"provide job_id+measured_volume_ml or ml_per_step")
	}
	if mlPerStep < 1e-6 || mlPerStep > 0.1 {
		return nil, device.ErrInvalidParams("ml_per_step", mlPerStep,
			"ml_per_step out of sane range [1e-6, 0.1]")
	}
	if cerr := d.persistCalibration(mlPerStep); cerr != nil {
		return nil, cerr
	}
	return map[string]any{"ml_per_step": mlPerStep}, nil
}

// persistCalibration is named for the on-disk store it historically wrote;
// that write is gone (the store/serial Driver fields it depended on were
// deleted — see Attach's doc comment in pump.go). Today it only updates
// in-memory state and pushes the device's EEPROM mirror, which is the sole
// place calibration now lives.
func (d *Driver) persistCalibration(mlPerStep float64) *device.CmdError {
	now := d.s.Now()
	d.mlPerStep, d.calSetAt = mlPerStep, now

	// EEPROM mirror (human-paced only — EEPROM wear rules). Round, don't
	// truncate: variant-A divisions rarely land on integers in float64 (e.g. 10.0/13 steps × 1e8 has a fractional part that truncation would drop).
	v := uint32(math.Round(mlPerStep * 1e8))
	if v > 0xFFFFFF {
		v = 0xFFFFFF
	}
	frame := []byte{13, 0, byte(v >> 16), byte(v >> 8), byte(v)} // #nosec G115 -- bounded by the [1e-6, 0.1] sanity check
	if _, err := d.s.Transact(frame, 0, time.Second); err != nil {
		return device.ErrHardware("calibration mirror write: " + err.Error())
	}
	reply, err := d.s.Transact(identifyFrame, 4, replyTimeout)
	if err != nil {
		return device.ErrHardware("calibration mirror verify: " + err.Error())
	}
	got := uint32(reply[1])<<16 | uint32(reply[2])<<8 | uint32(reply[3])
	if reply[0] != TypeCode || got != v {
		return device.ErrHardware("calibration mirror verify: device echoed different bytes")
	}
	d.s.SetInfo(d.info()) // capabilities changed (speed limits now reportable)
	return nil
}
