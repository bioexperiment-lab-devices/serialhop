// internal/device/session_test.go
package device_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// stubDriver is a scriptable Driver for session tests.
type stubDriver struct {
	s         *device.Session
	attachErr error
	attaches  atomic.Int32
	ticks     atomic.Int32
	detached  atomic.Bool
	exec      func(cmd string, params json.RawMessage) (any, *device.CmdError)
}

func (d *stubDriver) Attach(ctx context.Context, probeReply []byte) (device.Info, error) {
	d.attaches.Add(1)
	if d.attachErr != nil {
		return device.Info{}, d.attachErr
	}
	return device.Info{DeviceType: "stub", Model: "stub-1", Serial: "26-001",
		FirmwareVersion: "legacy", ProtocolVersion: "1.0"}, nil
}

func (d *stubDriver) Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *device.CmdError) {
	if d.exec != nil {
		return d.exec(cmd, params)
	}
	return nil, device.ErrUnknownCommand(cmd)
}

func (d *stubDriver) Tick(now time.Time) { d.ticks.Add(1) }
func (d *stubDriver) Detach()            { d.detached.Store(true) }

type sessionFixture struct {
	s      *device.Session
	drv    *stubDriver
	clock  *device.FakeClock
	port   *serial.FakePort
	opener *serial.FakeOpener
}

func newFixture(t *testing.T, mutate func(*device.SessionConfig, *stubDriver)) *sessionFixture {
	t.Helper()
	drv := &stubDriver{}
	clock := device.NewFakeClock(time.Unix(1000, 0))
	port := serial.NewFakePort("COM9")
	opener := serial.NewFakeOpener()
	opener.Add(port)
	conn, err := opener.Open("COM9")
	if err != nil {
		t.Fatal(err)
	}
	cfg := device.SessionConfig{
		ID: "stub_1", Type: "stub", TypeCode: 201, PortName: "COM9",
		Conn: conn, Opener: opener, Clock: clock, StateDir: t.TempDir(),
		Factory:    func(s *device.Session) device.Driver { drv.s = s; return drv },
		ProbeReply: []byte{201, 0, 0, 1},
		Reprobe:    func(p serial.Port) ([]byte, error) { return []byte{201, 0, 0, 1}, nil },
	}
	if mutate != nil {
		mutate(&cfg, drv)
	}
	s := device.NewSession(cfg)
	s.Start(context.Background())
	t.Cleanup(s.Close)
	return &sessionFixture{s: s, drv: drv, clock: clock, port: port, opener: opener}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSessionAttachesAndServesIdentify(t *testing.T) {
	f := newFixture(t, nil)
	waitFor(t, "attach", f.s.Connected)
	resp := f.s.Execute(context.Background(), device.Request{ID: "r1", Cmd: "identify"})
	if resp.Status != "ok" || resp.ID != "r1" {
		t.Fatalf("resp: %+v", resp)
	}
	info, ok := resp.Result.(device.Info)
	if !ok || info.Serial != "26-001" {
		t.Fatalf("identify result: %#v", resp.Result)
	}
	if got, ok := f.s.CachedInfo(); !ok || got.Model != "stub-1" {
		t.Fatalf("CachedInfo: %+v %v", got, ok)
	}
}

func TestSessionRoutesCommandsToDriver(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			if cmd != "ping" {
				return nil, device.ErrUnknownCommand(cmd)
			}
			return map[string]any{"uptime_ms": 5}, nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	resp := f.s.Execute(context.Background(), device.Request{ID: "r2", Cmd: "ping"})
	if resp.Status != "ok" {
		t.Fatalf("resp: %+v", resp)
	}
	resp = f.s.Execute(context.Background(), device.Request{ID: "r3", Cmd: "nope"})
	if resp.Status != "error" || resp.Error.Code != device.CodeUnknownCommand {
		t.Fatalf("resp: %+v", resp)
	}
}

func TestSessionGetJob(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			job, cerr := drv.s.Jobs().Start("dispense", 100*time.Second)
			if cerr != nil {
				return nil, cerr
			}
			return map[string]any{"job": job}, nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	if resp := f.s.Execute(context.Background(), device.Request{ID: "r4", Cmd: "dispense"}); resp.Status != "ok" {
		t.Fatalf("start: %+v", resp)
	}
	resp := f.s.Execute(context.Background(), device.Request{
		ID: "r5", Cmd: "get_job", Params: json.RawMessage(`{"job_id":"j-1"}`)})
	if resp.Status != "ok" {
		t.Fatalf("get_job: %+v", resp)
	}
	job, ok := resp.Result.(device.Job)
	if !ok || job.ID != "j-1" || job.State != device.JobRunning {
		t.Fatalf("job: %#v", resp.Result)
	}
	resp = f.s.Execute(context.Background(), device.Request{
		ID: "r6", Cmd: "get_job", Params: json.RawMessage(`{"job_id":"j-99"}`)})
	if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
		t.Fatalf("unknown job: %+v", resp)
	}
}

