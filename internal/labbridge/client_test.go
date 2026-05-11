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

const testUserAgent = "labbridge-test/1.0"

func TestFetchHealth_ChiselOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/public/health" {
			t.Errorf("path: got %q, want /api/public/health", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"chisel":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := FetchHealth(ctx, srv.Client(), srv.URL, testUserAgent)
	if err != nil {
		t.Fatalf("FetchHealth: %v", err)
	}
	if !got.ChiselOK {
		t.Errorf("ChiselOK: got false, want true")
	}
	if got.Detail != "" {
		t.Errorf("Detail: got %q, want empty", got.Detail)
	}
}

func TestFetchHealth_ChiselDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"chisel":"down","error":"connection refused"}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := FetchHealth(ctx, srv.Client(), srv.URL, testUserAgent)
	if err != nil {
		t.Fatalf("FetchHealth: %v", err)
	}
	if got.ChiselOK {
		t.Errorf("ChiselOK: got true, want false")
	}
	if got.Detail != "connection refused" {
		t.Errorf("Detail: got %q, want %q", got.Detail, "connection refused")
	}
}

func TestFetchHealth_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	_, err := FetchHealth(ctx, srv.Client(), srv.URL, testUserAgent)
	if err == nil || !strings.Contains(err.Error(), "parse health body") {
		t.Fatalf("expected parse-body error, got %v", err)
	}
}

func TestFetchHealth_5xxWrapsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	_, err := FetchHealth(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if !errors.Is(err, ErrServerError) {
		t.Fatalf("expected ErrServerError, got %v", err)
	}
}

func TestFetchHealth_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway - 100) // 402
	}))
	t.Cleanup(srv.Close)

	_, err := FetchHealth(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err == nil || !strings.Contains(err.Error(), "unexpected status 402") {
		t.Fatalf("expected unexpected-status error, got %v", err)
	}
}

func TestFetchHealth_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"chisel":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := FetchHealth(ctx, srv.Client(), srv.URL, testUserAgent)
	if err == nil {
		t.Fatal("expected ctx error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ctx.DeadlineExceeded, got %v", err)
	}
}

func TestFetchHealth_SendsUserAgent(t *testing.T) {
	gotUA := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"chisel":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	_, err := FetchHealth(context.Background(), srv.Client(), srv.URL, testUserAgent)
	if err != nil {
		t.Fatalf("FetchHealth: %v", err)
	}
	if gotUA != testUserAgent {
		t.Errorf("User-Agent: got %q, want %q", gotUA, testUserAgent)
	}
}

func TestFetchClient_200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/public/clients/devices_coordinator" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer s3cret" {
			t.Errorf("auth: got %q, want %q", got, "Bearer s3cret")
		}
		_, _ = w.Write([]byte(`{"port":8089,"connected":true}`))
	}))
	t.Cleanup(srv.Close)

	got, err := FetchClient(context.Background(), srv.Client(), srv.URL, "devices_coordinator", "s3cret", testUserAgent)
	if err != nil {
		t.Fatalf("FetchClient: %v", err)
	}
	if got.Port != 8089 || !got.Connected {
		t.Errorf("got %+v, want {Port:8089 Connected:true}", got)
	}
}

func TestFetchClient_401_WrapsErrUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"unauthorized"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	_, err := FetchClient(context.Background(), srv.Client(), srv.URL, "u", "p", testUserAgent)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestFetchClient_500_WrapsErrServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "roster broken", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	_, err := FetchClient(context.Background(), srv.Client(), srv.URL, "u", "p", testUserAgent)
	if !errors.Is(err, ErrServerError) {
		t.Fatalf("expected ErrServerError, got %v", err)
	}
}

func TestFetchClient_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	t.Cleanup(srv.Close)

	_, err := FetchClient(context.Background(), srv.Client(), srv.URL, "u", "p", testUserAgent)
	if err == nil || !strings.Contains(err.Error(), "parse client body") {
		t.Fatalf("expected parse-body error, got %v", err)
	}
}

func TestFetchClient_UsernameURLEscaped(t *testing.T) {
	gotPath := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"port":1,"connected":false}`))
	}))
	t.Cleanup(srv.Close)

	_, err := FetchClient(context.Background(), srv.Client(), srv.URL, "foo bar", "p", testUserAgent)
	if err != nil {
		t.Fatalf("FetchClient: %v", err)
	}
	if gotPath != "/api/public/clients/foo%20bar" {
		t.Errorf("escaped path: got %q, want %q", gotPath, "/api/public/clients/foo%20bar")
	}
}

func TestFetchClient_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"port":1,"connected":true}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := FetchClient(ctx, srv.Client(), srv.URL, "u", "p", testUserAgent)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ctx.DeadlineExceeded, got %v", err)
	}
}
