package labbridge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchServerInfo_Happy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/public/server-info" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("server-info must not send Authorization header")
		}
		_, _ = w.Write([]byte(`{
			"chisel": {"listen_port": 7000},
			"loki":   {"push_url": "http://127.0.0.1:3100/loki/api/v1/push"},
			"forward_tunnels": [
				{"name": "loki", "local": "127.0.0.1:3100", "remote": "loki:3100"}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	got, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err != nil {
		t.Fatalf("FetchServerInfo: %v", err)
	}
	if got.ChiselListenPort != 7000 {
		t.Errorf("ChiselListenPort: got %d, want 7000", got.ChiselListenPort)
	}
	if got.LokiPushURL != "http://127.0.0.1:3100/loki/api/v1/push" {
		t.Errorf("LokiPushURL: got %q", got.LokiPushURL)
	}
	if len(got.ForwardTunnels) != 1 {
		t.Fatalf("ForwardTunnels: got %d, want 1", len(got.ForwardTunnels))
	}
	ft := got.ForwardTunnels[0]
	if ft.Name != "loki" || ft.Local != "127.0.0.1:3100" || ft.Remote != "loki:3100" {
		t.Errorf("forward tunnel: got %+v", ft)
	}
}

func TestFetchServerInfo_IgnoresUnknownKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"chisel": {"listen_port": 7000, "fingerprint": "abc123"},
			"loki":   {"push_url": "http://x/loki"},
			"forward_tunnels": [],
			"agent":  {"version": "1.2.3", "sha256": "deadbeef"}
		}`))
	}))
	t.Cleanup(srv.Close)

	got, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err != nil {
		t.Fatalf("FetchServerInfo: %v", err)
	}
	if got.ChiselListenPort != 7000 {
		t.Errorf("ChiselListenPort: got %d, want 7000", got.ChiselListenPort)
	}
	if len(got.ForwardTunnels) != 0 {
		t.Errorf("ForwardTunnels: got %d, want 0", len(got.ForwardTunnels))
	}
}

func TestFetchServerInfo_NullForwardTunnels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"chisel": {"listen_port": 7000},
			"loki":   {"push_url": "http://x"},
			"forward_tunnels": null
		}`))
	}))
	t.Cleanup(srv.Close)

	got, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err != nil {
		t.Fatalf("FetchServerInfo: %v", err)
	}
	if got.ForwardTunnels != nil && len(got.ForwardTunnels) != 0 {
		t.Errorf("ForwardTunnels: got %v, want empty/nil", got.ForwardTunnels)
	}
}

func TestFetchServerInfo_RejectsListenPortOutOfRange(t *testing.T) {
	for _, body := range []string{
		`{"chisel":{"listen_port":0},"loki":{"push_url":"http://x"}}`,
		`{"chisel":{"listen_port":70000},"loki":{"push_url":"http://x"}}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		_, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "chisel.listen_port") {
			t.Errorf("body %q: want chisel.listen_port error, got %v", body, err)
		}
	}
}

func TestFetchServerInfo_RejectsEmptyLokiPushURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"chisel":{"listen_port":7000},"loki":{"push_url":""}}`))
	}))
	t.Cleanup(srv.Close)
	_, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err == nil || !strings.Contains(err.Error(), "loki.push_url") {
		t.Errorf("want loki.push_url error, got %v", err)
	}
}

func TestFetchServerInfo_RejectsEmptyForwardTunnelEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"chisel":{"listen_port":7000},
			"loki":{"push_url":"http://x"},
			"forward_tunnels":[{"name":"loki","local":"","remote":"loki:3100"}]
		}`))
	}))
	t.Cleanup(srv.Close)
	_, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err == nil || !strings.Contains(err.Error(), "forward_tunnels") {
		t.Errorf("want forward_tunnels error, got %v", err)
	}
}

func TestFetchServerInfo_5xxWrapsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	_, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if !errors.Is(err, ErrServerError) {
		t.Fatalf("want ErrServerError, got %v", err)
	}
}

func TestFetchServerInfo_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	t.Cleanup(srv.Close)
	_, err := FetchServerInfo(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err == nil || !strings.Contains(err.Error(), "parse server-info body") {
		t.Fatalf("want parse error, got %v", err)
	}
}

func TestFetchServerInfo_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := FetchServerInfo(ctx, srv.Client(), srv.URL, testUserAgent)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
}
