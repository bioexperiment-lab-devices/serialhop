package densitometer

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

const ringCap = 64

// ringBuffer holds the most recent ringCap readings (TRANSLATION §1 volatile
// state). Loop-only.
type ringBuffer struct {
	buf   []reading
	start int // index of the oldest entry
	count int
}

func newRingBuffer() *ringBuffer { return &ringBuffer{buf: make([]reading, ringCap)} }

func (rb *ringBuffer) push(r reading) {
	if rb.count < ringCap {
		rb.buf[(rb.start+rb.count)%ringCap] = r
		rb.count++
		return
	}
	rb.buf[rb.start] = r
	rb.start = (rb.start + 1) % ringCap
}

func (rb *ringBuffer) oldestSeq() int64 {
	if rb.count == 0 {
		return 0
	}
	return rb.buf[rb.start].seq
}

func (rb *ringBuffer) since(sinceSeq int64, limit int) []reading {
	var out []reading
	for i := 0; i < rb.count; i++ {
		r := rb.buf[(rb.start+i)%ringCap]
		if r.seq > sinceSeq {
			out = append(out, r)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

type startMonitoringResult struct {
	State     string `json:"state"`
	IntervalS int    `json:"interval_s"`
}

func (d *Driver) startMonitoring(params json.RawMessage) (any, *device.CmdError) {
	p := struct {
		IntervalS int `json:"interval_s"`
	}{IntervalS: 60}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
		}
	}
	if p.IntervalS < 10 {
		return nil, device.ErrInvalidParams("interval_s", p.IntervalS,
			"interval_s must be at least 10 (the sweep duration bound)")
	}
	if d.blank == nil {
		return nil, device.ErrNotCalibrated("no blank measured — run measure_blank first")
	}
	d.monitoring = monitoringState{enabled: true, intervalS: p.IntervalS, nextTickAt: d.s.Now()}
	return startMonitoringResult{State: "monitoring", IntervalS: p.IntervalS}, nil
}

type readingWire struct {
	Seq          int64   `json:"seq"`
	UptimeMs     int64   `json:"uptime_ms"`
	Absorbance   float64 `json:"absorbance"`
	TemperatureC float64 `json:"temperature_c"`
}

func (d *Driver) getReadings(params json.RawMessage) (any, *device.CmdError) {
	p := struct {
		SinceSeq int64 `json:"since_seq"`
		Limit    int   `json:"limit"`
	}{}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
		}
	}
	entries := d.ring.since(p.SinceSeq, p.Limit)
	out := make([]readingWire, 0, len(entries))
	for _, r := range entries {
		out = append(out, readingWire{
			Seq: r.seq, UptimeMs: r.uptimeMs,
			Absorbance: r.absorbance, TemperatureC: r.temperatureC,
		})
	}
	dropped := int64(0)
	if oldest := d.ring.oldestSeq(); oldest > p.SinceSeq+1 {
		dropped = oldest - p.SinceSeq - 1
	}
	return map[string]any{"readings": out, "dropped": dropped}, nil
}

// Tick runs ~1/s while attached: the monitoring scheduler then the idle reboot
// canary. Both need the port, so both are skipped while a sweep is in flight or
// a job is active.
func (d *Driver) Tick(now time.Time) {
	if now.Before(d.busyUntil) || d.s.Jobs().Active() != nil {
		return
	}
	if d.monitoring.enabled && !now.Before(d.monitoring.nextTickAt) {
		d.monitoring.nextTickAt = now.Add(time.Duration(d.monitoring.intervalS) * time.Second)
		d.startMonitorMeasure()
		return // a measure now owns the port; the canary waits for the next idle Tick
	}
	if !now.Before(d.nextCanaryAt) {
		d.nextCanaryAt = now.Add(CanaryInterval)
		if reply, err := d.s.Transact(thermReadFrame, 4, replyTimeout); err == nil {
			d.applyThermostatReadback(decodeFixedPoint(reply), true)
		}
	}
}

// startMonitorMeasure fires an internal measure sweep whose completion lands a
// reading in the ring (kind "monitor" → finishMeasure).
func (d *Driver) startMonitorMeasure() {
	if d.blank == nil {
		slog.Warn("densitometer: monitoring active without a blank — disabling", "device", d.serial)
		d.monitoring = monitoringState{}
		return
	}
	if _, cerr := d.runSweep("monitor", measureTrigger, SweepWait, sweep{}); cerr != nil {
		slog.Warn("densitometer: monitoring measure failed to start", "device", d.serial, "err", cerr)
	}
}
