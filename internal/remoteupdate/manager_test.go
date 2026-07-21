package remoteupdate

import (
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/updateresult"
)

func testManager(t *testing.T, enabled bool) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "update_result.json")
	m := New(Config{
		Enabled:       enabled,
		StagingDir:    dir,
		ResultPath:    resultPath,
		CurVersion:    "2.2.0",
		ExePath:       filepath.Join(dir, "SerialHop.exe"),
		Spawn:         func(string, []string) error { return nil },
		RunBackground: func(f func()) { f() }, // synchronous for tests
	})
	return m, resultPath
}

func TestEnabled(t *testing.T) {
	m, _ := testManager(t, false)
	if m.Enabled() {
		t.Error("Enabled should be false")
	}
}

func TestStatusNoneWhenNoFile(t *testing.T) {
	m, _ := testManager(t, true)
	if got := m.Status(); got.State != updateresult.StateNone {
		t.Errorf("Status = %q, want none", got.State)
	}
}

func TestReconcileInstallingToSucceeded(t *testing.T) {
	m, rp := testManager(t, true)
	_ = updateresult.Write(rp, updateresult.Result{State: updateresult.StateInstalling, From: "2.2.0", To: "2.2.0"})
	m.Reconcile() // CurVersion 2.2.0 == To -> succeeded
	if got := m.Status(); got.State != updateresult.StateSucceeded {
		t.Errorf("reconciled State = %q, want succeeded", got.State)
	}
}

func TestReconcileInstallingToFailed(t *testing.T) {
	m, rp := testManager(t, true)
	_ = updateresult.Write(rp, updateresult.Result{State: updateresult.StateInstalling, From: "2.2.0", To: "2.3.0"})
	m.Reconcile() // CurVersion 2.2.0 == From -> failed
	if got := m.Status(); got.State != updateresult.StateFailed {
		t.Errorf("reconciled State = %q, want failed", got.State)
	}
}

func TestReconcileLeavesTerminalStatesUntouched(t *testing.T) {
	m, rp := testManager(t, true)
	_ = updateresult.Write(rp, updateresult.Result{State: updateresult.StateSucceeded, From: "2.1.0", To: "2.2.0"})
	m.Reconcile()
	if got := m.Status(); got.State != updateresult.StateSucceeded {
		t.Errorf("terminal State changed to %q", got.State)
	}
}

func TestGuardRejectsSecondAcquire(t *testing.T) {
	m, _ := testManager(t, true)
	if !m.tryAcquire() {
		t.Fatal("first acquire should succeed")
	}
	if m.tryAcquire() {
		t.Error("second acquire should fail while in flight")
	}
	m.release()
	if !m.tryAcquire() {
		t.Error("acquire should succeed after release")
	}
}
