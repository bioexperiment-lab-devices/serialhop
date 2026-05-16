package updater

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

func TestDownload_Success(t *testing.T) {
	body := []byte("0123456789abcdef0123456789abcdef") // 32 bytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "32")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")

	var lastReceived int64
	progress := func(received, total int64) {
		if received < lastReceived {
			t.Errorf("progress not monotonic: %d → %d", lastReceived, received)
		}
		lastReceived = received
	}

	if err := Download(context.Background(), srv.Client(), srv.URL, dest, "SerialHop/test", progress); err != nil {
		t.Fatalf("Download: %v", err)
	}

	got, err := os.ReadFile(dest) //nolint:gosec // test reads temp file created by t.TempDir()
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("content mismatch")
	}

	// No .partial should remain.
	if _, err := os.Stat(dest + ".partial"); !os.IsNotExist(err) {
		t.Errorf(".partial should not exist after success: %v", err)
	}
}

func TestDownload_HTTP404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	err := Download(context.Background(), srv.Client(), srv.URL, dest, "SerialHop/test", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("err should mention 404: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("dest should not exist on failure")
	}
}

func TestDownload_ContextCancel(t *testing.T) {
	var started int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		// Write a small chunk and flush, then block.
		_, _ = w.Write(make([]byte, 16))
		if flusher != nil {
			flusher.Flush()
		}
		atomic.StoreInt32(&started, 1)
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")

	errCh := make(chan error, 1)
	go func() {
		errCh <- Download(ctx, srv.Client(), srv.URL, dest, "SerialHop/test", nil)
	}()

	// Wait until the server has started streaming.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&started) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	err := <-errCh
	if err == nil {
		t.Fatal("expected error on cancel")
	}
	// The .partial file should be cleaned up.
	if _, err := os.Stat(dest + ".partial"); !os.IsNotExist(err) {
		t.Errorf(".partial should be removed on cancel: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest should not exist on cancel: %v", err)
	}
}

func TestDownload_LogsInfoOnSuccess(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	body := []byte("test content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	if err := Download(context.Background(), srv.Client(), srv.URL, dest, "SerialHop/test", nil); err != nil {
		t.Fatalf("Download: %v", err)
	}

	rec.AssertRecord(t, slog.LevelInfo, "updater download start", map[string]any{
		"url":    srv.URL,
		"target": dest,
	})
	rec.AssertRecord(t, slog.LevelInfo, "updater download ok", map[string]any{
		"url":    srv.URL,
		"target": dest,
	})
}

func TestDownload_LogsErrorOnHTTPFailure(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	_ = Download(context.Background(), srv.Client(), srv.URL, dest, "SerialHop/test", nil)

	rec.AssertRecord(t, slog.LevelError, "updater download failed", map[string]any{
		"url": srv.URL,
	})
}
