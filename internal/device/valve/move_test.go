package valve_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// startMove issues a set_position and returns the job id. The caller must
// pre-feed a CHECK_BELIEF reply matching the current belief.
func startMove(t *testing.T, f *fixture, params string) string {
	t.Helper()
	resp := f.exec("set_position", params)
	if resp.Status != "ok" {
		t.Fatalf("set_position: %+v", resp)
	}
	job := f.resultMap(resp)["job"].(map[string]any)
	if job["state"] != "running" {
		t.Fatalf("job: %v", job)
	}
	return job["job_id"].(string)
}

func jobState(t *testing.T, f *fixture, id string) map[string]any {
	t.Helper()
	resp := f.exec("get_job", `{"job_id":"`+id+`"}`)
	if resp.Status != "ok" {
		t.Fatalf("get_job: %+v", resp)
	}
	return f.resultMap(resp)
}

// TestSetPositionTranslatesThroughOffset — the heart of virtual homing:
// homed at 4 while the device believes 0, a move to physical 1 must be sent
// as device-frame target 4 (delta = (1−4) mod 7 = 4), travel the shortest
// arc (3 slots, decreasing), and complete on the clock with a verified
// readback.
func TestSetPositionTranslatesThroughOffset(t *testing.T) {
	f := newHomedFixture(t, 4) // physical 4, device belief 0
	f.port.Feed([]byte{30, 1, 1, 0})
	id := startMove(t, f, `{"position":1}`)
	fr := f.frames()
	n := len(fr)
	// CHECK_BELIEF precedes the move; default mode (shortest) was already
	// pushed at attach, so no 35 frame here
	if !frameEq(fr[n-2], 33, 1, 0, 0, 0) {
		t.Fatalf("no CHECK_BELIEF before move: %v", fr)
	}
	if !frameEq(fr[n-1], 36, 1, 4, 0, 0) {
		t.Fatalf("move frame must target the device frame: %v", fr)
	}

	st := f.resultMap(f.exec("status", ""))
	if st["state"] != "moving" || st["position"] != nil || st["target_position"] != 1.0 {
		t.Fatalf("moving status: %v", st)
	}
	if countFrames(f.frames(), 33) != countFrames(fr, 33) {
		t.Fatal("status during a move must not touch the serial port")
	}

	// 3 slots × 0.92 s + 0.3 s margin = 3.06 s
	if js := jobState(t, f, id); js["estimated_duration_s"] != 3.06 {
		t.Fatalf("estimate: %v", js)
	}
	f.port.Feed([]byte{30, 1, 1, 4}) // post-move readback: device at target
	f.clock.Advance(3060 * time.Millisecond)
	waitFor(t, "job success", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
	res := jobState(t, f, id)["result"].(map[string]any)
	if res["position"] != 1.0 || res["from_position"] != 4.0 ||
		res["direction"] != "decreasing" || res["duration_s"] != 3.06 {
		t.Fatalf("result: %v", res)
	}
	ps := readState(t, f.dir)
	if ps["physical_position"] != 1.0 || ps["device_belief_at_shutdown"] != 4.0 {
		t.Fatalf("move must persist position knowledge: %v", ps)
	}
	f.port.Feed([]byte{30, 1, 1, 4})
	sm := f.resultMap(f.exec("status", ""))
	if sm["state"] != "idle" || sm["position"] != 1.0 {
		t.Fatalf("status after move: %v", sm)
	}
	if sm["job"].(map[string]any)["job_id"] != id {
		t.Fatalf("status must embed the last job: %v", sm)
	}
}

// TestSetPositionCurrentPositionSucceedsInstantly — the Δ=0 guard: in wrap
// mode the firmware would interpret "move to the current position" as a
// full 360° revolution, so that frame must NEVER go out; the driver returns
// an already-succeeded job instead.
func TestSetPositionCurrentPositionSucceedsInstantly(t *testing.T) {
	f := newHomedFixture(t, 3)
	f.port.Feed([]byte{30, 1, 1, 0}) // CHECK_BELIEF still runs first
	resp := f.exec("set_position", `{"position":3,"rotation":"wrap"}`)
	if resp.Status != "ok" {
		t.Fatalf("set_position: %+v", resp)
	}
	job := f.resultMap(resp)["job"].(map[string]any)
	if job["state"] != "succeeded" || job["progress"] != 1.0 {
		t.Fatalf("job: %v", job)
	}
	res := job["result"].(map[string]any)
	if res["position"] != 3.0 || res["from_position"] != 3.0 || res["duration_s"] != 0.0 {
		t.Fatalf("result: %v", res)
	}
	if _, ok := res["direction"]; ok {
		t.Fatalf("no-motion move must omit direction: %v", res)
	}
	if countFrames(f.frames(), 36) != 0 {
		t.Fatal("a move to the current position must never reach the device")
	}
}

// TestSetPositionRotationModeDedup: the 35 mode frame is pushed only when
// the requested mode differs from what the firmware last received.
func TestSetPositionRotationModeDedup(t *testing.T) {
	f := newHomedFixture(t, 0)
	// 1st move: wrap override differs from attach's shortest → 35 pushed.
	// d = 2 → wrap slots 7−2 = 5 → estimate 4.9 s, decreasing.
	f.port.Feed([]byte{30, 1, 1, 0})
	id := startMove(t, f, `{"position":2,"rotation":"wrap"}`)
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 35, 1, 2, 0, 0) || !frameEq(fr[n-1], 36, 1, 2, 0, 0) {
		t.Fatalf("wrap move frames: %v", fr)
	}
	f.port.Feed([]byte{30, 1, 1, 2})
	f.clock.Advance(4900 * time.Millisecond)
	waitFor(t, "first move", func() bool { return jobState(t, f, id)["state"] == "succeeded" })
	if res := jobState(t, f, id)["result"].(map[string]any); res["direction"] != "decreasing" {
		t.Fatalf("wrap direction: %v", res)
	}

	// 2nd move: wrap again → no 35 re-push; target (2+2) mod 7 = 4
	before := countFrames(f.frames(), 35)
	f.port.Feed([]byte{30, 1, 1, 2})
	id2 := startMove(t, f, `{"position":4,"rotation":"wrap"}`)
	if countFrames(f.frames(), 35) != before {
		t.Fatal("unchanged mode must not be re-pushed")
	}
	fr = f.frames()
	if !frameEq(fr[len(fr)-1], 36, 1, 4, 0, 0) {
		t.Fatalf("second move frame: %v", fr)
	}
	f.port.Feed([]byte{30, 1, 1, 4})
	f.clock.Advance(4900 * time.Millisecond)
	waitFor(t, "second move", func() bool { return jobState(t, f, id2)["state"] == "succeeded" })

	// 3rd move: no rotation param → default shortest ≠ last-pushed wrap →
	// 35 pushed again
	f.port.Feed([]byte{30, 1, 1, 4})
	startMove(t, f, `{"position":5}`)
	fr = f.frames()
	n = len(fr)
	if !frameEq(fr[n-2], 35, 1, 3, 0, 0) || !frameEq(fr[n-1], 36, 1, 5, 0, 0) {
		t.Fatalf("default mode must be re-pushed: %v", fr)
	}
}

