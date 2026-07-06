package densitometer

import (
	"encoding/json"
	"math"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

type pingResult struct {
	UptimeMs int64 `json:"uptime_ms"`
}

// ping (TRANSLATION §4): prove liveness with 71 2 3 4 0; uptime_ms is
// translator connection age (true device uptime is unknowable).
func (d *Driver) ping() (any, *device.CmdError) {
	if cerr := d.serialGate(); cerr != nil {
		return nil, cerr
	}
	reply, err := d.s.Transact(pingFrame, 4, replyTimeout)
	if err != nil {
		return nil, device.ErrHardware("ping: " + err.Error())
	}
	if reply[0] != TypeCode {
		return nil, device.ErrHardware("ping: unexpected reply")
	}
	return pingResult{UptimeMs: d.s.Now().Sub(d.connectedSince).Milliseconds()}, nil
}

type blankStatus struct {
	Slope        float64 `json:"slope"`
	TemperatureC float64 `json:"temperature_c"`
	AgeS         float64 `json:"age_s"`
}

type calibrationStatus struct {
	Blank          *blankStatus `json:"blank"`
	TubeCorrection float64      `json:"tube_correction"`
}

type thermostatStatus struct {
	Enabled bool    `json:"enabled"`
	TargetC float64 `json:"target_c"`
	Heating *bool   `json:"heating"` // GAP: never reported → null
	Cooling *bool   `json:"cooling"` // GAP: never reported → null
}

type lastMeasurement struct {
	Seq          int64   `json:"seq"`
	Absorbance   float64 `json:"absorbance"`
	TemperatureC float64 `json:"temperature_c"`
	AgeS         float64 `json:"age_s"`
}

type statusResult struct {
	State           string            `json:"state"`
	Job             *device.Job       `json:"job"`
	TemperatureC    float64           `json:"temperature_c"`
	Thermostat      thermostatStatus  `json:"thermostat"`
	Calibration     calibrationStatus `json:"calibration"`
	LastMeasurement *lastMeasurement  `json:"last_measurement"`
}

// statusJob returns the active job else the most recent one.
func (d *Driver) statusJob() *device.Job {
	if j := d.s.Jobs().Active(); j != nil {
		return j
	}
	if d.lastJobID != "" {
		return d.s.Jobs().Get(d.lastJobID)
	}
	return nil
}

// status (TRANSLATION §4) never blocks and never returns busy. When idle it
// reads live temperature + thermostat and runs the reboot canary; mid-sweep it
// serves the cached temperature with an age.
func (d *Driver) status() (any, *device.CmdError) {
	res := statusResult{
		State: d.stateName(),
		Job:   d.statusJob(),
		Thermostat: thermostatStatus{
			Enabled: d.thermo.Enabled, TargetC: d.thermo.TargetC,
		},
		Calibration: calibrationStatus{TubeCorrection: d.tubeCorrection},
	}
	if d.blank != nil {
		res.Calibration.Blank = &blankStatus{
			Slope: d.blank.Slope, TemperatureC: d.blank.TemperatureC,
			AgeS: d.s.Now().Sub(d.blank.MeasuredAt).Seconds(),
		}
	}
	if d.lastReading != nil {
		res.LastMeasurement = &lastMeasurement{
			Seq: d.lastReading.seq, Absorbance: d.lastReading.absorbance,
			TemperatureC: d.lastReading.temperatureC,
			AgeS:         d.s.Now().Sub(d.lastReading.measuredAt).Seconds(),
		}
	}

	if d.serialGate() != nil {
		// Mid-sweep: reuse cached temperature (flagged with its age via the
		// value only; the device cannot be read now).
		if d.haveCachTemp {
			res.TemperatureC = d.cachedTemp
		}
		return res, nil
	}

	// Idle: read live temperature and thermostat, run the canary.
	if tReply, err := d.s.Transact(tempFrame, 4, replyTimeout); err == nil {
		res.TemperatureC = decodeFixedPoint(tReply)
		d.cachedTemp, d.cachedTempAt, d.haveCachTemp = res.TemperatureC, d.s.Now(), true
	} else if d.haveCachTemp {
		res.TemperatureC = d.cachedTemp
	}
	if thReply, err := d.s.Transact(thermReadFrame, 4, replyTimeout); err == nil {
		d.applyThermostatReadback(decodeFixedPoint(thReply), true)
		// re-read enabled/target from the (possibly re-pushed) mirror
		res.Thermostat.Enabled = d.thermo.Enabled
		res.Thermostat.TargetC = d.thermo.TargetC
	}
	return res, nil
}

// stateName maps driver state to the JSON status.state enum.
func (d *Driver) stateName() string {
	switch {
	case d.monitoring.enabled:
		return "monitoring"
	case d.s.Jobs().Active() != nil:
		return "measuring"
	default:
		return "idle"
	}
}

func (d *Driver) setTubeCorrection(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Factor float64 `json:"factor"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if p.Factor < 0.5 || p.Factor > 2.0 {
		return nil, device.ErrInvalidParams("factor", p.Factor, "factor must be between 0.5 and 2.0")
	}
	d.tubeCorrection = p.Factor
	if err := d.persist(); err != nil {
		return nil, device.ErrInternal("persist tube correction: " + err.Error())
	}
	return map[string]any{"tube_correction": p.Factor}, nil
}

func (d *Driver) calibrateTube(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		ReferenceAbsorbance float64 `json:"reference_absorbance"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if d.lastReading == nil {
		return nil, device.ErrNotCalibrated("no measurement to calibrate from")
	}
	if p.ReferenceAbsorbance <= 0 {
		return nil, device.ErrInvalidParams("reference_absorbance", p.ReferenceAbsorbance,
			"reference_absorbance must be positive")
	}
	uncorrected := d.lastReading.absorbance / d.lastReading.tubeCorrectionAt
	if uncorrected == 0 {
		return nil, device.ErrInvalidParams("reference_absorbance", p.ReferenceAbsorbance,
			"last measurement absorbance is zero — cannot calibrate")
	}
	factor := p.ReferenceAbsorbance / uncorrected
	// A materially out-of-range factor means a bad reference or a unit error —
	// reject it rather than silently corrupt every later measurement. A factor
	// that only overshoots by float noise (the boundary case) is snapped to bound.
	const tol = 1e-6
	if factor < 0.5-tol || factor > 2.0+tol {
		return nil, device.ErrInvalidParams("reference_absorbance", p.ReferenceAbsorbance,
			"resulting tube correction out of range [0.5, 2.0]")
	}
	factor = math.Max(0.5, math.Min(2.0, factor))
	d.tubeCorrection = factor
	if err := d.persist(); err != nil {
		return nil, device.ErrInternal("persist tube correction: " + err.Error())
	}
	return map[string]any{"tube_correction": factor, "based_on_seq": d.lastReading.seq}, nil
}

// appendReading records the newest measurement. Task 8 also pushes it to the
// readings ring buffer.
func (d *Driver) appendReading(r reading) {
	rr := r
	d.lastReading = &rr
	d.ring.push(rr)
}

// setLED (TRANSLATION §4): drives the LED brightness directly, independent of
// a sweep. Gated by serialGate (mid-sweep this would collide with the sweep's
// own traffic); the firmware never acks a brightness set, so the result is
// optimistic.
func (d *Driver) setLED(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Level int `json:"level"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if cerr := d.serialGate(); cerr != nil {
		return nil, cerr
	}
	if p.Level < 0 || p.Level > 20 {
		return nil, device.ErrInvalidParams("level", p.Level, "level must be 0..20")
	}
	if _, err := d.s.Transact([]byte{75, 0, byte(p.Level), 0, 0}, 0, replyTimeout); err != nil { // #nosec G115 -- p.Level validated 0..20 above
		return nil, device.ErrHardware("set_led: " + err.Error())
	}
	return map[string]any{"level": p.Level}, nil // GAP: no readback, optimistic
}

type stopResult struct {
	State          string `json:"state"`
	CancelledJobID string `json:"cancelled_job_id,omitempty"`
}

// stop (TRANSLATION §4): sends 70 (LED off / stop continuous mode) — during a
// sweep this buffers in the device RX and runs when the sweep ends — then
// cancels the job bookkeeping immediately and disables monitoring. Exempt from
// serialGate: the 70 frame is write-only and safe to buffer. Deliberately does
// NOT touch busyUntil: the firmware cannot abort a sweep in flight, so the
// device is still physically busy until the original window elapses.
func (d *Driver) stop() (any, *device.CmdError) {
	if _, err := d.s.Transact(stopFrame, 0, replyTimeout); err != nil {
		return nil, device.ErrHardware("stop: " + err.Error())
	}
	res := stopResult{State: "idle"}
	if a := d.s.Jobs().Active(); a != nil {
		cancelled := d.s.Jobs().Cancel()
		res.CancelledJobID = cancelled.ID
	}
	d.monitoring = monitoringState{}
	d.clearSweep() // bumps sweepGen → pending completion callbacks no-op
	return res, nil
}

type stopMonitoringResult struct {
	State string `json:"state"`
}

// stopMonitoring disables the scheduler (bookkeeping only). Also invoked by stop.
func (d *Driver) stopMonitoring() (any, *device.CmdError) {
	d.monitoring = monitoringState{}
	return stopMonitoringResult{State: d.stateName()}, nil
}
