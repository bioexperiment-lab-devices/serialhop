package device_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// TestWriteFrameSingleWriteNoDrain: WriteFrame must write the frame exactly
// once and must not touch RX — pre-fed bytes (a stand-in for an in-flight
// opcode-18 completion reply) must survive it. DrainWindow is non-zero so a
// Drain call would observably wipe them.
func TestWriteFrameSingleWriteNoDrain(t *testing.T) {
	oldPB, oldDW := device.PerByteTimeout, device.DrainWindow
	device.PerByteTimeout, device.DrainWindow = 10*time.Millisecond, 30*time.Millisecond
	t.Cleanup(func() { device.PerByteTimeout, device.DrainWindow = oldPB, oldDW })

	preFed := []byte{9, 9, 9, 9}
	frame := []byte{19, 0, 0, 0, 0}
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			if err := drv.s.WriteFrame(frame); err != nil {
				return nil, device.ErrInternal("write: " + err.Error())
			}
			device.DrainWindow = 0 // read back RX without re-draining
			reply, err := drv.s.Transact([]byte{1, 2, 3, 0, 0}, 4, time.Second)
			if err != nil {
				return nil, device.ErrHardware(err.Error())
			}
			return reply, nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	f.port.Feed(preFed)
	resp := f.s.Execute(context.Background(), device.Request{ID: "w", Cmd: "tx"})
	if resp.Status != "ok" {
		t.Fatalf("resp: %+v", resp)
	}
	got, ok := resp.Result.([]byte)
	if !ok || string(got) != string(preFed) {
		t.Fatalf("pre-fed RX must survive WriteFrame: %#v", resp.Result)
	}
	written := f.port.Written()
	count := 0
	for i := 0; i+5 <= len(written); i++ {
		if written[i] == 19 && string(written[i:i+5]) == string(frame) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("frame must be written exactly once, found %d in %v", count, written)
	}
}

// TestWriteFrameFailureFlipsUnreachable: a failed write marks the session
// unreachable (so recovery resets pause belief) and does NOT retry.
func TestWriteFrameFailureFlipsUnreachable(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			_ = drv.s.Conn().Close() // kill the port under the session
			if err := drv.s.WriteFrame([]byte{19, 0, 0, 0, 0}); err == nil {
				return nil, device.ErrInternal("expected write failure")
			}
			return "failed as expected", nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	resp := f.s.Execute(context.Background(), device.Request{ID: "w", Cmd: "tx"})
	if resp.Status != "ok" {
		t.Fatalf("resp: %+v", resp)
	}
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })
}

// TestSessionShutdownPublishesDisconnectBeforeDetach: PR-1 review decision 6 —
// pump Detach writes a safety stop frame, so connected must already be false
// when Detach runs (a failing write then no-ops in markUnreachable).
func TestSessionShutdownPublishesDisconnectBeforeDetach(t *testing.T) {
	f := newFixture(t, nil)
	waitFor(t, "attach", f.s.Connected)
	f.s.Close()
	if !f.drv.detached.Load() {
		t.Fatal("Detach not called")
	}
	if f.drv.connAtDetach.Load() {
		t.Fatal("connected must be false before driver.Detach() runs")
	}
}
