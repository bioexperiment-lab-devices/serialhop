# Config & Logs Layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move `SerialHop_config.yaml` and all log files into `%ProgramData%\SerialHop\` (with logs in a `logs\` subdir), leaving the install dir holding only `SerialHop.exe`. Replace the panel's "Open log file" button with "Open logs folder".

**Architecture:** Add a new `internal/paths` package as the single source of truth for the on-disk layout. Refactor four callers (`logship`, `winsvc/worker`, `panel`, `cmd/serialhop/main`) to consume it. Clean break — no migration of v0.7.0 installs.

**Tech Stack:** Go 1.x, stdlib (`os`, `filepath`), `gopkg.in/natefinch/lumberjack.v2` (already in deps), `lxn/walk` for Windows GUI. Tests run cross-platform (macOS + Windows).

**Spec:** `docs/superpowers/specs/2026-05-11-config-logs-layout-design.md`

---

## Task 1: Create `internal/paths` package

**Files:**
- Create: `internal/paths/paths.go`
- Create: `internal/paths/paths_test.go`

This is the foundation everything else depends on. Pure stdlib, no build tags — runs on macOS/Linux/Windows.

- [ ] **Step 1: Write the test file**

Create `internal/paths/paths_test.go`:

```go
package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDataDirUsesOverrideWhenSet(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "/custom/root")
	t.Setenv("ProgramData", "/should/be/ignored")
	if got := DataDir(); got != "/custom/root" {
		t.Errorf("DataDir() = %q, want /custom/root", got)
	}
}

func TestDataDirFallsBackToProgramData(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", `C:\ProgramData`)
	want := filepath.Join(`C:\ProgramData`, "SerialHop")
	if got := DataDir(); got != want {
		t.Errorf("DataDir() = %q, want %q", got, want)
	}
}

func TestDataDirReturnsEmptyWhenNeitherSet(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	if got := DataDir(); got != "" {
		t.Errorf("DataDir() = %q, want empty", got)
	}
}

func TestComposedPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SERIALHOP_DATA_DIR", root)

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"ConfigPath", ConfigPath(), filepath.Join(root, "SerialHop_config.yaml")},
		{"LogsDir", LogsDir(), filepath.Join(root, "logs")},
		{"ServiceLogPath", ServiceLogPath(), filepath.Join(root, "logs", "SerialHop.log")},
		{"StderrLogPath", StderrLogPath(), filepath.Join(root, "logs", "SerialHop_stderr.log")},
		{"PanelErrorLogPath", PanelErrorLogPath(), filepath.Join(root, "logs", "SerialHop_panel_error.log")},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestComposedPathsAreEmptyWhenDataDirIsEmpty(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	if got := ConfigPath(); got != "" {
		t.Errorf("ConfigPath() = %q, want empty", got)
	}
	if got := LogsDir(); got != "" {
		t.Errorf("LogsDir() = %q, want empty", got)
	}
	if got := ServiceLogPath(); got != "" {
		t.Errorf("ServiceLogPath() = %q, want empty", got)
	}
	if got := StderrLogPath(); got != "" {
		t.Errorf("StderrLogPath() = %q, want empty", got)
	}
	if got := PanelErrorLogPath(); got != "" {
		t.Errorf("PanelErrorLogPath() = %q, want empty", got)
	}
}

func TestEnsureDirsCreatesBothLevels(t *testing.T) {
	root := filepath.Join(t.TempDir(), "SerialHop")
	t.Setenv("SERIALHOP_DATA_DIR", root)

	if err := EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	for _, p := range []string{root, filepath.Join(root, "logs")} {
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("stat %q: %v", p, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", p)
		}
	}
}

func TestEnsureDirsIsIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "SerialHop")
	t.Setenv("SERIALHOP_DATA_DIR", root)

	if err := EnsureDirs(); err != nil {
		t.Fatalf("first EnsureDirs: %v", err)
	}
	if err := EnsureDirs(); err != nil {
		t.Fatalf("second EnsureDirs: %v", err)
	}
}

func TestEnsureDirsErrorsWhenDataDirEmpty(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	if err := EnsureDirs(); err == nil {
		t.Fatal("EnsureDirs returned nil, want error")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail (compile error)**

```bash
cd /Users/khamitovdr/lab_devices_client
go test ./internal/paths/...
```

Expected: build failure (`undefined: DataDir`, `undefined: ConfigPath`, etc.) — the package doesn't exist yet.

- [ ] **Step 3: Implement the package**

Create `internal/paths/paths.go`:

```go
// Package paths owns the on-disk layout for the SerialHop client.
//
// In production, DataDir resolves to %ProgramData%\SerialHop. The
// SERIALHOP_DATA_DIR environment variable, if set, overrides this —
// tests use t.Setenv("SERIALHOP_DATA_DIR", t.TempDir()) for isolation.
//
// All composed-path getters (ConfigPath, LogsDir, ServiceLogPath,
// StderrLogPath, PanelErrorLogPath) return "" when DataDir() returns ""
// (i.e., %ProgramData% is unset and no test override is in effect).
// Callers can detect "no data dir available" with a single empty-string
// check.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	ConfigFileName        = "SerialHop_config.yaml"
	ServiceLogFileName    = "SerialHop.log"
	StderrLogFileName     = "SerialHop_stderr.log"
	PanelErrorLogFileName = "SerialHop_panel_error.log"
)

