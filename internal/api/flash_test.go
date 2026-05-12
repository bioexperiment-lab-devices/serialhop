package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

func newTestServerForFlash(t *testing.T) (*Server, *registry.Registry, *labserial.FakeOpener) {
	t.Helper()
	reg := registry.New()
	op := labserial.NewFakeOpener()
	s := New(reg, nil, op, true, nil, false)
	return s, reg, op
}

func TestDisconnect_EmptyRegistry(t *testing.T) {
	s, _, _ := newTestServerForFlash(t)
	req := httptest.NewRequest(http.MethodPost, "/devices/disconnect", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"released":0`) {
		t.Errorf("body: got %q, want released:0", rr.Body.String())
	}
}

func TestDisconnect_PopulatedRegistry(t *testing.T) {
	s, reg, _ := newTestServerForFlash(t)
	reg.Replace([]*registry.Device{
		{ID: "a", Type: "pump", TypeCode: 10, Port: "COM3", Conn: labserial.NewFakePort("COM3")},
		{ID: "b", Type: "valve", TypeCode: 30, Port: "COM4", Conn: labserial.NewFakePort("COM4")},
	})
	req := httptest.NewRequest(http.MethodPost, "/devices/disconnect", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"released":2`) {
		t.Errorf("body: %q", rr.Body.String())
	}
	if len(reg.List()) != 0 {
		t.Errorf("registry not empty after disconnect")
	}
}

func TestDetailedPorts_ReturnsAnnotatedPorts(t *testing.T) {
	s, reg, op := newTestServerForFlash(t)
	op.Add(labserial.NewFakePort("COM3"))
	op.Add(labserial.NewFakePort("COM4"))
	op.SetDetail("COM3", labserial.DetailedPort{
		Name: "COM3", IsUSB: true, VID: "2341", PID: "0043", Product: "Arduino Uno",
	})
	reg.Replace([]*registry.Device{
		{ID: "pump_1", Type: "pump", TypeCode: 10, Port: "COM3", Conn: labserial.NewFakePort("COM3")},
	})

	req := httptest.NewRequest(http.MethodGet, "/serial/ports/detailed", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: %d, body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"name":"COM3"`) {
		t.Errorf("missing COM3 in body: %s", body)
	}
	if !strings.Contains(body, `"name":"COM4"`) {
		t.Errorf("missing COM4 in body: %s", body)
	}
	if !strings.Contains(body, `"discovered":true`) {
		t.Errorf("expected discovered:true for COM3: %s", body)
	}
	if !strings.Contains(body, `"device_id":"pump_1"`) {
		t.Errorf("expected device_id pump_1: %s", body)
	}
}
