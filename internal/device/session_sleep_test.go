package device_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// TestSessionSleepWakesOnClockAdvance: Sleep must block on the injectable
// clock (not real time) and return once the clock passes the deadline.
func TestSessionSleepWakesOnClockAdvance(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			drv.s.Sleep(5 * time.Second)
			return "woke", nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	results := make(chan device.Response, 1)
	go func() {
		results <- f.s.Execute(context.Background(), device.Request{ID: "r", Cmd: "nap"})
	}()
	var resp device.Response
	waitFor(t, "sleep wakes", func() bool {
		f.clock.Advance(time.Second)
		select {
		case resp = <-results:
			return true
		default:
			return false
		}
	})
	if resp.Status != "ok" || resp.Result != "woke" {
		t.Fatalf("resp: %+v", resp)
	}
}

// TestSessionSleepInterruptedByClose: a Close during a Sleep must not hang
// until the (hour-long) deadline — shutdown wakes the sleeper.
func TestSessionSleepInterruptedByClose(t *testing.T) {
	entered := make(chan struct{})
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			close(entered)
			drv.s.Sleep(time.Hour)
			return "woke", nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	go f.s.Execute(context.Background(), device.Request{ID: "r", Cmd: "nap"})
	<-entered
	f.s.Close() // hangs (test timeout) if Sleep ignores shutdown
}
