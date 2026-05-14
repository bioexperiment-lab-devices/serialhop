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
	cli := NewServiceCli(seedCache(t, port), staticUser("alice"))
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
	cli := NewServiceCli(filepath.Join(t.TempDir(), "missing.json"), staticUser("alice"))
	_, status, err := cli.GetDevices(context.Background())
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if status != StatusUnreachable {
		t.Errorf("status: got %v, want StatusUnreachable", status)
	}
}

func TestServiceCli_GetDevices_ActualPortZeroReturnsUnreachable(t *testing.T) {
	cli := NewServiceCli(seedCache(t, 0), staticUser("alice"))
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
	cli := NewServiceCli(seedCache(t, 1), staticUser("alice")) // port 1 reserved → conn refused
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
	cli := NewServiceCli(seedCache(t, port), staticUser("alice"))
	_, status, err := cli.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if status != StatusOK {
		t.Errorf("status: got %v, want StatusOK", status)
	}
}

// staticUser returns a userFn that always reports the same user; for tests
// that don't care about credential rotation.
func staticUser(u string) func() string { return func() string { return u } }

// TestServiceCli_PicksUpUserChange covers the first-run race: the panel
// captured an empty user at startup (because the YAML hadn't been written
// yet), and later the user filled in credentials and the service wrote a
// cache anchored to the new user. A captured-at-construction-time user
// would leave the panel stuck on StatusUnreachable forever. With a userFn,
// the next call reads the current user and succeeds.
func TestServiceCli_PicksUpUserChange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(api.DevicesResponse{Devices: []api.DeviceDTO{}})
	}))
	defer srv.Close()

	port := mustPortFromURL(t, srv.URL)
	cachePath := seedCache(t, port) // cache anchored to "alice"

	currentUser := "" // simulate startup before config was saved
	cli := NewServiceCli(cachePath, func() string { return currentUser })

	_, status, _ := cli.GetDevices(context.Background())
	if status != StatusUnreachable {
		t.Errorf("with empty user: got %v, want StatusUnreachable", status)
	}

	currentUser = "alice" // config now has the real user
	_, status, _ = cli.GetDevices(context.Background())
	if status != StatusOK {
		t.Errorf("after user matches cache: got %v, want StatusOK", status)
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
