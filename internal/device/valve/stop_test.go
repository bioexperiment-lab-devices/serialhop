package valve_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// stopAsync issues stop on a goroutine and advances the fake clock in
// slices until it returns. The slices (200 ms) are far below the move
// estimate, so the stop command reaches the loop and registers its
// Sleep(remaining) long before the cumulative advance could overshoot the
// estimate and let the completion timer settle the job first.
func stopAsync(t *testing.T, f *fixture) device.Response {
	t.Helper()
	respCh := make(chan device.Response, 1)
	go func() { respCh <- f.exec("stop", "") }()
	var resp device.Response
	waitFor(t, "stop returns", func() bool {
		f.clock.Advance(200 * time.Millisecond)
		select {
		case resp = <-respCh:
			return true
		default:
			return false
		}
	})
	return resp
}

// TestStopWaitsOutMoveAndPreservesPosition — the documented spec deviation:
// the firmware cannot abort, so stop waits out the move (blocking the
// session loop), verifies, keeps position knowledge, and marks the job
// cancelled to record intent.
func TestStopWaitsOutMoveAndPreservesPosition(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	id := startMove(t, f, `{"position":1,"rotation":"wrap"}`) // 6 slots → 5.82 s
	f.port.Feed([]byte{30, 1, 1, 1})                          // stop's settle readback
	resp := stopAsync(t, f)
	if resp.Status != "ok" {
		t.Fatalf("stop: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["state"] != "idle" || m["cancelled_job_id"] != id {
		t.Fatalf("stop result: %v", m)
	}
	if js := jobState(t, f, id); js["state"] != "cancelled" {
		t.Fatalf("job: %v", js)
	}
	ps := readState(t, f.dir)
	if ps["physical_position"] != 1.0 {
		t.Fatalf("position knowledge must be preserved: %v", ps)
	}
	f.port.Feed([]byte{30, 1, 1, 1})
	sm := f.resultMap(f.exec("status", ""))
	if sm["state"] != "idle" || sm["position"] != 1.0 {
		t.Fatalf("status after stop: %v", sm)
	}
	if countFrames(f.frames(), 36) != 1 {
		t.Fatal("stop must not send any motion/abort frame")
	}
}

func TestStopIdleIsNoop(t *testing.T) {
	f := newFixture(t, 0) // unhomed, idle
	n := len(f.port.Written())
	m := f.resultMap(f.exec("stop", ""))
	if m["state"] != "unhomed" {
		t.Fatalf("unhomed idle stop: %v", m)
	}
	if _, ok := m["cancelled_job_id"]; ok {
		t.Fatalf("nothing to cancel: %v", m)
	}
	if len(f.port.Written()) != n {
		t.Fatal("idle stop must be serial-silent")
	}

	f2 := newHomedFixture(t, 2)
	if m2 := f2.resultMap(f2.exec("stop", "")); m2["state"] != "idle" {
		t.Fatalf("homed idle stop: %v", m2)
	}
}

// TestStopVerificationMismatchUnhomes: the settle readback finds a rebooted
// device → same handling as set_position step 10 mismatch (job failed,
// unhomed), surfaced as a hardware_error response.
func TestStopVerificationMismatchUnhomes(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	id := startMove(t, f, `{"position":2}`) // 2 slots → 2.14 s
	f.port.Feed([]byte{30, 1, 1, 0})        // readback: reboot signature
	resp := stopAsync(t, f)
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("stop: %+v", resp)
	}
	if js := jobState(t, f, id); js["state"] != "failed" {
		t.Fatalf("job: %v", js)
	}
	f.port.Feed([]byte{30, 1, 1, 0})
	if sm := f.resultMap(f.exec("status", "")); sm["state"] != "unhomed" {
		t.Fatalf("status: %v", sm)
	}
}
