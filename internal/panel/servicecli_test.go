package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/api"
	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

func seedCache(t *testing.T, port int) string {
	t.Helper()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.json")
	c := bootstrap.Cache{
		Version:        1,
		FetchedAt:      "2026-05-13T00:00:00Z",
		User:           "alice",
		ServerInfo:     labbridge.ServerInfo{ChiselListenPort: 7000},
		RemotePort:     8089,
		ActualRestPort: port,
	}
	if err := bootstrap.WriteCache(cachePath, c); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return cachePath
}

func TestServiceCli_GetDevices_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/devices" {
			t.Errorf("path: got %s, want /devices", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(api.DevicesResponse{
			Devices: []api.DeviceDTO{{ID: "pump_1", Type: "pump", Port: "COM5"}},
		})
	}))
	defer srv.Close()

	port := mustPortFromURL(t, srv.URL)
	cli := NewServiceCli(seedCache(t, port))
	resp, status, err := cli.GetDevices(context.Background())
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if status != StatusOK {
		t.Errorf("status: got %v, want StatusOK", status)
	}
	if len(resp.Devices) != 1 || resp.Devices[0].ID != "pump_1" {
		t.Errorf("unexpected devices: %+v", resp.Devices)
	}
}

func TestServiceCli_GetDevices_CacheMissingReturnsUnreachable(t *testing.T) {
	cli := NewServiceCli(filepath.Join(t.TempDir(), "missing.json"))
	_, status, err := cli.GetDevices(context.Background())
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if status != StatusUnreachable {
		t.Errorf("status: got %v, want StatusUnreachable", status)
	}
}

func TestServiceCli_GetDevices_ActualPortZeroReturnsUnreachable(t *testing.T) {
	cli := NewServiceCli(seedCache(t, 0))
	_, status, err := cli.GetDevices(context.Background())
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if status != StatusUnreachable {
		t.Errorf("status: got %v, want StatusUnreachable", status)
	}
}

func TestServiceCli_GetDevices_ConnectionRefusedReturnsServiceDown(t *testing.T) {
	// Use a port we know nothing is listening on.
	cli := NewServiceCli(seedCache(t, 1)) // port 1 reserved → conn refused
	_, status, err := cli.GetDevices(context.Background())
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if status != StatusServiceDown {
		t.Errorf("status: got %v, want StatusServiceDown", status)
	}
}

func TestServiceCli_Discover_PostsToDiscover(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/discover" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(api.DevicesResponse{Devices: []api.DeviceDTO{}})
	}))
	defer srv.Close()
	port := mustPortFromURL(t, srv.URL)
	cli := NewServiceCli(seedCache(t, port))
	_, status, err := cli.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if status != StatusOK {
		t.Errorf("status: got %v, want StatusOK", status)
	}
}

// TestServiceCli_IgnoresCacheUserAnchor proves the panel reaches the
// service even when the cache was written under a DIFFERENT lab-bridge
// user than the panel's current config — e.g., the operator edited
// lab_bridge.user in the YAML but hasn't restarted the service yet, or
// the panel reads cfg.LabBridge.User before the user has filled it in.
// Production reports of "Can't reach the local service" all reduce to
// this scenario; the user-anchor was load-bearing for the service but
// pointless for the panel's local-port lookup.
func TestServiceCli_IgnoresCacheUserAnchor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(api.DevicesResponse{Devices: []api.DeviceDTO{}})
	}))
	defer srv.Close()

	// seedCache writes the file with User="alice"; the panel calling
	// code has no notion of "alice" — and that's the point: it should
	// resolve the port anyway.
	cli := NewServiceCli(seedCache(t, mustPortFromURL(t, srv.URL)))
	_, status, _ := cli.GetDevices(context.Background())
	if status != StatusOK {
		t.Errorf("with mismatched/unknown cache user: got %v, want StatusOK", status)
	}
}

func mustPortFromURL(t *testing.T, raw string) int {
	t.Helper()
	idx := strings.LastIndex(raw, ":")
	if idx < 0 {
		t.Fatalf("can't parse port from %q", raw)
	}
	p, err := strconv.Atoi(raw[idx+1:])
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return p
}
