package densitometer

import (
	"encoding/json"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

var (
	blankTrigger   = []byte{78, 3, 0, 0, 0}
	measureTrigger = []byte{78, 4, 0, 0, 0}
)

// runSweep implements TRANSLATION §4 RUN_SWEEP: start a job, fire the trigger
// fire-and-forget (the firmware never acks), open the busy_until window, and
// schedule the After completion chain. No reply-expecting traffic touches the
// port until busy_until passes.
func (d *Driver) runSweep(kind string, trigger []byte, wait time.Duration, sw sweep) (device.Job, *device.CmdError) {
	if cerr := d.busyGuard(); cerr != nil {
		return device.Job{}, cerr
	}
	job, cerr := d.s.Jobs().Start(kind, wait+2*time.Second)
	if cerr != nil {
		return device.Job{}, cerr // unreachable: busyGuard ran first
	}
	if _, err := d.s.Transact(trigger, 0, replyTimeout); err != nil {
		// Transact double-fail already failed the job + flipped unreachable.
		return device.Job{}, device.ErrHardware("sweep trigger: " + err.Error())
	}
	d.sweepGen++
	sw.gen, sw.kind = d.sweepGen, kind
	d.sweep = &sw
	d.busyUntil = d.s.Now().Add(wait)
	d.lastJobID = job.ID
	gen := d.sweepGen
	d.s.After(wait, func() { d.onSweepDone(gen) })
	return job, nil
}

// stale reports whether a completion callback is for a superseded sweep.
func (d *Driver) stale(gen int) bool {
	return gen != d.sweepGen || d.sweep == nil || d.s.Jobs().Active() == nil
}

func (d *Driver) onSweepDone(gen int) {
	if d.stale(gen) {
		return
	}
	d.livenessAttempt(gen, 1)
}

// softPing does one bounded liveness read via the raw port. Unlike Transact it
// does NOT trip the session unreachable — the completion chain retries it up to
// LivenessRetries because the device may still be finishing its sweep
// (TRANSLATION §4 step 6). Loop-only; bounded to a few PerByteTimeouts.
func (d *Driver) softPing() bool {
	port := d.s.Conn()
	if err := port.Drain(device.DrainWindow); err != nil {
		return false
	}
	if _, err := port.Write(pingFrame); err != nil {
		return false
	}
	if err := port.SetReadTimeout(device.PerByteTimeout); err != nil {
		return false
	}
	buf := make([]byte, 0, 4)
	deadline := d.s.Now().Add(replyTimeout)
	for len(buf) < 4 {
		if d.s.Now().After(deadline) {
			return false
		}
		chunk := make([]byte, 4-len(buf))
		n, err := port.Read(chunk)
		if err != nil || n == 0 {
			return false
		}
		buf = append(buf, chunk[:n]...)
	}
	return buf[0] == TypeCode
}

func (d *Driver) livenessAttempt(gen, n int) {
	if d.stale(gen) {
		return
	}
	if n < LivenessRetries {
		if d.softPing() {
			d.readSweepAndFinish(gen)
			return
		}
		d.s.After(LivenessSpacing, func() { d.livenessAttempt(gen, n+1) })
		return
	}
	// Final attempt is a hard Transact: on failure it trips unreachable and
	// fails the active job (TRANSLATION §5); on success we proceed.
	reply, err := d.s.Transact(pingFrame, 4, replyTimeout)
	if err != nil {
		return // markUnreachable already failed the job + scheduled reattach
	}
	if reply[0] != TypeCode {
		d.s.Jobs().Fail(device.ErrHardware("liveness: unexpected reply"))
		d.clearSweep()
		return
	}
	d.readSweepAndFinish(gen)
}

// readSweepAndFinish reads the 20-point array (validate + one retry) and the
// sweep-time temperature, then dispatches to the kind-specific finisher.
func (d *Driver) readSweepAndFinish(gen int) {
	if d.stale(gen) {
		return
	}
	intensities, cerr := d.readIntensityArray()
	if cerr != nil {
		d.s.Jobs().Fail(cerr)
		d.clearSweep()
		return
	}
	var tempC float64
	if tReply, err := d.s.Transact(tempFrame, 4, replyTimeout); err == nil {
		tempC = decodeFixedPoint(tReply)
		d.cachedTemp, d.cachedTempAt, d.haveCachTemp = tempC, d.s.Now(), true
	} else {
		d.s.Jobs().Fail(device.ErrHardware("sweep temperature read: " + err.Error()))
		d.clearSweep()
		return
	}
	switch d.sweep.kind {
	case "blank":
		d.finishBlank(gen, intensities, tempC)
	case "measure", "monitor":
		d.finishMeasure(gen, intensities, tempC)
	// "read_raw" wired in Task 7
	default:
		d.s.Jobs().Fail(device.ErrInternal("unknown sweep kind: " + d.sweep.kind))
		d.clearSweep()
	}
}

// readIntensityArray reads and validates the 80-byte array, flushing and
// retrying once on a header/index mismatch (button-session interleave).
func (d *Driver) readIntensityArray() ([20]int, *device.CmdError) {
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := d.s.Transact(arrayReadFrame, 80, ArrayReadTimeout)
		if err != nil {
			// A timeout trips unreachable inside Transact; surface hardware_error.
			return [20]int{}, device.ErrHardware("array read: " + err.Error())
		}
		intensities, cerr := parseIntensityArray(raw)
		if cerr == nil {
			return intensities, nil
		}
		if attempt == 1 {
			return [20]int{}, cerr
		}
	}
	return [20]int{}, device.ErrInternal("array read: unreachable")
}

