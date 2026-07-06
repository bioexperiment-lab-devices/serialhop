package valve

import (
	"testing"
	"time"
)

func TestMod(t *testing.T) {
	cases := []struct{ x, size, want int }{
		{3, 7, 3}, {-3, 7, 4}, {7, 7, 0}, {-7, 7, 0}, {13, 7, 6}, {0, 7, 0},
	}
	for _, c := range cases {
		if got := mod(c.x, c.size); got != c.want {
			t.Errorf("mod(%d,%d) = %d, want %d", c.x, c.size, got, c.want)
		}
	}
}

func TestRotationCode(t *testing.T) {
	for mode, want := range map[string]byte{"direct": 1, "wrap": 2, "shortest": 3} {
		code, ok := rotationCode(mode)
		if !ok || code != want {
			t.Errorf("rotationCode(%q) = %d %v", mode, code, ok)
		}
	}
	for _, bad := range []string{"", "spiral", "Shortest"} {
		if _, ok := rotationCode(bad); ok {
			t.Errorf("rotationCode(%q) must be rejected", bad)
		}
	}
}

// All cases use S = 7 (the 6-output build: rotor detents 0..6).
// delta = (target − physical) mod S; targetDevice = (belief + delta) mod S;
// d = targetDevice − belief (signed). Slots/direction mirror the firmware:
// direct |d| (increasing iff d > 0), wrap S−|d| (increasing iff d < 0),
// shortest picks the smaller arc.
func TestPlanMove(t *testing.T) {
	cases := []struct {
		name                     string
		target, physical, belief int
		mode                     string
		wantDevice, wantSlots    int
		wantDir                  string
	}{
		// zero offset (belief == physical)
		{"direct zero-offset", 4, 2, 2, "direct", 4, 2, "increasing"},
		{"wrap zero-offset", 4, 2, 2, "wrap", 4, 5, "decreasing"},
		{"shortest near arc", 4, 2, 2, "shortest", 4, 2, "increasing"},
		// nonzero offset: physical 4 sits at device 0 → delta 4, d = 4,
		// shortest takes the complementary arc (3 slots, decreasing)
		{"shortest far arc", 1, 4, 0, "shortest", 4, 3, "decreasing"},
		// transit-path gap illustration: physical 5→0 crosses the 6↔0
		// boundary, but the device frame moves 1→3 without crossing it
		{"direct offset boundary", 0, 5, 1, "direct", 3, 2, "increasing"},
		// negative device-frame difference: delta 5, td 3, d = −2
		{"direct negative d", 5, 0, 5, "direct", 3, 2, "decreasing"},
		{"wrap negative d", 5, 0, 5, "wrap", 3, 5, "increasing"},
		{"shortest negative d", 5, 0, 5, "shortest", 3, 2, "decreasing"},
		// the device target itself wraps mod S: delta 6, td (6+6) mod 7 = 5
		{"target wraps mod S", 6, 0, 6, "shortest", 5, 1, "decreasing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planMove(c.target, c.physical, c.belief, 7, c.mode)
			if got.targetDevice != c.wantDevice || got.slots != c.wantSlots ||
				got.direction != c.wantDir {
				t.Fatalf("planMove = %+v", got)
			}
			want := time.Duration(c.wantSlots)*SlotDuration + MoveMargin
			if got.estimate != want {
				t.Fatalf("estimate = %v, want %v", got.estimate, want)
			}
		})
	}
}
