package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/khamitovdr/lab_devices_client/internal/registry"
	"github.com/khamitovdr/lab_devices_client/internal/serial"
)

// fakeDiscoverFn returns a closure suitable for Server.discover.
func fakeDiscoverFn(devs []*registry.Device, err error) DiscoverFn {
	return func(ctx context.Context) ([]*registry.Device, error) {
		return devs, err
	}
}

func newTestServer(t *testing.T, reg *registry.Registry, disc DiscoverFn) http.Handler {
	t.Helper()
	if disc == nil {
		disc = fakeDiscoverFn(nil, nil)
	}
	return New(reg, disc).Handler()
}

func decode(t *testing.T, body io.Reader, into any) {
	t.Helper()
	if err := json.NewDecoder(body).Decode(into); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestGetDevices_Empty(t *testing.T) {
	reg := registry.New()
	srv := newTestServer(t, reg, nil)
	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp DevicesResponse
	decode(t, rec.Body, &resp)
	if len(resp.Devices) != 0 {
		t.Errorf("devices: got %v, want []", resp.Devices)
	}
	if resp.DiscoveredAt != nil {
		t.Errorf("discovered_at: got %v, want nil", resp.DiscoveredAt)
	}
}

func TestGetDevices_AfterDiscovery(t *testing.T) {
	reg := registry.New()
	d := &registry.Device{
		ID: "pump_1", Type: "pump", TypeCode: 10, Port: "COM3",
		Conn:   serial.NewFakePort("COM3"),
		Opener: serial.NewFakeOpener(),
	}
	reg.Replace([]*registry.Device{d})

	srv := newTestServer(t, reg, nil)
	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp DevicesResponse
	decode(t, rec.Body, &resp)
	if len(resp.Devices) != 1 || resp.Devices[0].ID != "pump_1" {
		t.Errorf("devices: %v", resp.Devices)
	}
	if resp.DiscoveredAt == nil {
		t.Errorf("discovered_at: got nil, want timestamp")
	}
}

func TestPostDiscover_Success(t *testing.T) {
	reg := registry.New()
	dev := &registry.Device{
		ID: "valve_1", Type: "valve", TypeCode: 30, Port: "COM4",
		Conn:   serial.NewFakePort("COM4"),
		Opener: serial.NewFakeOpener(),
	}
	srv := newTestServer(t, reg, fakeDiscoverFn([]*registry.Device{dev}, nil))

	req := httptest.NewRequest(http.MethodPost, "/discover", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp DevicesResponse
	decode(t, rec.Body, &resp)
	if len(resp.Devices) != 1 || resp.Devices[0].ID != "valve_1" {
		t.Errorf("devices: %v", resp.Devices)
	}
	// Registry must reflect the discovery output.
	if got, ok := reg.Get("valve_1"); !ok || got.Port != "COM4" {
		t.Errorf("registry not updated: got=%v ok=%v", got, ok)
	}
}

func TestPostDiscover_AlreadyRunning(t *testing.T) {
	reg := registry.New()
	if !reg.LockDiscovery() {
		t.Fatal("setup: LockDiscovery should succeed")
	}
	defer reg.UnlockDiscovery()
	srv := newTestServer(t, reg, nil)

	req := httptest.NewRequest(http.MethodPost, "/discover", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "discovery in progress") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func makeFakeDevice(t *testing.T, id, port string, typeCode byte, fp *serial.FakePort, opener *serial.FakeOpener) *registry.Device {
	t.Helper()
	if opener == nil {
		opener = serial.NewFakeOpener()
	}
	if fp == nil {
		fp = serial.NewFakePort(port)
	}
	opener.Add(fp)
	typeName := map[byte]string{10: "pump", 30: "valve", 70: "densitometer"}[typeCode]
	return &registry.Device{
		ID: id, Type: typeName, TypeCode: typeCode, Port: port,
		Conn: fp, Opener: opener,
	}
}

func postCmd(t *testing.T, srv http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestPostCommand_DeviceNotFound(t *testing.T) {
	reg := registry.New()
	srv := newTestServer(t, reg, nil)
	rec := postCmd(t, srv, "/devices/pump_1/command", `{"command":[1,2,3,4,0]}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rec.Code)
	}
}

func TestPostCommand_HappyPathWithReply(t *testing.T) {
	reg := registry.New()
	fp := serial.NewFakePort("COM3")
	fp.Feed([]byte{10, 1, 2, 3})
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, fp, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command", `{"command":[1,2,3,4,0]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp CommandResponse
	decode(t, rec.Body, &resp)
	want := []int{10, 1, 2, 3}
	if len(resp.Response) != len(want) {
		t.Fatalf("response: got %v, want %v", resp.Response, want)
	}
	for i := range want {
		if resp.Response[i] != want[i] {
			t.Errorf("response[%d]: got %d, want %d", i, resp.Response[i], want[i])
		}
	}
	// Verify the device received the command bytes.
	written := fp.Written()
	wantWritten := []byte{1, 2, 3, 4, 0}
	if string(written) != string(wantWritten) {
		t.Errorf("written: got %v, want %v", written, wantWritten)
	}
}

func TestPostCommand_NoReplyReturnsEmpty(t *testing.T) {
	reg := registry.New()
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, nil, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command?timeout_ms=20", `{"command":[1,2,3]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp CommandResponse
	decode(t, rec.Body, &resp)
	if len(resp.Response) != 0 {
		t.Errorf("response: got %v, want []", resp.Response)
	}
}

func TestPostCommand_WaitForResponseFalse(t *testing.T) {
	reg := registry.New()
	fp := serial.NewFakePort("COM3")
	fp.Feed([]byte{10, 1, 2, 3}) // would be returned, but caller opts out
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, fp, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command?wait_for_response=false", `{"command":[1,2,3]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp CommandResponse
	decode(t, rec.Body, &resp)
	if len(resp.Response) != 0 {
		t.Errorf("response: got %v, want []", resp.Response)
	}
}

func TestPostCommand_ExpectedBytesStopsEarly(t *testing.T) {
	reg := registry.New()
	fp := serial.NewFakePort("COM3")
	fp.Feed([]byte{10, 1, 2, 3, 99, 99, 99}) // more than 4 — should stop at 4
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, fp, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command?expected_response_bytes=4", `{"command":[1,2,3]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp CommandResponse
	decode(t, rec.Body, &resp)
	if len(resp.Response) != 4 {
		t.Errorf("response: got %v, want 4 bytes", resp.Response)
	}
}

func TestPostCommand_DeviceBusy(t *testing.T) {
	reg := registry.New()
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, nil, nil)
	reg.Replace([]*registry.Device{dev})
	if !dev.TryLock() {
		t.Fatal("setup: TryLock should succeed")
	}
	defer dev.Unlock()
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command", `{"command":[1,2,3]}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "device busy") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func TestPostCommand_BadByte(t *testing.T) {
	reg := registry.New()
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, nil, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command", `{"command":[300,1,2]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
}

func TestPostCommand_BadQueryParam(t *testing.T) {
	reg := registry.New()
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, nil, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command?timeout_ms=99999999", `{"command":[1]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
}
