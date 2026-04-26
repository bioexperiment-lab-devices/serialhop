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
