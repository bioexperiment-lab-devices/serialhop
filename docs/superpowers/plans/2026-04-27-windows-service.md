# Windows Service & Control Panel — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Repackage `lab_devices_client` so it runs as a Windows service installed/managed from a small native control-panel window. The current chisel/REST/discovery behavior is unchanged.

**Architecture:** One `.exe`, one icon. The binary dispatches at startup based on context: launched by SCM → service worker; launched with `--admin-action=...` → elevated SCM action subcommand; launched with `--foreground` → existing console behavior; otherwise → walk control panel. The panel runs unelevated and re-launches itself with `ShellExecuteEx "runas"` for install/uninstall/restart.

**Tech Stack:** Go 1.25, `github.com/lxn/walk` (native Win32 GUI, no CGO), `gopkg.in/natefinch/lumberjack.v2` (log rotation), `golang.org/x/sys/windows/svc` (service handler) and `.../svc/mgr` (SCM client), `github.com/josephspurrier/goversioninfo` (build-time `.syso` resource compiler for icon + UAC manifest + version).

**Spec:** [`docs/superpowers/specs/2026-04-27-windows-service-design.md`](../specs/2026-04-27-windows-service-design.md). Read it first; this plan implements that spec verbatim.

**Conventions:**
- All Windows-only files have a `//go:build windows` build constraint. They compile only when `GOOS=windows`.
- Platform-neutral logic (config, button-state rules, the SCM-action core that takes an injected interface) lives in non-tagged files so it can be unit-tested on macOS.
- `task test` runs `go test ./...` and must continue passing on macOS throughout the plan. Windows-only files contribute nothing to non-Windows builds and are silently skipped.
- `task build` cross-compiles to `GOOS=windows GOARCH=amd64` from macOS. It must continue working throughout the plan.
- Commit after each task. Use conventional commits (`feat:`, `refactor:`, `test:`, `chore:`).

---

## Task 1: Extract `app.Run(ctx, cfg)` into `internal/app/`

**Why:** The service worker, the foreground mode, and the panel will all share the same shutdownable runner. Today its body lives inline in `cmd/lab_devices_client/main.go::run()`. Move it out so we can call it from the new entry points.

**Files:**
- Create: `internal/app/app.go`
- Modify: `cmd/lab_devices_client/main.go`

- [ ] **Step 1.1: Create the new `internal/app/app.go`**

```go
package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/khamitovdr/lab_devices_client/internal/api"
	"github.com/khamitovdr/lab_devices_client/internal/chisel"
	"github.com/khamitovdr/lab_devices_client/internal/config"
	"github.com/khamitovdr/lab_devices_client/internal/discovery"
	"github.com/khamitovdr/lab_devices_client/internal/registry"
	labserial "github.com/khamitovdr/lab_devices_client/internal/serial"
)

func Run(ctx context.Context, cfg config.Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	slog.Info("lab_devices_client starting",
		"chisel_server", cfg.Chisel.Server,
		"remote_port", cfg.Chisel.RemotePort,
		"rest_port", cfg.Rest.Port,
		"discovery_include", cfg.Discovery.Include,
		"discovery_exclude", cfg.Discovery.Exclude,
	)

	listener, localPort, err := api.Listen(cfg.Rest.Port)
	if err != nil {
		return fmt.Errorf("bind rest: %w", err)
	}
	slog.Info("rest listening", "addr", listener.Addr().String())

	reg := registry.New()
	opener := labserial.NewRealOpener()
	include := append([]string(nil), cfg.Discovery.Include...)
	exclude := append([]string(nil), cfg.Discovery.Exclude...)

	discoverFn := func(ctx context.Context) ([]*registry.Device, error) {
		all, err := opener.List()
		if err != nil {
			return nil, fmt.Errorf("list ports: %w", err)
		}
		ports := discovery.FilterPorts(all, include, exclude)
		slog.Info("discovery: starting", "candidates", ports)
		return discovery.Run(ctx, opener, ports)
	}

	srv := api.New(reg, discoverFn)

	chiselDone := make(chan error, 1)
	go func() {
		chiselDone <- chisel.Run(ctx, chisel.Config{
			Server:     cfg.Chisel.Server,
			User:       cfg.Chisel.User,
			Pass:       cfg.Chisel.Pass,
			RemotePort: cfg.Chisel.RemotePort,
			LocalPort:  localPort,
		})
	}()

	apiDone := make(chan error, 1)
	go func() {
		apiDone <- api.Serve(ctx, listener, srv.Handler())
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	case err := <-chiselDone:
		slog.Error("chisel exited", "err", err)
		cancel()
	case err := <-apiDone:
		slog.Error("rest server exited", "err", err)
		cancel()
	}

	<-chiselDone
	<-apiDone

	reg.Replace(nil)
	slog.Info("shutdown complete")
	return nil
}
```

- [ ] **Step 1.2: Rewrite `cmd/lab_devices_client/main.go` to call `app.Run`**

Replace the entire file with:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/khamitovdr/lab_devices_client/internal/app"
	"github.com/khamitovdr/lab_devices_client/internal/config"
)

const configFileName = "lab_devices_client_config.yaml"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	cfgPath := filepath.Join(filepath.Dir(exePath), configFileName)

	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		f, err := os.Create(cfgPath)
		if err != nil {
			return fmt.Errorf("create scaffold: %w", err)
		}
		if err := config.WriteScaffold(f); err != nil {
			f.Close()
			return fmt.Errorf("write scaffold: %w", err)
		}
		f.Close()
		fmt.Printf("Config file created at %s. Please review and edit it, then run again.\n", cfgPath)
		return errors.New("config scaffold generated; please edit and rerun")
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	configureLogger(cfg.Log.Level)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return app.Run(ctx, cfg)
}

func configureLogger(level string) {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "info":
		l = slog.LevelInfo
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	slog.SetDefault(slog.New(h))
}
```

- [ ] **Step 1.3: Run all tests to confirm refactor preserved behavior**

Run: `task test`
Expected: PASS — all existing tests still pass. The refactor moved code, did not change behavior.

- [ ] **Step 1.4: Cross-compile to verify the binary still builds**

Run: `task build`
Expected: PASS — produces `dist/lab_devices_client.exe`. (You don't have to run it; just confirm the build succeeds.)

- [ ] **Step 1.5: Commit**

```bash
git add internal/app/app.go cmd/lab_devices_client/main.go
git commit -m "refactor: extract app.Run into internal/app

Moves the body of cmd/lab_devices_client/main.go::run() into
internal/app/app.go::Run(ctx, cfg). The service worker, foreground
mode, and any future test harness will all use the same shutdownable
entry point."
```

---

## Task 2: Add `config.LoadPartial` for the panel

**Why:** The panel needs to render whatever values are in the config file alongside the validation error, even when validation fails. The existing `config.Load` returns a zero `Config` on validation error; the panel needs the parsed-but-invalid values.

**Files:**
- Modify: `internal/config/load.go`
- Modify: `internal/config/load_test.go`

- [ ] **Step 2.1: Add new tests in `internal/config/load_test.go`**

Append the following to `internal/config/load_test.go`:

```go
func TestLoadPartial_Valid(t *testing.T) {
	dir := t.TempDir()
	body := `
chisel:
  server: "10.0.0.1:7000"
  remote_port: 9001
  user: "u"
  pass: "p"
rest:
  port: 8080
discovery:
  include: ["COM3"]
log:
  level: "debug"
`
	p := writeFile(t, dir, "cfg.yaml", body)
	cfg, err := LoadPartial(p)
	if err != nil {
		t.Fatalf("LoadPartial err: %v", err)
	}
	if cfg.Chisel.Server != "10.0.0.1:7000" {
		t.Errorf("server: got %q", cfg.Chisel.Server)
	}
	if cfg.Chisel.RemotePort != 9001 {
		t.Errorf("remote_port: got %d", cfg.Chisel.RemotePort)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("level: got %q", cfg.Log.Level)
	}
}

