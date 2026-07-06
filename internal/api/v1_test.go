package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/power"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// fakeDriver is the spec-§6 test driver: no device logic, scriptable Execute.
type fakeDriver struct {
	s         *device.Session
	attachErr error
	exec      func(cmd string, params json.RawMessage) (any, *device.CmdError)
	detached  atomic.Bool
}

func (d *fakeDriver) Attach(ctx context.Context, probeReply []byte) (device.Info, error) {
	if d.attachErr != nil {
		return device.Info{}, d.attachErr
	}
	return device.Info{DeviceType: "fake-device", Model: "fake-1",
		FirmwareVersion: "legacy", ProtocolVersion: "1.0"}, nil
}
func (d *fakeDriver) Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *device.CmdError) {
	if d.exec != nil {
		return d.exec(cmd, params)
	}
	return nil, device.ErrUnknownCommand(cmd)
}
func (d *fakeDriver) Tick(now time.Time) {}
func (d *fakeDriver) Detach()            { d.detached.Store(true) }

// newFakeSession starts a session hosting drv under test type code 240.
func newFakeSession(t *testing.T, id string, drv *fakeDriver) *device.Session {
	t.Helper()
	port := serial.NewFakePort("TEST-" + id)
	opener := serial.NewFakeOpener()
	opener.Add(port)
	conn, err := opener.Open(port.Name())
	if err != nil {
		t.Fatal(err)
	}
	s := device.NewSession(device.SessionConfig{
		ID: id, Type: "fake", TypeCode: 240, PortName: port.Name(),
		Conn: conn, Opener: opener, StateDir: t.TempDir(),
		Factory: func(sess *device.Session) device.Driver { drv.s = sess; return drv },
		Reprobe: func(serial.Port) ([]byte, error) { return nil, errors.New("no reprobe in tests") },
	})
	s.Start(context.Background())
	s.WaitFirstAttach(context.Background())
	t.Cleanup(s.Close)
	return s
}

func newV1Server(t *testing.T, reg *registry.Registry, disc DiscoverFn) http.Handler {
	t.Helper()
	ka, err := power.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ka.Close() })
	return New(reg, disc, serial.NewFakeOpener(), nil, false, ka).Handler()
}

