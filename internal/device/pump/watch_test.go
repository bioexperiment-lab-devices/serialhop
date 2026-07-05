package pump_test

import (
	"testing"
	"time"
)

// TestDispenseForwardUsesOpcode18AndMeasuredDuration: plain forward dispense
// must be issued as opcode 18 and complete on the device's elapsed-µs reply
// (a real hardware completion), not the clock.
func TestDispenseForwardUsesOpcode18AndMeasuredDuration(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	fr := f.frames()
	if last := fr[len(fr)-1]; last[0] != 18 || !frameEq(last, 18, 0, 0, 7, 208) {
		t.Fatalf("plain forward dispense must use opcode 18: %v", last)
	}

	// Completion reply: 19,400,000 µs = 0x01280540, then the disarm ping reply.
	f.port.Feed([]byte{0x01, 0x28, 0x05, 0x40})
	f.port.Feed([]byte{10, 26, 25, 1})
	waitFor(t, "hardware completion", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
	res := jobState(t, f, id)["result"].(map[string]any)
	if res["duration_s"] != 19.4 { // measured, not the 20 s estimate
		t.Fatalf("duration_s = %v, want 19.4 (measured)", res["duration_s"])
	}
	if f.resultMap(f.exec("status", ""))["state"] != "idle" {
		t.Fatal("must return to idle")
	}
	// reader must be released: an idle ping (reply-expecting) works
	f.port.Feed([]byte{10, 0, 0, 0})
	if resp := f.exec("ping", ""); resp.Status != "ok" {
		t.Fatalf("ping after completion: %+v", resp)
	}
}

// TestWatcherBlocksReplyExpectingTraffic: while the opcode-18 reply is
// pending, a reply-expecting command sneaking onto the wire would interleave
// with the completion reply — memory-served ping must NOT touch the port.
func TestWatcherServesPingFromMemoryMidJob(t *testing.T) {
	f := newCalibratedFixture(t)
	startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	before := len(f.frames())
	resp := f.exec("ping", "")
	if resp.Status != "ok" {
		t.Fatalf("mid-job ping: %+v", resp)
	}
	if len(f.frames()) != before {
		t.Fatalf("mid-job ping must not touch the serial port: %v", f.frames()[before:])
	}
}

// TestWatchdogTimeoutFailsJob: no completion reply ever arrives (e.g. panel
// STOP silently halted the run) → after estimate×1.5 + 5 s of active time
// the watchdog abandons the wait and fails the job; the disarm ping still
// runs afterwards.
func TestWatchdogTimeoutFailsJob(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.clock.Advance(35 * time.Second) // 20 × 1.5 + 5
	time.Sleep(20 * time.Millisecond)
	f.port.Feed([]byte{10, 26, 25, 1}) // the post-timeout disarm ping reply
	waitFor(t, "watchdog failure", func() bool {
		return jobState(t, f, id)["state"] == "failed"
	})
	js := jobState(t, f, id)
	errObj := js["error"].(map[string]any)
	if errObj["code"] != "hardware_error" {
		t.Fatalf("job error: %v", js)
	}
	if f.resultMap(f.exec("status", ""))["state"] != "idle" {
		t.Fatal("must return to idle after watchdog failure")
	}
	// session must still be reachable (device may be fine; only the run
	// outcome is unknown) and the reader released
	if !f.s.Connected() {
		t.Fatal("watchdog timeout must not flip the session unreachable")
	}
	f.port.Feed([]byte{10, 0, 0, 0})
	if resp := f.exec("ping", ""); resp.Status != "ok" {
		t.Fatalf("ping after watchdog: %+v", resp)
	}
}

// TestWatchdogExtendedByPause: paused time must not count against the
// watchdog budget (TRANSLATION §4 dispense step 9).
func TestWatchdogExtendedByPause(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.exec("pause", "")
	f.clock.Advance(2 * time.Minute) // paused: frozen clock, watchdog re-arms
	if js := jobState(t, f, id); js["state"] != "paused" {
		t.Fatalf("job must survive a long pause: %v", js)
	}
	f.exec("resume", "")
	// complete normally after resume
	f.port.Feed([]byte{0x01, 0x28, 0x05, 0x40})
	f.port.Feed([]byte{10, 26, 25, 1})
	waitFor(t, "completion after pause", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
}

// TestPauseDuringWatcherJobUsesWriteOnlyPath: cmd 19 while the reader is
// held must go out as a bare write (no drain that would eat the pending
// completion reply). The completion reply fed BEFORE the pause frame is
// still consumed correctly afterwards.
func TestPauseDuringWatcherJobUsesWriteOnlyPath(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	if resp := f.exec("pause", ""); resp.Status != "ok" {
		t.Fatalf("pause during opcode-18 job: %+v", resp)
	}
	if resp := f.exec("resume", ""); resp.Status != "ok" {
		t.Fatalf("resume: %+v", resp)
	}
	f.port.Feed([]byte{0x01, 0x28, 0x05, 0x40})
	f.port.Feed([]byte{10, 26, 25, 1})
	waitFor(t, "completion", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
}

// TestWatcherToleratesPortDeathMidJob (decisions 1+2): the port dies while
// the watcher blocks on the completion reply. The watcher — which captured
// the port value on the loop, never via Conn() from its own goroutine —
// unblocks with ErrClosed and its posted event fails the job cleanly (no
// panic, no double state change). The session itself stays connected: no
// Transact failed, and recovery belongs to the unreachable machinery
// whenever the next command touches the port.
func TestWatcherToleratesPortDeathMidJob(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	_ = f.port.Close() // port dies under the watcher
	waitFor(t, "job failed by watcher death", func() bool {
		return jobState(t, f, id)["state"] == "failed"
	})
	if st := f.resultMap(f.exec("status", ""))["state"]; st != "idle" {
		t.Fatalf("driver must return to idle after watcher death: %v", st)
	}
}
