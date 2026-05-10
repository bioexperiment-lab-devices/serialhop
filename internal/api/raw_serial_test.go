package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// rawSrv builds an api.Server.Handler() with the given registry, opener, and
// raw_serial.enabled flag. Used by every test in this file.
func rawSrv(t *testing.T, reg *registry.Registry, opener serial.Opener, enabled bool) http.Handler {
	t.Helper()
	return New(reg, fakeDiscoverFn(nil, nil), opener, enabled).Handler()
}

func TestGetSerialPorts_DisabledReturns403(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	srv := rawSrv(t, reg, opener, false)

	req := httptest.NewRequest(http.MethodGet, "/serial/ports", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "raw serial disabled") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func TestGetSerialPorts_EmptyRegistry(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	opener.Add(serial.NewFakePort("COM5"))
	srv := rawSrv(t, reg, opener, true)

	req := httptest.NewRequest(http.MethodGet, "/serial/ports", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp PortsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Ports) != 2 {
		t.Fatalf("ports: got %d, want 2 (%v)", len(resp.Ports), resp.Ports)
	}
	if !sort.SliceIsSorted(resp.Ports, func(i, j int) bool { return resp.Ports[i].Name < resp.Ports[j].Name }) {
		t.Errorf("ports not sorted by name: %v", resp.Ports)
	}
	for _, p := range resp.Ports {
		if p.Discovered || p.DeviceID != "" {
			t.Errorf("port %q: got discovered=%v device_id=%q, want discovered=false device_id=\"\"", p.Name, p.Discovered, p.DeviceID)
		}
	}
}

func TestGetSerialPorts_AnnotatesDiscoveredDevices(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	opener.Add(serial.NewFakePort("COM5"))
	opener.Add(serial.NewFakePort("COM7"))

	reg.Replace([]*registry.Device{
		{ID: "pump_1", Type: "pump", TypeCode: 10, Port: "COM3", Conn: serial.NewFakePort("COM3"), Opener: opener},
		{ID: "valve_1", Type: "valve", TypeCode: 30, Port: "COM7", Conn: serial.NewFakePort("COM7"), Opener: opener},
	})

	srv := rawSrv(t, reg, opener, true)
	req := httptest.NewRequest(http.MethodGet, "/serial/ports", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp PortsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]struct {
		discovered bool
		id         string
	}{
		"COM3": {true, "pump_1"},
		"COM5": {false, ""},
		"COM7": {true, "valve_1"},
	}
	for _, p := range resp.Ports {
		w, ok := want[p.Name]
		if !ok {
			t.Errorf("unexpected port %q in response", p.Name)
			continue
		}
		if p.Discovered != w.discovered || p.DeviceID != w.id {
			t.Errorf("port %q: got discovered=%v id=%q, want discovered=%v id=%q",
				p.Name, p.Discovered, p.DeviceID, w.discovered, w.id)
		}
	}
}
