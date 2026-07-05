// internal/device/session_resilience_test.go
package device_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// execTransact makes the stub driver run one Transact on command "tx".
func execTransact(drv *stubDriver, replyLen int) {
	drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
		if cmd == "job" {
			job, cerr := drv.s.Jobs().Start("move", 100*time.Second)
			if cerr != nil {
				return nil, cerr
			}
			return job, nil
		}
		reply, err := drv.s.Transact([]byte{33, 1, 0, 0, 0}, replyLen, 50*time.Millisecond)
		if err != nil {
			return nil, device.ErrHardware(err.Error())
		}
		return reply, nil
	}
}

func TestTransactDoubleFailureFlipsUnreachableAndFailsJob(t *testing.T) {
	shrinkTimeoutsExt(t)
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		execTransact(drv, 4)
	})
	waitFor(t, "attach", f.s.Connected)

	if resp := f.s.Execute(context.Background(), device.Request{ID: "j", Cmd: "job"}); resp.Status != "ok" {
		t.Fatalf("job start: %+v", resp)
	}
	// nothing fed to the port → both transaction attempts time out
	resp := f.s.Execute(context.Background(), device.Request{ID: "t", Cmd: "tx"})
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("tx: %+v", resp)
	}
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })

	// active job must have been failed by the transition
	resp = f.s.Execute(context.Background(), device.Request{ID: "g", Cmd: "get_job"})
	if resp.Status != "error" || resp.Error.Code != device.CodeDeviceUnreachable {
		t.Fatalf("commands while unreachable must fail fast: %+v", resp)
	}
}

func TestSessionReattachesAfterBackoff(t *testing.T) {
	shrinkTimeoutsExt(t)
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		execTransact(drv, 4)
	})
	waitFor(t, "attach", f.s.Connected)
	if f.drv.attaches.Load() != 1 {
		t.Fatalf("attaches = %d", f.drv.attaches.Load())
	}

	f.s.Execute(context.Background(), device.Request{ID: "t", Cmd: "tx"}) // → unreachable
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })

	f.clock.Advance(device.ReattachBase)
	waitFor(t, "reattach", f.s.Connected)
	if f.drv.attaches.Load() != 2 {
		t.Fatalf("attaches = %d", f.drv.attaches.Load())
	}
}

func TestSessionReattachRejectsIdentityChange(t *testing.T) {
	shrinkTimeoutsExt(t)
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		execTransact(drv, 4)
		cfg.Reprobe = func(p serial.Port) ([]byte, error) {
			return []byte{70, 0, 0, 2}, nil // a densitometer appeared on our port
		}
	})
	waitFor(t, "attach", f.s.Connected)
	f.s.Execute(context.Background(), device.Request{ID: "t", Cmd: "tx"})
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })

	f.clock.Advance(device.ReattachBase)
	time.Sleep(20 * time.Millisecond)
	if f.s.Connected() {
		t.Fatal("must not attach to a different device type")
	}
	if f.drv.attaches.Load() != 1 {
		t.Fatalf("driver.Attach must not run on identity mismatch, attaches = %d", f.drv.attaches.Load())
	}
}

func TestSessionReattachFailureBacksOffExponentially(t *testing.T) {
	shrinkTimeoutsExt(t)
	reprobeErr := errors.New("still dead")
	var reprobes atomic.Int32 // written on the session goroutine, read by the test
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		execTransact(drv, 4)
		cfg.Reprobe = func(p serial.Port) ([]byte, error) {
			reprobes.Add(1)
			return nil, reprobeErr
		}
	})
	waitFor(t, "attach", f.s.Connected)
	f.s.Execute(context.Background(), device.Request{ID: "t", Cmd: "tx"})
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })

	f.clock.Advance(device.ReattachBase) // 5s: attempt 1
	waitFor(t, "first reprobe", func() bool { return reprobes.Load() >= 1 })
	f.clock.Advance(device.ReattachBase) // only 5s more — attempt 2 needs 10s
	time.Sleep(20 * time.Millisecond)
	if reprobes.Load() != 1 {
		t.Fatalf("backoff not doubled: reprobes = %d", reprobes.Load())
	}
	f.clock.Advance(device.ReattachBase) // total 10s since attempt 1
	waitFor(t, "second reprobe", func() bool { return reprobes.Load() >= 2 })
}

func TestHoldReaderBlocksReplyExpectingTransact(t *testing.T) {
	shrinkTimeoutsExt(t)
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			drv.s.HoldReader()
			defer drv.s.ReleaseReader()
			if _, err := drv.s.Transact([]byte{19, 0, 0, 0, 0}, 0, time.Second); err != nil {
				return nil, device.ErrInternal("write-only must pass: " + err.Error())
			}
			_, err := drv.s.Transact([]byte{1, 2, 3, 0, 0}, 4, time.Second)
			if !errors.Is(err, device.ErrReaderHeld) {
				return nil, device.ErrInternal("expected ErrReaderHeld")
			}
			return "ok", nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	resp := f.s.Execute(context.Background(), device.Request{ID: "h", Cmd: "tx"})
	if resp.Status != "ok" {
		t.Fatalf("resp: %+v", resp)
	}
	if f.s.Connected() != true {
		t.Fatal("ErrReaderHeld must not flip the session unreachable")
	}
}

// shrinkTimeoutsExt shrinks transact knobs for the resilience tests
// (session_test.go's fixture does not touch them).
func shrinkTimeoutsExt(t *testing.T) {
	t.Helper()
	oldPB, oldDW := device.PerByteTimeout, device.DrainWindow
	device.PerByteTimeout, device.DrainWindow = 10*time.Millisecond, 0
	t.Cleanup(func() { device.PerByteTimeout, device.DrainWindow = oldPB, oldDW })
}
