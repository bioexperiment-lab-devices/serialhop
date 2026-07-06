package densitometer

import (
	"math"
	"testing"
)

func TestDecodeFixedPoint(t *testing.T) {
	if got := decodeFixedPoint([]byte{5, 5, 27, 45}); math.Abs(got-27.45) > 1e-9 {
		t.Fatalf("decodeFixedPoint = %v, want 27.45", got)
	}
	if got := decodeFixedPoint([]byte{70, 5, 10, 0}); got != 10.0 {
		t.Fatalf("decodeFixedPoint = %v, want 10.0", got)
	}
}

func TestDecodeIntensity(t *testing.T) {
	// value 300 → lo=44 hi=1 (44 + 256)
	if got := decodeIntensity([]byte{105, 3, 44, 1}); got != 300 {
		t.Fatalf("decodeIntensity = %d, want 300", got)
	}
}

// buildArray renders a 20-record intensity array with value(i)=fn(i), i=1..20.
func buildArray(fn func(i int) int) []byte {
	buf := make([]byte, 0, 80)
	for i := 1; i <= 20; i++ {
		v := fn(i)
		buf = append(buf, 105, byte(i), byte(v%256), byte(v/256)) // #nosec G115 -- test data; i and derived low/high bytes are bounded to 0..255 by construction
	}
	return buf
}

func TestParseIntensityArray(t *testing.T) {
	got, cerr := parseIntensityArray(buildArray(func(i int) int { return 100 * i }))
	if cerr != nil {
		t.Fatal(cerr)
	}
	for i := 0; i < 20; i++ {
		if got[i] != 100*(i+1) {
			t.Fatalf("intensities[%d] = %d, want %d", i, got[i], 100*(i+1))
		}
	}
}

func TestParseIntensityArrayRejectsBadHeader(t *testing.T) {
	bad := buildArray(func(i int) int { return 100 * i })
	bad[4*7] = 99 // corrupt the 8th record header (button-session interleave)
	if _, cerr := parseIntensityArray(bad); cerr == nil || cerr.Code != "hardware_error" {
		t.Fatalf("want hardware_error, got %v", cerr)
	}
	if _, cerr := parseIntensityArray([]byte{105, 1, 0, 0}); cerr == nil {
		t.Fatal("short buffer must error")
	}
}

func TestLeastSquaresSlope(t *testing.T) {
	// perfect line v=100*i → slope 100
	slope, cerr := leastSquaresSlope(arr(func(i int) int { return 100 * i }))
	if cerr != nil {
		t.Fatal(cerr)
	}
	if math.Abs(slope-100) > 1e-6 {
		t.Fatalf("slope = %v, want 100", slope)
	}
}

func TestLeastSquaresSlopeFiltersRange(t *testing.T) {
	// all zero → 0 usable points → hardware_error
	if _, cerr := leastSquaresSlope([20]int{}); cerr == nil || cerr.Code != "hardware_error" {
		t.Fatalf("dark detector must be hardware_error, got %v", cerr)
	}
	// all saturated (>3000) → filtered out → hardware_error
	var sat [20]int
	for i := range sat {
		sat[i] = 5000
	}
	if _, cerr := leastSquaresSlope(sat); cerr == nil {
		t.Fatal("saturated detector must be hardware_error")
	}
}

// arr adapts buildArray's generator into the [20]int the slope fn takes.
func arr(fn func(i int) int) [20]int {
	var out [20]int
	for i := 1; i <= 20; i++ {
		out[i-1] = fn(i)
	}
	return out
}

func TestAbsorbance(t *testing.T) {
	// blank 100, sample 50, no temp delta, tube 1.0 → |log10(2)| = 0.30103
	final, raw := absorbance(100, 50, 27.45, 27.45, 1.0)
	if math.Abs(raw-0.30103) > 1e-4 || math.Abs(final-0.30103) > 1e-4 {
		t.Fatalf("absorbance final=%v raw=%v, want ~0.30103", final, raw)
	}
	// +10 °C over blank → +0.022 compensation; tube 2.0 doubles it
	final, raw = absorbance(100, 50, 37.45, 27.45, 2.0)
	wantFinal := (0.30103 + 0.022) * 2.0
	if math.Abs(final-wantFinal) > 1e-3 {
		t.Fatalf("compensated final=%v, want ~%v (raw=%v)", final, wantFinal, raw)
	}
}

func TestFormatSerial(t *testing.T) {
	if got := formatSerial(25, 6); got != "25-006" {
		t.Fatalf("formatSerial = %q, want 25-006", got)
	}
}