func TestLoadPartial_InvalidValidationReturnsParsedFields(t *testing.T) {
	dir := t.TempDir()
	body := `
chisel:
  server: ""
  remote_port: 9001
log:
  level: "info"
`
	p := writeFile(t, dir, "cfg.yaml", body)
	cfg, err := LoadPartial(p)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "chisel.server must be non-empty") {
		t.Errorf("unexpected err: %v", err)
	}
	if cfg.Chisel.RemotePort != 9001 {
		t.Errorf("remote_port should still be parsed: got %d", cfg.Chisel.RemotePort)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("log level should still be parsed: got %q", cfg.Log.Level)
	}
}

func TestLoadPartial_MalformedYAMLReturnsDefault(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "cfg.yaml", "::: not yaml :::")
	cfg, err := LoadPartial(p)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	def := Default()
	if cfg.Chisel.Server != def.Chisel.Server {
		t.Errorf("on parse failure, expected Default()-server %q, got %q", def.Chisel.Server, cfg.Chisel.Server)
	}
}

func TestLoadPartial_MissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope.yaml")
	cfg, err := LoadPartial(p)
	if !os.IsNotExist(err) {
		t.Errorf("expected os.IsNotExist, got %v", err)
	}
	def := Default()
	if cfg.Chisel.Server != def.Chisel.Server {
		t.Errorf("on missing file, expected Default()-server %q, got %q", def.Chisel.Server, cfg.Chisel.Server)
	}
}
```

- [ ] **Step 2.2: Run tests, confirm they fail**

Run: `go test ./internal/config -run TestLoadPartial -v`
Expected: FAIL with `undefined: LoadPartial`.

- [ ] **Step 2.3: Implement `LoadPartial` in `internal/config/load.go`**

Append to `internal/config/load.go`:

```go
// LoadPartial parses path and returns whatever fields were populated, plus
// the first validation error (or nil if valid). Distinct from Load, which
// returns a zero Config on validation failure. Used by the GUI panel to
// display current config values alongside any validation warning.
func LoadPartial(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Default(), err
	}
	c := Default()
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Default(), fmt.Errorf("parse %s: %w", path, err)
	}
	return c, Validate(&c)
}
```

- [ ] **Step 2.4: Run tests, confirm they pass**

Run: `go test ./internal/config -v`
Expected: PASS — all existing tests plus the four new `TestLoadPartial_*` tests.

- [ ] **Step 2.5: Commit**

```bash
git add internal/config/load.go internal/config/load_test.go
git commit -m "feat(config): add LoadPartial for GUI panel display

Returns parsed-best-effort Config plus the validation error, instead
of zeroing the Config on validation failure. The panel uses it to
render current values alongside an inline warning."
```

---

## Task 3: Add `lumberjack` dependency

**Why:** The service mode logs to a rotated file; lumberjack handles size-based rotation cleanly.

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 3.1: Add the dependency**

Run: `go get gopkg.in/natefinch/lumberjack.v2@latest`
Expected: PASS — `go.mod` and `go.sum` updated.

- [ ] **Step 3.2: Run `go mod tidy` and confirm tests still pass**

Run: `task tidy && task test`
Expected: PASS.

- [ ] **Step 3.3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add lumberjack for service log rotation"
```

---

## Task 4: Define platform-neutral SCM interface

**Why:** `internal/winsvc/control.go` (which implements install/uninstall/restart) needs to be unit-testable on macOS. We hide the `golang.org/x/sys/windows/svc/mgr` calls behind a small interface, and inject a fake in tests. The real Windows-backed implementation lives in a `_windows.go` file added in Task 6.

**Files:**
- Create: `internal/winsvc/scm.go`

- [ ] **Step 4.1: Create `internal/winsvc/scm.go`**

```go
package winsvc

import "errors"

// ServiceState is the platform-neutral set of service states the panel and
// SCM-action code operate on. It maps 1:1 to a subset of svc.State on Windows.
type ServiceState int

const (
	StateNotInstalled ServiceState = iota
	StateStopped
	StateStartPending
	StateRunning
	StateStopPending
)

func (s ServiceState) String() string {
	switch s {
	case StateNotInstalled:
		return "Not installed"
	case StateStopped:
		return "Stopped"
	case StateStartPending:
		return "Starting"
	case StateRunning:
		return "Running"
	case StateStopPending:
		return "Stopping"
	default:
		return "Unknown"
	}
}

// ServiceConfig is the configuration we pass to CreateService. Mapped to
// mgr.Config + extras on Windows.
type ServiceConfig struct {
	DisplayName      string
	Description      string
	BinaryPath       string
	AutoStart        bool   // true → SERVICE_AUTO_START, false → SERVICE_DEMAND_START
	ServiceStartName string // empty → LocalSystem
}

// SCMConn is a connection to the Windows Service Control Manager, abstracted
// for testability.
type SCMConn interface {
	Disconnect() error
	OpenService(name string) (SCMService, error)
	CreateService(name string, cfg ServiceConfig) (SCMService, error)
}

// SCMService is a handle to a single service.
type SCMService interface {
	Query() (ServiceState, error)
	Start() error
	Stop() error
	Delete() error
	Close() error
}

// Sentinel errors returned by SCMConn implementations and surfaced as friendly
// messages by RunAdminAction.
var (
	ErrServiceMissing = errors.New("service is not installed")
	ErrServiceExists  = errors.New("service is already installed")
)

// DialSCM opens a real connection to the Windows SCM. Defined per-platform
// (real on windows, stub elsewhere). Tests inject their own SCMConn instead
// of going through this.
func DialSCM() (SCMConn, error) {
	return dialSCM()
}
```

- [ ] **Step 4.2: Add a non-Windows stub so the package compiles on macOS**

Create `internal/winsvc/scm_other.go`:

```go
//go:build !windows

package winsvc

import "errors"

func dialSCM() (SCMConn, error) {
	return nil, errors.New("SCM not available on this platform")
}
```

- [ ] **Step 4.3: Verify the package compiles on macOS**

Run: `go build ./internal/winsvc/...`
Expected: PASS — compiles cleanly.

- [ ] **Step 4.4: Commit**

```bash
git add internal/winsvc/scm.go internal/winsvc/scm_other.go
git commit -m "feat(winsvc): add platform-neutral SCM interface

Defines SCMConn / SCMService / ServiceState / ServiceConfig as the
abstraction layer over golang.org/x/sys/windows/svc/mgr. Tests inject
a fake SCMConn; the real mgr-backed implementation is added in a
later task as scm_windows.go."
```

---

## Task 5: Implement `internal/winsvc/control.go` with TDD

**Why:** This is the platform-neutral install/uninstall/restart logic, exercised by the elevated child process. We unit-test it on macOS with a fake `SCMConn`.

**Files:**
- Create: `internal/winsvc/control.go`
- Create: `internal/winsvc/control_test.go`

- [ ] **Step 5.1: Create the test file with a fake SCMConn**

Create `internal/winsvc/control_test.go`:

