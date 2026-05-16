//go:build windows

package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/api"
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

func TestDisconnectPort_OKReleasesOne(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(api.DisconnectResponse{Released: 1})
	}))
	defer srv.Close()

	app := NewApp()
	app.svc = NewServiceCli(seedCache(t, mustPortFromURL(t, srv.URL)))

	res := app.DisconnectPort("COM3")
	if res.Released != 1 {
		t.Errorf("Released: got %d, want 1", res.Released)
	}
	if !res.Status.Reachable {
		t.Errorf("Status.Reachable: got false, want true")
	}
	if gotPath != "/devices/disconnect/by-port/COM3" {
		t.Errorf("path: got %s, want /devices/disconnect/by-port/COM3", gotPath)
	}
}

func TestDisconnectPort_NotFoundTreatedAsBenign(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"device not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	app := NewApp()
	app.svc = NewServiceCli(seedCache(t, mustPortFromURL(t, srv.URL)))

	res := app.DisconnectPort("COM99")
	if res.Released != 0 {
		t.Errorf("Released: got %d, want 0", res.Released)
	}
	// 404 means the service was reachable; only the device wasn't.
	if !res.Status.Reachable {
		t.Errorf("Status.Reachable: got false, want true (404 is reachable + missing)")
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
