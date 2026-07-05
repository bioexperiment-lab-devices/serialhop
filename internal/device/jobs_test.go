package device

import (
	"testing"
	"time"
)

func TestJobsLifecycleWithPause(t *testing.T) {
	c := NewFakeClock(time.Unix(0, 0))
	j := NewJobs(c)

	job, cerr := j.Start("dispense", 100*time.Second)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if job.ID != "j-1" || job.State != JobRunning || job.EstimatedS != 100 {
		t.Fatalf("start: %+v", job)
	}
	if _, cerr := j.Start("other", time.Second); cerr == nil || cerr.Code != CodeBusy {
		t.Fatalf("second Start must be busy, got %v", cerr)
	}

	c.Advance(35 * time.Second)
	a := j.Active()
	if a.ElapsedS != 35 || a.Progress != 0.35 {
		t.Fatalf("at 35s: %+v", a)
	}

	j.Freeze()
	if j.Active().State != JobPaused {
		t.Fatal("freeze must pause")
	}
	c.Advance(10 * time.Second) // paused time must not count
	if got := j.Active(); got.ElapsedS != 35 || got.Progress != 0.35 {
		t.Fatalf("paused clock leaked: %+v", got)
	}
	j.Unfreeze()
	c.Advance(5 * time.Second)
	if got := j.Active(); got.ElapsedS != 40 {
		t.Fatalf("after resume: %+v", got)
	}

	done := j.Complete(map[string]any{"dispensed_ml": 10.0})
	if done.State != JobSucceeded || done.Progress != 1.0 || done.Result == nil {
		t.Fatalf("complete: %+v", done)
	}
	if j.Active() != nil {
		t.Fatal("no active job after completion")
	}
	if got := j.Get("j-1"); got == nil || got.State != JobSucceeded {
		t.Fatalf("history lookup: %+v", got)
	}
}

func TestJobsTerminalSnapshotFrozenAfterCompletion(t *testing.T) {
	c := NewFakeClock(time.Unix(0, 0))
	j := NewJobs(c)
	if _, cerr := j.Start("dispense", 100*time.Second); cerr != nil {
		t.Fatal(cerr)
	}
	c.Advance(40 * time.Second)
	done := j.Complete(map[string]any{"dispensed_ml": 10.0})
	if done.ElapsedS != 40 || done.Progress != 1.0 {
		t.Fatalf("complete: %+v", done)
	}

	c.Advance(60 * time.Second) // long after completion — must not keep counting

	got := j.Get(done.ID)
	if got == nil {
		t.Fatal("completed job must remain in history")
	}
	if got.ElapsedS != 40 || got.Progress != 1.0 {
		t.Fatalf("terminal snapshot must stay frozen: %+v", got)
	}
}

func TestJobsProgressClampedBelowOneWhileRunning(t *testing.T) {
	c := NewFakeClock(time.Unix(0, 0))
	j := NewJobs(c)
	if _, cerr := j.Start("move", 2*time.Second); cerr != nil {
		t.Fatal(cerr)
	}
	c.Advance(10 * time.Second) // overdue but not verified done
	if got := j.Active(); got.Progress != 0.99 {
		t.Fatalf("overdue progress must clamp to 0.99: %+v", got)
	}
}

func TestJobsFailAndCancelKeepProgress(t *testing.T) {
	c := NewFakeClock(time.Unix(0, 0))
	j := NewJobs(c)
	if _, cerr := j.Start("move", 100*time.Second); cerr != nil {
		t.Fatal(cerr)
	}
	c.Advance(50 * time.Second)
	failed := j.Fail(ErrHardware("device became unreachable mid-job"))
	if failed.State != JobFailed || failed.Progress != 0.5 || failed.Error == nil {
		t.Fatalf("fail: %+v", failed)
	}

	if _, cerr := j.Start("move2", 100*time.Second); cerr != nil {
		t.Fatal(cerr)
	}
	c.Advance(25 * time.Second)
	cancelled := j.Cancel()
	if cancelled.State != JobCancelled || cancelled.Progress != 0.25 {
		t.Fatalf("cancel: %+v", cancelled)
	}
	if j.Cancel() != nil {
		t.Fatal("cancel with no active job must return nil")
	}
}

func TestJobsHistoryRingKeepsEight(t *testing.T) {
	c := NewFakeClock(time.Unix(0, 0))
	j := NewJobs(c)
	for i := 0; i < 10; i++ {
		if _, cerr := j.Start("k", time.Second); cerr != nil {
			t.Fatal(cerr)
		}
		j.Complete(nil)
	}
	if j.Get("j-1") != nil || j.Get("j-2") != nil {
		t.Fatal("oldest jobs must be evicted")
	}
	if j.Get("j-3") == nil || j.Get("j-10") == nil {
		t.Fatal("last 8 jobs must be retained")
	}
}