func TestSetPositionNotHomed(t *testing.T) {
	f := newFixture(t, 0)
	n := len(f.port.Written())
	resp := f.exec("set_position", `{"position":2}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeNotHomed {
		t.Fatalf("resp: %+v", resp)
	}
	if len(f.port.Written()) != n {
		t.Fatal("not_homed must fire before any serial traffic")
	}
}

func TestSetPositionValidation(t *testing.T) {
	f := newHomedFixture(t, 0)
	n := len(f.port.Written())
	for name, params := range map[string]string{
		"out of range": `{"position":7}`,
		"missing":      `{}`,
		"bad rotation": `{"position":2,"rotation":"spiral"}`,
	} {
		resp := f.exec("set_position", params)
		if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
			t.Fatalf("%s: %+v", name, resp)
		}
	}
	if len(f.port.Written()) != n {
		t.Fatal("validation must precede serial traffic")
	}
}

// TestSetPositionBusyDuringMove: no mid-move retargeting — the firmware
// would compute from its already-advanced counter while the rotor is
// between detents (TRANSLATION §5).
func TestSetPositionBusyDuringMove(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	startMove(t, f, `{"position":2}`)
	n := len(f.port.Written())
	for _, c := range []struct{ cmd, params string }{
		{"set_position", `{"position":5}`},
		{"home", `{"position":0}`},
	} {
		resp := f.exec(c.cmd, c.params)
		if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
			t.Fatalf("%s during move: %+v", c.cmd, resp)
		}
	}
	if len(f.port.Written()) != n {
		t.Fatal("busy commands must not reach the device")
	}
}
