package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
)

func decode(t *testing.T, body io.Reader, into any) {
	t.Helper()
	if err := json.NewDecoder(body).Decode(into); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestGetAgentInfo_200JSON(t *testing.T) {
	reg := registry.New()
	srv := newV1Server(t, reg, nil)
	req := httptest.NewRequest(http.MethodGet, "/agent/info", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control: got %q, want no-store", cc)
	}

	var got map[string]any
	decode(t, rec.Body, &got)
	for _, key := range []string{"version", "os", "arch", "hostname", "uptime_seconds"} {
		if _, ok := got[key]; !ok {
			t.Errorf("required key %q missing from response: %v", key, got)
		}
	}
}

func TestGetAgentInfo_RejectsNonGET(t *testing.T) {
	reg := registry.New()
	srv := newV1Server(t, reg, nil)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/agent/info", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /agent/info: got %d, want 405", method, rec.Code)
		}
	}
}
