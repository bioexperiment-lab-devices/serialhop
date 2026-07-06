package valve_test

import (
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// TestConfigureFramesAndEcho pins the exact frames — including the INVERTED
// hold-torque encoding: N3=0 means hold ON (stepper stays energized), N3=1
// means hold OFF.
func TestConfigureFramesAndEcho(t *testing.T) {
	f := newFixture(t, 0)
	resp := f.exec("configure", `{"default_rotation":"direct","hold_torque":true}`)
	if resp.Status != "ok" {
		t.Fatalf("configure: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["default_rotation"] != "direct" || m["hold_torque"] != true {
		t.Fatalf("echo: %v", m)
	}
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 35, 1, 1, 0, 0) || !frameEq(fr[n-1], 35, 2, 0, 0, 0) {
		t.Fatalf("configure frames: %v", fr)
	}
	cfg := readState(t, f.dir)["config"].(map[string]any)
	if cfg["default_rotation"] != "direct" || cfg["hold_torque"] != true {
		t.Fatalf("persisted config: %v", cfg)
	}

	// hold OFF → N3 = 1; omitted rotation stays untouched
	m = f.resultMap(f.exec("configure", `{"hold_torque":false}`))
	if m["default_rotation"] != "direct" || m["hold_torque"] != false {
		t.Fatalf("partial echo: %v", m)
	}
	fr = f.frames()
	if !frameEq(fr[len(fr)-1], 35, 2, 1, 0, 0) {
		t.Fatalf("hold-off frame: %v", fr)
	}
	if countFrames(fr, 35) != 5 { // 2 attach + 2 full configure + 1 partial
		t.Fatalf("unexpected config frame count: %v", fr)
	}
}

func TestConfigureEmptyEchoesCurrent(t *testing.T) {
	f := newFixture(t, 0)
	n := len(f.port.Written())
	m := f.resultMap(f.exec("configure", `{}`))
	if m["default_rotation"] != "shortest" || m["hold_torque"] != false {
		t.Fatalf("echo: %v", m)
	}
	if len(f.port.Written()) != n {
		t.Fatal("no fields provided → no frames")
	}
}

func TestConfigureValidation(t *testing.T) {
	f := newFixture(t, 0)
	resp := f.exec("configure", `{"default_rotation":"spiral"}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
		t.Fatalf("resp: %+v", resp)
	}
}

func TestConfigureBusyDuringMove(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	startMove(t, f, `{"position":2}`)
	resp := f.exec("configure", `{"hold_torque":true}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("configure during move: %+v", resp)
	}
}

// TestConfigureSurvivesRestart: the JSON contract's "settings persist
// across power cycles" promise is honored by the TRANSLATOR — persisted
// mirror, re-pushed on the next attach.
func TestConfigureSurvivesRestart(t *testing.T) {
	f := newFixture(t, 0)
	if resp := f.exec("configure", `{"default_rotation":"wrap","hold_torque":true}`); resp.Status != "ok" {
		t.Fatalf("configure: %+v", resp)
	}
	dir := f.dir
	f.s.Close()
	f2 := newFixture(t, 0, withStateDir(dir))
	fr := f2.frames()
	if !frameEq(fr[1], 35, 1, 2, 0, 0) || !frameEq(fr[2], 35, 2, 0, 0, 0) {
		t.Fatalf("restart must push the persisted config: %v", fr)
	}
}

// TestConfigureUpdatesModeDedup: a configured default becomes the
// last-pushed mode, so the next default-mode move skips the 35 frame.
func TestConfigureUpdatesModeDedup(t *testing.T) {
	f := newHomedFixture(t, 0)
	if resp := f.exec("configure", `{"default_rotation":"direct"}`); resp.Status != "ok" {
		t.Fatalf("configure: %+v", resp)
	}
	before := countFrames(f.frames(), 35)
	f.port.Feed([]byte{30, 1, 1, 0})
	startMove(t, f, `{"position":2}`) // default (direct) already pushed by configure
	if countFrames(f.frames(), 35) != before {
		t.Fatal("move must not re-push the mode configure just set")
	}
}
