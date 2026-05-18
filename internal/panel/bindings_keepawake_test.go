//go:build windows

package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestGetKeepAwake_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"active": true})
	}))
	t.Cleanup(srv.Close)

	a := &App{
		svc: NewServiceCli(seedCache(t, mustPortFromURL(t, srv.URL))),
		ctx: context.Background(),
	}
	got := a.GetKeepAwake()
	if !got.Reachable {
		t.Errorf("Reachable = false; reason=%q", got.Reason)
	}
	if !got.Active {
		t.Errorf("Active = false")
	}
	if got.ErrorMessage != "" {
		t.Errorf("ErrorMessage = %q", got.ErrorMessage)
	}
}

func TestEnableKeepAwake_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"active": true})
	}))
	t.Cleanup(srv.Close)

	a := &App{
		svc: NewServiceCli(seedCache(t, mustPortFromURL(t, srv.URL))),
		ctx: context.Background(),
	}
	got := a.EnableKeepAwake()
	if !got.Reachable || !got.Active {
		t.Errorf("got %+v", got)
	}
}

func TestDisableKeepAwake_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"active": false})
	}))
	t.Cleanup(srv.Close)

	a := &App{
		svc: NewServiceCli(seedCache(t, mustPortFromURL(t, srv.URL))),
		ctx: context.Background(),
	}
	got := a.DisableKeepAwake()
	if !got.Reachable {
		t.Errorf("Reachable = false")
	}
	if got.Active {
		t.Errorf("Active = true after disable")
	}
}

func TestKeepAwake_ServiceDown(t *testing.T) {
	// Server that we close before the test calls in — transport-level
	// failure inside ServiceCli.do.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	port := mustPortFromURL(t, srv.URL)
	srv.Close()

	a := &App{
		svc: NewServiceCli(seedCache(t, port)),
		ctx: context.Background(),
	}
	got := a.EnableKeepAwake()
	if got.Reachable {
		t.Errorf("Reachable = true on closed server")
	}
	if got.Reason != "service_down" {
		t.Errorf("Reason = %q, want service_down", got.Reason)
	}
}

func TestKeepAwake_Unreachable_MissingCache(t *testing.T) {
	a := &App{
		svc: NewServiceCli(filepath.Join(t.TempDir(), "absent.cache.json")),
		ctx: context.Background(),
	}
	got := a.GetKeepAwake()
	if got.Reachable {
		t.Errorf("Reachable = true with missing cache")
	}
	if got.Reason != "unreachable" {
		t.Errorf("Reason = %q, want unreachable", got.Reason)
	}
}