// DataDir returns the SerialHop root data directory.
// SERIALHOP_DATA_DIR overrides %ProgramData% for tests. Returns ""
// when neither is set.
func DataDir() string {
	if v := os.Getenv("SERIALHOP_DATA_DIR"); v != "" {
		return v
	}
	if pd := os.Getenv("ProgramData"); pd != "" {
		return filepath.Join(pd, "SerialHop")
	}
	return ""
}

// LogsDir returns <DataDir>/logs, or "" if DataDir is empty.
func LogsDir() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "logs")
}

// ConfigPath returns <DataDir>/SerialHop_config.yaml, or "" if DataDir is empty.
func ConfigPath() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, ConfigFileName)
}

// ServiceLogPath returns <LogsDir>/SerialHop.log, or "" if LogsDir is empty.
func ServiceLogPath() string {
	d := LogsDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, ServiceLogFileName)
}

// StderrLogPath returns <LogsDir>/SerialHop_stderr.log, or "" if LogsDir is empty.
func StderrLogPath() string {
	d := LogsDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, StderrLogFileName)
}

// PanelErrorLogPath returns <LogsDir>/SerialHop_panel_error.log,
// or "" if LogsDir is empty.
func PanelErrorLogPath() string {
	d := LogsDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, PanelErrorLogFileName)
}

