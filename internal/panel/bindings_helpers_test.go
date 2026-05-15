package panel

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/api"
)

// These tests gate the cross-platform helpers used by the Wails bindings
// (bindings.go is //go:build windows, so its end-to-end tests don't run
// in Linux CI). Keep the helpers narrow and tested here so any future
// regression of "slice marshals as null" is caught on every PR.

func TestNormalizeDevicesResponse_NilDevicesBecomesEmptyArrayInJSON(t *testing.T) {
	got := normalizeDevicesResponse(api.DevicesResponse{})
	if got.Devices == nil {
		t.Fatalf("Devices: got nil, want non-nil empty slice")
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"devices":null`) {
		t.Fatalf(`marshal still emits "devices":null: %s`, b)
	}
	if !strings.Contains(string(b), `"devices":[]`) {
		t.Fatalf(`expected "devices":[] in: %s`, b)
	}
}

func TestNormalizeDevicesResponse_PreservesNonNilSliceIdentity(t *testing.T) {
	in := api.DevicesResponse{Devices: []api.DeviceDTO{{ID: "d1"}}}
	got := normalizeDevicesResponse(in)
	if len(got.Devices) != 1 || got.Devices[0].ID != "d1" {
		t.Fatalf("non-nil slice should pass through unchanged: %+v", got)
	}
}

func TestNormalizeDetailedPortsResponse_NilPortsBecomesEmptyArrayInJSON(t *testing.T) {
	got := normalizeDetailedPortsResponse(api.DetailedPortsResponse{})
	if got.Ports == nil {
		t.Fatalf("Ports: got nil, want non-nil empty slice")
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"ports":null`) {
		t.Fatalf(`marshal still emits "ports":null: %s`, b)
	}
	if !strings.Contains(string(b), `"ports":[]`) {
		t.Fatalf(`expected "ports":[] in: %s`, b)
	}
}

func TestNormalizeDetailedPortsResponse_PreservesNonNilSliceIdentity(t *testing.T) {
	in := api.DetailedPortsResponse{Ports: []api.DetailedPortDTO{{Name: "COM1"}}}
	got := normalizeDetailedPortsResponse(in)
	if len(got.Ports) != 1 || got.Ports[0].Name != "COM1" {
		t.Fatalf("non-nil slice should pass through unchanged: %+v", got)
	}
}
