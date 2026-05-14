//go:build windows

package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
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

	got := app.GetPorts()
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

	got := app.GetDevices()
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

	got := app.Discover()
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

	got := app.GetPorts()
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
	got := app.GetPorts()
	if got.Status.Reachable {
		t.Errorf("expected Reachable=false when cache is missing, got reachable=true")
	}
	if got.Status.Reason != "unreachable" {
		t.Errorf("Status.Reason: got %q, want %q", got.Status.Reason, "unreachable")
	}
}

// TestApp_Diagnostics_HTTPProbe_OK exercises the new HTTP roundtrip
// probe baked into Diagnostics(). When the cache is sane and a service
// is actually serving, http_probe_status must come back "ok" so we can
// distinguish "cache resolves a port but loopback is dead" (the
// scenario the user keeps hitting) from "cache resolves a port and
// loopback works fine."
func TestApp_Diagnostics_HTTPProbe_OK(t *testing.T) {
	srv := fakeServiceServer(t)
	defer srv.Close()
	app := newAppPointedAt(t, srv)

	d := app.Diagnostics()
	if d.BaseURLStatus != "ok" {
		t.Errorf("BaseURLStatus: got %q, want ok", d.BaseURLStatus)
	}
	if d.HTTPProbeStatus != "ok" {
		t.Errorf("HTTPProbeStatus: got %q, want ok; error=%q", d.HTTPProbeStatus, d.HTTPProbeError)
	}
	if d.HTTPProbePortsLen != 1 {
		t.Errorf("HTTPProbePortsLen: got %d, want 1", d.HTTPProbePortsLen)
	}
}

// TestApp_Diagnostics_HTTPProbe_LoopbackDead is the regression test
// for the specific shape of the user's bug: cache resolves correctly
// (BaseURLStatus=ok), but no one is listening on the resolved port.
// The HTTP probe must surface a service_down status AND a non-empty
// error string so the user-facing diagnostics blob explains WHY (e.g.
// "connect: connection refused", "i/o timeout").
func TestApp_Diagnostics_HTTPProbe_LoopbackDead(t *testing.T) {
	// Seed a cache that points at a port we know nothing is bound to.
	// Port 1 (TCPMUX) is reserved and usually refused immediately.
	cachePath := seedCache(t, 1)
	app := NewApp()
	app.svc = NewServiceCli(cachePath)

	d := app.Diagnostics()
	if d.BaseURLStatus != "ok" {
		t.Fatalf("BaseURLStatus: got %q, want ok (cache should resolve)", d.BaseURLStatus)
	}
	if d.HTTPProbeStatus == "ok" {
		t.Errorf("HTTPProbeStatus: got ok; want service_down (nothing should be listening on :1)")
	}
	if d.HTTPProbeError == "" {
		t.Errorf("HTTPProbeError empty; want a transport error message")
	}
}

// TestApp_NoBoundMethodTakesContextContext is the regression test for
// the *actual* "Can't reach the local service" bug. Wails v2.12.0 does
// not auto-inject context.Context as the first argument for methods
// reached through embedding (main.App embeds *panel.App). The JS-side
// shim in src/wails/go/main/App.ts always calls bindings with zero
// arguments; if any bound method expects a context.Context, the
// Wails bridge rejects the call with:
//
//	"error parsing arguments: received 0 arguments to method 'main.App.X',
//	 expected 1"
//
// which the SPA never sees as an error in a try/catch and silently
// drops back to its initial reachable=false state — producing the
// "Can't reach the local service" banner that survived three prior
// speculative fixes. Walk every exported method on *App and fail if
// it takes a context.Context. The unblock for any future need-a-ctx
// case is `app.callCtx()` inside the method body.
func TestApp_NoBoundMethodTakesContextContext(t *testing.T) {
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	appType := reflect.TypeOf(&App{})
	for i := 0; i < appType.NumMethod(); i++ {
		m := appType.Method(i)
		// receiver counts as the first input, so real args start at 1.
		for j := 1; j < m.Type.NumIn(); j++ {
			if m.Type.In(j).Implements(ctxType) || m.Type.In(j) == ctxType {
				t.Errorf("App.%s takes %s as parameter %d — Wails v2.12.0 won't auto-inject through embedded methods, so the bridge will reject every JS-side invocation. Drop the parameter and call a.callCtx() in the body instead.",
					m.Name, m.Type.In(j), j-1)
				break
			}
			// Implements check above catches the interface form. Also
			// reject struct types whose name contains "Context" as a
			// belt-and-braces signal that someone tried again — the
			// stdlib alias is the only legitimate match, which the
			// Implements check covers; anything else here is a likely
			// typo or wrapper that won't behave.
			if name := m.Type.In(j).String(); strings.Contains(strings.ToLower(name), "context") {
				t.Errorf("App.%s takes %s (likely a context type) as parameter %d — see comment in this test for why that fails through Wails.",
					m.Name, name, j-1)
				break
			}
		}
	}
}
