package valve

import "time"

// Timing knobs are vars so tests can shrink them (core precedent:
// PerByteTimeout / DrainWindow).
var (
	// SlotDuration is the rotor travel time per adjacent position: 460
	// step-pin toggles × 2000 µs (PROTOCOL.md §4 cmd 36; TRANSLATION.md §1
	// SECONDS_PER_SLOT = 0.92 s).
	SlotDuration = 920 * time.Millisecond
	// MoveMargin pads the clock-simulated completion estimate
	// (TRANSLATION.md §4 set_position step 8).
	MoveMargin = 300 * time.Millisecond
)

// rotationCode maps a JSON rotation mode to the firmware's 35-1-R code
// (PROTOCOL.md §4 cmd 35): direct=1, wrap=2, shortest=3.
func rotationCode(mode string) (byte, bool) {
	switch mode {
	case "direct":
		return 1, true
	case "wrap":
		return 2, true
	case "shortest":
		return 3, true
	}
	return 0, false
}

// mod returns x modulo size, always in [0, size).
func mod(x, size int) int { return ((x % size) + size) % size }

// movePlan is the resolved device-frame plan for one move.
type movePlan struct {
	targetDevice int
	slots        int
	direction    string // "increasing" | "decreasing"
	estimate     time.Duration
}

// planMove implements TRANSLATION.md §4 set_position steps 5–6: translate
// the physical target through the virtual-homing offset and mirror the
// firmware's arc arithmetic for the duration estimate.
//
// Correctness: every firmware mode moves the rotor by a step count
// CONGRUENT to (targetDevice − belief) mod size, so the final position is
// always right.
//
// Transit-path gap (direct/wrap modes only, documented hardware
// limitation): every port the rotor transits is momentarily opened, so the
// *path* can matter to the plumbing, not just the destination. Direct and
// wrap choose their arc from the SIGNED device-frame difference; with a
// nonzero virtual-homing offset that arc can differ from what the physical
// position numbers suggest (e.g. a physical 2→4 move may travel the long
// way around through 0). The offset never changes on its own — it is fixed
// at home time and only a device reboot disturbs it. Mitigation for
// path-sensitive installations: establish a ZERO offset — bring the rotor
// physically to position 0, power-cycle the valve (device belief resets to
// 0), then home {position: 0}. Shortest mode is frame-invariant.
func planMove(target, physical, belief, size int, mode string) movePlan {
	delta := mod(target-physical, size)
	targetDevice := mod(belief+delta, size)
	d := targetDevice - belief // signed, in −(size−1)..(size−1); never 0 (Δ=0 is guarded upstream)
	abs := d
	if abs < 0 {
		abs = -abs
	}
	var slots int
	var increasing bool
	switch mode {
	case "direct":
		slots, increasing = abs, d > 0
	case "wrap":
		slots, increasing = size-abs, d < 0
	default: // shortest — the firmware's default. On an equal-arc tie the
		// firmware's pick is unspecified; mirror its direct arc. The
		// duration is identical either way, only the reported direction
		// could differ.
		if abs <= size-abs {
			slots, increasing = abs, d > 0
		} else {
			slots, increasing = size-abs, d < 0
		}
	}
	dir := "decreasing"
	if increasing {
		dir = "increasing"
	}
	return movePlan{
		targetDevice: targetDevice,
		slots:        slots,
		direction:    dir,
		estimate:     time.Duration(slots)*SlotDuration + MoveMargin,
	}
}
