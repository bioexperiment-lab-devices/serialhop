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

func TestFilterDetailedPorts(t *testing.T) {
	sample := api.DetailedPortsResponse{Ports: []api.DetailedPortDTO{
		{Name: "COM1"}, {Name: "COM3"}, {Name: "COM4"}, {Name: "COM7"},
	}}
	cases := []struct {
		name             string
		include, exclude []string
		wantNamesInOrder []string
	}{
		{"empty config returns unchanged", nil, nil, []string{"COM1", "COM3", "COM4", "COM7"}},
		{"include keeps only listed", []string{"COM3", "COM7"}, nil, []string{"COM3", "COM7"}},
		{"exclude drops listed", nil, []string{"COM1", "COM4"}, []string{"COM3", "COM7"}},
		{"include wins when both set", []string{"COM3"}, []string{"COM7"}, []string{"COM3"}},
		{"include with no matches → empty result", []string{"COM99"}, nil, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterDetailedPorts(sample, tc.include, tc.exclude)
			if len(got.Ports) != len(tc.wantNamesInOrder) {
				t.Fatalf("len: got %d, want %d (%+v)", len(got.Ports), len(tc.wantNamesInOrder), got.Ports)
			}
			for i, n := range tc.wantNamesInOrder {
				if got.Ports[i].Name != n {
					t.Errorf("ports[%d]: got %q, want %q", i, got.Ports[i].Name, n)
				}
			}
		})
	}
}

func TestFilterDetailedPorts_DoesNotMutateInput(t *testing.T) {
	in := api.DetailedPortsResponse{Ports: []api.DetailedPortDTO{
		{Name: "COM1"}, {Name: "COM3"},
	}}
	_ = filterDetailedPorts(in, []string{"COM3"}, nil)
	if len(in.Ports) != 2 || in.Ports[0].Name != "COM1" || in.Ports[1].Name != "COM3" {
		t.Fatalf("input mutated: %+v", in.Ports)
	}
}