```go
package winsvc

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Fake SCMConn --------------------------------------------------------

type fakeService struct {
	name      string
	state     ServiceState
	started   bool
	deleted   bool
	startErr  error
	stopErr   error
	deleteErr error
	queryErr  error

	// Sequenced state changes: when len(stateProgression)>0, each Query() pops
	// the head and uses it as the current state. Lets a test simulate
	// "Running → StopPending → Stopped" over multiple polls.
	stateProgression []ServiceState

	mu sync.Mutex
}

func (s *fakeService) Query() (ServiceState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queryErr != nil {
		return 0, s.queryErr
	}
	if len(s.stateProgression) > 0 {
		s.state = s.stateProgression[0]
		s.stateProgression = s.stateProgression[1:]
	}
	return s.state, nil
}

func (s *fakeService) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startErr != nil {
		return s.startErr
	}
	s.started = true
	s.state = StateStartPending
	return nil
}

func (s *fakeService) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopErr != nil {
		return s.stopErr
	}
	s.state = StateStopPending
	return nil
}

func (s *fakeService) Delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deleted = true
	return nil
}

func (s *fakeService) Close() error { return nil }

type fakeSCM struct {
	services map[string]*fakeService

	openErr   error
	createErr error
}

func newFakeSCM() *fakeSCM {
	return &fakeSCM{services: map[string]*fakeService{}}
}

func (f *fakeSCM) Disconnect() error { return nil }

func (f *fakeSCM) OpenService(name string) (SCMService, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	s, ok := f.services[name]
	if !ok || s.deleted {
		return nil, ErrServiceMissing
	}
	return s, nil
}

func (f *fakeSCM) CreateService(name string, cfg ServiceConfig) (SCMService, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	if _, ok := f.services[name]; ok {
		return nil, ErrServiceExists
	}
	s := &fakeService{name: name, state: StateStopped}
	f.services[name] = s
	return s, nil
}

// --- install --------------------------------------------------------------

func TestInstall_Success(t *testing.T) {
	scm := newFakeSCM()
	if err := install(scm, "C:\\bin\\lab.exe"); err != nil {
		t.Fatalf("install: %v", err)
	}
	s := scm.services[ServiceName]
	if s == nil {
		t.Fatal("service was not created")
	}
	if !s.started {
		t.Error("service was created but Start() was not called")
	}
}

func TestInstall_AlreadyExists(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{state: StateRunning}
	err := install(scm, "C:\\bin\\lab.exe")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "already installed") {
		t.Errorf("err: %v", err)
	}
}

// --- uninstall ------------------------------------------------------------

func TestUninstall_StoppedService(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{state: StateStopped}
	if err := uninstall(scm, 100*time.Millisecond, time.Millisecond); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !scm.services[ServiceName].deleted {
		t.Error("service was not deleted")
	}
}

func TestUninstall_RunningServiceStopsThenDeletes(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopPending, StateStopped},
	}
	if err := uninstall(scm, 100*time.Millisecond, time.Millisecond); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !scm.services[ServiceName].deleted {
		t.Error("service was not deleted")
	}
}

func TestUninstall_StopTimeout(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{state: StateRunning} // stays Running forever
	err := uninstall(scm, 20*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "did not stop") {
		t.Errorf("err: %v", err)
	}
}

func TestUninstall_NotInstalled(t *testing.T) {
	scm := newFakeSCM()
	err := uninstall(scm, 100*time.Millisecond, time.Millisecond)
	if !errors.Is(err, ErrServiceMissing) {
		t.Errorf("err: %v", err)
	}
}

// --- restart --------------------------------------------------------------

func TestRestart_RunningService(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopped, StateStartPending, StateRunning},
	}
	if err := restart(scm, 100*time.Millisecond, time.Millisecond); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !scm.services[ServiceName].started {
		t.Error("Start() not called")
	}
}

func TestRestart_StoppedService(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateStopped,
		stateProgression: []ServiceState{StateStopped, StateStartPending, StateRunning},
	}
	if err := restart(scm, 100*time.Millisecond, time.Millisecond); err != nil {
		t.Fatalf("restart: %v", err)
	}
}

func TestRestart_StartTimeout(t *testing.T) {
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateStopped,
		stateProgression: []ServiceState{StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped, StateStopped},
	}
	err := restart(scm, 20*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("expected start timeout")
	}
	if !strings.Contains(err.Error(), "failed to start") {
		t.Errorf("err: %v", err)
	}
}

func TestRestart_NotInstalled(t *testing.T) {
	scm := newFakeSCM()
	err := restart(scm, 100*time.Millisecond, time.Millisecond)
	if !errors.Is(err, ErrServiceMissing) {
		t.Errorf("err: %v", err)
	}
}
```

- [ ] **Step 5.2: Run the tests and confirm they fail**

Run: `go test ./internal/winsvc -v`
Expected: FAIL — `undefined: install`, `undefined: uninstall`, `undefined: restart`, `undefined: ServiceName`.

- [ ] **Step 5.3: Implement `internal/winsvc/control.go`**

```go
package winsvc

import (
	"errors"
	"fmt"
	"os"
	"time"
)

const (
	ServiceName = "LabDevicesClient"
	DisplayName = "Lab Devices Client"
	Description = "Exposes serial-port lab devices via chisel reverse tunnel."

	productionStopTimeout  = 15 * time.Second
	productionStartTimeout = 15 * time.Second
	productionPollInterval = 250 * time.Millisecond
)

// RunAdminAction is the entry point used by the main dispatcher when the
// binary is launched with --admin-action=<name>. It connects to SCM, runs
// the requested action, writes any error to errorFile (UTF-8), and returns
// 0 on success or 1 on failure.
func RunAdminAction(action, errorFile string) int {
	err := func() error {
		exePath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate executable: %w", err)
		}
		scm, err := DialSCM()
		if err != nil {
			return fmt.Errorf("connect SCM: %w", err)
		}
		defer scm.Disconnect()

		switch action {
		case "install":
			return install(scm, exePath)
		case "uninstall":
			return uninstall(scm, productionStopTimeout, productionPollInterval)
		case "restart":
			return restart(scm, productionStartTimeout, productionPollInterval)
		default:
			return fmt.Errorf("unknown action %q", action)
		}
	}()
	if err != nil {
		_ = os.WriteFile(errorFile, []byte(err.Error()), 0o644)
		return 1
	}
	return 0
}

func install(scm SCMConn, exePath string) error {
	cfg := ServiceConfig{
		DisplayName: DisplayName,
		Description: Description,
		BinaryPath:  exePath,
		AutoStart:   true,
		// ServiceStartName "" → LocalSystem
	}
	s, err := scm.CreateService(ServiceName, cfg)
	if err != nil {
		if errors.Is(err, ErrServiceExists) {
			return errors.New("Service already installed.")
		}
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w (service is installed; use Restart after fixing the underlying issue)", err)
	}
	return nil
}

func uninstall(scm SCMConn, stopTimeout, pollInterval time.Duration) error {
	s, err := scm.OpenService(ServiceName)
	if err != nil {
		return err
	}
	defer s.Close()

	state, err := s.Query()
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	if state == StateRunning || state == StateStartPending {
		if err := s.Stop(); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
		if err := waitForState(s, StateStopped, stopTimeout, pollInterval); err != nil {
			return fmt.Errorf("Service did not stop within %s; check the log file or kill the process manually.", stopTimeout)
		}
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

func restart(scm SCMConn, timeout, pollInterval time.Duration) error {
	s, err := scm.OpenService(ServiceName)
	if err != nil {
		return err
	}
	defer s.Close()

	state, err := s.Query()
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	if state == StateRunning || state == StateStartPending {
		if err := s.Stop(); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
		if err := waitForState(s, StateStopped, timeout, pollInterval); err != nil {
			return fmt.Errorf("Service did not stop within %s; check the log file.", timeout)
		}
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if err := waitForRunning(s, timeout, pollInterval); err != nil {
		return errors.New("Service failed to start; check log file.")
	}
	return nil
}

func waitForState(s SCMService, target ServiceState, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st == target {
			return nil
		}
		time.Sleep(poll)
	}
	return errors.New("timeout")
}

func waitForRunning(s SCMService, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st == StateRunning {
			return nil
		}
		// StartPending is fine; we keep polling until Running or timeout.
		time.Sleep(poll)
	}
	return errors.New("timeout")
}
```

