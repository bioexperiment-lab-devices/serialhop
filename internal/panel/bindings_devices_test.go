//go:build windows

package panel

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestGetDevices_DevicesSliceMarshalsAsEmptyArrayWhenUnreachable is the
// regression test for the silent UI-blank bug:
//
//	When the service is not installed, the bootstrap cache is missing,
//	ServiceCli.do returns early with `out` left at its zero value, the
//	embedded api.DevicesResponse has Devices == nil, and JSON marshals
//	that to `"devices":null`. The SPA's DevicesTab then evaluates
//	`resp.devices.length` on null and React unmounts the whole tree
//	(including TitleBar), leaving the user with a blank, uncloseable
//	window.
//
// The fix point is the binding boundary: bound methods must hand the
// JS side an empty array, not null, regardless of which ServiceCli
// status path was taken.
func TestGetDevices_DevicesSliceMarshalsAsEmptyArrayWhenUnreachable(t *testing.T) {
	app := NewApp()
	// Point ServiceCli at a non-existent cache so baseURL() returns
	// StatusUnreachable — exactly the "service not installed" scenario.
	app.svc = NewServiceCli(filepath.Join(t.TempDir(), "absent.cache.json"))

	res := app.GetDevices()

	if res.Status.Reachable {
		t.Fatalf("Status.Reachable: got true, want false (cache is absent)")
	}
	if res.Devices == nil {
		t.Fatalf("Devices: got nil slice, want non-nil empty slice")
	}

	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"devices":null`) {
		t.Fatalf(`JSON contains "devices":null — must marshal as "devices":[]: %s`, b)
	}
	if !strings.Contains(string(b), `"devices":[]`) {
		t.Fatalf(`JSON missing "devices":[]: %s`, b)
	}
}

func TestDiscover_DevicesSliceMarshalsAsEmptyArrayWhenUnreachable(t *testing.T) {
	app := NewApp()
	app.svc = NewServiceCli(filepath.Join(t.TempDir(), "absent.cache.json"))

	res := app.Discover()
	if res.Devices == nil {
		t.Fatalf("Devices: got nil slice, want non-nil empty slice")
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"devices":null`) {
		t.Fatalf(`JSON contains "devices":null: %s`, b)
	}
}

func TestGetPorts_PortsSliceMarshalsAsEmptyArrayWhenUnreachable(t *testing.T) {
	app := NewApp()
	app.svc = NewServiceCli(filepath.Join(t.TempDir(), "absent.cache.json"))

	res := app.GetPorts()
	if res.Ports == nil {
		t.Fatalf("Ports: got nil slice, want non-nil empty slice")
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"ports":null`) {
		t.Fatalf(`JSON contains "ports":null: %s`, b)
	}
}
