package device_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// TestIdentifyServedFromCacheWhileUnreachable: a successful Attach populated
// the identify cache; the unreachable transition must not empty it (spec §3
// memory-served exception).
func TestIdentifyServedFromCacheWhileUnreachable(t *testing.T) {
	shrinkTimeoutsExt(t)
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		execTransact(drv, 4)
	})
	waitFor(t, "attach", f.s.Connected)

	f.s.Execute(context.Background(), device.Request{ID: "t", Cmd: "tx"}) // → unreachable
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })

	resp := f.s.Execute(context.Background(), device.Request{ID: "i", Cmd: "identify"})
	if resp.Status != "ok" {
		t.Fatalf("identify while unreachable must serve cached info: %+v", resp)
	}
	if info := resp.Result.(device.Info); info.Serial != "26-001" {
		t.Fatalf("cached info: %+v", info)
	}
}

// TestIdentifyUnreachableWhenNeverAttached: no successful Attach has ever
// populated the cache — there is nothing to serve.
func TestIdentifyUnreachableWhenNeverAttached(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.attachErr = errors.New("device silent")
	})
	time.Sleep(20 * time.Millisecond) // let the failed attach land
	resp := f.s.Execute(context.Background(), device.Request{ID: "i", Cmd: "identify"})
	if resp.Status != "error" || resp.Error.Code != device.CodeDeviceUnreachable {
		t.Fatalf("identify with no cached info must be device_unreachable: %+v", resp)
	}
}

// TestGetJobServedWhileNeverAttached: get_job is always jobs-engine-served;
// an unknown job_id stays invalid_params even while unreachable.
func TestGetJobServedWhileNeverAttached(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.attachErr = errors.New("device silent")
	})
	time.Sleep(20 * time.Millisecond)
	resp := f.s.Execute(context.Background(), device.Request{
		ID: "g", Cmd: "get_job", Params: []byte(`{"job_id":"j-1"}`)})
	if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
		t.Fatalf("get_job must be memory-served (invalid_params, not unreachable): %+v", resp)
	}
}