- [ ] **Step 5.4: Run tests, confirm they pass**

Run: `go test ./internal/winsvc -v`
Expected: PASS — all `TestInstall_*`, `TestUninstall_*`, `TestRestart_*`.

- [ ] **Step 5.5: Commit**

```bash
git add internal/winsvc/control.go internal/winsvc/control_test.go
git commit -m "feat(winsvc): implement install/uninstall/restart

Platform-neutral SCM-action core, tested with a fake SCMConn. The
real Windows-backed SCMConn is added in the next task."
```

---

## Task 6: Implement Windows-only `internal/winsvc/scm_windows.go`

**Why:** Real implementation of `SCMConn` / `SCMService` backed by `golang.org/x/sys/windows/svc/mgr`. Used in production by `RunAdminAction`. Not unit-tested — manual QA on Windows.

**Files:**
- Create: `internal/winsvc/scm_windows.go`

- [ ] **Step 6.1: Create `internal/winsvc/scm_windows.go`**

```go
//go:build windows

package winsvc

import (
	"errors"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func dialSCM() (SCMConn, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, err
	}
	return &winSCM{m: m}, nil
}

type winSCM struct{ m *mgr.Mgr }

func (w *winSCM) Disconnect() error { return w.m.Disconnect() }

func (w *winSCM) OpenService(name string) (SCMService, error) {
	s, err := w.m.OpenService(name)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil, ErrServiceMissing
		}
		return nil, err
	}
	return &winService{s: s}, nil
}

func (w *winSCM) CreateService(name string, cfg ServiceConfig) (SCMService, error) {
	startType := uint32(mgr.StartManual)
	if cfg.AutoStart {
		startType = mgr.StartAutomatic
	}
	mgrCfg := mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        startType,
		ErrorControl:     mgr.ErrorNormal,
		DisplayName:      cfg.DisplayName,
		Description:      cfg.Description,
		ServiceStartName: cfg.ServiceStartName,
	}
	s, err := w.m.CreateService(name, cfg.BinaryPath, mgrCfg)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_EXISTS) {
			return nil, ErrServiceExists
		}
		return nil, err
	}
	if err := s.SetRecoveryActions(nil, 0); err != nil {
		// Non-fatal: recovery actions are optional. Ignore.
	}
	return &winService{s: s}, nil
}

type winService struct{ s *mgr.Service }

func (w *winService) Query() (ServiceState, error) {
	st, err := w.s.Query()
	if err != nil {
		return 0, err
	}
	return mapState(st.State), nil
}

func (w *winService) Start() error  { return w.s.Start() }
func (w *winService) Stop() error {
	_, err := w.s.Control(svc.Stop)
	return err
}
func (w *winService) Delete() error { return w.s.Delete() }
func (w *winService) Close() error  { return w.s.Close() }

func mapState(s svc.State) ServiceState {
	switch s {
	case svc.Stopped:
		return StateStopped
	case svc.StartPending:
		return StateStartPending
	case svc.StopPending:
		return StateStopPending
	case svc.Running:
		return StateRunning
	default:
		return StateStopped
	}
}
```

- [ ] **Step 6.2: Verify it cross-compiles to Windows**

Run: `GOOS=windows GOARCH=amd64 go build ./internal/winsvc/...`
Expected: PASS — compiles for windows/amd64.

- [ ] **Step 6.3: Verify the macOS build still works (Windows-only file is excluded)**

Run: `task test`
Expected: PASS — `internal/winsvc` tests still pass on macOS using the fake.

- [ ] **Step 6.4: Commit**

```bash
git add internal/winsvc/scm_windows.go
git commit -m "feat(winsvc): add Windows-backed SCM implementation

Wraps golang.org/x/sys/windows/svc/mgr behind the SCMConn / SCMService
interface from scm.go. Maps the platform-neutral ServiceState back to
svc.State values."
```

---

## Task 7: Implement service worker `internal/winsvc/worker.go`

**Why:** When SCM launches the binary, the dispatcher calls `winsvc.RunWorker()`. The worker registers a `svc.Handler` and drives `app.Run` under SCM lifecycle control.

**Files:**
- Create: `internal/winsvc/worker.go`
- Create: `internal/winsvc/worker_other.go`

- [ ] **Step 7.1: Create `internal/winsvc/worker.go` (Windows-only)**

```go
//go:build windows

package winsvc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/khamitovdr/lab_devices_client/internal/app"
	"github.com/khamitovdr/lab_devices_client/internal/config"

	"golang.org/x/sys/windows/svc"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	configFileName = "lab_devices_client_config.yaml"
	logFileName    = "lab_devices_client.log"

	workerStopGracePeriod = 30 * time.Second
)

// RunWorker is the service-mode entry point. It must only be called when
// svc.IsWindowsService() returns true. It sets up file logging immediately
// (so the handler can record config-load failures) and hands off to svc.Run.
func RunWorker() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	dir := filepath.Dir(exePath)
	configureFileLogger(filepath.Join(dir, logFileName), slog.LevelInfo)
	return svc.Run(ServiceName, &handler{dir: dir})
}

type handler struct {
	dir string
}

func (h *handler) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}

	cfgPath := filepath.Join(h.dir, configFileName)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("config load failed", "path", cfgPath, "err", err)
		changes <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
		return false, 1
	}
	configureFileLogger(filepath.Join(h.dir, logFileName), parseLogLevel(cfg.Log.Level))

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
				changes <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		case err := <-appDone:
			if err != nil {
				slog.Error("app exited unexpectedly", "err", err)
			} else {
				slog.Info("app exited cleanly")
			}
			changes <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}

func configureFileLogger(path string, level slog.Level) {
	w := &lumberjack.Logger{
		Filename:   path,
		MaxSize:    10, // megabytes
		MaxBackups: 3,
		LocalTime:  true,
		Compress:   false,
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(h))
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
```

- [ ] **Step 7.2: Create non-Windows stub so the package keeps compiling on macOS**

Create `internal/winsvc/worker_other.go`:

```go
//go:build !windows

package winsvc

import "errors"

func RunWorker() error {
	return errors.New("RunWorker is only available on Windows")
}
```

- [ ] **Step 7.3: Verify both build targets**

Run: `task test && GOOS=windows GOARCH=amd64 go build ./...`
Expected: PASS for both.

- [ ] **Step 7.4: Commit**

```bash
git add internal/winsvc/worker.go internal/winsvc/worker_other.go
git commit -m "feat(winsvc): add service worker

Registers a svc.Handler, runs app.Run under a context cancelled by
SCM Stop, configures slog to a lumberjack-backed JSON file next to the
executable. Honors a 30s grace before SCM forces termination."
```

---

## Task 8: Add `lxn/walk` dependency

**Why:** GUI library for the panel. Pure-Go Win32 bindings; no CGO. Cross-compiles cleanly from macOS.

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 8.1: Add the dependency**

Run: `go get github.com/lxn/walk@latest`
Expected: PASS.

- [ ] **Step 8.2: Confirm tests still pass and Windows cross-compile still works**

Run: `task tidy && task test && GOOS=windows GOARCH=amd64 go build ./...`
Expected: PASS.

