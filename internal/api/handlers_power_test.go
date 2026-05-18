package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

// errorKeepAwake is a KeepAwake that returns the supplied error from
// the named operation. Used to exercise the 500 path in the enable /
// disable handlers without relying on real syscall failures.
type errorKeepAwake struct {
	active     bool
	enableErr  error
	disableErr error
}

func (e *errorKeepAwake) Enable(_ string) error {
	if e.enableErr != nil {
		return e.enableErr
	}
	e.active = true
	return nil
}
func (e *errorKeepAwake) Disable() error {
	if e.disableErr != nil {
		return e.disableErr
	}
	e.active = false
	return nil
}
func (e *errorKeepAwake) Active() bool { return e.active }
func (e *errorKeepAwake) Close() error { return nil }

var errSyscallFake = errors.New("synthetic failure")

func powerTestServerWith(t *testing.T, ka power.KeepAwake) http.Handler {
	t.Helper()
	reg := registry.New()
	return New(reg, fakeDiscoverFn(nil, nil), serial.NewFakeOpener(), false, nil, false, ka).Handler()
}

func TestEnableKeepAwake_FlipsActive(t *testing.T) {
	srv, ka := powerTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/power/keep-awake/enable", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp keepAwakeStatusBody
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Active {
		t.Errorf("response active = false")
	}
	if !ka.Active() {
		t.Errorf("ka.Active() = false")
	}
}

func TestEnableKeepAwake_IsIdempotent(t *testing.T) {
	srv, _ := powerTestServer(t)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/power/keep-awake/enable", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("call %d status: got %d", i, rec.Code)
		}
	}
}

func TestEnableKeepAwake_Returns500OnSyscallFailure(t *testing.T) {
	ka := &errorKeepAwake{enableErr: errSyscallFake}
	srv := powerTestServerWith(t, ka)
	req := httptest.NewRequest(http.MethodPost, "/power/keep-awake/enable", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Fatalf("status: got %d, want 500", rec.Code)
	}
	var body ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "keep-awake enable failed" {
		t.Errorf("error code: got %q", body.Error)
	}
	if !strings.Contains(body.Detail, "synthetic failure") {
		t.Errorf("detail: got %q, want substring 'synthetic failure'", body.Detail)
	}
	if ka.Active() {
		t.Errorf("ka.Active() = true after failed Enable")
	}
}
