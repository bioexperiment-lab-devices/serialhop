package valve_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// TestMoveReadbackMismatchUnhomesAndFails: mid-move reboot — the readback
// reports 0 instead of the target → job failed, valve unhomed, RAM config
// re-pushed (TRANSLATION §4 step 10 mismatch + §2 reboot signature).
func TestMoveReadbackMismatchUnhomesAndFails(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	id := startMove(t, f, `{"position":2}`) // shortest: 2 slots → 2.14 s
	f.port.Feed([]byte{30, 1, 1, 0})        // readback: counter reset mid-move
	f.clock.Advance(2140 * time.Millisecond)
	waitFor(t, "job failure", func() bool {
		return jobState(t, f, id)["state"] == "failed"
	})
	em := jobState(t, f, id)["error"].(map[string]any)
	if em["code"] != "hardware_error" {
		t.Fatalf("job error: %v", em)
	}
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 35, 1, 3, 0, 0) || !frameEq(fr[n-1], 35, 2, 1, 0, 0) {
		t.Fatalf("config not re-pushed after mid-move reboot: %v", fr)
	}
	f.port.Feed([]byte{30, 1, 1, 0})
	sm := f.resultMap(f.exec("status", ""))
	if sm["state"] != "unhomed" || sm["position"] != nil {
		t.Fatalf("status: %v", sm)
	}
}

// TestUnreachableMidMoveRefusesStaleRecovery: the post-move readback gets
// no reply → session unreachable, job failed; on reattach the persisted
// belief (from home time) no longer matches the counter → recovery refused.
func TestUnreachableMidMoveRefusesStaleRecovery(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	id := startMove(t, f, `{"position":2}`)
	// no readback reply fed: the verification transaction double-fails
	f.clock.Advance(2140 * time.Millisecond)
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })
	f.port.Feed([]byte{30, 1, 1, 2}) // reattach's position-query reply
	f.clock.Advance(device.ReattachBase)
	waitFor(t, "reattach", f.s.Connected)
	if js := jobState(t, f, id); js["state"] != "failed" {
		t.Fatalf("job after unreachable: %v", js)
	}
	f.port.Feed([]byte{30, 1, 1, 2})
	sm := f.resultMap(f.exec("status", ""))
	if sm["state"] != "unhomed" {
		t.Fatalf("stale recovery must be refused: %v", sm)
	}
}

// TestPingFailureMidMoveFailsJob: any transaction double-failure while a
// move is in flight voids position knowledge (TRANSLATION §5). Ping is the
// only reply-expecting command allowed mid-move.
func TestPingFailureMidMoveFailsJob(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	id := startMove(t, f, `{"position":3}`)
	resp := f.exec("ping", "") // no reply fed
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("ping: %+v", resp)
	}
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })
	f.port.Feed([]byte{30, 1, 1, 3})
	f.clock.Advance(device.ReattachBase)
	waitFor(t, "reattach", f.s.Connected)
	if js := jobState(t, f, id); js["state"] != "failed" {
		t.Fatalf("job: %v", js)
	}
}

// TestPingDuringMoveDoesNotFeedBelief: a mid-move reply reflects the target
// the firmware already counts from — even a 0 (reboot-looking) reply must
// not trigger belief handling while a move is in flight; the post-move
// readback is the arbiter.
func TestPingDuringMoveDoesNotFeedBelief(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	startMove(t, f, `{"position":3}`)
	f.port.Feed([]byte{30, 1, 1, 0})
	if resp := f.exec("ping", ""); resp.Status != "ok" {
		t.Fatalf("ping: %+v", resp)
	}
	if countFrames(f.frames(), 35) != 2 { // attach's config push only
		t.Fatal("mid-move ping must not trigger belief recovery")
	}
}

// TestDetachMidMovePersistsSettledOutcome: the firmware finishes an
// accepted move autonomously — Detach persists the settled target, with no
// serial I/O.
func TestDetachMidMovePersistsSettledOutcome(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	startMove(t, f, `{"position":3}`)
	n := len(f.port.Written())
	f.s.Close()
	if len(f.port.Written()) != n {
		t.Fatal("Detach must not write to the serial port")
	}
	ps := readState(t, f.dir)
	if ps["physical_position"] != 3.0 || ps["device_belief_at_shutdown"] != 3.0 {
		t.Fatalf("persisted: %v", ps)
	}
}

// TestDetachMidMoveToDeviceZeroPersistsUnhomed: a move targeting
// device-frame 0 is the one case where a restart cannot distinguish
// "completed" (counter 0) from "valve power-cycled mid-move" (counter
// reset to 0) — both pass the recovery check — so Detach persists
// unhomed rather than an optimistic, possibly wrong position.
func TestDetachMidMoveToDeviceZeroPersistsUnhomed(t *testing.T) {
	f := newFixture(t, 2)            // attach: device counter 2
	f.port.Feed([]byte{30, 1, 1, 2}) // home's belief resync
	if resp := f.exec("home", `{"position":3}`); resp.Status != "ok" {
		t.Fatalf("home: %+v", resp)
	}
	f.port.Feed([]byte{30, 1, 1, 2})  // pre-move CHECK_BELIEF
	startMove(t, f, `{"position":1}`) // delta (1−3) mod 7 = 5 → device target (2+5) mod 7 = 0
	fr := f.frames()
	if !frameEq(fr[len(fr)-1], 36, 1, 0, 0, 0) {
		t.Fatalf("move frame must target device 0: %v", fr)
	}
	f.s.Close()
	ps := readState(t, f.dir)
	if ps["physical_position"] != nil || ps["device_belief_at_shutdown"] != 0.0 {
		t.Fatalf("device-frame-0 target must persist unhomed: %v", ps)
	}
}
