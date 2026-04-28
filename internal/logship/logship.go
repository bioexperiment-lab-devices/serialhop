// Package logship streams the client's slog output and stderr to the
// in-VPS Loki via the chisel forward tunnel.
//
// It also owns the durable on-disk log files (lab_devices_client.log,
// lab_devices_client_stderr.log) so disabling the shipper does not
// affect on-disk logging.
package logship

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"
)

const LogFileName = "lab_devices_client.log"
const StderrLogFileName = "lab_devices_client_stderr.log"

// defaultPushURL is the local end of the chisel forward tunnel that
// reaches the in-VPS Loki.
const defaultPushURL = "http://127.0.0.1:3100/loki/api/v1/push"

// Manager owns the capture taps, ring buffer, and shipper goroutine.
type Manager struct {
	dir     string
	version string

	levelVar *slog.LevelVar

	slogDisk   *lumberjack.Logger
	stderrDisk *lumberjack.Logger
	stderrTap  *stderrTap

	q *queue

	mu       sync.Mutex
	pushURL  string
	shipperC int                // count of shippers started (for tests)
	shipCtx  context.Context
	shipStop context.CancelFunc
	shipDone chan struct{}
}

// Init builds the on-disk log writers, allocates the ring buffer, and
// installs the slog and stderr taps. The shipper is NOT started yet —
// call StartShipper once the chisel user is known.
func Init(dir, version string, level slog.Level) (*Manager, error) {
	m := &Manager{
		dir:      dir,
		version:  version,
		levelVar: new(slog.LevelVar),
		pushURL:  defaultPushURL,
		q:        newQueue(10_000),
	}
	m.levelVar.Set(level)

	m.slogDisk = &lumberjack.Logger{
		Filename:   filepath.Join(dir, LogFileName),
		MaxSize:    10,
		MaxBackups: 3,
		LocalTime:  true,
	}
	m.stderrDisk = &lumberjack.Logger{
		Filename:   filepath.Join(dir, StderrLogFileName),
		MaxSize:    10,
		MaxBackups: 3,
		LocalTime:  true,
	}

	if err := installSlogTap(m.slogDisk, m.levelVar, m.q); err != nil {
		return nil, err
	}
	tap, err := installStderrTap(m.stderrDisk, m.q)
	if err != nil {
		return nil, err
	}
	m.stderrTap = tap
	return m, nil
}

// SetLevel changes the slog level without re-installing the tap.
func (m *Manager) SetLevel(level slog.Level) {
	m.levelVar.Set(level)
}

// StartShipper starts the shipper goroutine if clientLabel is non-empty
// and no shipper is already running. Idempotent.
func (m *Manager) StartShipper(clientLabel string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shipCtx != nil {
		return // already started
	}
	if clientLabel == "" {
		slog.Warn("log streaming disabled (no chisel user)")
		return
	}
	labels := map[string]map[string]string{
		"stdout": buildLabels(clientLabel, "stdout", m.version),
		"stderr": buildLabels(clientLabel, "stderr", m.version),
	}
	s := newShipper(m.q, m.pushURL, labels, realClock{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.run(ctx)
		close(done)
	}()

	m.shipCtx = ctx
	m.shipStop = cancel
	m.shipDone = done
	m.shipperC++
}

func buildLabels(client, stream, version string) map[string]string {
	return map[string]string{
		"client":  client,
		"stream":  stream,
		"service": "lab_devices_client",
		"version": version,
	}
}

// Shutdown stops the shipper (giving it the caller's deadline to drain
// in-flight records), closes the stderr tap, and closes the on-disk
// writers. Single-call: not safe under concurrent invocation. Designed
// as a process-exit hook owned by the service worker.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	stop := m.shipStop
	done := m.shipDone
	m.mu.Unlock()

	if stop != nil {
		stop()
		select {
		case <-done:
		case <-ctx.Done():
		}
	}

	if m.stderrTap != nil {
		m.stderrTap.close()
		m.stderrTap = nil
	}
	if m.slogDisk != nil {
		_ = m.slogDisk.Close()
	}
	if m.stderrDisk != nil {
		_ = m.stderrDisk.Close()
	}
}

// --- test-only helpers (lower-cased; only callable from logship_test.go) ---

func (m *Manager) setPushURLForTest(url string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pushURL = url
}

func (m *Manager) shipperCountForTest() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.shipperC
}