func postEnvelope(t *testing.T, srv http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestV1DevicesEmpty(t *testing.T) {
	reg := registry.New()
	srv := newV1Server(t, reg, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"devices":[]`) {
		t.Errorf("body must carry an empty array, not null: %s", body)
	}
	if !strings.Contains(body, `"discovered_at":null`) {
		t.Errorf("body must carry discovered_at:null: %s", body)
	}
}

func TestV1DevicesListsSessions(t *testing.T) {
	reg := registry.New()
	attached := newFakeSession(t, "fake_1", &fakeDriver{})
	never := newFakeSession(t, "fake_2", &fakeDriver{attachErr: errors.New("no device")})
	reg.Replace([]*device.Session{attached, never})

	srv := newV1Server(t, reg, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	var resp DevicesResponse
	decode(t, rec.Body, &resp)

	if len(resp.Devices) != 2 {
		t.Fatalf("devices: got %d, want 2 (%+v)", len(resp.Devices), resp.Devices)
	}
	byID := map[string]DeviceDTO{}
	for _, d := range resp.Devices {
		byID[d.ID] = d
	}
	d1, ok := byID["fake_1"]
	if !ok {
		t.Fatalf("fake_1 missing from response: %+v", resp.Devices)
	}
	if !d1.Connected {
		t.Errorf("fake_1 Connected = false, want true")
	}
	if d1.Identify == nil || d1.Identify.DeviceType != "fake-device" {
		t.Errorf("fake_1 Identify = %+v, want DeviceType fake-device", d1.Identify)
	}
	d2, ok := byID["fake_2"]
	if !ok {
		t.Fatalf("fake_2 missing from response: %+v", resp.Devices)
	}
	if d2.Connected {
		t.Errorf("fake_2 Connected = true, want false (attach failed)")
	}
	if d2.Identify != nil {
		t.Errorf("fake_2 Identify = %+v, want nil (never attached)", d2.Identify)
	}
	if resp.DiscoveredAt == nil {
		t.Errorf("discovered_at = nil, want non-nil (Replace stamped it)")
	}
}

func TestV1CommandOK(t *testing.T) {
	reg := registry.New()
	drv := &fakeDriver{exec: func(cmd string, _ json.RawMessage) (any, *device.CmdError) {
		return map[string]any{"echo": cmd}, nil
	}}
	sess := newFakeSession(t, "fake_1", drv)
	reg.Replace([]*device.Session{sess})
	srv := newV1Server(t, reg, nil)

	rec := postEnvelope(t, srv, "/api/v1/devices/fake_1/command", `{"id":"r1","cmd":"ping"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp device.Response
	decode(t, rec.Body, &resp)
	if resp.ID != "r1" {
		t.Errorf("id: got %q, want r1", resp.ID)
	}
	if resp.Status != "ok" {
		t.Errorf("status: got %q, want ok", resp.Status)
	}
	result, _ := resp.Result.(map[string]any)
	if result["echo"] != "ping" {
		t.Errorf("result.echo: got %v, want ping", result["echo"])
	}
}

func TestV1CommandDriverErrorIs200(t *testing.T) {
	reg := registry.New()
	drv := &fakeDriver{exec: func(_ string, _ json.RawMessage) (any, *device.CmdError) {
		return nil, device.ErrInvalidParams("volume_ml", -1, "must be positive")
	}}
	sess := newFakeSession(t, "fake_1", drv)
	reg.Replace([]*device.Session{sess})
	srv := newV1Server(t, reg, nil)

	rec := postEnvelope(t, srv, "/api/v1/devices/fake_1/command", `{"id":"r1","cmd":"dispense"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (device-decided outcomes are 200); body=%s", rec.Code, rec.Body.String())
	}
	var resp device.Response
	decode(t, rec.Body, &resp)
	if resp.Status != "error" {
		t.Errorf("status: got %q, want error", resp.Status)
	}
	if resp.Error == nil || resp.Error.Code != device.CodeInvalidParams {
		t.Errorf("error: got %+v, want code invalid_params", resp.Error)
	}
}

func TestV1CommandUnknownDeviceIs404(t *testing.T) {
	reg := registry.New()
	srv := newV1Server(t, reg, nil)

	rec := postEnvelope(t, srv, "/api/v1/devices/nope/command", `{"id":"r9","cmd":"ping"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	var resp device.Response
	decode(t, rec.Body, &resp)
	if resp.Status != "error" {
		t.Errorf("status: got %q, want error", resp.Status)
	}
	if resp.ID != "r9" {
		t.Errorf("id: got %q, want r9 (echoed from request)", resp.ID)
	}
	if resp.Error == nil || resp.Error.Code != device.CodeUnknownDevice {
		t.Errorf("error: got %+v, want code unknown_device", resp.Error)
	}
}

func TestV1CommandUnreachableIs503(t *testing.T) {
	reg := registry.New()
	sess := newFakeSession(t, "fake_1", &fakeDriver{attachErr: errors.New("no device")})
	reg.Replace([]*device.Session{sess})
	srv := newV1Server(t, reg, nil)

	// A device-facing command on an unreachable device: 503.
	rec := postEnvelope(t, srv, "/api/v1/devices/fake_1/command", `{"id":"r2","cmd":"status"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	var resp device.Response
	decode(t, rec.Body, &resp)
	if resp.Error == nil || resp.Error.Code != device.CodeDeviceUnreachable {
		t.Errorf("status cmd: error = %+v, want device_unreachable", resp.Error)
	}

	// identify with no cached info (never attached) is also 503.
	rec = postEnvelope(t, srv, "/api/v1/devices/fake_1/command", `{"id":"r3","cmd":"identify"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("identify status: got %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	resp = device.Response{}
	decode(t, rec.Body, &resp)
	if resp.Error == nil || resp.Error.Code != device.CodeDeviceUnreachable {
		t.Errorf("identify: error = %+v, want device_unreachable", resp.Error)
	}

	// get_job is memory-served even while unreachable, so an unknown job id
	// yields a 200 invalid_params rather than a 503.
	rec = postEnvelope(t, srv, "/api/v1/devices/fake_1/command",
		`{"id":"r4","cmd":"get_job","params":{"job_id":"j-1"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("get_job status: got %d, want 200 (memory-served); body=%s", rec.Code, rec.Body.String())
	}
	resp = device.Response{}
	decode(t, rec.Body, &resp)
	if resp.Error == nil || resp.Error.Code != device.CodeInvalidParams {
		t.Errorf("get_job: error = %+v, want invalid_params", resp.Error)
	}
}

func TestV1CommandMalformedBodyIs400(t *testing.T) {
	reg := registry.New()
	srv := newV1Server(t, reg, nil)

	cases := []struct {
		name string
		body string
	}{
		{"not json", `{`},
		{"missing id and cmd", `{}`},
		{"missing cmd", `{"id":"x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := postEnvelope(t, srv, "/api/v1/devices/anything/command", c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			var resp device.Response
			decode(t, rec.Body, &resp)
			if resp.Error == nil || resp.Error.Code != device.CodeInvalidRequest {
				t.Errorf("error: got %+v, want code invalid_request", resp.Error)
			}
		})
	}
}

func TestV1DiscoverReplacesSessions(t *testing.T) {
	reg := registry.New()
	drvA := &fakeDriver{}
	sessA := newFakeSession(t, "fake_1", drvA)
	reg.Replace([]*device.Session{sessA})

	var sessB *device.Session
	disc := func(ctx context.Context) ([]*device.Session, error) {
		sessB = newFakeSession(t, "fake_2", &fakeDriver{})
		return []*device.Session{sessB}, nil
	}
	srv := newV1Server(t, reg, disc)

	rec := postEnvelope(t, srv, "/api/v1/discover", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	if !drvA.detached.Load() {
		t.Errorf("session A driver.Detach not called on Replace")
	}
	var resp DevicesResponse
	decode(t, rec.Body, &resp)
	if len(resp.Devices) != 1 || resp.Devices[0].ID != "fake_2" {
		t.Fatalf("devices: got %+v, want only fake_2", resp.Devices)
	}
	if !resp.Devices[0].Connected {
		t.Errorf("fake_2 Connected = false, want true")
	}
}

func TestV1DiscoverBusyIs409(t *testing.T) {
	reg := registry.New()
	release := make(chan struct{})
	disc := func(ctx context.Context) ([]*device.Session, error) {
		<-release
		return nil, nil
	}
	srv := newV1Server(t, reg, disc)

	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discover", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !reg.IsDiscovering() {
		if time.Now().After(deadline) {
			close(release)
			<-done
			t.Fatal("discovery gate never acquired")
		}
		time.Sleep(time.Millisecond)
	}

	rec := postEnvelope(t, srv, "/api/v1/discover", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("second discover: got %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "discovery in progress") {
		t.Errorf("body: %s", rec.Body.String())
	}

	close(release)
	<-done
}

func TestV1DiscoverActiveJobIs409(t *testing.T) {
	reg := registry.New()
	// The closure captures drv and reads drv.s at call time — by then
	// newFakeSession's Factory has populated it. Execute runs on the session
	// goroutine, so calling the loop-only Jobs().Start here is sound.
	drv := &fakeDriver{}
	drv.exec = func(cmd string, _ json.RawMessage) (any, *device.CmdError) {
		if cmd == "start" {
			job, cerr := drv.s.Jobs().Start("work", time.Minute)
			if cerr != nil {
				return nil, cerr
			}
			return job, nil
		}
		return nil, device.ErrUnknownCommand(cmd)
	}
	sess := newFakeSession(t, "fake_1", drv)
	reg.Replace([]*device.Session{sess})

	discovered := false
	disc := func(ctx context.Context) ([]*device.Session, error) {
		discovered = true
		return nil, nil
	}
	srv := newV1Server(t, reg, disc)

	rec := postEnvelope(t, srv, "/api/v1/devices/fake_1/command", `{"id":"r1","cmd":"start"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("start command: got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = postEnvelope(t, srv, "/api/v1/discover", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("discover: got %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "job in progress") {
		t.Errorf("body: %s", rec.Body.String())
	}
	if discovered {
		t.Errorf("discover fn ran despite active-job conflict")
	}
	if _, ok := reg.Get("fake_1"); !ok {
		t.Errorf("session cleared from registry; the 409 path must not tear it down")
	}
}

func TestV1DiscoverErrorIs500(t *testing.T) {
	reg := registry.New()
	disc := func(ctx context.Context) ([]*device.Session, error) {
		return nil, errors.New("boom")
	}
	srv := newV1Server(t, reg, disc)

	rec := postEnvelope(t, srv, "/api/v1/discover", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	var body ErrorBody
	decode(t, rec.Body, &body)
	if body.Error != "discovery failed" {
		t.Errorf("error: got %q, want 'discovery failed'", body.Error)
	}
	if body.Detail != "boom" {
		t.Errorf("detail: got %q, want 'boom'", body.Detail)
	}
}

// TestV1CommandsSerializePerSession pins the guarantee the driver
// completion-window guards rest on: concurrent HTTP requests to one device
// execute strictly one at a time on the session goroutine.
func TestV1CommandsSerializePerSession(t *testing.T) {
	var inFlight, maxSeen atomic.Int32
	drv := &fakeDriver{}
	drv.exec = func(cmd string, _ json.RawMessage) (any, *device.CmdError) {
		cur := inFlight.Add(1)
		for {
			m := maxSeen.Load()
			if cur <= m || maxSeen.CompareAndSwap(m, cur) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		return "done", nil
	}
	sess := newFakeSession(t, "fake_1", drv)
	reg := registry.New()
	reg.Replace([]*device.Session{sess})
	srv := newV1Server(t, reg, nil)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"id":"c%d","cmd":"work"}`, n)
			rec := postEnvelope(t, srv, "/api/v1/devices/fake_1/command", body)
			if rec.Code != http.StatusOK {
				t.Errorf("request %d: status %d", n, rec.Code)
			}
		}(i)
	}
	wg.Wait()
	if maxSeen.Load() != 1 {
		t.Fatalf("commands overlapped in the driver: max in-flight %d", maxSeen.Load())
	}
}
