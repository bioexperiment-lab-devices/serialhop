package valve_test

import (
	"fmt"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// newHomedFixture returns a fixture homed at `at` with device belief 0
// (fresh-boot device counter).
func newHomedFixture(t *testing.T, at int) *fixture {
	t.Helper()
	f := newFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0}) // home's belief-resync reply
	if resp := f.exec("home", fmt.Sprintf(`{"position":%d}`, at)); resp.Status != "ok" {
		t.Fatalf("home: %+v", resp)
	}
	return f
}

// TestHomeDeclaresPositionAndPersists: home is a translator-side
// declaration — no motion frame; it resyncs belief from the device, sets
// physical_position, and persists both (TRANSLATION §4 home).
func TestHomeDeclaresPositionAndPersists(t *testing.T) {
	f := newFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 5}) // device counter drifted to 5 meanwhile
	resp := f.exec("home", `{"position":2}`)
	if resp.Status != "ok" {
		t.Fatalf("home: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["homed"] != true || m["position"] != 2.0 {
		t.Fatalf("home result: %v", m)
	}
	fr := f.frames()
	if !frameEq(fr[len(fr)-1], 33, 1, 0, 0, 0) {
		t.Fatalf("home must resync belief with a position query: %v", fr)
	}
	if countFrames(fr, 36) != 0 {
		t.Fatal("home must not move the rotor")
	}
	st := readState(t, f.dir)
	if st["physical_position"] != 2.0 || st["device_belief_at_shutdown"] != 5.0 {
		t.Fatalf("persisted: %v", st)
	}
	f.port.Feed([]byte{30, 1, 1, 5}) // status's idle CHECK_BELIEF
	sm := f.resultMap(f.exec("status", ""))
	if sm["state"] != "idle" || sm["homed"] != true || sm["position"] != 2.0 {
		t.Fatalf("status after home: %v", sm)
	}
}

func TestHomeValidation(t *testing.T) {
	f := newFixture(t, 0)
	n := len(f.port.Written())
	for name, params := range map[string]string{
		"out of range": `{"position":7}`,
		"negative":     `{"position":-1}`,
		"missing":      `{}`,
	} {
		resp := f.exec("home", params)
		if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
			t.Fatalf("%s: %+v", name, resp)
		}
	}
	if len(f.port.Written()) != n {
		t.Fatal("validation failures must not reach the device")
	}
}

// TestAttachRecoversHomedState: restart recovery (TRANSLATION §3 step 3) —
// persisted physical position + matching device counter → homed without
// operator involvement.
func TestAttachRecoversHomedState(t *testing.T) {
	f := newHomedFixture(t, 4)
	dir := f.dir
	f.s.Close()

	f2 := newFixture(t, 0, withStateDir(dir)) // device counter still 0 == persisted belief
	f2.port.Feed([]byte{30, 1, 1, 0})
	sm := f2.resultMap(f2.exec("status", ""))
	if sm["state"] != "idle" || sm["position"] != 4.0 {
		t.Fatalf("recovery must restore homed state: %v", sm)
	}
}

// TestAttachRefusesStaleRecovery: the device counter no longer matches the
// persisted belief (reboot or foreign host while we were away) → require an
// explicit home.
func TestAttachRefusesStaleRecovery(t *testing.T) {
	f := newHomedFixture(t, 4)
	dir := f.dir
	f.s.Close()

	f2 := newFixture(t, 3, withStateDir(dir)) // counter 3 ≠ persisted belief 0
	f2.port.Feed([]byte{30, 1, 1, 3})
	sm := f2.resultMap(f2.exec("status", ""))
	if sm["state"] != "unhomed" || sm["position"] != nil {
		t.Fatalf("stale recovery must be refused: %v", sm)
	}
}

// TestAttachPushesRecoveredConfig: the persisted config mirror (not the
// defaults) is what attach re-pushes — including the inverted hold-ON
// encoding (35 2 0).
func TestAttachPushesRecoveredConfig(t *testing.T) {
	dir := t.TempDir()
	st := device.NewStore(dir, "valve-COM9")
	if err := st.Save(map[string]any{
		"schema_version": 1, "physical_position": 4,
		"device_belief_at_shutdown": 2,
		"config":                    map[string]any{"default_rotation": "direct", "hold_torque": true},
	}); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, 2, withStateDir(dir))
	fr := f.frames()
	if !frameEq(fr[1], 35, 1, 1, 0, 0) || !frameEq(fr[2], 35, 2, 0, 0, 0) {
		t.Fatalf("recovered config frames: %v", fr)
	}
	f.port.Feed([]byte{30, 1, 1, 2})
	sm := f.resultMap(f.exec("status", ""))
	if sm["position"] != 4.0 {
		t.Fatalf("recovered homed state: %v", sm)
	}
	cfg := sm["config"].(map[string]any)
	if cfg["default_rotation"] != "direct" || cfg["hold_torque"] != true {
		t.Fatalf("recovered config: %v", cfg)
	}
}

// TestRebootWhileIdlePreservesHoming: the idle auto-recovery keeps homed
// and physical_position — the offset math absorbs the counter reset.
func TestRebootWhileIdlePreservesHoming(t *testing.T) {
	f := newFixture(t, 2) // attach: belief 2
	f.port.Feed([]byte{30, 1, 1, 2})
	if resp := f.exec("home", `{"position":3}`); resp.Status != "ok" {
		t.Fatalf("home: %+v", resp)
	}
	f.port.Feed([]byte{30, 1, 1, 0}) // silent reboot: counter reset
	sm := f.resultMap(f.exec("status", ""))
	if sm["state"] != "idle" || sm["homed"] != true || sm["position"] != 3.0 {
		t.Fatalf("auto-recovery must preserve homing: %v", sm)
	}
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 35, 1, 3, 0, 0) || !frameEq(fr[n-1], 35, 2, 1, 0, 0) {
		t.Fatalf("config not re-pushed after reboot: %v", fr)
	}
}

// TestForeignMoveUnhomes: a mismatched, nonzero counter voids position
// knowledge even when homed.
func TestForeignMoveUnhomes(t *testing.T) {
	f := newFixture(t, 2)
	f.port.Feed([]byte{30, 1, 1, 2})
	if resp := f.exec("home", `{"position":3}`); resp.Status != "ok" {
		t.Fatalf("home: %+v", resp)
	}
	f.port.Feed([]byte{30, 1, 1, 6}) // someone else moved the valve
	sm := f.resultMap(f.exec("status", ""))
	if sm["state"] != "unhomed" || sm["position"] != nil {
		t.Fatalf("foreign mismatch must unhome: %v", sm)
	}
}
