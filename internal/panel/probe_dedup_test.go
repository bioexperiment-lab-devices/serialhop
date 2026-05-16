//go:build windows

package panel

import (
	"testing"
	"time"
)

func TestProbeDedup_FirstFailureLogs(t *testing.T) {
	d := newProbeDedup(5 * time.Minute)
	if !d.shouldLog("server", "i/o timeout", time.Unix(0, 0)) {
		t.Error("first failure must log")
	}
}

func TestProbeDedup_RepeatSameReasonSuppressed(t *testing.T) {
	d := newProbeDedup(5 * time.Minute)
	t0 := time.Unix(0, 0)
	_ = d.shouldLog("server", "i/o timeout", t0)
	if d.shouldLog("server", "i/o timeout", t0.Add(30*time.Second)) {
		t.Error("repeat same reason within window must be suppressed")
	}
}

func TestProbeDedup_ReasonChangeLogs(t *testing.T) {
	d := newProbeDedup(5 * time.Minute)
	t0 := time.Unix(0, 0)
	_ = d.shouldLog("server", "i/o timeout", t0)
	if !d.shouldLog("server", "dns error", t0.Add(10*time.Second)) {
		t.Error("reason change must log")
	}
}

func TestProbeDedup_WindowExpiry(t *testing.T) {
	d := newProbeDedup(5 * time.Minute)
	t0 := time.Unix(0, 0)
	_ = d.shouldLog("server", "i/o timeout", t0)
	if !d.shouldLog("server", "i/o timeout", t0.Add(6*time.Minute)) {
		t.Error("repeat after window must log")
	}
}

func TestProbeDedup_RecoveryReset(t *testing.T) {
	d := newProbeDedup(5 * time.Minute)
	t0 := time.Unix(0, 0)
	_ = d.shouldLog("server", "i/o timeout", t0)
	d.reset("server")
	if !d.shouldLog("server", "i/o timeout", t0.Add(10*time.Second)) {
		t.Error("post-reset failure must log")
	}
}