- [ ] **Step 8.3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add github.com/lxn/walk for native Win32 GUI"
```

---

## Task 9: Implement `internal/panel/state.go` with TDD

**Why:** Pure logic that decides which buttons are enabled given the SCM state, config validity, and log-file existence. Lives in its own file so it can be unit-tested with a table on macOS.

**Files:**
- Create: `internal/panel/state.go`
- Create: `internal/panel/state_test.go`

- [ ] **Step 9.1: Write the failing tests**

Create `internal/panel/state_test.go`:

```go
package panel

import (
	"testing"

	"github.com/khamitovdr/lab_devices_client/internal/winsvc"
)

func TestComputeButtons(t *testing.T) {
	cases := []struct {
		name      string
		state     winsvc.ServiceState
		cfgValid  bool
		logExists bool
		want      ButtonState
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
		{
			name:      "log exists toggles OpenLog",
			state:     winsvc.StateRunning,
			cfgValid:  true,
			logExists: true,
			want:      ButtonState{Uninstall: true, Restart: true, OpenLog: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeButtons(tc.state, tc.cfgValid, tc.logExists)
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

- [ ] **Step 9.2: Run tests, confirm they fail**

Run: `go test ./internal/panel -v`
Expected: FAIL — `undefined: ComputeButtons`, etc.

- [ ] **Step 9.3: Implement `internal/panel/state.go`**

```go
package panel

import "github.com/khamitovdr/lab_devices_client/internal/winsvc"

type ButtonState struct {
	Install   bool
	Uninstall bool
	Restart   bool
	OpenLog   bool
}

type StatusColor int

const (
	ColorGrey StatusColor = iota
	ColorYellow
	ColorGreen
	ColorRed
)

// ComputeButtons returns which admin buttons should be enabled given the
// current SCM state, whether the config validates, and whether the log
// file exists on disk.
func ComputeButtons(state winsvc.ServiceState, cfgValid, logExists bool) ButtonState {
	bs := ButtonState{OpenLog: logExists}
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

- [ ] **Step 9.4: Run tests, confirm pass**

Run: `go test ./internal/panel -v`
Expected: PASS.

- [ ] **Step 9.5: Commit**

```bash
git add internal/panel/state.go internal/panel/state_test.go
git commit -m "feat(panel): add platform-neutral button-state and status logic"
```

---

## Task 10: Implement `internal/panel/elevate.go`

**Why:** Helper used by the panel to relaunch itself elevated for install/uninstall/restart. Wraps `ShellExecuteEx` with `lpVerb="runas"` and waits for the child to exit.

**Files:**
- Create: `internal/panel/elevate.go`
- Create: `internal/panel/elevate_other.go`

- [ ] **Step 10.1: Create `internal/panel/elevate.go` (Windows-only)**

```go
//go:build windows

package panel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const seMaskNoCloseProcess = 0x00000040

type shellExecuteInfoW struct {
	cbSize         uint32
	fMask          uint32
	hwnd           uintptr
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       uintptr
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      uintptr
	dwHotKey       uint32
	hIconOrMonitor uintptr
	hProcess       uintptr
}

var (
	modShell32          = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = modShell32.NewProc("ShellExecuteExW")
)

// RunElevatedAdminAction relaunches the current executable elevated, asking
// it to perform an admin action. Returns the contents of the temp error
// file on failure (or an empty string on success). Returns ErrUserCancelled
// if the user dismissed the UAC prompt.
func RunElevatedAdminAction(action string) (errMsg string, err error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	errFile := filepath.Join(os.TempDir(), fmt.Sprintf("lab_devices_client_admin_%d.err", os.Getpid()))
	defer os.Remove(errFile)

	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exePath)
	params, _ := windows.UTF16PtrFromString(fmt.Sprintf("--admin-action=%s --error-file=%q", action, errFile))

	info := shellExecuteInfoW{
		cbSize:       uint32(unsafe.Sizeof(shellExecuteInfoW{})),
		fMask:        seMaskNoCloseProcess,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        1, // SW_SHOWNORMAL
	}
	r1, _, lastErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		if lastErr == syscall.Errno(windows.ERROR_CANCELLED) {
			return "", ErrUserCancelled
		}
		return "", fmt.Errorf("ShellExecuteExW: %w", lastErr)
	}
	if info.hProcess == 0 {
		return "", errors.New("ShellExecuteExW returned no process handle")
	}

	hProc := windows.Handle(info.hProcess)
	defer windows.CloseHandle(hProc)
	if _, err := windows.WaitForSingleObject(hProc, windows.INFINITE); err != nil {
		return "", fmt.Errorf("WaitForSingleObject: %w", err)
	}

	data, readErr := os.ReadFile(errFile)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", fmt.Errorf("read error file: %w", readErr)
	}
	return strings.TrimSpace(string(data)), nil
}

// ErrUserCancelled is returned when the user dismisses the UAC prompt.
var ErrUserCancelled = errors.New("user cancelled UAC prompt")

// OpenWithDefaultApp invokes ShellExecute with verb "open" on the given
// path. Used by the panel's "Open config file" / "Open log file" buttons.
func OpenWithDefaultApp(path string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(path)
	r1, _, lastErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&shellExecuteInfoW{
		cbSize: uint32(unsafe.Sizeof(shellExecuteInfoW{})),
		lpVerb: verb,
		lpFile: file,
		nShow:  1,
	})))
	if r1 == 0 {
		return fmt.Errorf("ShellExecuteExW: %w", lastErr)
	}
	return nil
}
```

- [ ] **Step 10.2: Create non-Windows stub**

Create `internal/panel/elevate_other.go`:

```go
//go:build !windows

package panel

import "errors"

var ErrUserCancelled = errors.New("user cancelled")

func RunElevatedAdminAction(action string) (string, error) {
	return "", errors.New("RunElevatedAdminAction is only available on Windows")
}

func OpenWithDefaultApp(path string) error {
	return errors.New("OpenWithDefaultApp is only available on Windows")
}
```

- [ ] **Step 10.3: Verify both targets build**

Run: `task test && GOOS=windows GOARCH=amd64 go build ./...`
Expected: PASS.

- [ ] **Step 10.4: Commit**

```bash
git add internal/panel/elevate.go internal/panel/elevate_other.go
git commit -m "feat(panel): add ShellExecuteEx helpers for UAC relaunch and Open-with"
```

---

## Task 11: Implement `internal/panel/panel.go`

**Why:** The actual walk-based GUI window. Composes the labels, buttons, polling timer, and click handlers.

**Files:**
- Create: `internal/panel/panel.go`
- Create: `internal/panel/panel_other.go`

- [ ] **Step 11.1: Create `internal/panel/panel.go` (Windows-only)**

```go
//go:build windows

package panel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/khamitovdr/lab_devices_client/internal/config"
	"github.com/khamitovdr/lab_devices_client/internal/winsvc"
)

const (
	configFileName = "lab_devices_client_config.yaml"
	logFileName    = "lab_devices_client.log"
	pollInterval   = 1 * time.Second
)

