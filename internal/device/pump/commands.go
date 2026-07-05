package pump

import (
	"encoding/json"
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
	Unverified    bool    `json:"unverified,omitempty"`
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
	return &calibrationInfo{MlPerStep: d.mlPerStep, SetAtUptimeMs: upMs, Unverified: d.unverified}
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
	if d.mlPerStep > 0 && !d.unverified {
		d.rotSpeedML = actualSpeedMlMin(d.mlPerStep, actualUs)
	}
	return rotateRawResult{State: "rotating", SpeedPct: p.SpeedPct}, nil
}
