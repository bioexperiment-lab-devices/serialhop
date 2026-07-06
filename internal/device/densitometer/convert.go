// Package densitometer implements the cell-density / optical-absorbance
// detector Driver for the v2 JSON device protocol. It translates
// docs/protocol_translation_docs/densitometer/JSON_PROTOCOL.md onto the legacy
// 5-byte firmware protocol exactly as
// docs/protocol_translation_docs/densitometer/TRANSLATION.md specifies. Design
// principle: the device is a sensor/actuator only; all slope fitting,
// absorbance math, temperature compensation, and tube correction live here.
package densitometer

import (
	"fmt"
	"math"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// Translator timing knobs (TRANSLATION.md §2). Vars so tests can shrink them
// (repo precedent: device.PerByteTimeout, pump.WatchPoll).
var (
	// SweepWait bounds a full 20-level sweep (78 3 / 78 4): ~3.5 s of ADC work
	// plus main-loop slack, with margin.
	SweepWait = 6 * time.Second
	// SingleLevelWait bounds the single-level read (75 1): ~12 s (5× the ADC
	// reads per slot).
	SingleLevelWait = 15 * time.Second
	// ArrayReadTimeout bounds the 80-byte array read (79 1 0): 80 bytes ×
	// ~15 ms inter-byte delay.
	ArrayReadTimeout = 3 * time.Second
	// ThermoSettle is how long set_thermostat waits after 75 2 before verifying
	// — the firmware blocks ~1 s redrawing the display before reading serial.
	ThermoSettle = 1500 * time.Millisecond
	// LivenessSpacing separates post-sweep liveness retries.
	LivenessSpacing = time.Second
	// LivenessRetries is how many liveness polls a sweep completion attempts
	// before declaring the device unreachable.
	LivenessRetries = 3
	// CanaryInterval is the idle reboot-canary poll period (TRANSLATION §5).
	CanaryInterval = 30 * time.Second
)

// replyTimeout bounds the small 4-byte replies (arrive within ~60 ms).
const replyTimeout = 2 * time.Second

// decodeFixedPoint decodes the firmware's 2-byte fixed-point float carried in a
// reply's bytes 2–3: value = int + hundredths/100.
func decodeFixedPoint(reply []byte) float64 {
	return float64(reply[2]) + float64(reply[3])/100
}

// decodeIntensity decodes one [hdr, idx, lo, hi] record: value = lo + 256×hi.
func decodeIntensity(rec []byte) int {
	return int(rec[2]) + 256*int(rec[3])
}

// parseIntensityArray validates and decodes the 80-byte reply of 79 1 0 into 20
// intensities. Every record header must be 105 and the index must run 1..20;
// otherwise a button session interleaved and the read is unusable.
func parseIntensityArray(buf []byte) ([20]int, *device.CmdError) {
	var out [20]int
	if len(buf) != 80 {
		return out, device.ErrHardware(
			fmt.Sprintf("intensity array: got %d bytes, want 80", len(buf)))
	}
	for k := 0; k < 20; k++ {
		rec := buf[4*k : 4*k+4]
		if rec[0] != 105 || int(rec[1]) != k+1 {
			return out, device.ErrHardware(
				"intensity array: record header/index mismatch (button interference?)")
		}
		out[k] = decodeIntensity(rec)
	}
	return out, nil
}

// leastSquaresSlope fits a line through (index, intensity) for points with
// 0 < v ≤ 3000 (the firmware's own filter). Fewer than 3 usable points means
// the detector is dark or saturated.
func leastSquaresSlope(intensities [20]int) (float64, *device.CmdError) {
	var n, sx, sy, sxx, sxy float64
	for idx, v := range intensities {
		if v <= 0 || v > 3000 {
			continue
		}
		x := float64(idx + 1)
		y := float64(v)
		n++
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	if n < 3 {
		return 0, device.ErrHardware("sweep unusable: detector dark or saturated")
	}
	denom := n*sxx - sx*sx
	if denom == 0 {
		return 0, device.ErrHardware("sweep unusable: degenerate brightness points")
	}
	return (n*sxy - sx*sy) / denom, nil
}

// absorbance computes the temperature-compensated, tube-corrected absorbance
// and the raw (pre-compensation, pre-correction) value. 0.0022/°C is the
// firmware's own compensation coefficient (TRANSLATION §4 measure step 5).
func absorbance(blankSlope, sampleSlope, tempC, blankTempC, tubeCorrection float64) (final, raw float64) {
	raw = math.Abs(math.Log10(blankSlope / sampleSlope))
	final = (raw + (tempC-blankTempC)*0.0022) * tubeCorrection
	return final, raw
}

// formatSerial renders the compile-time serial (71 0 0 5 → sn1, sn2) as
// "<year>-<unit>" zero-padded to match the JSON identify example ("25-006").
func formatSerial(sn1, sn2 byte) string {
	return fmt.Sprintf("%d-%03d", sn1, sn2)
}
