// Package pump implements the peristaltic-pump Driver for the v2 JSON device
// protocol. It translates docs/protocol_translation_docs/pump/JSON_PROTOCOL.md
// onto the legacy 5-byte firmware protocol exactly as specified by
// docs/protocol_translation_docs/pump/TRANSLATION.md.
package pump

import (
	"encoding/binary"
	"math"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// Translator config (TRANSLATION §2). Vars so tests and installations can tune.
var (
	// MinDelTimeUs is the fastest allowed step half-period — protects
	// against stalling the motor (TRANSLATION SPEED_TO_BYTES step 4).
	MinDelTimeUs = 400.0
	// CalSteps is the fixed calibration-run step count (TRANSLATION §4
	// start_calibration step 2) — big enough to weigh accurately.
	CalSteps = int64(20000)
	// WatchPoll is the opcode-18 watcher's per-read timeout (real time;
	// bounds how fast an abandoned watcher notices its stop signal).
	WatchPoll = 500 * time.Millisecond
	// TimerGrace pads clock-simulated completions (TRANSLATION §4
	// dispense step 9: "grace wait 0.5 s").
	TimerGrace = 500 * time.Millisecond
)

// maxDelTimeUs = 255 × 255 × 100: the slowest half-period the byte pair encodes.
const maxDelTimeUs = 6_502_500

// gradient ramp endpoints, hardware-fixed (TRANSLATION §4 dispense step 8).
const (
	gradT0Us = 300.0
	gradTEUs = 30000.0
)

// factorDelTime quantizes a half-period onto the firmware's N3×N4×100 µs grid
// (SPEED_TO_BYTES steps 5–7). delUs must already be range-checked.
func factorDelTime(delUs float64) (n3, n4 byte, actualDelUs float64) {
	p := math.Max(1, math.Round(delUs/100))
	f3 := math.Ceil(p / 255)
	f4 := math.Round(p / f3)
	return byte(f3), byte(f4), f3 * f4 * 100
}

// speedToBytes implements SPEED_TO_BYTES (TRANSLATION §2). The caller is
// responsible for the not_calibrated check; mlPerStep must be > 0 here.
func speedToBytes(speedMlMin, mlPerStep float64) (n3, n4 byte, actualDelUs float64, cerr *device.CmdError) {
	if speedMlMin <= 0 {
		return 0, 0, 0, device.ErrInvalidParams("speed_ml_min", speedMlMin, "speed_ml_min must be positive")
	}
	stepsPerS := speedMlMin / 60 / mlPerStep
	delUs := 500000 / stepsPerS
	if delUs < MinDelTimeUs || delUs > maxDelTimeUs {
		return 0, 0, 0, device.ErrInvalidParams("speed_ml_min", speedMlMin, "speed out of range")
	}
	n3, n4, actual := factorDelTime(delUs)
	return n3, n4, actual, nil
}

// actualSpeedMlMin reports the speed the quantized half-period really gives:
// 30_000_000 × ml_per_step / del_time_us (TRANSLATION §2 step 8).
func actualSpeedMlMin(mlPerStep, delTimeUs float64) float64 {
	return 30_000_000 * mlPerStep / delTimeUs
}

// volumeToSteps converts ml to full steps (TRANSLATION §2).
func volumeToSteps(volumeMl, mlPerStep float64) (int64, *device.CmdError) {
	steps := int64(math.Round(volumeMl / mlPerStep))
	if steps < 1 || steps > 2_000_000_000 {
		return 0, device.ErrInvalidParams("volume_ml", volumeMl, "volume out of range")
	}
	return steps, nil
}

// rawDelTimeUs maps a speed percentage to a half-period, bypassing
// calibration (TRANSLATION §4 rotate_raw): 100% → 100 µs, 1% → 10 ms,
// clamped to [MinDelTimeUs, maxDelTimeUs].
func rawDelTimeUs(speedPct int) float64 {
	delUs := math.Round(10000 / float64(speedPct))
	return math.Min(math.Max(delUs, MinDelTimeUs), maxDelTimeUs)
}

// be32 renders a step count as the 4 big-endian parameter bytes N2..N5.
func be32(steps int64) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(steps)) // #nosec G115 -- capped at 2e9 by volumeToSteps
	return b
}

// quantizeSuckback converts a suckback volume to the firmware's DropMult
// (drop quantum = 100 steps), clamped to [2, 255]; returns the actual
// quantized volume to echo (TRANSLATION §4 dispense step 4).
func quantizeSuckback(suckbackMl, mlPerStep float64) (dropMult int, actualMl float64) {
	dropUnitMl := 100 * mlPerStep
	m := math.Round(suckbackMl / dropUnitMl)
	m = math.Min(math.Max(m, 2), 255)
	return int(m), m * dropUnitMl
}

// plainEstimate: steps × 2 toggles × delUs (TRANSLATION §2 duration estimate).
func plainEstimate(steps int64, delUs float64) time.Duration {
	return time.Duration(float64(steps) * 2 * delUs * float64(time.Microsecond))
}

// suckbackEstimate: steps already includes the drop inflation; the reverse
// leg's 200×dropMult toggles run at doubled period, plus the firmware's
// 100 ms turnaround pause (TRANSLATION §4 dispense step 8).
func suckbackEstimate(steps int64, dropMult int, delUs float64) time.Duration {
	toggles := float64(2*steps) + 400*float64(dropMult)
	return time.Duration(toggles*delUs*float64(time.Microsecond)) + 100*time.Millisecond
}

// gradientEstimate integrates the firmware's fixed quadratic ramp
// half-period(k) = 1/sqrt(A + B·k), k = 1..2×steps, with endpoints
// T(1) = 30000 µs and T(2×steps) = 300 µs. Closed form of the integral:
// ∫ dk/sqrt(A+Bk) = 2·sqrt(A+Bk)/B, evaluated at bounds k = 1.0 (lower)
// to k = kmax+0.5 (upper). Lower bound is 1.0 because A is negative by
// construction (for typical step counts); sqrt(A+B·k) has a domain singularity
// just below k≈1. At k=1.0 exactly, A+B·1 = 1/TE², always positive and safe.
// Absolute error stays a small fraction of the TimerGrace (0.5 s) across
// realistic step counts.
func gradientEstimate(steps int64) time.Duration {
	kmax := float64(2 * steps)
	if kmax < 2 {
		return time.Duration(gradTEUs) * time.Microsecond
	}
	b := (1/(gradT0Us*gradT0Us) - 1/(gradTEUs*gradTEUs)) / (kmax - 1)
	a := 1/(gradTEUs*gradTEUs) - b
	integral := func(k float64) float64 { return 2 * math.Sqrt(a+b*k) / b }
	sumUs := integral(kmax+0.5) - integral(1.0)
	return time.Duration(sumUs) * time.Microsecond
}
