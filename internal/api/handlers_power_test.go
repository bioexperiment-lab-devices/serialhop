package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/power"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// powerTestServer builds a Server backed by the cross-platform power
// fake. The returned ka is exposed so tests can pre-flip Active() to
// exercise the GET handler's state reporting.
func powerTestServer(t *testing.T) (http.Handler, power.KeepAwake) {
	t.Helper()
	ka, err := power.New()
	if err != nil {
		t.Fatalf("power.New: %v", err)
	}
	t.Cleanup(func() { _ = ka.Close() })
	reg := registry.New()
	srv := New(reg, fakeDiscoverFn(nil, nil), serial.NewFakeOpener(), false, nil, false, ka).Handler()
	return srv, ka
}

func TestGetKeepAwake_ReturnsCurrentState(t *testing.T) {
	srv, ka := powerTestServer(t)

	// Default: inactive.
	req := httptest.NewRequest(http.MethodGet, "/power/keep-awake", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET status: got %d", rec.Code)
	}
	var resp struct {
		Active bool `json:"active"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Active {
		t.Errorf("active = true on cold server, want false")
	}

	// Flip Active and reread.
	if err := ka.Enable("test"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/power/keep-awake", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET status after Enable: got %d", rec.Code)
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Active {
		t.Errorf("active = false after Enable, want true")
	}
}
