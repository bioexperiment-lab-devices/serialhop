package logship

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerInitInstallsCaptureSoSlogReachesDisk(t *testing.T) {
	dir := t.TempDir()
	prevStderr := os.Stderr
	prevSlog := slog.Default()
	t.Cleanup(func() {
		os.Stderr = prevStderr
		slog.SetDefault(prevSlog)
	})

	m, err := Init(dir, "1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	slog.Info("hello-from-init")

	deadline := time.Now().Add(time.Second)
	logPath := filepath.Join(dir, LogFileName)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(logPath) //nolint:gosec // test reads temp file created by t.TempDir()
		if strings.Contains(string(data), "hello-from-init") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ := os.ReadFile(logPath) //nolint:gosec // test reads temp file created by t.TempDir()
	t.Fatalf("hello-from-init missing on disk:\n%s", data)
}

func TestManagerSetLevelChangesFiltering(t *testing.T) {
	dir := t.TempDir()
	prevStderr := os.Stderr
	prevSlog := slog.Default()
	t.Cleanup(func() {
		os.Stderr = prevStderr
		slog.SetDefault(prevSlog)
	})

	m, err := Init(dir, "1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	slog.Debug("debug-suppressed")
	m.SetLevel(slog.LevelDebug)
	slog.Debug("debug-passes")

	deadline := time.Now().Add(time.Second)
	logPath := filepath.Join(dir, LogFileName)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(logPath) //nolint:gosec // test reads temp file created by t.TempDir()
		if strings.Contains(string(data), "debug-passes") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ := os.ReadFile(logPath) //nolint:gosec // test reads temp file created by t.TempDir()
	if strings.Contains(string(data), "debug-suppressed") {
		t.Errorf("debug-suppressed leaked at Info level:\n%s", data)
	}
	if !strings.Contains(string(data), "debug-passes") {
		t.Errorf("debug-passes missing after SetLevel(Debug):\n%s", data)
	}
}

func TestManagerStartShipperEmptyClientLabelIsNoOp(t *testing.T) {
	dir := t.TempDir()
	prevStderr := os.Stderr
	prevSlog := slog.Default()
	t.Cleanup(func() {
		os.Stderr = prevStderr
		slog.SetDefault(prevSlog)
	})

	m, err := Init(dir, "1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	m.StartShipper("") // must not start a goroutine, must not panic
	// No assertion beyond "didn't crash"; further behavior is covered
	// by TestManagerStartShipperPushes.
	_ = m
}

func TestManagerStartShipperPushes(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	prevStderr := os.Stderr
	prevSlog := slog.Default()
	t.Cleanup(func() {
		os.Stderr = prevStderr
		slog.SetDefault(prevSlog)
	})

	m, err := Init(dir, "1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	// Override the URL the manager hands the shipper.
	m.setPushURLForTest(srv.URL)

	m.StartShipper("lab-1")
	for i := 0; i < 10; i++ {
		slog.Info("line", "i", i)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hits.Load() >= 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no push received; hits=%d", hits.Load())
}

func TestManagerStartShipperIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	prevStderr := os.Stderr
	prevSlog := slog.Default()
	t.Cleanup(func() {
		os.Stderr = prevStderr
		slog.SetDefault(prevSlog)
	})

	m, err := Init(dir, "1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	m.StartShipper("lab-1")
	m.StartShipper("lab-1") // must not panic, must not start twice
	if got := m.shipperCountForTest(); got != 1 {
		t.Fatalf("shipper count = %d, want 1", got)
	}
}

// Sanity: Shutdown is safe to call when StartShipper was never called.
func TestManagerShutdownWithoutShipper(t *testing.T) {
	dir := t.TempDir()
	prevStderr := os.Stderr
	prevSlog := slog.Default()
	t.Cleanup(func() {
		os.Stderr = prevStderr
		slog.SetDefault(prevSlog)
	})

	m, err := Init(dir, "1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { m.Shutdown(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return")
	}
}

// Verify Shutdown drains in-flight records before returning.
func TestManagerShutdownDrainsBuffer(t *testing.T) {
	var (
		mu   sync.Mutex
		seen int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	dir := t.TempDir()
	prevStderr := os.Stderr
	prevSlog := slog.Default()
	t.Cleanup(func() {
		os.Stderr = prevStderr
		slog.SetDefault(prevSlog)
	})

	m, err := Init(dir, "1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	m.setPushURLForTest(srv.URL)
	m.StartShipper("lab-1")

	for i := 0; i < 5; i++ {
		slog.Info("line", "i", i)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m.Shutdown(ctx)

	mu.Lock()
	defer mu.Unlock()
	if seen == 0 {
		t.Fatal("Shutdown did not drain pending records")
	}
}
