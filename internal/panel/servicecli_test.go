package panel

import (
	"context"
	"encoding/json"
	"errors"
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

func TestServiceCli_GetDevices_IgnoresCacheUserMismatch(t *testing.T) {
	// The property under test: ServiceCli reaches the local REST port
	// regardless of who the cache claims to be anchored to. The cache
	// here is anchored to "alice"; we prove this matters by first
	// confirming that the OLD anchored read path (bootstrap.ReadCache
	// with a mismatched user) rejects the file, and then that the new
	// ServiceCli (which uses ReadCacheRaw) talks to the server anyway.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.DevicesResponse{Devices: []api.DeviceDTO{{ID: "x"}}})
	}))
	defer srv.Close()
	port := mustPortFromURL(t, srv.URL)
	cachePath := seedCache(t, port) // cache anchored to "alice"

	// Counterfactual: the anchored read with a mismatched user must
	// reject the cache. If this stops being true, the contrast in this
	// test is meaningless and the test name lies.
	if _, err := bootstrap.ReadCache(cachePath, "bob"); err == nil {
		t.Fatalf("ReadCache with mismatched user: want ErrCacheMissing, got nil")
	}

	cli := NewServiceCli(cachePath)
	_, status, err := cli.GetDevices(context.Background())
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if status != StatusOK {
		t.Errorf("status: got %v, want StatusOK", status)
	}
}

func TestServiceCli_DisconnectPort_OK(t *testing.T) {
	var gotMethod, gotPath, gotPortQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotPortQuery = r.URL.Query().Get("port")
		_ = json.NewEncoder(w).Encode(api.DisconnectResponse{Released: 1})
	}))
	defer srv.Close()

	port := mustPortFromURL(t, srv.URL)
	cli := NewServiceCli(seedCache(t, port))
	resp, status, err := cli.DisconnectPort(context.Background(), "COM3")
	if err != nil {
		t.Fatalf("DisconnectPort: %v", err)
	}
	if status != StatusOK {
		t.Errorf("status: got %v, want StatusOK", status)
	}
	if resp.Released != 1 {
		t.Errorf("Released: got %d, want 1", resp.Released)
	}
	if gotMethod != "POST" {
		t.Errorf("method: got %s, want POST", gotMethod)
	}
	if gotPath != "/devices/disconnect" {
		t.Errorf("path: got %s, want /devices/disconnect", gotPath)
	}
	if gotPortQuery != "COM3" {
		t.Errorf("port query: got %q, want COM3", gotPortQuery)
	}
}

func TestServiceCli_DisconnectPort_NotFoundReturnsSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"device not found","detail":"COM99"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	port := mustPortFromURL(t, srv.URL)
	cli := NewServiceCli(seedCache(t, port))
	resp, status, err := cli.DisconnectPort(context.Background(), "COM99")
	if !errors.Is(err, ErrPortNotFound) {
		t.Fatalf("err: got %v, want ErrPortNotFound", err)
	}
	if status != StatusOK {
		t.Errorf("status: got %v, want StatusOK (service was reachable)", status)
	}
	if resp.Released != 0 {
		t.Errorf("Released: got %d, want 0", resp.Released)
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
