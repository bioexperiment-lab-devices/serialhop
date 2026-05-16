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

	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
)

func setupTestEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SERIALHOP_DATA_DIR", dir)
	prevStderr := os.Stderr
	prevSlog := slog.Default()
	t.Cleanup(func() {
		os.Stderr = prevStderr
		slog.SetDefault(prevSlog)
	})
	return dir
}

func TestManagerInitInstallsCaptureSoSlogReachesDisk(t *testing.T) {
	dir := setupTestEnv(t)

	m, err := Init("1.4.2", slog.LevelInfo)
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
	logPath := filepath.Join(dir, "logs", "SerialHop.log")
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
	dir := setupTestEnv(t)

	m, err := Init("1.4.2", slog.LevelInfo)
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
	logPath := filepath.Join(dir, "logs", "SerialHop.log")
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
	setupTestEnv(t)

	m, err := Init("1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	m.StartShipper("")
	_ = m
}

func TestManagerStartShipperPushes(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	setupTestEnv(t)

	m, err := Init("1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	m.SetPushURL(srv.URL)

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
	setupTestEnv(t)

	m, err := Init("1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	m.SetPushURL("http://localhost:3100/loki/api/v1/push")
	m.StartShipper("lab-1")
	m.StartShipper("lab-1")
	if got := m.shipperCountForTest(); got != 1 {
		t.Fatalf("shipper count = %d, want 1", got)
	}
}

func TestManagerShutdownWithoutShipper(t *testing.T) {
	setupTestEnv(t)

	m, err := Init("1.4.2", slog.LevelInfo)
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

	setupTestEnv(t)

	m, err := Init("1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	m.SetPushURL(srv.URL)
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

func TestInitErrorsWhenDataDirUnavailable(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	if _, err := Init("1.4.2", slog.LevelInfo); err == nil {
		t.Fatal("Init returned nil, want error when data dir unavailable")
	}
}

func TestManagerStartShipperWithEmptyPushURLIsNoOp(t *testing.T) {
	setupTestEnv(t)
	m, err := Init("1.4.2", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	m.SetPushURL("") // explicit empty
	m.StartShipper("lab-1")
	if got := m.shipperCountForTest(); got != 0 {
		t.Fatalf("shipper started with empty push URL; count = %d", got)
	}
}

func TestInit_StartsPanelTailer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SERIALHOP_DATA_DIR", dir)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	prevSlog := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prevSlog) })

	m, err := Init("v", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	panelLogPath := filepath.Join(dir, "logs", "SerialHop_panel.log")

	// Write a pre-existing line before the tailer has anchored.
	if err := os.WriteFile(panelLogPath, []byte("pre-existing\n"), 0o644); err != nil {
		t.Fatalf("write pre-existing: %v", err)
	}

	// Sleep long enough for the tailer to tick once (poll=500ms) and anchor at EOF.
	time.Sleep(700 * time.Millisecond)

	// Append the "new" line after the tailer has anchored.
	f, err := os.OpenFile(panelLogPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open panel log for append: %v", err)
	}
	if _, err := f.WriteString("new\n"); err != nil {
		_ = f.Close()
		t.Fatalf("append new line: %v", err)
	}
	_ = f.Close()

	// Drain records in a 2s loop, expecting "new" but NOT "pre-existing".
	deadline := time.Now().Add(2 * time.Second)
	var sawNew, sawPreExisting bool
	for time.Now().Before(deadline) {
		recs := m.QueueDrainForTest(10)
		for _, r := range recs {
			if r.Stream != "panel" {
				continue
			}
			if strings.Contains(r.Line, "new") {
				sawNew = true
			}
			if strings.Contains(r.Line, "pre-existing") {
				sawPreExisting = true
			}
		}
		if sawNew {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !sawNew {
		t.Error("expected to see panel record containing \"new\" but did not")
	}
	if sawPreExisting {
		t.Error("saw panel record containing \"pre-existing\"; cold-start anchor failed")
	}
}