type blankJobResult struct {
	Slope        float64 `json:"slope"`
	TemperatureC float64 `json:"temperature_c"`
	Sweep        []int   `json:"sweep"`
}

func (d *Driver) finishBlank(gen int, intensities [20]int, tempC float64) {
	if d.stale(gen) {
		return
	}
	slope, cerr := leastSquaresSlope(intensities)
	if cerr != nil {
		d.s.Jobs().Fail(cerr)
		d.clearSweep()
		return
	}
	now := d.s.Now()
	d.blank = &blankState{Slope: slope, TemperatureC: tempC, MeasuredAt: now}
	if err := d.persist(); err != nil {
		d.s.Jobs().Fail(device.ErrInternal("persist blank: " + err.Error()))
		d.clearSweep()
		return
	}
	d.s.Jobs().Complete(blankJobResult{
		Slope: slope, TemperatureC: tempC, Sweep: sliceOf(intensities),
	})
	d.clearSweep()
}

// sliceOf converts the fixed array to a slice for JSON marshaling.
func sliceOf(a [20]int) []int {
	out := make([]int, 20)
	copy(out, a[:])
	return out
}

func (d *Driver) measureBlank() (any, *device.CmdError) {
	job, cerr := d.runSweep("blank", blankTrigger, SweepWait, sweep{})
	if cerr != nil {
		return nil, cerr
	}
	return map[string]any{"job": job}, nil
}

func (d *Driver) measure(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		IncludeRaw bool `json:"include_raw"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
		}
	}
	if d.blank == nil {
		return nil, device.ErrNotCalibrated("no blank measured — run measure_blank first")
	}
	job, cerr := d.runSweep("measure", measureTrigger, SweepWait, sweep{includeRaw: p.IncludeRaw})
	if cerr != nil {
		return nil, cerr
	}
	return map[string]any{"job": job}, nil
}

type measureJobResult struct {
	Absorbance     float64 `json:"absorbance"`
	AbsorbanceRaw  float64 `json:"absorbance_raw"`
	Slope          float64 `json:"slope"`
	BlankSlope     float64 `json:"blank_slope"`
	TemperatureC   float64 `json:"temperature_c"`
	TubeCorrection float64 `json:"tube_correction"`
	Seq            int64   `json:"seq"`
	Raw            []int   `json:"raw"`
}

// finishMeasure computes absorbance, records the reading, and completes the
// job. Shared by the measure command and the monitoring scheduler (kind
// "measure" and "monitor" both land here).
func (d *Driver) finishMeasure(gen int, intensities [20]int, tempC float64) {
	if d.stale(gen) {
		return
	}
	slope, cerr := leastSquaresSlope(intensities)
	if cerr != nil {
		d.s.Jobs().Fail(cerr)
		d.clearSweep()
		return
	}
	final, raw := absorbance(d.blank.Slope, slope, tempC, d.blank.TemperatureC, d.tubeCorrection)
	now := d.s.Now()
	d.seqCounter++
	r := reading{
		seq: d.seqCounter, measuredAt: now,
		uptimeMs:   now.Sub(d.connectedSince).Milliseconds(),
		absorbance: final, temperatureC: tempC, tubeCorrectionAt: d.tubeCorrection,
	}
	d.appendReading(r)

	var rawSweep []int
	if d.sweep.includeRaw {
		rawSweep = sliceOf(intensities)
	}
	d.s.Jobs().Complete(measureJobResult{
		Absorbance: final, AbsorbanceRaw: raw, Slope: slope,
		BlankSlope: d.blank.Slope, TemperatureC: tempC,
		TubeCorrection: d.tubeCorrection, Seq: r.seq, Raw: rawSweep,
	})
	d.clearSweep()
}