// EnsureDirs creates DataDir and LogsDir with os.MkdirAll (0o755).
// Idempotent. Returns an error if DataDir() is empty or MkdirAll fails.
func EnsureDirs() error {
	d := DataDir()
	if d == "" {
		return errors.New("paths: data directory unavailable (%ProgramData% not set)")
	}
	logs := filepath.Join(d, "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return fmt.Errorf("paths: create %s: %w", logs, err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/paths/... -v
```

Expected: all 8 tests PASS.

- [ ] **Step 5: Run vet + lint to keep CI happy**

```bash
go vet ./internal/paths/...
gofmt -l internal/paths/
```

Expected: no output from either.

- [ ] **Step 6: Commit**

```bash
git add internal/paths/paths.go internal/paths/paths_test.go
git commit -m "$(cat <<'EOF'
feat(paths): new internal/paths package for on-disk layout

Single source of truth for where config and logs live. DataDir()
returns %ProgramData%\SerialHop, overridable via SERIALHOP_DATA_DIR
for tests. Composed getters return "" when no data dir is available
so callers can detect the missing-env-var case with a single check.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Migrate `internal/logship` to use `paths`

**Files:**
- Modify: `internal/logship/logship.go`
- Modify: `internal/logship/logship_test.go`
- Modify: `internal/winsvc/worker.go`

`logship.Init(dir, version, level)` becomes `Init(version, level)`. Filenames come from `paths`. The only consumer is `winsvc/worker.go`, which gets updated in the same commit so the build stays green.

- [ ] **Step 1: Update `logship_test.go` to use the new signature**

Replace `internal/logship/logship_test.go`. The structural change in each test is: replace `dir := t.TempDir(); ... Init(dir, ...)` with `dir := t.TempDir(); t.Setenv("SERIALHOP_DATA_DIR", dir); ... Init(...)`. Update the on-disk path assertions to use `filepath.Join(dir, "logs", LogFileName)` where `LogFileName` (the test-local constant) is replaced by `"SerialHop.log"`.

Concrete replacement for the whole file:

```go
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
```

Also add a new test for the missing-env-var case at the end of the file:

```go
func TestInitErrorsWhenDataDirUnavailable(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	// Init must not panic, must not create disk taps, must return an error.
	if _, err := Init("1.4.2", slog.LevelInfo); err == nil {
		t.Fatal("Init returned nil, want error when data dir unavailable")
	}
}
```

- [ ] **Step 2: Verify tests fail to compile**

```bash
cd /Users/khamitovdr/lab_devices_client
go build ./internal/logship/...
```

Expected: build failure — `Init(dir, ...)` still takes three args in `logship.go`; tests pass two. The test-side reference to the `LogFileName` constant is also gone (we inlined `"SerialHop.log"` in the asserts).

- [ ] **Step 3: Update `internal/logship/logship.go`**

Replace the file:

```go
// Package logship streams the client's slog output and stderr to the
// in-VPS Loki via the chisel forward tunnel.
//
// It also owns the durable on-disk log files (SerialHop.log,
// SerialHop_stderr.log) so disabling the shipper does not
// affect on-disk logging.
package logship

import (
	"context"
	"log/slog"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
)

// defaultPushURL is the local end of the chisel forward tunnel that
// reaches the in-VPS Loki.
const defaultPushURL = "http://127.0.0.1:3100/loki/api/v1/push"

// Manager owns the capture taps, ring buffer, and shipper goroutine.
type Manager struct {
	version string

	levelVar *slog.LevelVar

	slogDisk   *lumberjack.Logger
	stderrDisk *lumberjack.Logger
	stderrTap  *stderrTap

	q *queue

	mu       sync.Mutex
	pushURL  string
	shipperC int // count of shippers started (for tests)
	shipCtx  context.Context
	shipStop context.CancelFunc
	shipDone chan struct{}
}

// Init builds the on-disk log writers, allocates the ring buffer, and
// installs the slog and stderr taps. Log file paths come from the
// internal/paths package — call paths.EnsureDirs() before Init.
// The shipper is NOT started yet — call StartShipper once the chisel
// user is known.
func Init(version string, level slog.Level) (*Manager, error) {
	servicePath := paths.ServiceLogPath()
	stderrPath := paths.StderrLogPath()
	if servicePath == "" || stderrPath == "" {
		return nil, errInitMissingPaths
	}

	m := &Manager{
		version:  version,
		levelVar: new(slog.LevelVar),
		pushURL:  defaultPushURL,
		q:        newQueue(10_000),
	}
	m.levelVar.Set(level)

	m.slogDisk = &lumberjack.Logger{
		Filename:   servicePath,
		MaxSize:    10,
		MaxBackups: 3,
		LocalTime:  true,
	}
	m.stderrDisk = &lumberjack.Logger{
		Filename:   stderrPath,
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
		"service": "serialhop",
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
```

And add at the top of the file (or in a new tiny `errors.go`) the sentinel:

```go
var errInitMissingPaths = errors.New("logship: paths.ServiceLogPath/StderrLogPath unavailable; call paths.EnsureDirs first")
```

Add `"errors"` to the import block.

- [ ] **Step 4: Update `internal/winsvc/worker.go` to use the new `Init` signature**

Edit `worker.go`. Replace the imports block, drop the `configFileName` constant, and rewrite `RunWorker` plus the `handler` struct:

```go
//go:build windows

package winsvc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/app"
	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/logship"
	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
	"github.com/bioexperiment-lab-devices/serialhop/internal/version"

	"golang.org/x/sys/windows/svc"
)

const (
	workerStopGracePeriod = 30 * time.Second
	logshipShutdown       = 2 * time.Second
)

// RunWorker is the service-mode entry point. It must only be called when
// svc.IsWindowsService() returns true. It initializes log streaming
// before svc.Run so that even a config-load failure is captured both
// on disk and (if a previous successful run cached chisel auth) in
// Loki on the next push.
func RunWorker() error {
	if err := paths.EnsureDirs(); err != nil {
		return fmt.Errorf("paths setup: %w", err)
	}

	manager, err := logship.Init(version.Version, slog.LevelInfo)
	if err != nil {
		return fmt.Errorf("logship init: %w", err)
	}

	return svc.Run(ServiceName, &handler{manager: manager})
}

type handler struct {
	manager *logship.Manager
}

func (h *handler) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}

	cfgPath := paths.ConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("config load failed", "path", cfgPath, "err", err)
		h.shutdownLogship()
		changes <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
		return false, 1
	}
	h.manager.SetLevel(logship.ParseLogLevel(cfg.Log.Level))
	h.manager.StartShipper(cfg.Chisel.User)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	appDone := make(chan error, 1)
	go func() {
		appDone <- app.Run(ctx, cfg)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: accepts}

	for {
		select {
		case req := <-r:
			switch req.Cmd {
			case svc.Interrogate:
				changes <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				slog.Info("service stop requested")
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case err := <-appDone:
					if err != nil {
						slog.Error("app exited with error during stop", "err", err)
					}
				case <-time.After(workerStopGracePeriod):
					slog.Error("app did not exit within grace period; forcing stop", "grace", workerStopGracePeriod)
				}
				h.shutdownLogship()
				changes <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		case err := <-appDone:
			if err != nil {
				slog.Error("app exited unexpectedly", "err", err)
				h.shutdownLogship()
				changes <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
				return false, 1
			}
			slog.Info("app exited cleanly")
			h.shutdownLogship()
			changes <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

func (h *handler) shutdownLogship() {
	ctx, cancel := context.WithTimeout(context.Background(), logshipShutdown)
	defer cancel()
	h.manager.Shutdown(ctx)
}
```

Note: imports of `os`, `path/filepath` are removed; they were only needed for the previous `os.Executable()` / `filepath.Dir` calls.

- [ ] **Step 5: Run tests to verify everything passes**

```bash
go test ./internal/paths/... ./internal/logship/... -count=1
go build ./...
```

Expected: all tests pass; build succeeds (including `./internal/winsvc/...` on Windows, no-op on non-Windows).

- [ ] **Step 6: Run vet + lint**

```bash
go vet ./internal/logship/... ./internal/winsvc/...
gofmt -l internal/logship/ internal/winsvc/
```

Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add internal/logship/logship.go internal/logship/logship_test.go internal/winsvc/worker.go
git commit -m "$(cat <<'EOF'
refactor(logship): consume paths package, drop dir parameter from Init

logship.Init now reads its file paths from internal/paths instead of
taking a dir argument. The only caller (winsvc/worker.go) is updated
to call paths.EnsureDirs() at startup and pass version/level only.
Constants LogFileName / StderrLogFileName moved to paths package.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Drop `logExists` from `ComputeButtons`

**Files:**
- Modify: `internal/panel/state.go`
- Modify: `internal/panel/state_test.go`
- Modify: `internal/panel/panel.go` (caller — single line)

The "Open log file" button is being replaced by "Open logs folder", which doesn't gate on file existence. The `OpenLog` field and `logExists` parameter become dead weight.

- [ ] **Step 1: Update `state_test.go` to drop `logExists`**

Replace `internal/panel/state_test.go`:

```go
package panel

import (
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

func TestComputeButtons(t *testing.T) {
	cases := []struct {
		name     string
		state    winsvc.ServiceState
		cfgValid bool
		want     ButtonState
	}{
		{
			name:     "not installed, valid config",
			state:    winsvc.StateNotInstalled,
			cfgValid: true,
			want:     ButtonState{Install: true},
		},
		{
			name:     "not installed, invalid config",
			state:    winsvc.StateNotInstalled,
			cfgValid: false,
			want:     ButtonState{},
		},
		{
			name:     "running",
			state:    winsvc.StateRunning,
			cfgValid: true,
			want:     ButtonState{Uninstall: true, Restart: true},
		},
		{
			name:     "stopped",
			state:    winsvc.StateStopped,
			cfgValid: true,
			want:     ButtonState{Uninstall: true, Restart: true},
		},
		{
			name:  "starting (transient, all disabled)",
			state: winsvc.StateStartPending,
			want:  ButtonState{},
		},
		{
			name:  "stopping (transient, all disabled)",
			state: winsvc.StateStopPending,
			want:  ButtonState{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeButtons(tc.state, tc.cfgValid)
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestStatusColor(t *testing.T) {
	cases := []struct {
		state    winsvc.ServiceState
		cfgValid bool
		want     StatusColor
	}{
		{winsvc.StateRunning, true, ColorGreen},
		{winsvc.StateStartPending, true, ColorYellow},
		{winsvc.StateStopPending, true, ColorYellow},
		{winsvc.StateStopped, true, ColorGrey},
		{winsvc.StateNotInstalled, true, ColorGrey},
		{winsvc.StateNotInstalled, false, ColorRed},
	}
	for _, tc := range cases {
		got := StatusIndicator(tc.state, tc.cfgValid)
		if got != tc.want {
			t.Errorf("state=%v cfgValid=%v: got %v, want %v", tc.state, tc.cfgValid, got, tc.want)
		}
	}
}
```

The two changes vs. the old file: the `logExists` field and the corresponding case `"log exists toggles OpenLog"` are deleted; `ComputeButtons(tc.state, tc.cfgValid, tc.logExists)` becomes `ComputeButtons(tc.state, tc.cfgValid)`.

- [ ] **Step 2: Run tests to verify they fail to compile**

```bash
go build ./internal/panel/...
```

Expected: build failure — `ComputeButtons` in `state.go` still expects three args but the test passes two; the `OpenLog` field is referenced but consistent (it's just unused by the test now).

- [ ] **Step 3: Update `state.go`**

Replace `internal/panel/state.go`:

```go
package panel

import "github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"

type ButtonState struct {
	Install   bool
	Uninstall bool
	Restart   bool
}

type StatusColor int

const (
	ColorGrey StatusColor = iota
	ColorYellow
	ColorGreen
	ColorRed
)

// ComputeButtons returns which admin buttons should be enabled given the
// current SCM state and whether the config validates. The file-action
// buttons ("Open config file", "Open logs folder") are not gated through
// this function — they're enabled whenever paths.EnsureDirs() succeeded
// at panel startup.
func ComputeButtons(state winsvc.ServiceState, cfgValid bool) ButtonState {
	var bs ButtonState
	switch state {
	case winsvc.StateNotInstalled:
		bs.Install = cfgValid
	case winsvc.StateStopped, winsvc.StateRunning:
		bs.Uninstall = true
		bs.Restart = true
	case winsvc.StateStartPending, winsvc.StateStopPending:
		// transient states: nothing enabled
	}
	return bs
}

// StatusIndicator returns the color of the status dot for a given state.
// Red is reserved for "not installed AND config invalid".
func StatusIndicator(state winsvc.ServiceState, cfgValid bool) StatusColor {
	switch state {
	case winsvc.StateRunning:
		return ColorGreen
	case winsvc.StateStartPending, winsvc.StateStopPending:
		return ColorYellow
	case winsvc.StateStopped:
		return ColorGrey
	case winsvc.StateNotInstalled:
		if !cfgValid {
			return ColorRed
		}
		return ColorGrey
	default:
		return ColorGrey
	}
}
```

- [ ] **Step 4: Update the one caller in `panel.go`**

In `internal/panel/panel.go`, find the line in `refresh`:

```go
btns := ComputeButtons(state, cfgErr == nil, logExists)
```

(panel.go line ~147). Replace with:

```go
btns := ComputeButtons(state, cfgErr == nil)
```

Also delete the surrounding lines that compute `logExists` (panel.go ~112-113):

```go
_, logStatErr := os.Stat(logPath)
logExists := logStatErr == nil
```

And delete the line that uses `OpenLog` (panel.go ~151):

```go
btnOpenLog.SetEnabled(btns.OpenLog)
```

(Leave `btnOpenLog` itself for now — Task 4 deletes it entirely. The line that sets its enabled state simply goes away.)

- [ ] **Step 5: Verify build + tests pass**

```bash
go build ./...
go test ./internal/panel/... -count=1
```

Expected: build succeeds (the panel package builds on macOS too, since `state.go` / `state_test.go` are not Windows-gated); both panel tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/panel/state.go internal/panel/state_test.go internal/panel/panel.go
git commit -m "$(cat <<'EOF'
refactor(panel): drop logExists from ComputeButtons

The "Open log file" button is being replaced by "Open logs folder",
which doesn't gate on file existence — the directory always exists
after paths.EnsureDirs(). Drop the OpenLog field, logExists parameter,
and the os.Stat probe in refresh().

The full button-row swap happens in the next commit (Task 4).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Rewrite the panel UI to use `paths`

**Files:**
- Modify: `internal/panel/panel.go`

This task replaces the "Open log file" button with "Open logs folder", routes all path lookups through `paths`, and refactors `writePanelDebugLog` to drop its `installDir` argument. The `installDir` local in `Run` stays but is repurposed (used only for update-staging paths).

- [ ] **Step 1: Update the constants and `Run` prologue**

In `internal/panel/panel.go`:

Replace the `const` block at the top:

```go
const (
	configFileName = "SerialHop_config.yaml"
	logFileName    = "SerialHop.log"
	pollInterval   = 1 * time.Second
)
```

with:

```go
const pollInterval = 1 * time.Second
```

Then update the `Run` prologue. Find:

```go
func Run() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	dir := filepath.Dir(exePath)
	cfgPath := filepath.Join(dir, configFileName)
	logPath := filepath.Join(dir, logFileName)

	if err := ensureScaffold(cfgPath); err != nil {
		// Non-fatal: the panel can still run; it'll show "config missing".
		_ = err
	}
```

Replace with:

```go
func Run() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	installDir := filepath.Dir(exePath)
	pathsErr := paths.EnsureDirs() // non-fatal: surfaced via warn label and disabled file buttons

	cfgPath := paths.ConfigPath()
	if pathsErr == nil {
		if err := ensureScaffold(cfgPath); err != nil {
			// Non-fatal: the panel can still run; it'll show "config missing".
			_ = err
		}
	}
```

Add this import to the import block:

```go
"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
```

- [ ] **Step 2: Update `refresh` to drop the log-existence probe and to disable file buttons when `pathsErr != nil`**

Find the `refresh` closure (panel.go ~104-152). Replace the body up through the button-state lines with:

```go
	refresh := func() {
		state, ok := queryServiceState()
		if !ok {
			state = lastState
		} else {
			lastState = state
		}
		cfg, cfgErr := config.LoadPartial(cfgPath)

		statusLabel.SetText(state.String())
		statusDot.SetText("●")
		switch StatusIndicator(state, cfgErr == nil) {
		case ColorGreen:
			statusDot.SetTextColor(walk.RGB(0, 160, 0))
		case ColorYellow:
			statusDot.SetTextColor(walk.RGB(200, 160, 0))
		case ColorRed:
			statusDot.SetTextColor(walk.RGB(192, 0, 0))
		default:
			statusDot.SetTextColor(walk.RGB(128, 128, 128))
		}

		serverLbl.SetText("Chisel server:    " + cfg.Chisel.Server)
		remotePort.SetText(fmt.Sprintf("Remote port:      %d", cfg.Chisel.RemotePort))
		restPort.SetText(fmt.Sprintf("REST port:        %d", cfg.Rest.Port))
		discoveryLbl.SetText(fmt.Sprintf("Discovery:        include=%v, exclude=%v", cfg.Discovery.Include, cfg.Discovery.Exclude))
		rawSerialState := "disabled"
		if cfg.RawSerial.Enabled {
			rawSerialState = "enabled"
		}
		rawSerialLbl.SetText("Raw serial:       " + rawSerialState)
		logLevel.SetText("Log level:        " + cfg.Log.Level)

		switch {
		case pathsErr != nil:
			warnLabel.SetText("⚠ " + pathsErr.Error())
			warnLabel.SetVisible(true)
		case cfgErr != nil:
			warnLabel.SetText("⚠ " + cfgErr.Error())
			warnLabel.SetVisible(true)
		default:
			warnLabel.SetText("")
			warnLabel.SetVisible(false)
		}

		btns := ComputeButtons(state, cfgErr == nil)
		btnInstall.SetEnabled(btns.Install)
		btnUninstall.SetEnabled(btns.Uninstall)
		btnRestart.SetEnabled(btns.Restart)
		btnOpenCfg.SetEnabled(pathsErr == nil)
		btnOpenLogs.SetEnabled(pathsErr == nil)
	}
```

Note: `btnOpenLogs` is the new variable name (replaces `btnOpenLog`). Renaming is done in the next step.

- [ ] **Step 3: Update the button variable declarations and the declarative layout**

Find the variable declarations block near the top of `Run` (panel.go ~57-85). The line:

```go
		btnOpenLog   *walk.PushButton
```

becomes:

```go
		btnOpenLogs  *walk.PushButton
```

Then find the second button row in the declarative layout (panel.go ~242-256):

```go
				Composite{
					Layout: HBox{},
					Children: []Widget{
						PushButton{AssignTo: &btnOpenCfg, Text: "Open config file", OnClicked: func() {
							if err := OpenWithDefaultApp(cfgPath); err != nil {
								walk.MsgBox(mw, "Error", err.Error(), walk.MsgBoxIconError)
							}
						}},
						PushButton{AssignTo: &btnOpenLog, Text: "Open log file", OnClicked: func() {
							if err := OpenWithDefaultApp(logPath); err != nil {
								walk.MsgBox(mw, "Error", err.Error(), walk.MsgBoxIconError)
							}
						}},
					},
				},
```

Replace with:

```go
				Composite{
					Layout: HBox{},
					Children: []Widget{
						PushButton{AssignTo: &btnOpenCfg, Text: "Open config file", OnClicked: func() {
							if err := OpenWithDefaultApp(paths.ConfigPath()); err != nil {
								walk.MsgBox(mw, "Error", err.Error(), walk.MsgBoxIconError)
							}
						}},
						PushButton{AssignTo: &btnOpenLogs, Text: "Open logs folder", OnClicked: func() {
							if err := OpenWithDefaultApp(paths.LogsDir()); err != nil {
								walk.MsgBox(mw, "Error", err.Error(), walk.MsgBoxIconError)
							}
						}},
					},
				},
```

- [ ] **Step 4: Refactor `writePanelDebugLog` to drop its `installDir` argument**

Find the function at the bottom of the file (panel.go ~623-635):

```go
// writePanelDebugLog appends a single line to SerialHop_panel_error.log.
// Used for failures the operator might want to inspect post-mortem without
// surfacing a popup. Best-effort.
func writePanelDebugLog(installDir, code string, err error) {
	line := fmt.Sprintf("%s %s: %v\n", time.Now().Format(time.RFC3339), code, err)
	f, ferr := os.OpenFile(filepath.Join(installDir, "SerialHop_panel_error.log"), //nolint:gosec // path is constructed from the install directory; not user-controlled
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close() //nolint:errcheck
	_, _ = f.WriteString(line)
}
```

Replace with:

```go
// writePanelDebugLog appends a single line to SerialHop_panel_error.log
// inside %ProgramData%\SerialHop\logs\. Used for failures the operator
// might want to inspect post-mortem without surfacing a popup.
// Best-effort: if the target path is unreachable (paths.LogsDir() == ""),
// the entry is silently dropped.
func writePanelDebugLog(code string, err error) {
	target := paths.PanelErrorLogPath()
	if target == "" {
		return
	}
	line := fmt.Sprintf("%s %s: %v\n", time.Now().Format(time.RFC3339), code, err)
	f, ferr := os.OpenFile(target, //nolint:gosec // target is paths.PanelErrorLogPath(), not user-controlled
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close() //nolint:errcheck
	_, _ = f.WriteString(line)
}
```

- [ ] **Step 5: Update every `writePanelDebugLog` call site**

Find every call to `writePanelDebugLog(installDir, ...)` in `panel.go` and drop the first argument. Six call sites (line numbers approximate):

```go
writePanelDebugLog(installDir, "update_check_failed", err)
writePanelDebugLog(installDir, "update_check_parse_failed", err)
writePanelDebugLog(installDir, "update_check_no_asset", fmt.Errorf("no SerialHop-v*.exe asset on release %s", rel.TagName))
writePanelDebugLog(installDir, "update_download_failed", err)
writePanelDebugLog(installDir, "update_no_sums_asset", fmt.Errorf("release %s has no SHA256SUMS.txt", rel.TagName))
writePanelDebugLog(installDir, "update_fetch_sums_failed", err)
writePanelDebugLog(installDir, "update_verify_failed", err)
```

Each becomes:

```go
writePanelDebugLog("update_check_failed", err)
writePanelDebugLog("update_check_parse_failed", err)
writePanelDebugLog("update_check_no_asset", fmt.Errorf("no SerialHop-v*.exe asset on release %s", rel.TagName))
writePanelDebugLog("update_download_failed", err)
writePanelDebugLog("update_no_sums_asset", fmt.Errorf("release %s has no SHA256SUMS.txt", rel.TagName))
writePanelDebugLog("update_fetch_sums_failed", err)
writePanelDebugLog("update_verify_failed", err)
```

A quick sanity check command:

```bash
grep -n "writePanelDebugLog(installDir" internal/panel/panel.go
```

Expected after the edit: no output.

Also check the function signatures of `runUpdateCheck`, `ctlDownload`, `ctlInstall`. They still take `installDir` as a parameter because they need it for `cleanupStaleStagedFiles`, the staged-file path, etc. — leave those signatures alone.

- [ ] **Step 6: Verify build + tests pass**

The panel package compiles only on Windows for `panel.go` (the `//go:build windows` file). Run a Windows-targeted build:

```bash
GOOS=windows GOARCH=amd64 go build ./...
go test ./internal/panel/... -count=1   # state.go tests run cross-platform
```

Expected: both succeed.

- [ ] **Step 7: Run vet + lint**

```bash
go vet ./internal/panel/...
gofmt -l internal/panel/
```

Expected: no output.

- [ ] **Step 8: Commit**

```bash
git add internal/panel/panel.go
git commit -m "$(cat <<'EOF'
feat(panel): use paths package; replace 'Open log file' with 'Open logs folder'

The panel now consults internal/paths for the config and logs locations.
The 'Open log file' button is replaced by 'Open logs folder', which
opens %ProgramData%\SerialHop\logs\ in Explorer — letting operators
reach all rotated backups, the stderr log, and the panel-error log
from one place.

writePanelDebugLog now writes to the new logs folder via
paths.PanelErrorLogPath(). The installDir local in Run is retained
but only used for update-staging paths.

paths.EnsureDirs failures are surfaced via the warn label; the file
buttons are disabled when the data directory is unreachable.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Refactor `cmd/serialhop/main.go`

**Files:**
- Modify: `cmd/serialhop/main.go`
- Create: `cmd/serialhop/startup_error_test.go`

Foreground developer mode now reads/writes the shared config in `%ProgramData%\SerialHop\`. `writePanelStartupError` uses `paths.PanelErrorLogPath()` when available, falling back to the install dir as a last-resort breadcrumb. The fallback path-selection is extracted into a pure helper so it can be tested.

- [ ] **Step 1: Write the failing test**

Create `cmd/serialhop/startup_error_test.go`:

```go
package main

import (
	"path/filepath"
	"testing"
)

func TestPanelErrorPathPrefersDataDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SERIALHOP_DATA_DIR", root)

	got := panelErrorPath(`C:\Tools\SerialHop`)
	want := filepath.Join(root, "logs", "SerialHop_panel_error.log")
	if got != want {
		t.Errorf("panelErrorPath() = %q, want %q", got, want)
	}
}

func TestPanelErrorPathFallsBackToExeDirWhenDataDirEmpty(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")

	got := panelErrorPath(`C:\Tools\SerialHop`)
	want := filepath.Join(`C:\Tools\SerialHop`, "SerialHop_panel_error.log")
	if got != want {
		t.Errorf("panelErrorPath() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./cmd/serialhop/... -run TestPanelErrorPath -count=1
```

Expected: build failure — `undefined: panelErrorPath`.

- [ ] **Step 3: Implement the helper and rewire `writePanelStartupError`**

In `cmd/serialhop/main.go`, find and update the relevant blocks.

First, the import block — add `"github.com/bioexperiment-lab-devices/serialhop/internal/paths"`. Drop nothing yet (we still use `os`, `filepath`).

Drop the constant:

```go
const configFileName = "SerialHop_config.yaml"
```

Replace `writePanelStartupError` (main.go ~89-97):

```go
// writePanelStartupError records a panel startup failure to a file so
// the operator can see what went wrong. Stderr is `/dev/null` under
// the windowsgui subsystem, so without this the failure is invisible.
// Writes to %ProgramData%\SerialHop\logs\ when that path is reachable,
// otherwise falls back to a file next to the .exe — the only place in
// the codebase that still writes a log entry to the install directory,
// and only when the new layout is unreachable.
func writePanelStartupError(panelErr error) {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	target := panelErrorPath(filepath.Dir(exePath))
	line := fmt.Sprintf("%s panel startup failed: %v\n", time.Now().Format(time.RFC3339), panelErr)
	_ = os.WriteFile(target, []byte(line), 0o600)
}

// panelErrorPath returns the path for the panel-error log:
// paths.PanelErrorLogPath() when DataDir is available, else
// <exeDir>\SerialHop_panel_error.log as a last-resort breadcrumb.
// Pure function — testable without touching the filesystem.
func panelErrorPath(exeDir string) string {
	if p := paths.PanelErrorLogPath(); p != "" {
		return p
	}
	return filepath.Join(exeDir, paths.PanelErrorLogFileName)
}
```

Replace `runForeground` (main.go ~99-131):

```go
func runForeground() error {
	if err := paths.EnsureDirs(); err != nil {
		return fmt.Errorf("paths setup: %w", err)
	}
	cfgPath := paths.ConfigPath()

	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		f, err := os.Create(cfgPath) //nolint:gosec // cfgPath is paths.ConfigPath(), not user-controlled
		if err != nil {
			return fmt.Errorf("create scaffold: %w", err)
		}
		if writeErr := config.WriteScaffold(f); writeErr != nil {
			_ = f.Close()
			return fmt.Errorf("write scaffold: %w", writeErr)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close scaffold: %w", err)
		}
		fmt.Printf("Config file created at %s. Please review and edit it, then run again.\n", cfgPath)
		return errors.New("config scaffold generated; please edit and rerun")
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	configureStdoutLogger(cfg.Log.Level)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return app.Run(ctx, cfg)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./cmd/serialhop/... -count=1
```

Expected: both `TestPanelErrorPath*` tests PASS.

- [ ] **Step 5: Build the whole tree to make sure nothing regressed**

```bash
go build ./...
GOOS=windows GOARCH=amd64 go build ./...
```

Expected: both succeed.

- [ ] **Step 6: Run vet + lint + gofmt across the whole module**

```bash
go vet ./...
gofmt -l .
```

Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add cmd/serialhop/main.go cmd/serialhop/startup_error_test.go
git commit -m "$(cat <<'EOF'
refactor(cmd): foreground mode uses paths; panelErrorPath helper

runForeground now creates and reads %ProgramData%\SerialHop\
SerialHop_config.yaml, sharing config with the service. Behavior
change: on a dev machine with the service installed, both modes
see the same config file.

writePanelStartupError picks its destination via a new pure helper
panelErrorPath(exeDir) — prefers paths.PanelErrorLogPath(), falls
back to <exeDir>\SerialHop_panel_error.log when the data directory
is unreachable. Tests cover both branches.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Run the full pre-flight check, then update docs

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-05-11-auto-update-design.md`

- [ ] **Step 1: Full local pre-flight (matches `pr.yml`'s verify job)**

```bash
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

Optional but recommended (catches CI failures locally):

```bash
golangci-lint run
govulncheck ./...
```

Expected: all clean. If anything fails, fix in place before continuing.

- [ ] **Step 2: Update `README.md`**

Find this paragraph (around the "Install on a Windows lab machine" section):

```
- Logs go to `SerialHop.log` (slog JSON) and `SerialHop_stderr.log` (chisel state, panic traces) next to the .exe — both rotated at 10 MB with 3 backups. Click **Open log file** to view the main log.
```

Replace with:

```
- Logs go to `%ProgramData%\SerialHop\logs\` (`SerialHop.log` for slog JSON, `SerialHop_stderr.log` for chisel state and panic traces, both rotated at 10 MB with 3 backups). Click **Open logs folder** to open the directory in Explorer.
- Config lives at `%ProgramData%\SerialHop\SerialHop_config.yaml`. Click **Open config file** to edit.
```

Find this earlier paragraph (step 2 of "Install on a Windows lab machine"):

```
2. Double-click the .exe. The control panel opens. On first launch it writes `SerialHop_config.yaml` next to the .exe and shows a validation warning if anything's wrong.
```

Replace with:

```
2. Double-click the .exe. The control panel opens. On first launch it creates `%ProgramData%\SerialHop\` and writes a `SerialHop_config.yaml` scaffold there, then shows a validation warning if anything's wrong.
```

Find this paragraph in the "Log streaming to Loki" section:

```
In service mode, the client streams every line written to `SerialHop.log` and `SerialHop_stderr.log` to the in-VPS Loki via a forward tunnel (`127.0.0.1:3100 → loki:3100`) added to the same chisel session. The on-disk rotated files remain the durable record; Loki is a queryable mirror.
```

No change needed — the filenames still match; only the directory changed and that's covered in the install section above.

- [ ] **Step 3: Update `docs/superpowers/specs/2026-05-11-auto-update-design.md`**

The auto-update spec references `SerialHop_panel_error.log` "next to the .exe" in several places. Update them in place.

Line ~106 (in the state-machine table):

```
| Network error during check | (no row; logged to `SerialHop_panel_error.log` at debug) | — |
```

Replace with:

```
| Network error during check | (no row; logged to `%ProgramData%\SerialHop\logs\SerialHop_panel_error.log` at debug) | — |
```

Line ~118:

```
- Network failures (DNS, TCP, TLS, non-200 HTTP) are logged to `SerialHop_panel_error.log` at one line per failure with `time.Now().Format(time.RFC3339)` prefix, and the update row stays hidden. No popup, no status-bar noise — a flaky upstream shouldn't badger the operator.
```

Replace with:

```
- Network failures (DNS, TCP, TLS, non-200 HTTP) are logged to `%ProgramData%\SerialHop\logs\SerialHop_panel_error.log` at one line per failure with `time.Now().Format(time.RFC3339)` prefix, and the update row stays hidden. No popup, no status-bar noise — a flaky upstream shouldn't badger the operator.
```

Line ~125:

```
- After the body completes, fetch `SHA256SUMS.txt` from the same release (separate request, 10 s timeout). Parse `<hex>  <filename>` lines, find the row whose filename equals `<asset.Name>`, compare against the SHA-256 of the downloaded file. On mismatch: delete the file, set the row to the red "checksum mismatch" state, log full detail to `SerialHop_panel_error.log`.
```

Replace `SerialHop_panel_error.log` at the end with `%ProgramData%\SerialHop\logs\SerialHop_panel_error.log`.

Line ~258:

```
| Update check network failure | Silent in panel; `SerialHop_panel_error.log` | (logged only) |
```

Replace with:

```
| Update check network failure | Silent in panel; `%ProgramData%\SerialHop\logs\SerialHop_panel_error.log` | (logged only) |
```

A grep to confirm nothing was missed:

```bash
grep -n "SerialHop_panel_error.log" docs/superpowers/specs/2026-05-11-auto-update-design.md
```

Every match should be prefixed with `%ProgramData%\SerialHop\logs\`.

- [ ] **Step 4: Commit**

```bash
git add README.md docs/superpowers/specs/2026-05-11-auto-update-design.md
git commit -m "$(cat <<'EOF'
docs: update README and auto-update spec for new layout

Reflect the move of config and logs into %ProgramData%\SerialHop\.
The README now points operators at the new paths and the renamed
'Open logs folder' button. The auto-update spec's references to
SerialHop_panel_error.log are updated in place.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 5: Final whole-tree sanity check**

```bash
git log --oneline docs/config-logs-layout ^main
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

Expected: six commits ahead of `main` (one spec + five code/docs commits, or six code/docs commits if you count the spec on this branch); no fmt issues; clean vet; all tests pass.

The branch is now ready for PR. Per `CLAUDE.md`, use a Conventional Commits-style title; suggested:

```
feat: relocate config and logs to %ProgramData%, add Open logs folder button
```

That title will become the squash commit on `main`, which release-please reads to bump the minor version. If you'd prefer a patch bump, use `fix:` instead — but `feat:` is the more honest description.

---

## Spec coverage check

| Spec section | Covered by |
|---|---|
| §2 on-disk layout | Tasks 1, 2, 4, 5 (file paths produced via `paths` package) |
| §3 paths package API | Task 1 |
| §4.1 logship refactor | Task 2 |
| §4.2 winsvc/worker refactor | Task 2 |
| §4.3 panel refactor | Tasks 3, 4 |
| §4.4 state.go pruning | Task 3 |
| §4.5 cmd/serialhop refactor | Task 5 |
| §5 UI changes | Task 4 |
| §6 edge cases (env-var missing, file ownership) | Tasks 1 (test), 4 (warn label), 5 (fallback helper) |
| §7 testing | Tasks 1 (paths), 2 (logship), 3 (state), 5 (panelErrorPath) |
| §8 doc updates | Task 6 |

All sections accounted for.
