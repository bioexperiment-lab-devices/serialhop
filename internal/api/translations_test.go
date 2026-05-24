package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
)

func writeEndpoint(t *testing.T, dir string, host string, port int) string {
	t.Helper()
	p := filepath.Join(dir, "panel-endpoint.json")
	if err := bootstrap.WritePanelEndpoint(p, bootstrap.PanelEndpoint{Host: host, Port: port, PID: 1}); err != nil {
		t.Fatalf("WritePanelEndpoint: %v", err)
	}
	return p
}

func panelPortFromURL(u string) int {
	var port int
	_, _ = fmt.Sscanf(u, "http://127.0.0.1:%d", &port)
	return port
}

func newProxyTestServer(t *testing.T, endpoint string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	NewTranslationsProxy(endpoint).Mount(mux)
	return httptest.NewServer(mux)
}

func TestProxyGet_PanelUp(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"translations":[{"id":"cam-A","label":"Cam A"}]}`))
	}))
	defer panel.Close()
	dir := t.TempDir()
	endpoint := writeEndpoint(t, dir, "127.0.0.1", panelPortFromURL(panel.URL))
	srv := newProxyTestServer(t, endpoint)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/translations")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(b, []byte(`"cam-A"`)) {
		t.Fatalf("body: %s", b)
	}
}

func TestProxyGet_PanelDown_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	endpoint := writeEndpoint(t, dir, "127.0.0.1", 1) // port 1 unreachable
	srv := newProxyTestServer(t, endpoint)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/translations")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body struct {
		Translations []any `json:"translations"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Translations) != 0 {
		t.Fatalf("want empty translations, got %+v", body)
	}
}

func TestProxyStart_PanelDown_503(t *testing.T) {
	dir := t.TempDir()
	endpoint := writeEndpoint(t, dir, "127.0.0.1", 1)
	srv := newProxyTestServer(t, endpoint)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/translations/cam-A/start", "application/json",
		bytes.NewBufferString(`{"session_id":"S1"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 503 {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
}

func TestProxyStop_PanelDown_204(t *testing.T) {
	dir := t.TempDir()
	endpoint := writeEndpoint(t, dir, "127.0.0.1", 1)
	srv := newProxyTestServer(t, endpoint)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/translations/cam-A/stop", "application/json",
		bytes.NewBufferString(`{"session_id":"S1"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 204 {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}
}

func TestProxyStart_PassesBodyThrough(t *testing.T) {
	var seenBody []byte
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer panel.Close()
	dir := t.TempDir()
	endpoint := writeEndpoint(t, dir, "127.0.0.1", panelPortFromURL(panel.URL))
	srv := newProxyTestServer(t, endpoint)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/translations/cam-A/start", "application/json",
		bytes.NewBufferString(`{"session_id":"S1","whip_url":"u"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !bytes.Contains(seenBody, []byte(`"S1"`)) {
		t.Fatalf("body not forwarded: %s", seenBody)
	}
}
