package pump

import (
	"math"
	"testing"
	"time"
)

// mlPerStep = 0.0005 gives round numbers: 3 ml/min → 100 steps/s → 5000 µs.
const testCal = 0.0005

func TestSpeedToBytes(t *testing.T) {
	cases := []struct {
		name    string
		speed   float64
		n3, n4  byte
		actual  float64
		wantErr bool
	}{
		{name: "exact", speed: 3.0, n3: 1, n4: 50, actual: 5000},
		// 2.9 ml/min → delUs 5172.4 → P=52 → 5200 µs (quantized)
		{name: "quantized", speed: 2.9, n3: 1, n4: 52, actual: 5200},
		// > 30e6×0.0005/400 = 37.5 ml/min busts MinDelTimeUs
		{name: "too fast", speed: 40, wantErr: true},
		// < 30e6×0.0005/6502500 ≈ 0.0023 ml/min busts maxDelTimeUs
		{name: "too slow", speed: 0.001, wantErr: true},
		{name: "zero", speed: 0, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n3, n4, actual, cerr := speedToBytes(c.speed, testCal)
			if c.wantErr {
				if cerr == nil {
					t.Fatalf("want invalid_params, got n3=%d n4=%d", n3, n4)
				}
				if cerr.Code != "invalid_params" {
					t.Fatalf("code = %s", cerr.Code)
				}
				return
			}
			if cerr != nil {
				t.Fatal(cerr)
			}
			if n3 != c.n3 || n4 != c.n4 || actual != c.actual {
				t.Fatalf("got n3=%d n4=%d actual=%v, want n3=%d n4=%d actual=%v",
					n3, n4, actual, c.n3, c.n4, c.actual)
			}
		})
	}
}

func TestSpeedToBytesLargePFactorizes(t *testing.T) {
	// 0.005 ml/min → steps/s = 1/6 → delUs = 3e6 → P = 30000 → n3 must
	// exceed 1 and both bytes stay in 1..255.
	n3, n4, actual, cerr := speedToBytes(0.005, testCal)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if n3 < 1 || n4 < 1 {
		t.Fatalf("bytes out of range: n3=%d n4=%d", n3, n4)
	}
	p := int(math.Round(3e6 / 100))
	if got := int(n3) * int(n4); got < p-int(n3) || got > p+int(n3) {
		t.Fatalf("n3×n4 = %d too far from P = %d", got, p)
	}
	if actual != float64(int(n3)*int(n4)*100) {
		t.Fatalf("actual %v != n3×n4×100", actual)
	}
}

func TestActualSpeedRoundTrip(t *testing.T) {
	if got := actualSpeedMlMin(testCal, 5000); got != 3.0 {
		t.Fatalf("actualSpeedMlMin = %v, want 3.0", got)
	}
}

func TestVolumeToSteps(t *testing.T) {
	steps, cerr := volumeToSteps(1.0, testCal)
	if cerr != nil || steps != 2000 {
		t.Fatalf("steps=%d cerr=%v", steps, cerr)
	}
	if _, cerr := volumeToSteps(0.0001, testCal); cerr == nil {
		t.Fatal("sub-step volume must be invalid_params") // rounds to 0 < 1
	}
	if _, cerr := volumeToSteps(2e6, testCal); cerr == nil {
		t.Fatal("steps > 2e9 must be invalid_params") // 4e9 steps
	}
}

func TestRawDelTime(t *testing.T) {
	// 50% → 200 µs, clamped up to MinDelTimeUs (400)
	if got := rawDelTimeUs(50); got != 400 {
		t.Fatalf("50%% = %v, want 400", got)
	}
	// 1% → 10000 µs
	if got := rawDelTimeUs(1); got != 10000 {
		t.Fatalf("1%% = %v, want 10000", got)
	}
}

func TestBe32(t *testing.T) {
	b := be32(2000)
	if b[0] != 0 || b[1] != 0 || b[2] != 7 || b[3] != 208 {
		t.Fatalf("be32(2000) = %v", b)
	}
}

func TestQuantizeSuckback(t *testing.T) {
	// drop unit = 100 × 0.0005 = 0.05 ml. 0.12 ml → round(2.4) = 2 units.
	mult, actual := quantizeSuckback(0.12, testCal)
	if mult != 2 || actual != 0.1 {
		t.Fatalf("got mult=%d actual=%v", mult, actual)
	}
	// below the 2-unit floor: 0.05 ml → round(1) = 1 → clamped to 2.
	mult, actual = quantizeSuckback(0.05, testCal)
	if mult != 2 || actual != 0.1 {
		t.Fatalf("floor: mult=%d actual=%v", mult, actual)
	}
	// ceiling 255
	mult, _ = quantizeSuckback(100, testCal)
	if mult != 255 {
		t.Fatalf("ceiling: mult=%d", mult)
	}
}

func TestEstimates(t *testing.T) {
	// plain: 2000 steps × 2 × 5000 µs = 20 s
	if got := plainEstimate(2000, 5000); got != 20*time.Second {
		t.Fatalf("plain = %v", got)
	}
	// suckback: (2×2200 + 400×2) × 5000 µs + 0.1 s = 26.1 s
	if got := suckbackEstimate(2200, 2, 5000); got != 26100*time.Millisecond {
		t.Fatalf("suckback = %v", got)
	}
}

// TestGradientEstimate cross-checks the closed-form integral against the
// firmware ramp summed toggle-by-toggle (an independent computation).
func TestGradientEstimate(t *testing.T) {
	steps := int64(1000)
	kmax := 2 * steps
	// firmware ramp: half-period(k) = 1/sqrt(A + B·k), T(1)=30000, T(kmax)=300
	b := (1/(300.0*300.0) - 1/(30000.0*30000.0)) / float64(kmax-1)
	a := 1/(30000.0*30000.0) - b
	var sumUs float64
	for k := int64(1); k <= kmax; k++ {
		sumUs += 1 / math.Sqrt(a+b*float64(k))
	}
	want := time.Duration(sumUs) * time.Microsecond
	got := gradientEstimate(steps)
	diff := math.Abs(float64(got-want)) / float64(want)
	if diff > 0.02 {
		t.Fatalf("gradientEstimate = %v, brute-force = %v (%.1f%% off)", got, want, diff*100)
	}
}
