package valve_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device/valve"
)

// countFrames counts written 5-byte frames with the given command byte.
func countFrames(fr [][]byte, cmd byte) int {
	n := 0
	for _, f := range fr {
		if len(f) == 5 && f[0] == cmd {
			n++
		}
	}
	return n
}

func TestStatusUnhomedFresh(t *testing.T) {
	f := newFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0}) // idle status runs CHECK_BELIEF
	m := f.resultMap(f.exec("status", ""))
	if m["state"] != "unhomed" || m["homed"] != false || m["position"] != nil ||
		m["target_position"] != nil || m["job"] != nil {
		t.Fatalf("status: %v", m)
	}
	cfg := m["config"].(map[string]any)
	if cfg["default_rotation"] != "shortest" || cfg["hold_torque"] != false {
		t.Fatalf("config: %v", cfg)
	}
	fr := f.frames()
	if !frameEq(fr[len(fr)-1], 33, 1, 0, 0, 0) {
		t.Fatalf("idle status must run CHECK_BELIEF: %v", fr)
	}
}

// TestBeliefRebootAutoRecovery: pos==0 while belief≠0 and no move in flight
// is the reboot signature — belief resets to 0 and the RAM-only config is
// re-pushed; no alarm (TRANSLATION §2 step 3, recovery branch).
func TestBeliefRebootAutoRecovery(t *testing.T) {
	f := newFixture(t, 3)            // attach: belief = 3
	f.port.Feed([]byte{30, 1, 1, 0}) // reboot: counter reset to 0
	if resp := f.exec("status", ""); resp.Status != "ok" {
		t.Fatalf("status: %+v", resp)
	}
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 35, 1, 3, 0, 0) || !frameEq(fr[n-1], 35, 2, 1, 0, 0) {
		t.Fatalf("reboot recovery must re-push the config mirror: %v", fr)
	}
	// belief resynced to 0: a second consistent read stays quiet
	f.port.Feed([]byte{30, 1, 1, 0})
	f.exec("status", "")
	if got := countFrames(f.frames(), 35); got != 4 { // 2 at attach + 2 at recovery
		t.Fatalf("no further config pushes expected, got %d", got)
	}
}

// TestBeliefForeignMismatchAlarms: pos ≠ 0 and ≠ belief means a lost
// command or a foreign host — no config re-push, belief resyncs to reality
// (TRANSLATION §2 step 4).
func TestBeliefForeignMismatchAlarms(t *testing.T) {
	f := newFixture(t, 3)
	f.port.Feed([]byte{30, 1, 1, 5})
	f.exec("status", "")
	if got := countFrames(f.frames(), 35); got != 2 { // attach only
		t.Fatalf("mismatch must not re-push config, got %d frames", got)
	}
	f.port.Feed([]byte{30, 1, 1, 5}) // belief resynced: now consistent
	f.exec("status", "")
	if got := countFrames(f.frames(), 35); got != 2 {
		t.Fatalf("resynced belief must stay quiet, got %d frames", got)
	}
}

func TestPingReportsUptime(t *testing.T) {
	f := newFixture(t, 0)
	f.clock.Advance(5 * time.Second) // one heartbeat fires; CheckInterval (30 s) not due
	f.port.Feed([]byte{30, 1, 1, 0})
	resp := f.exec("ping", "")
	if resp.Status != "ok" {
		t.Fatalf("ping: %+v", resp)
	}
	if m := f.resultMap(resp); m["uptime_ms"] != 5000.0 {
		t.Fatalf("uptime: %v", m)
	}
	fr := f.frames()
	if !frameEq(fr[len(fr)-1], 31, 2, 3, 4, 5) {
		t.Fatalf("ping frame: %v", fr)
	}
}

// TestPingFeedsBelief: ping's reply position is fed into the CHECK_BELIEF
// logic opportunistically (TRANSLATION §4 ping).
func TestPingFeedsBelief(t *testing.T) {
	f := newFixture(t, 3)
	f.port.Feed([]byte{30, 1, 1, 0}) // reboot signature via a ping reply
	if resp := f.exec("ping", ""); resp.Status != "ok" {
		t.Fatalf("ping: %+v", resp)
	}
	if got := countFrames(f.frames(), 35); got != 4 { // attach + recovery re-push
		t.Fatalf("ping must trigger reboot recovery, got %d config frames", got)
	}
}

// TestTickRunsIdleCheckBelief: Tick runs CHECK_BELIEF only once
// CheckInterval has elapsed. Early ticks must stay serial-silent — a
// premature CHECK_BELIEF would find an empty RX buffer, double-fail, and
// flip the session unreachable, which the final asserts catch loudly.
func TestTickRunsIdleCheckBelief(t *testing.T) {
	old := valve.CheckInterval
	valve.CheckInterval = 3 * time.Second
	t.Cleanup(func() { valve.CheckInterval = old })
	f := newFixture(t, 0)
	f.clock.Advance(time.Second) // tick at +1 s: below interval
	time.Sleep(10 * time.Millisecond)
	f.clock.Advance(time.Second) // tick at +2 s: below interval
	time.Sleep(10 * time.Millisecond)
	f.port.Feed([]byte{30, 1, 1, 0})
	f.clock.Advance(time.Second) // tick at ≥ +3 s: CHECK_BELIEF due
	waitFor(t, "idle CHECK_BELIEF", func() bool {
		return countFrames(f.frames(), 33) >= 2 // attach's + the tick's
	})
	if got := countFrames(f.frames(), 33); got != 2 {
		t.Fatalf("expected exactly one idle check, got %d queries", got)
	}
	if !f.s.Connected() {
		t.Fatal("session must stay connected (no premature CHECK_BELIEF)")
	}
}
