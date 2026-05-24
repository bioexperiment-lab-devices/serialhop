package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/streamer"
)

func setupManager(t *testing.T) streamer.Manager {
	t.Helper()
	dir := t.TempDir()
	store := streamer.NewStore(filepath.Join(dir, "armed.json"))
	m := streamer.NewManager(streamer.ManagerConfig{
		Store:       store,
		Enumerator:  fixedEnum{},
		FFmpegReady: func() error { return nil },
		BearerFlag:  "-authorization",
		Spawner:     noopSpawner{},
	})
	if _, err := m.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if err := m.SetArmed("cam-A", true); err != nil {
		t.Fatalf("SetArmed: %v", err)
	}
	return m
}

type fixedEnum struct{}

func (fixedEnum) List(_ context.Context) ([]streamer.Camera, error) {
	return []streamer.Camera{{ID: "cam-A", Label: "Cam A"}}, nil
}

type noopSpawner struct{}

func (noopSpawner) Start(_ context.Context, _ []string) (streamer.SessionHandleForTest, error) {
	ch := make(chan struct{})
	return noopSessionHandle{done: ch}, nil
}

type noopSessionHandle struct{ done chan struct{} }

func (n noopSessionHandle) Done() <-chan struct{}        { return n.done }
func (n noopSessionHandle) Stop(_ context.Context) error { close(n.done); return nil }
func (n noopSessionHandle) LastError() string            { return "" }
func (n noopSessionHandle) PID() int                     { return 0 }

func TestStreamingHTTP_GetTranslations(t *testing.T) {
	m := setupManager(t)
	srv := httptest.NewServer(streamingHandler(m))
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
		Translations []streamer.Translation `json:"translations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Translations) != 1 || body.Translations[0].ID != "cam-A" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestStreamingHTTP_StartUnknown_404(t *testing.T) {
	m := setupManager(t)
	srv := httptest.NewServer(streamingHandler(m))
	defer srv.Close()
	body := bytes.NewBufferString(`{"session_id":"S1","whip_url":"u","whip_token":"tk"}`)
	resp, err := http.Post(srv.URL+"/api/translations/nope/start", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestStreamingHTTP_Start_Then_Stop_RoundTrip(t *testing.T) {
	m := setupManager(t)
	srv := httptest.NewServer(streamingHandler(m))
	defer srv.Close()
	startBody := `{"session_id":"S1","whip_url":"u","whip_token":"tk"}`
	resp, err := http.Post(srv.URL+"/api/translations/cam-A/start", "application/json", bytes.NewBufferString(startBody))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("want 202 start, got %d", resp.StatusCode)
	}
	stopBody := `{"session_id":"S1"}`
	resp2, err := http.Post(srv.URL+"/api/translations/cam-A/stop", "application/json", bytes.NewBufferString(stopBody))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != 204 {
		t.Fatalf("want 204 stop, got %d", resp2.StatusCode)
	}
}

func TestStreamingHTTP_StaleStop_409(t *testing.T) {
	m := setupManager(t)
	srv := httptest.NewServer(streamingHandler(m))
	defer srv.Close()
	startResp, err := http.Post(srv.URL+"/api/translations/cam-A/start",
		"application/json",
		bytes.NewBufferString(`{"session_id":"REAL","whip_url":"u","whip_token":"tk"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = startResp.Body.Close()
	resp, err := http.Post(srv.URL+"/api/translations/cam-A/stop",
		"application/json",
		bytes.NewBufferString(`{"session_id":"STALE"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 409 {
		t.Fatalf("want 409, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(b, []byte(`"active_session_id":"REAL"`)) {
		t.Fatalf("expected active_session_id in body, got %s", b)
	}
}

func TestStreamingHTTP_MethodNotAllowed(t *testing.T) {
	m := setupManager(t)
	srv := httptest.NewServer(streamingHandler(m))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/translations", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 405 {
		t.Fatalf("want 405, got %d", resp.StatusCode)
	}
}