// Run opens the control-panel window and blocks until the user closes it.
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

	var (
		mw          *walk.MainWindow
		statusDot   *walk.Label
		statusLabel *walk.Label
		warnLabel   *walk.Label
		statusBar   *walk.Label

		serverLbl    *walk.Label
		remotePort   *walk.Label
		restPort     *walk.Label
		discoveryLbl *walk.Label
		logLevel     *walk.Label

		btnInstall   *walk.PushButton
		btnUninstall *walk.PushButton
		btnRestart   *walk.PushButton
		btnOpenCfg   *walk.PushButton
		btnOpenLog   *walk.PushButton
	)

	// Last-known SCM state. On transient SCM errors we keep showing this
	// instead of blinking to "Not installed".
	lastState := winsvc.StateNotInstalled

	refresh := func() {
		state, ok := queryServiceState()
		if !ok {
			state = lastState
		} else {
			lastState = state
		}
		cfg, cfgErr := config.LoadPartial(cfgPath)
		_, logStatErr := os.Stat(logPath)
		logExists := logStatErr == nil

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
		logLevel.SetText("Log level:        " + cfg.Log.Level)

		if cfgErr != nil {
			warnLabel.SetText("⚠ " + cfgErr.Error())
			warnLabel.SetVisible(true)
		} else {
			warnLabel.SetText("")
			warnLabel.SetVisible(false)
		}

		btns := ComputeButtons(state, cfgErr == nil, logExists)
		btnInstall.SetEnabled(btns.Install)
		btnUninstall.SetEnabled(btns.Uninstall)
		btnRestart.SetEnabled(btns.Restart)
		btnOpenLog.SetEnabled(btns.OpenLog)
	}

	performAdmin := func(action, successMsg string) {
		btnInstall.SetEnabled(false)
		btnUninstall.SetEnabled(false)
		btnRestart.SetEnabled(false)
		statusBar.SetText("Working…")

		errMsg, err := RunElevatedAdminAction(action)
		switch {
		case errors.Is(err, ErrUserCancelled):
			statusBar.SetText("Cancelled.")
		case err != nil:
			walk.MsgBox(mw, "Error", err.Error(), walk.MsgBoxIconError)
			statusBar.SetText("Failed.")
		case errMsg != "":
			walk.MsgBox(mw, "Error", errMsg, walk.MsgBoxIconError)
			statusBar.SetText("Failed.")
		default:
			statusBar.SetText(successMsg + " at " + time.Now().Format("15:04:05"))
		}
		refresh()
	}

	if err := (MainWindow{
		AssignTo: &mw,
		Title:    "Lab Devices Client",
		Size:     Size{Width: 480, Height: 360},
		MinSize:  Size{Width: 480, Height: 360},
		Layout:   VBox{},
		Children: []Widget{
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					Label{Text: "Status:"},
					Label{AssignTo: &statusDot, Text: "●", MinSize: Size{Width: 16}},
					Label{AssignTo: &statusLabel, Text: "…"},
				},
			},
			Label{Text: "─── Configuration ─────────────────────────────"},
			Label{AssignTo: &serverLbl},
			Label{AssignTo: &remotePort},
			Label{AssignTo: &restPort},
			Label{AssignTo: &discoveryLbl},
			Label{AssignTo: &logLevel},
			Label{AssignTo: &warnLabel, TextColor: walk.RGB(192, 0, 0)},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					PushButton{AssignTo: &btnInstall, Text: "Install", OnClicked: func() { performAdmin("install", "Service installed") }},
					PushButton{AssignTo: &btnUninstall, Text: "Uninstall", OnClicked: func() { performAdmin("uninstall", "Service uninstalled") }},
					PushButton{AssignTo: &btnRestart, Text: "Restart", OnClicked: func() { performAdmin("restart", "Service restarted") }},
				},
			},
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
			Label{AssignTo: &statusBar, Text: ""},
		},
	}).Create(); err != nil {
		return err
	}

	timer, err := newTickTimer(mw, pollInterval, refresh)
	if err != nil {
		return err
	}
	defer timer.Dispose()

	refresh()
	mw.Run()
	return nil
}

func ensureScaffold(cfgPath string) error {
	if _, err := os.Stat(cfgPath); err == nil {
		return nil
	}
	f, err := os.Create(cfgPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return config.WriteScaffold(f)
}

// queryServiceState returns the current SCM state plus an "ok" flag.
// ok=false signals a transient SCM error (Connect failure, Query failure,
// etc.) — the panel should keep displaying the last-known state in that
// case to avoid blinking the indicator. ok=true with state==StateNotInstalled
// is the legitimate "service is not registered" reading; the SCM call itself
// succeeded.
func queryServiceState() (winsvc.ServiceState, bool) {
	scm, err := winsvc.DialSCM()
	if err != nil {
		return winsvc.StateNotInstalled, false
	}
	defer scm.Disconnect()
	s, err := scm.OpenService(winsvc.ServiceName)
	if err != nil {
		if errors.Is(err, winsvc.ErrServiceMissing) {
			return winsvc.StateNotInstalled, true
		}
		return winsvc.StateNotInstalled, false
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return winsvc.StateStopped, false
	}
	return st, true
}
```

- [ ] **Step 11.2: Create `internal/panel/timer_windows.go` (walk timer helper)**

Walk's declarative API doesn't expose a timer directly; we wrap a `walk.MainWindow.Synchronize` ticker. Create `internal/panel/timer_windows.go`:

```go
//go:build windows

package panel

import (
	"time"

	"github.com/lxn/walk"
)

type tickTimer struct {
	stop chan struct{}
}

func newTickTimer(mw *walk.MainWindow, interval time.Duration, fn func()) (*tickTimer, error) {
	t := &tickTimer{stop: make(chan struct{})}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				mw.Synchronize(fn)
			case <-t.stop:
				return
			}
		}
	}()
	return t, nil
}

func (t *tickTimer) Dispose() { close(t.stop) }
```

- [ ] **Step 11.3: Create non-Windows stubs**

Create `internal/panel/panel_other.go`:

```go
//go:build !windows

package panel

import "errors"

func Run() error {
	return errors.New("panel.Run is only available on Windows")
}
```

- [ ] **Step 11.4: Verify both targets build**

Run: `task test && GOOS=windows GOARCH=amd64 go build ./...`
Expected: PASS.

- [ ] **Step 11.5: Commit**

```bash
git add internal/panel/panel.go internal/panel/panel_other.go internal/panel/timer_windows.go
git commit -m "feat(panel): add walk-based control-panel window

Renders status, config values, validation warning, and the
Install/Uninstall/Restart + Open-config/Open-log buttons. Polls SCM
and re-reads the config file every second."
```

---

## Task 12: Refactor `cmd/lab_devices_client/main.go` to dispatch by mode

**Why:** Single entry point; runs the panel by default, the service worker when SCM launches us, the elevated admin action when relaunched with `--admin-action`, or the foreground app on `--foreground`.

**Files:**
- Modify: `cmd/lab_devices_client/main.go`

- [ ] **Step 12.1: Replace `cmd/lab_devices_client/main.go` with the dispatcher**

Replace the entire file:

```go
//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/khamitovdr/lab_devices_client/internal/app"
	"github.com/khamitovdr/lab_devices_client/internal/config"
	"github.com/khamitovdr/lab_devices_client/internal/panel"
	"github.com/khamitovdr/lab_devices_client/internal/winsvc"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

const configFileName = "lab_devices_client_config.yaml"

var (
	flagAdminAction = flag.String("admin-action", "", "internal: install|uninstall|restart (used by the GUI)")
	flagErrorFile   = flag.String("error-file", "", "internal: path the elevated child writes its error message to")
	flagForeground  = flag.Bool("foreground", false, "run the device-client logic in the console (developer mode)")
)

func main() {
	flag.Parse()

	isService, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal: detect SCM context:", err)
		os.Exit(1)
	}

	switch {
	case isService:
		if err := winsvc.RunWorker(); err != nil {
			os.Exit(1)
		}
	case *flagAdminAction != "":
		os.Exit(winsvc.RunAdminAction(*flagAdminAction, *flagErrorFile))
	case *flagForeground:
		attachParentConsole()
		if err := runForeground(); err != nil {
			fmt.Fprintln(os.Stderr, "fatal:", err)
			os.Exit(1)
		}
	default:
		if err := panel.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "fatal:", err)
			os.Exit(1)
		}
	}
}

