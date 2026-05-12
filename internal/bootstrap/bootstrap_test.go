package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func newServer(t *testing.T, serverInfoHandler, clientHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/public/server-info", serverInfoHandler)
	mux.HandleFunc("/api/public/clients/", clientHandler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func okServerInfo(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte(`{
		"chisel":{"listen_port":7000},
		"loki":{"push_url":"http://127.0.0.1:3100/loki/api/v1/push"},
		"forward_tunnels":[{"name":"loki","local":"127.0.0.1:3100","remote":"loki:3100"}]
	}`))
}

func okClient(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte(`{"port":8089,"connected":true}`))
}

func TestResolve_LiveSuccess_WritesCacheAndReturnsLive(t *testing.T) {
	srv := newServer(t, okServerInfo, okClient)
	cachePath := filepath.Join(t.TempDir(), "cache.json")

	opts := Options{
		HTTPClient: srv.Client(),
		Base:       srv.URL,
		User:       "alice",
		Pass:       "s3cret",
		CachePath:  cachePath,
		UserAgent:  "test/1",
	}
	got, err := Resolve(context.Background(), opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ServerInfo.ChiselListenPort != 7000 {
		t.Errorf("ChiselListenPort: got %d", got.ServerInfo.ChiselListenPort)
	}
	if got.RemotePort != 8089 {
		t.Errorf("RemotePort: got %d", got.RemotePort)
	}
	c, err := ReadCache(cachePath, "alice")
	if err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	if c.User != "alice" {
		t.Errorf("cache user: got %q, want alice", c.User)
	}
}

func TestResolve_5xxThenCache_ReturnsCache(t *testing.T) {
	srv := newServer(t,
		func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) },
		okClient,
	)
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	if err := WriteCache(cachePath, sampleCache()); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	opts := Options{
		HTTPClient: srv.Client(), Base: srv.URL, User: "alice", Pass: "p",
		CachePath: cachePath, UserAgent: "test/1",
	}
	got, err := Resolve(context.Background(), opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.RemotePort != 8089 {
		t.Errorf("expected cached RemotePort 8089, got %d", got.RemotePort)
	}
}

func TestResolve_401_BypassesCacheAndRetries(t *testing.T) {
	// Even with a valid cache present, a live 401 must force the retry
	// loop — never serve cached values when creds are demonstrably wrong.
	var clientCalls atomic.Int32
	srv := newServer(t, okServerInfo, func(w http.ResponseWriter, _ *http.Request) {
		n := clientCalls.Add(1)
		if n < 3 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"port":8089,"connected":true}`))
	})
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	if err := WriteCache(cachePath, sampleCache()); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	opts := Options{
		HTTPClient:     srv.Client(),
		Base:           srv.URL,
		User:           "alice",
		Pass:           "p",
		CachePath:      cachePath,
		UserAgent:      "test/1",
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
	}
	got, err := Resolve(context.Background(), opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if clientCalls.Load() < 3 {
		t.Errorf("expected at least 3 client calls (retry past 401), got %d", clientCalls.Load())
	}
	if got.RemotePort != 8089 {
		t.Errorf("RemotePort: got %d", got.RemotePort)
	}
}

func TestResolve_NoCache_RetriesUntilSuccess(t *testing.T) {
	var serverInfoCalls atomic.Int32
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		n := serverInfoCalls.Add(1)
		if n < 2 {
			http.Error(w, "boom", 500)
			return
		}
		okServerInfo(w, nil)
	}, okClient)
	cachePath := filepath.Join(t.TempDir(), "cache.json")

	opts := Options{
		HTTPClient:     srv.Client(),
		Base:           srv.URL,
		User:           "alice",
		Pass:           "p",
		CachePath:      cachePath,
		UserAgent:      "test/1",
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
	}
	got, err := Resolve(context.Background(), opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ServerInfo.ChiselListenPort != 7000 {
		t.Errorf("ChiselListenPort: got %d", got.ServerInfo.ChiselListenPort)
	}
	if serverInfoCalls.Load() < 2 {
		t.Errorf("expected at least 2 server-info calls, got %d", serverInfoCalls.Load())
	}
}

func TestResolve_CtxCancelledNoCache_ReturnsCtxErr(t *testing.T) {
	srv := newServer(t,
		func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) },
		func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) },
	)
	cachePath := filepath.Join(t.TempDir(), "cache.json")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	opts := Options{
		HTTPClient:     srv.Client(),
		Base:           srv.URL,
		User:           "alice",
		Pass:           "p",
		CachePath:      cachePath,
		UserAgent:      "test/1",
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
	}
	_, err := Resolve(ctx, opts)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

func TestResolve_LiveSuccessCarriesCorrectUserInCache(t *testing.T) {
	srv := newServer(t, okServerInfo, okClient)
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	opts := Options{
		HTTPClient: srv.Client(), Base: srv.URL, User: "bob", Pass: "p",
		CachePath: cachePath, UserAgent: "test/1",
	}
	if _, err := Resolve(context.Background(), opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Reading with the wrong user must yield ErrCacheMissing.
	if _, err := ReadCache(cachePath, "alice"); !errors.Is(err, ErrCacheMissing) {
		t.Errorf("user-mismatch read: expected ErrCacheMissing, got %v", err)
	}
	if _, err := ReadCache(cachePath, "bob"); err != nil {
		t.Errorf("matching user read: got %v", err)
	}
}

func TestResolve_VerifiesBearerHeaderOnClientCall(t *testing.T) {
	var gotAuth string
	srv := newServer(t, okServerInfo, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"port":8089,"connected":true}`))
	})
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	opts := Options{
		HTTPClient: srv.Client(), Base: srv.URL, User: "u", Pass: "s3cret",
		CachePath: cachePath, UserAgent: "test/1",
	}
	if _, err := Resolve(context.Background(), opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotAuth != "Bearer s3cret" {
		t.Errorf("Authorization: got %q, want %q", gotAuth, "Bearer s3cret")
	}
}

func TestResolve_FetchTimeoutHonored(t *testing.T) {
	// If FetchTimeout is short and the server is slow, Resolve must fall
	// through to the retry path; we then race the test against retries.
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		okServerInfo(w, nil)
	}, okClient)
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	opts := Options{
		HTTPClient: srv.Client(), Base: srv.URL, User: "u", Pass: "p",
		CachePath: cachePath, UserAgent: "test/1",
		FetchTimeout:   10 * time.Millisecond,
		InitialBackoff: 1 * time.Millisecond, MaxBackoff: 2 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := Resolve(ctx, opts)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}