func TestSessionSerializesCommands(t *testing.T) {
	release := make(chan struct{})
	var inFlight, maxInFlight atomic.Int32
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			cur := inFlight.Add(1)
			if cur > maxInFlight.Load() {
				maxInFlight.Store(cur)
			}
			<-release
			inFlight.Add(-1)
			return "done", nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	results := make(chan device.Response, 2)
	for i := 0; i < 2; i++ {
		go func() {
			results <- f.s.Execute(context.Background(), device.Request{ID: "x", Cmd: "slow"})
		}()
	}
	waitFor(t, "first command entered", func() bool { return inFlight.Load() == 1 })
	close(release)
	<-results
	<-results
	if maxInFlight.Load() != 1 {
		t.Fatalf("commands overlapped: max in flight = %d", maxInFlight.Load())
	}
}

func TestSessionHeartbeatTicksDriver(t *testing.T) {
	f := newFixture(t, nil)
	waitFor(t, "attach", f.s.Connected)
	f.clock.Advance(device.HeartbeatInterval)
	waitFor(t, "tick", func() bool { return f.drv.ticks.Load() >= 1 })
}

func TestSessionAfterRunsOnLoop(t *testing.T) {
	f := newFixture(t, nil)
	waitFor(t, "attach", f.s.Connected)
	var fired atomic.Bool
	done := make(chan struct{})
	f.s.Post(func() {
		f.drv.s.After(10*time.Second, func() { fired.Store(true) })
		close(done)
	})
	<-done
	f.clock.Advance(9 * time.Second)
	time.Sleep(10 * time.Millisecond)
	if fired.Load() {
		t.Fatal("After fired early")
	}
	f.clock.Advance(time.Second)
	waitFor(t, "after callback", fired.Load)
}

func TestSessionUnreachableWhenAttachFails(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.attachErr = context.DeadlineExceeded // any error
	})
	waitFor(t, "first attach attempt", func() bool { return f.drv.attaches.Load() >= 1 })
	resp := f.s.Execute(context.Background(), device.Request{ID: "r7", Cmd: "ping"})
	if resp.Status != "error" || resp.Error.Code != device.CodeDeviceUnreachable {
		t.Fatalf("resp: %+v", resp)
	}
	if f.s.Connected() {
		t.Fatal("must not report connected")
	}
}

func TestSessionCloseDetachesDriver(t *testing.T) {
	f := newFixture(t, nil)
	waitFor(t, "attach", f.s.Connected)
	f.s.Close()
	if !f.drv.detached.Load() {
		t.Fatal("Detach not called on Close")
	}
	resp := f.s.Execute(context.Background(), device.Request{ID: "r8", Cmd: "ping"})
	if resp.Status != "error" || resp.Error.Code != device.CodeDeviceUnreachable {
		t.Fatalf("Execute after Close: %+v", resp)
	}
	if !strings.Contains(resp.Error.Message, "closed") {
		t.Fatalf("message should say session closed: %+v", resp.Error)
	}
}
