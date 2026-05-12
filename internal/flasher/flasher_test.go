package flasher

import (
	"testing"
	"time"

	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

func TestOutcomeString(t *testing.T) {
	cases := map[Outcome]string{
		OutcomeSuccess:                "success",
		OutcomeRolledBackVerifyFailed: "rolled_back_verify_failed",
		OutcomeRolledBackTestFailed:   "rolled_back_test_failed",
		OutcomeFailedPreflight:        "failed_preflight",
		OutcomeFailedBackup:           "failed_backup",
		OutcomeFailedNoRecovery:       "failed_no_recovery",
	}
	for o, want := range cases {
		if got := o.String(); got != want {
			t.Errorf("Outcome %d: got %q, want %q", int(o), got, want)
		}
	}
}

func TestNewFlasher_RejectsEmptyBackupDir(t *testing.T) {
	op := labserial.NewFakeOpener()
	_, err := New(op, "", 10, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected error for empty backup dir, got nil")
	}
}
