//go:build windows

package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/api"
)

// These are the regression tests for the "Can't reach the local service"
// bug in the Devices and Ports tabs. Unlike the existing ServiceCli unit
// tests, they go through the actual *App method that Wails exposes to
// the SPA — exercising the embedded-struct serialization shape that the
// pure ServiceCli tests skip. They're Windows-tagged because the App
// type only compiles on Windows (the rest of the Wails app surface is
// build-tag-gated there).
//
// When this file lives on a non-Windows host, it is excluded by the
// build tag and the existing tests still run. The Windows CI workflow
// (.github/workflows/windows-e2e.yml) builds and runs these tests on a
// windows-latest runner so any regression in the binding layer fails
// before reaching a release.

// fakeServiceServer stands in for the local SerialHop service: it
// answers /serial/ports/detailed, /devices, and /discover with stub
// payloads on a real httptest.Server bound to 127.0.0.1.
func fakeServiceServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/serial/ports/detailed", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(api.DetailedPortsResponse{
			Ports: []api.DetailedPortDTO{{Name: "COM3", IsUSB: true, VID: "2341", PID: "0043"}},
		})
	})
	mux.HandleFunc("/devices", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(api.DevicesResponse{
			Devices: []api.DeviceDTO{{ID: "pump_1", Type: "pump", Port: "COM3"}},
		})
	})
	mux.HandleFunc("/discover", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(api.DevicesResponse{
			Devices: []api.DeviceDTO{{ID: "pump_1", Type: "pump", Port: "COM3"}},
		})
	})
	return httptest.NewServer(mux)
}

// newAppPointedAt builds an App wired to talk to a seeded cache + fake
// service. ctx is left nil so emitEvent calls no-op (Wails runtime isn't
// initialized in tests).
func newAppPointedAt(t *testing.T, srv *httptest.Server) *App {
	t.Helper()
	port := mustPortFromURL(t, srv.URL)
	cachePath := seedCache(t, port)
	app := NewApp()
	app.svc = NewServiceCli(cachePath)
	return app
}

func TestApp_GetPorts_ReachesServiceEndToEnd(t *testing.T) {
	srv := fakeServiceServer(t)
	defer srv.Close()
	app := newAppPointedAt(t, srv)

	got := app.GetPorts(context.Background())
	if !got.Status.Reachable {
		t.Fatalf("Status.Reachable=false, reason=%q; want true", got.Status.Reason)
	}
	if len(got.Ports) != 1 || got.Ports[0].Name != "COM3" {
		t.Errorf("Ports: got %+v, want one COM3 entry", got.Ports)
	}
}

func TestApp_GetDevices_ReachesServiceEndToEnd(t *testing.T) {
	srv := fakeServiceServer(t)
	defer srv.Close()
	app := newAppPointedAt(t, srv)

	got := app.GetDevices(context.Background())
	if !got.Status.Reachable {
		t.Fatalf("Status.Reachable=false, reason=%q; want true", got.Status.Reason)
	}
	if len(got.Devices) != 1 || got.Devices[0].ID != "pump_1" {
		t.Errorf("Devices: got %+v, want one pump_1 entry", got.Devices)
	}
}

func TestApp_Discover_ReachesServiceEndToEnd(t *testing.T) {
	srv := fakeServiceServer(t)
	defer srv.Close()
	app := newAppPointedAt(t, srv)

	got := app.Discover(context.Background())
	if !got.Status.Reachable {
		t.Fatalf("Status.Reachable=false, reason=%q; want true", got.Status.Reason)
	}
	if len(got.Devices) != 1 || got.Devices[0].ID != "pump_1" {
		t.Errorf("Devices: got %+v, want one pump_1 entry", got.Devices)
	}
}

// TestApp_GetPorts_JSONShapeMatchesSPAContract asserts the exact wire
// shape Wails will JSON-encode for the SPA. The Devices/Ports tabs in
// the SPA depend on:
//
//	{ "ports": [...], "status": { "reachable": bool, "reason": "..." } }
//
// (embedded api.DetailedPortsResponse fields promoted alongside Status).
// If a future refactor breaks that flattening — or if an encoder quirk
// hides Status under the embedded type name — the SPA will see
// `resp.status` as undefined, treat the tab as unreachable, and render
// "Can't reach the local service" even though the service is up. The
// test serializes the result with encoding/json (the same package Wails
// uses) and asserts the resulting JSON has both top-level keys.
func TestApp_GetPorts_JSONShapeMatchesSPAContract(t *testing.T) {
	srv := fakeServiceServer(t)
	defer srv.Close()
	app := newAppPointedAt(t, srv)

	got := app.GetPorts(context.Background())
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := decoded["ports"]; !ok {
		t.Errorf("missing top-level \"ports\" key in %s", string(raw))
	}
	status, ok := decoded["status"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing top-level \"status\" object in %s", string(raw))
	}
	if reachable, ok := status["reachable"].(bool); !ok || !reachable {
		t.Errorf("status.reachable: got %v (type %T), want true; raw=%s", status["reachable"], status["reachable"], string(raw))
	}
}

// TestApp_GetPorts_CacheMissingSurfacesUnreachable double-checks the
// failure path: when the cache file doesn't exist, the binding must
// return Reachable=false with reason="unreachable" (the value the SPA
// keys on for the "Can't reach the local service" banner).
func TestApp_GetPorts_CacheMissingSurfacesUnreachable(t *testing.T) {
	app := NewApp()
	app.svc = NewServiceCli(filepath.Join(t.TempDir(), "absent.json"))
	got := app.GetPorts(context.Background())
	if got.Status.Reachable {
		t.Errorf("expected Reachable=false when cache is missing, got reachable=true")
	}
	if got.Status.Reason != "unreachable" {
		t.Errorf("Status.Reason: got %q, want %q", got.Status.Reason, "unreachable")
	}
}