func runForeground() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	cfgPath := filepath.Join(filepath.Dir(exePath), configFileName)

	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		f, err := os.Create(cfgPath)
		if err != nil {
			return fmt.Errorf("create scaffold: %w", err)
		}
		if err := config.WriteScaffold(f); err != nil {
			f.Close()
			return fmt.Errorf("write scaffold: %w", err)
		}
		f.Close()
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

func attachParentConsole() {
	const ATTACH_PARENT_PROCESS = ^uint32(0) // -1 as DWORD
	_ = windows.AttachConsole(ATTACH_PARENT_PROCESS)
}

func configureStdoutLogger(level string) {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "info":
		l = slog.LevelInfo
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	slog.SetDefault(slog.New(h))
}
```

- [ ] **Step 12.2: Verify Windows cross-compile and macOS test pass**

Run: `task test && GOOS=windows GOARCH=amd64 go build ./...`
Expected: PASS. (`task test` skips the Windows-only `cmd/lab_devices_client` package automatically.)

- [ ] **Step 12.3: Run the cross-compile via the Taskfile**

Run: `task build`
Expected: PASS — produces `dist/lab_devices_client.exe`.

- [ ] **Step 12.4: Commit**

```bash
git add cmd/lab_devices_client/main.go
git commit -m "refactor(cmd): dispatch by run mode

Single entrypoint that detects SCM context, --admin-action,
--foreground, or default (panel). Foreground keeps the existing
console-mode behavior."
```

---

## Task 13: Add icon, version manifest, and `.gitignore` updates

**Why:** Goversioninfo requires an `.ico` and a `version.json`. The generated `.syso` is a build artifact and must be excluded from version control.

**Files:**
- Create: `assets/icon.ico`
- Create: `assets/version.json`
- Modify: `.gitignore`

- [ ] **Step 13.1: Create `assets/icon.ico` placeholder**

Run (if you have ImageMagick installed):

```bash
mkdir -p assets
convert -size 32x32 xc:steelblue assets/icon.ico
```

If you don't have ImageMagick, any 32×32 ICO will do — generate one online or grab a permissively-licensed placeholder. The icon will be replaced later by the user; we just need a valid ICO file at this path so `goversioninfo` succeeds.

Verify the file exists and is non-empty:
```bash
ls -la assets/icon.ico
```
Expected: a non-empty file.

- [ ] **Step 13.2: Create `assets/version.json`**

```json
{
  "FixedFileInfo": {
    "FileVersion":    {"Major": 0, "Minor": 1, "Patch": 0, "Build": 0},
    "ProductVersion": {"Major": 0, "Minor": 1, "Patch": 0, "Build": 0},
    "FileFlagsMask":  "3f",
    "FileFlags":      "00",
    "FileOS":         "040004",
    "FileType":       "01",
    "FileSubType":    "00"
  },
  "StringFileInfo": {
    "CompanyName":      "Lab Devices",
    "FileDescription":  "Lab Devices Client",
    "FileVersion":      "0.1.0",
    "InternalName":     "lab_devices_client",
    "LegalCopyright":   "",
    "OriginalFilename": "lab_devices_client.exe",
    "ProductName":      "Lab Devices Client",
    "ProductVersion":   "0.1.0"
  },
  "VarFileInfo": {
    "Translation": {"LangID": "0409", "CharsetID": "04B0"}
  },
  "ManifestPath": "assets/manifest.xml",
  "IconPath":     "assets/icon.ico"
}
```

- [ ] **Step 13.3: Create `assets/manifest.xml` (the UAC manifest)**

```xml
<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity version="0.1.0.0" processorArchitecture="*" name="LabDevicesClient" type="win32"/>
  <description>Lab Devices Client</description>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security>
      <requestedPrivileges>
        <requestedExecutionLevel level="asInvoker" uiAccess="false"/>
      </requestedPrivileges>
    </security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application>
      <supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/>
      <supportedOS Id="{1f676c76-80e1-4239-95bb-83d0f6d0da78}"/>
      <supportedOS Id="{4a2f28e3-53b9-4441-ba9c-d69d4a4a6e38}"/>
      <supportedOS Id="{35138b9a-5d96-4fbd-8e2d-a2440225f93a}"/>
      <supportedOS Id="{e2011457-1546-43c5-a5fe-008deee3d3f0}"/>
    </application>
  </compatibility>
</assembly>
```

- [ ] **Step 13.4: Update `.gitignore` to exclude generated `.syso`**

Replace `.gitignore` with:

```
# Build outputs
/dist/
*.exe
*.syso

# Runtime config (not committed; scaffold is generated by the binary)
/lab_devices_client_config.yaml
/lab_devices_client.log
/lab_devices_client.log.*

# Editor / OS
.idea/
.vscode/
.DS_Store
```

- [ ] **Step 13.5: Commit**

```bash
git add assets/icon.ico assets/version.json assets/manifest.xml .gitignore
git commit -m "chore: add icon, UAC manifest, and version metadata

Placeholder steel-blue 32×32 .ico for now (swap later). The manifest
sets requestedExecutionLevel=asInvoker so the panel runs unelevated;
admin actions relaunch via ShellExecuteEx \"runas\". Excludes the
generated *.syso build artifact from git."
```

---

## Task 14: Update `Taskfile.yaml` — resource step + GUI subsystem

**Why:** Bake the icon and manifest into the .exe via `goversioninfo`, and select the Windows GUI subsystem so double-click doesn't flash a console.

**Files:**
- Modify: `Taskfile.yaml`

- [ ] **Step 14.1: Replace `Taskfile.yaml`**

```yaml
version: '3'

vars:
  GOOS: '{{.GOOS | default "windows"}}'
  GOARCH: '{{.GOARCH | default "amd64"}}'
  OUTPUT_DIR: dist
  BINARY_NAME: 'lab_devices_client{{if eq .GOOS "windows"}}.exe{{end}}'
  RESOURCE_FILE: cmd/lab_devices_client/resource_windows.syso

tasks:
  resource:
    desc: Compile the .syso resource (icon + UAC manifest + version metadata)
    cmds:
      - go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest
          -64
          -o {{.RESOURCE_FILE}}
          -icon=assets/icon.ico
          -manifest=assets/manifest.xml
          assets/version.json

  build:
    desc: Build the binary (override target via `task build GOOS=... GOARCH=...`)
    deps: [resource]
    cmds:
      - mkdir -p {{.OUTPUT_DIR}}
      - GOOS={{.GOOS}} GOARCH={{.GOARCH}} go build
          -ldflags="-s -w -H windowsgui"
          -o {{.OUTPUT_DIR}}/{{.BINARY_NAME}} ./cmd/lab_devices_client

  test:
    desc: Run all unit tests
    cmds:
      - go test ./...

  tidy:
    desc: Tidy go.mod / go.sum
    cmds:
      - go mod tidy

  clean:
    desc: Remove build outputs and generated resources
    cmds:
      - rm -rf {{.OUTPUT_DIR}} {{.RESOURCE_FILE}}
```

- [ ] **Step 14.2: Run the build to verify the resource step works**

Run: `task build`
Expected: PASS. The first run downloads `goversioninfo` via `go run`, generates `cmd/lab_devices_client/resource_windows.syso`, then builds `dist/lab_devices_client.exe`.

If `go run github.com/josephspurrier/goversioninfo/...` fails with a module-resolution error, run `go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest` once and re-run `task build`.

- [ ] **Step 14.3: Verify the .syso ended up in the right place and the binary embeds resources**

```bash
ls -l cmd/lab_devices_client/resource_windows.syso dist/lab_devices_client.exe
file dist/lab_devices_client.exe
```
Expected: both files exist; `file` reports the .exe as a "PE32+ executable (GUI)" — note "GUI", not "console" — confirming `-H windowsgui` was applied.

- [ ] **Step 14.4: Commit**

```bash
git add Taskfile.yaml
git commit -m "build: add resource step, GUI subsystem flag, clean target

Compiles assets/{icon.ico,manifest.xml,version.json} into a .syso
embedded by go build. -H windowsgui prevents the console-window flash
on double-click."
```

---

## Task 15: Update `README.md` for the new workflow

**Why:** The user-facing instructions changed: the binary is now a control panel + service, not a foreground process. The README must reflect this.

**Files:**
- Modify: `README.md`

- [ ] **Step 15.1: Replace `README.md`**

```markdown
# lab_devices_client

Single-binary Go application that exposes serial-port lab devices to a remote HTTP client through a chisel reverse tunnel. Runs as a Windows service; managed through a small native control-panel window.

## Build

Default target is Windows / amd64:
```
task build
```

Override target via env variables:
```
task build GOOS=linux GOARCH=arm64
```

Output: `dist/lab_devices_client.exe`.

The build embeds an icon, a UAC manifest (`asInvoker`), and version metadata via `goversioninfo`. The first build downloads `goversioninfo` automatically.

## Install on a Windows lab machine

1. Copy `lab_devices_client.exe` to an install location (e.g., `C:\Tools\LabDevicesClient\`).
2. Double-click the .exe. The control panel opens. On first launch it writes `lab_devices_client_config.yaml` next to the .exe and shows a validation warning if anything's wrong.
3. Click **Open config file**, set `chisel.remote_port` (and any other site-specific values), save.
4. Click **Install**. UAC prompts; approve. The service is registered as `LabDevicesClient` (auto-start at boot, runs as LocalSystem) and started immediately.

After install:

- The service runs across reboots without the panel being open.
- To apply config changes: edit the YAML file, then click **Restart** in the panel.
- To remove: click **Uninstall** in the panel.
- Logs go to `lab_devices_client.log` next to the .exe (rotated at 10 MB, 3 backups). Click **Open log file** to view.

## Run modes

The single binary detects how it was launched and behaves accordingly:

| Launched via               | Mode               |
| -------------------------- | ------------------ |
| SCM (after install)        | Service worker     |
| Double-click               | Control panel      |
| `--admin-action=...` (UAC) | Internal: SCM op   |
| `--foreground`             | Console developer mode (legacy behavior; JSON logs to stdout, Ctrl-C to stop) |

## REST API

(Unchanged from the prior design.) The REST API is bound to `127.0.0.1` on the lab machine; it is reachable from outside **only** through the chisel reverse tunnel.

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/discover` | Run a fresh discovery and return the device list |
| `GET`  | `/devices`  | Return the cached device list |
| `POST` | `/devices/{id}/command` | Send raw bytes; optionally read a reply |

See [`docs/superpowers/specs/2026-04-26-lab-devices-client-design.md`](docs/superpowers/specs/2026-04-26-lab-devices-client-design.md) for full request/response shapes and behavior.

## Tests

```
task test
```

Tests run on macOS and Windows. The Windows-only files (service worker, real SCM client, walk panel) are silently skipped on non-Windows hosts; their logic is covered by tests against fakes.
```

- [ ] **Step 15.2: Commit**

```bash
git add README.md
git commit -m "docs: update README for service + control-panel workflow"
```

---

## Task 16: Manual QA on Windows

**Why:** The walk panel, the UAC relaunch, the service worker under SCM, and the lumberjack-rotated log file are all only exercisable on a real Windows machine. The unit tests cover button-state logic and the SCM-action core; everything else needs a smoke test.

**Files:** none (manual procedure).

- [ ] **Step 16.1: Cross-compile and copy to a Windows test machine**

On the dev host (macOS):
```bash
task build
```
Copy `dist/lab_devices_client.exe` to a folder on the Windows machine, e.g., `C:\Tools\LabDevicesClient\`.

- [ ] **Step 16.2: First-launch / scaffold test**

Double-click the .exe. Expected:
- No console window flashes.
- Control-panel window appears.
- Status reads `Not installed`, dot is grey (or red if scaffold defaults validate as invalid for your environment).
- `lab_devices_client_config.yaml` is now present in the install folder.
- Config values are shown as labeled rows.
- "Install" is enabled (assuming valid scaffold) or disabled with the validation warning visible.
- "Uninstall" and "Restart" are disabled.
- "Open log file" is disabled.

- [ ] **Step 16.3: Edit-config + Install test**

Click **Open config file**. Notepad opens the YAML. Edit `chisel.remote_port` to a valid number for your test setup (or leave the default). Save & close Notepad. Within ≈1 s the panel re-reads the file; warning disappears (if any was present).

Click **Install**. UAC prompts; click Yes. Expected:
- Status transitions through `Starting` → `Running` (yellow → green dot).
- Status bar shows "Service installed at HH:MM:SS".
- "Install" disables; "Uninstall" and "Restart" enable.
- "Open log file" enables (the worker has now written log lines).
- Open `services.msc`: "Lab Devices Client" is listed, Status = Running, Startup Type = Automatic, Log On As = `Local System`.

- [ ] **Step 16.4: Restart-after-config-edit test**

Edit `lab_devices_client_config.yaml`, change `log.level` from `info` to `debug`. Save. Click **Restart**. UAC; Yes. Expected:
- Status transitions through `Stopping` → `Stopped` → `Starting` → `Running`.
- Log file gains lines at `level: DEBUG`.
- Status bar shows "Service restarted at HH:MM:SS".

- [ ] **Step 16.5: Boot-persistence test**

Reboot the Windows machine. After login, before opening the panel, run `Get-Service LabDevicesClient` in PowerShell. Expected: `Status: Running`. The service started automatically without the panel.

- [ ] **Step 16.6: Uninstall test**

Open the panel. Click **Uninstall**. UAC; Yes. Expected:
- Status transitions to `Stopped` then to `Not installed`.
- "Install" enables; "Uninstall"/"Restart" disable.
- `services.msc` no longer lists the service.

- [ ] **Step 16.7: UAC-cancel test**

Click **Install**. When UAC prompts, click No. Expected:
- Status bar reads "Cancelled."
- No MessageBox.
- Service state unchanged (still `Not installed`).

- [ ] **Step 16.8: Invalid-config-blocks-Install test**

Edit the YAML so it's invalid (e.g., set `log.level: nope`). Save. Within ≈1 s the panel shows the validation warning in red and disables **Install**. Restore a valid value to re-enable.

- [ ] **Step 16.9: Foreground developer mode**

In `cmd.exe`, run:
```
lab_devices_client.exe --foreground
```
Expected: the binary runs as before (JSON logs to the console). Press Ctrl-C; it shuts down cleanly.

- [ ] **Step 16.10: If anything failed, file an issue and reopen the relevant task**

Document the failure mode and the panel/service state observed, including:
- Windows version
- Snippet from `lab_devices_client.log`
- Any MessageBox text
- The current `lab_devices_client_config.yaml` contents (sanitized)

If everything passes, you're done.

- [ ] **Step 16.11: Tag the release**

```bash
git tag -a v0.1.0 -m "First service-mode release"
```

(Push the tag if appropriate for your workflow.)
