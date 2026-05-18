# Keep-Awake Button Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Status-tab toggle that holds a Windows "system required" power request inside the SerialHop service, preventing idle sleep and unattended automatic shutdown until the operator turns it off.

**Architecture:** New `internal/power` package owns Win32 `PowerCreateRequest` + `PowerSetRequest(PowerRequestSystemRequired)` lifecycle (process-bound handle, visible in `powercfg /requests`). The service exposes three loopback HTTP routes (`GET/POST /power/keep-awake[/enable|/disable]`). The panel adds matching `ServiceCli` methods and Wails bindings; the Status tab gains a "Power" section (lamp + button). State is in-memory in the service and re-fetched by the panel on mount and after service-lamp recoveries.

**Tech Stack:** Go (`golang.org/x/sys/windows` for syscalls — already a dep), Wails v2, React + Vite + Vitest + Testing Library, jest-dom matchers.

**Spec:** `docs/superpowers/specs/2026-05-19-keep-awake-design.md`.

**Build / test commands you'll use repeatedly:**
- Go (entire repo, cross-platform): `go test -race -count=1 ./...`
- Go (one package): `go test -race -count=1 ./internal/power/...`
- Go fmt + vet (before commit): `gofmt -l .` (must print nothing) + `go vet ./...`
- Frontend tests (from `internal/panel/frontend/`): `npm test`
- Frontend type-check + build: `npm run build`
- Frontend lint: `npm run lint`

**Project conventions** (also in `CLAUDE.md`):
- One PR = one logical change. Commits should be Conventional-Commits-prefixed (`feat:`, `fix:`, `test:`, `refactor:` — anything other than `feat:`/`fix:` is changelog-hidden).
- Tests pass on macOS AND Windows. Windows-only code lives in `_windows.go`; non-Windows files (`_other.go`) provide fakes so coverage compiles on macOS/Linux.
- The panel's bound methods MUST NOT take `context.Context`. There's a regression test (`bindings_ctx_check_test.go`) that AST-parses `bindings.go` and fails the build if anyone forgets. Use `a.callCtx()` instead.

---

## File map

**New files:**
- `internal/power/keepawake.go` — interface + `New()` factory + shared constants
- `internal/power/keepawake_other.go` — non-Windows fake (`//go:build !windows`)
- `internal/power/keepawake_other_test.go` — fake state-machine tests
- `internal/power/keepawake_windows.go` — real Win32 impl (`//go:build windows`)
- `internal/power/keepawake_windows_test.go` — Windows smoke test
- `internal/api/handlers_power_test.go` — handler-level tests
- `internal/panel/bindings_keepawake_test.go` — bindings tests (`//go:build windows`)
- `internal/panel/frontend/src/tabs/StatusTab.test.tsx` — Power-section tests

**Modified files:**
- `internal/api/handlers.go` — `Server` struct gains a `keepAwake power.KeepAwake` field; `New(...)` takes one extra param; `Handler()` registers three new routes; three new handler funcs.
- `internal/api/handlers_test.go` — `newTestServer` passes a fake `KeepAwake` through `New(...)`.
- `internal/api/flash_test.go` — two `New(...)` callsites pass a fake `KeepAwake`.
- `internal/app/app.go` — construct `power.New()`, pass to `api.New(...)`, defer `Close()`.
- `internal/panel/servicecli.go` — three new methods (`GetKeepAwake`, `EnableKeepAwake`, `DisableKeepAwake`).
- `internal/panel/servicecli_test.go` — tests for the new methods.
- `internal/panel/bindings.go` — `KeepAwakeResult` DTO + three bound methods.
- `internal/panel/frontend/src/types.ts` — `KeepAwakePayload` interface.
- `internal/panel/frontend/src/wails/go/main/App.ts` — three new JS stub exports.
- `internal/panel/frontend/src/state/globalStore.ts` — keep-awake state slice + reconcile-on-service-lamp-recovery effect.
- `internal/panel/frontend/src/App.tsx` — destructure keep-awake state, pass to `StatusTab`.
- `internal/panel/frontend/src/tabs/StatusTab.tsx` — add Power section between Service health and Service control.

---

## Task 1: Bootstrap `internal/power` package — interface + factory

**Files:**
- Create: `internal/power/keepawake.go`

This is the package's public surface: an interface plus a `New()` factory. The interface is platform-independent; `newPlatform()` is implemented separately for Windows and non-Windows in later tasks.

- [ ] **Step 1: Create the interface file**

```go
// Package power exposes a KeepAwake handle that, while active, prevents
// Windows from idling into sleep or scheduled automatic shutdown. The
// real implementation calls PowerCreateRequest / PowerSetRequest with
// the PowerRequestSystemRequired type; the request is process-bound, so
// the handle's lifetime is bound to the owning process (here, the
// SerialHop service). A non-Windows fake exists so packages that depend
// on this interface compile and test on macOS/Linux.
package power

// KeepAwake holds the OS-level "keep the system awake" request. The
// underlying resource is process-bound; the service owns one instance
// for its entire lifetime.
type KeepAwake interface {
	// Enable activates the keep-awake request. Idempotent: a second
	// Enable while already active is a successful no-op. reason is
	// surfaced in `powercfg /requests` on Windows (ignored on other
	// platforms). It is captured on the first Enable call and reused
	// for the lifetime of the handle.
	Enable(reason string) error

	// Disable clears the keep-awake request. Idempotent.
	Disable() error

	// Active returns the most recent successfully-applied state.
	Active() bool

	// Close releases the underlying handle. After Close, the instance
	// is unusable. Called from service shutdown.
	Close() error
}

// New returns a platform-appropriate KeepAwake. On Windows it allocates
// the underlying PowerRequest handle lazily on first Enable; on other
// platforms it returns a fake that just tracks the flag in memory.
func New() (KeepAwake, error) { return newPlatform() }
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/power/...`
Expected: build error referencing `undefined: newPlatform` (we haven't written the platform shims yet — fine; the next task adds the non-Windows shim, then the build passes on macOS).

- [ ] **Step 3: Commit**

```bash
git add internal/power/keepawake.go
git commit -m "$(cat <<'EOF'
feat(power): scaffold KeepAwake interface

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Non-Windows fake + cross-platform tests

**Files:**
- Create: `internal/power/keepawake_other.go`
- Test: `internal/power/keepawake_other_test.go`

The fake makes the interface usable from cross-platform tests (api handlers, etc.). It's also the implementation that runs in macOS CI.

- [ ] **Step 1: Write the failing tests**

```go
//go:build !windows

package power

import (
	"sync"
	"testing"
)

func TestFake_StartsInactive(t *testing.T) {
	ka, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = ka.Close() })
	if ka.Active() {
		t.Errorf("Active before Enable = true, want false")
	}
}

func TestFake_EnableThenDisable(t *testing.T) {
	ka, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = ka.Close() })

	if err := ka.Enable("test"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !ka.Active() {
		t.Errorf("Active after Enable = false, want true")
	}
	if err := ka.Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if ka.Active() {
		t.Errorf("Active after Disable = true, want false")
	}
}

func TestFake_EnableIsIdempotent(t *testing.T) {
	ka, _ := New()
	t.Cleanup(func() { _ = ka.Close() })
	if err := ka.Enable("a"); err != nil {
		t.Fatalf("first Enable: %v", err)
	}
	if err := ka.Enable("b"); err != nil {
		t.Fatalf("second Enable: %v", err)
	}
	if !ka.Active() {
		t.Errorf("Active = false after double Enable")
	}
}

func TestFake_DisableIsIdempotent(t *testing.T) {
	ka, _ := New()
	t.Cleanup(func() { _ = ka.Close() })
	if err := ka.Disable(); err != nil {
		t.Fatalf("Disable on cold instance: %v", err)
	}
	_ = ka.Enable("test")
	if err := ka.Disable(); err != nil {
		t.Fatalf("first Disable: %v", err)
	}
	if err := ka.Disable(); err != nil {
		t.Fatalf("second Disable: %v", err)
	}
	if ka.Active() {
		t.Errorf("Active = true after double Disable")
	}
}

func TestFake_CloseClearsActive(t *testing.T) {
	ka, _ := New()
	_ = ka.Enable("test")
	if err := ka.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ka.Active() {
		t.Errorf("Active = true after Close, want false")
	}
}

func TestFake_ConcurrentEnableDoesNotRace(t *testing.T) {
	ka, _ := New()
	t.Cleanup(func() { _ = ka.Close() })

	const N = 64
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = ka.Enable("test")
		}()
	}
	wg.Wait()
	if !ka.Active() {
		t.Errorf("Active = false after %d concurrent Enables", N)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/power/...`
Expected: FAIL with build errors — `newPlatform` undefined, `New` undefined, etc. (everything cascades from the missing impl.)

- [ ] **Step 3: Write the fake implementation**

```go
//go:build !windows

package power

import "sync/atomic"

type fakeKeepAwake struct {
	active atomic.Bool
}

func newPlatform() (KeepAwake, error) {
	return &fakeKeepAwake{}, nil
}

func (f *fakeKeepAwake) Enable(_ string) error { f.active.Store(true); return nil }
func (f *fakeKeepAwake) Disable() error        { f.active.Store(false); return nil }
func (f *fakeKeepAwake) Active() bool          { return f.active.Load() }
func (f *fakeKeepAwake) Close() error          { f.active.Store(false); return nil }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race -count=1 ./internal/power/...`
Expected: PASS (all six tests).

- [ ] **Step 5: Commit**

```bash
git add internal/power/keepawake_other.go internal/power/keepawake_other_test.go
git commit -m "$(cat <<'EOF'
feat(power): non-windows fake + state-machine tests

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Windows real implementation + smoke test

**Files:**
- Create: `internal/power/keepawake_windows.go`
- Test: `internal/power/keepawake_windows_test.go`

This task can't run its tests on macOS — the file is `//go:build windows`, so it only compiles on Windows. The smoke test should be executed on a Windows host before this PR merges; the implementation must at least pass `go vet` on macOS, which it does because the file is excluded by build tag.

References for the Win32 constants (encode them as Go constants — `golang.org/x/sys/windows` does not expose them):
- `POWER_REQUEST_CONTEXT_VERSION = 0`
- `POWER_REQUEST_CONTEXT_SIMPLE_STRING = 0x1`
- `PowerRequestSystemRequired = 1` (the `POWER_REQUEST_TYPE` enum is `Display=0, System=1, AwayMode=2, Execution=3` — easy to get wrong; double-check against MSDN before declaring complete).

- [ ] **Step 1: Write the failing smoke test**

```go
//go:build windows

package power

import "testing"

func TestWindows_EnableDisableSmoke(t *testing.T) {
	ka, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = ka.Close() })

	if ka.Active() {
		t.Fatalf("Active before Enable = true, want false")
	}
	if err := ka.Enable("unit test: TestWindows_EnableDisableSmoke"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !ka.Active() {
		t.Errorf("Active after Enable = false, want true")
	}
	if err := ka.Enable("idempotent second call"); err != nil {
		t.Fatalf("second Enable: %v", err)
	}
	if err := ka.Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if ka.Active() {
		t.Errorf("Active after Disable = true, want false")
	}
	if err := ka.Disable(); err != nil {
		t.Fatalf("second Disable: %v", err)
	}
}

func TestWindows_CloseAfterEnableReleasesHandle(t *testing.T) {
	ka, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := ka.Enable("test"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := ka.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After Close the request is cleared and the handle freed; Active
	// must report false.
	if ka.Active() {
		t.Errorf("Active = true after Close")
	}
}
```

- [ ] **Step 2: Verify the tests can't run (no impl yet)**

On macOS, `go test ./internal/power/...` should already pass the non-Windows tests. On a Windows host: `go test ./internal/power/...` would currently fail to build (no `newPlatform` on windows). On macOS, you'll have to trust the build-tagged code; that's normal for `_windows.go` files in this repo.

- [ ] **Step 3: Write the Windows implementation**

```go
//go:build windows

package power

import (
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Constants from the Windows SDK. golang.org/x/sys/windows does not
// expose these.
const (
	powerRequestContextVersion       = 0
	powerRequestContextSimpleString  = 0x1
	powerRequestSystemRequired       uint32 = 1
)

// powerRequestContext mirrors the SDK's REASON_CONTEXT when used with
// POWER_REQUEST_CONTEXT_SIMPLE_STRING. The reason string is a *uint16
// (UTF-16, null-terminated).
type powerRequestContext struct {
	Version      uint32
	Flags        uint32
	SimpleReason *uint16
}

var (
	modKernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procPowerCreateRequest   = modKernel32.NewProc("PowerCreateRequest")
	procPowerSetRequest      = modKernel32.NewProc("PowerSetRequest")
	procPowerClearRequest    = modKernel32.NewProc("PowerClearRequest")
)

type winKeepAwake struct {
	mu     sync.Mutex
	handle windows.Handle // 0 until first Enable
	active atomic.Bool
}

func newPlatform() (KeepAwake, error) {
	return &winKeepAwake{}, nil
}

func (k *winKeepAwake) Enable(reason string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.active.Load() {
		return nil
	}
	if k.handle == 0 {
		h, err := createRequest(reason)
		if err != nil {
			return fmt.Errorf("PowerCreateRequest: %w", err)
		}
		k.handle = h
	}
	if err := setRequest(k.handle); err != nil {
		return fmt.Errorf("PowerSetRequest: %w", err)
	}
	k.active.Store(true)
	return nil
}

func (k *winKeepAwake) Disable() error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if !k.active.Load() {
		return nil
	}
	if k.handle == 0 {
		// Nothing was ever set; defensive.
		k.active.Store(false)
		return nil
	}
	if err := clearRequest(k.handle); err != nil {
		return fmt.Errorf("PowerClearRequest: %w", err)
	}
	k.active.Store(false)
	return nil
}

func (k *winKeepAwake) Active() bool { return k.active.Load() }

func (k *winKeepAwake) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.handle == 0 {
		k.active.Store(false)
		return nil
	}
	if k.active.Load() {
		// Best-effort clear before close; ignore the error so we still
		// release the handle.
		_ = clearRequest(k.handle)
		k.active.Store(false)
	}
	if err := windows.CloseHandle(k.handle); err != nil {
		k.handle = 0
		return fmt.Errorf("CloseHandle: %w", err)
	}
	k.handle = 0
	return nil
}

// createRequest wraps PowerCreateRequest. The returned handle owns the
// reason string for its entire lifetime; PowerCreateRequest copies it
// internally so we don't need to pin it.
func createRequest(reason string) (windows.Handle, error) {
	rPtr, err := windows.UTF16PtrFromString(reason)
	if err != nil {
		return 0, err
	}
	ctx := powerRequestContext{
		Version:      powerRequestContextVersion,
		Flags:        powerRequestContextSimpleString,
		SimpleReason: rPtr,
	}
	ret, _, callErr := procPowerCreateRequest.Call(uintptr(unsafe.Pointer(&ctx)))
	if windows.Handle(ret) == windows.InvalidHandle {
		return 0, callErr
	}
	return windows.Handle(ret), nil
}

func setRequest(h windows.Handle) error {
	ret, _, callErr := procPowerSetRequest.Call(uintptr(h), uintptr(powerRequestSystemRequired))
	if ret == 0 {
		return callErr
	}
	return nil
}

func clearRequest(h windows.Handle) error {
	ret, _, callErr := procPowerClearRequest.Call(uintptr(h), uintptr(powerRequestSystemRequired))
	if ret == 0 {
		return callErr
	}
	return nil
}
```

- [ ] **Step 4: Verify builds on macOS**

Run: `go build ./internal/power/...`
Expected: clean (the `_windows.go` files are excluded by build tag; the fake is what compiles).

- [ ] **Step 5: Verify `go vet` is clean across the repo**

Run: `go vet ./...`
Expected: no output.

- [ ] **Step 6: Verify gofmt is clean**

Run: `gofmt -l internal/power/`
Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add internal/power/keepawake_windows.go internal/power/keepawake_windows_test.go
git commit -m "$(cat <<'EOF'
feat(power): windows PowerCreateRequest implementation

PowerRequestSystemRequired keeps the box awake until cleared.
Visible in `powercfg /requests` with the supplied reason string.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Thread `KeepAwake` through `api.Server` constructor

**Files:**
- Modify: `internal/api/handlers.go` (lines 22-47 — struct + `New`)
- Modify: `internal/api/handlers_test.go` (line 31 — `newTestServer`)
- Modify: `internal/api/flash_test.go` (lines 20, 158 — two `New(...)` callsites)
- Modify: `internal/app/app.go` (line 79 — production wiring)

This task is mechanical: thread a new constructor parameter through. After it, every existing test still passes; no new routes yet.

- [ ] **Step 1: Add the field + extend the constructor**

In `internal/api/handlers.go`, change the imports block to add the `power` package:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/agentinfo"
	"github.com/bioexperiment-lab-devices/serialhop/internal/discovery"
	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher"
	"github.com/bioexperiment-lab-devices/serialhop/internal/power"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)
```

Change the `Server` struct (currently lines 22-29) to:

```go
type Server struct {
	reg              *registry.Registry
	discover         DiscoverFn
	opener           labserial.Opener
	rawSerialEnabled bool
	flasher          flasher.Flasher
	flashingEnabled  bool
	keepAwake        power.KeepAwake
}
```

Change `New` (currently lines 31-47) to:

```go
func New(
	reg *registry.Registry,
	discover DiscoverFn,
	opener labserial.Opener,
	rawSerialEnabled bool,
	fl flasher.Flasher,
	flashingEnabled bool,
	keepAwake power.KeepAwake,
) *Server {
	return &Server{
		reg:              reg,
		discover:         discover,
		opener:           opener,
		rawSerialEnabled: rawSerialEnabled,
		flasher:          fl,
		flashingEnabled:  flashingEnabled,
		keepAwake:        keepAwake,
	}
}
```

- [ ] **Step 2: Update `handlers_test.go` fixture**

Change `newTestServer` (line 26-32) to pass a fake:

```go
func newTestServer(t *testing.T, reg *registry.Registry, disc DiscoverFn) http.Handler {
	t.Helper()
	if disc == nil {
		disc = fakeDiscoverFn(nil, nil)
	}
	ka, err := power.New()
	if err != nil {
		t.Fatalf("power.New: %v", err)
	}
	t.Cleanup(func() { _ = ka.Close() })
	return New(reg, disc, serial.NewFakeOpener(), false, nil, false, ka).Handler()
}
```

Add `"github.com/bioexperiment-lab-devices/serialhop/internal/power"` to the imports.

- [ ] **Step 3: Update `flash_test.go` fixtures**

Find the two `New(...)` callsites (lines 20 and 158). Each currently looks like:

```go
s := New(reg, nil, op, true, nil, false)
```

Replace each with:

```go
ka, err := power.New()
if err != nil {
	t.Fatalf("power.New: %v", err)
}
t.Cleanup(func() { _ = ka.Close() })
s := New(reg, nil, op, true, nil, false, ka)
```

(For the second occurrence, the args are `(reg, nil, op, true, fl, enabled)` — preserve those, just append `, ka`.)

Add the `power` import.

- [ ] **Step 4: Update production wiring in `internal/app/app.go`**

After the existing flasher block (around line 78), construct a `KeepAwake` and pass it. Replace the line:

```go
srv := api.New(reg, discoverFn, opener, cfg.RawSerial.Enabled, fl, flashingEnabled)
```

with:

```go
keepAwake, err := power.New()
if err != nil {
	return fmt.Errorf("power.New: %w", err)
}
defer func() { _ = keepAwake.Close() }()
srv := api.New(reg, discoverFn, opener, cfg.RawSerial.Enabled, fl, flashingEnabled, keepAwake)
```

Add `"github.com/bioexperiment-lab-devices/serialhop/internal/power"` to the imports.

- [ ] **Step 5: Run all Go tests**

Run: `go test -race -count=1 ./...`
Expected: PASS. No new tests yet — this proves the threading was done correctly without breaking existing coverage.

- [ ] **Step 6: Commit**

```bash
git add internal/api/handlers.go internal/api/handlers_test.go internal/api/flash_test.go internal/app/app.go
git commit -m "$(cat <<'EOF'
refactor(api): inject KeepAwake into Server constructor

Threads a KeepAwake dependency through api.New so subsequent
handlers can call it. Production wiring constructs power.New();
tests use the cross-platform fake.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: `GET /power/keep-awake` handler

**Files:**
- Modify: `internal/api/handlers.go` (`Handler()` route table + new handler func)
- Create: `internal/api/handlers_power_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/api/handlers_power_test.go`:

```go
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/power"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// powerTestServer builds a Server backed by the cross-platform power
// fake. The returned ka is exposed so tests can pre-flip Active() to
// exercise the GET handler's state reporting.
func powerTestServer(t *testing.T) (http.Handler, power.KeepAwake) {
	t.Helper()
	ka, err := power.New()
	if err != nil {
		t.Fatalf("power.New: %v", err)
	}
	t.Cleanup(func() { _ = ka.Close() })
	reg := registry.New()
	srv := New(reg, fakeDiscoverFn(nil, nil), serial.NewFakeOpener(), false, nil, false, ka).Handler()
	return srv, ka
}

func TestGetKeepAwake_ReturnsCurrentState(t *testing.T) {
	srv, ka := powerTestServer(t)

	// Default: inactive.
	req := httptest.NewRequest(http.MethodGet, "/power/keep-awake", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET status: got %d", rec.Code)
	}
	var resp struct {
		Active bool `json:"active"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Active {
		t.Errorf("active = true on cold server, want false")
	}

	// Flip Active and reread.
	if err := ka.Enable("test"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/power/keep-awake", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET status after Enable: got %d", rec.Code)
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Active {
		t.Errorf("active = false after Enable, want true")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -run TestGetKeepAwake_ReturnsCurrentState ./internal/api/...`
Expected: FAIL — the route doesn't exist yet; `httptest` returns 404.

- [ ] **Step 3: Wire the route and write the handler**

In `internal/api/handlers.go`, extend `Handler()` (currently lines 49-61) to add the new route:

```go
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /devices", s.handleGetDevices)
	mux.HandleFunc("POST /discover", s.handlePostDiscover)
	mux.HandleFunc("POST /devices/{id}/command", s.handlePostCommand)
	mux.HandleFunc("GET /serial/ports", s.handleGetSerialPorts)
	mux.HandleFunc("POST /serial/ports/{port}/command", s.handlePostSerialCommand)
	mux.HandleFunc("POST /devices/disconnect", s.handlePostDevicesDisconnect)
	mux.HandleFunc("GET /serial/ports/detailed", s.handleGetSerialPortsDetailed)
	mux.HandleFunc("POST /flash/{port}", s.handlePostFlashPort)
	mux.HandleFunc("GET /agent/info", s.handleGetAgentInfo)
	mux.HandleFunc("GET /power/keep-awake", s.handleGetKeepAwake)
	return logMiddleware(mux)
}
```

Add the handler near the bottom of the file (after `handleGetAgentInfo`):

```go
// keepAwakeStatusBody is the response body for the three /power/keep-awake
// routes. Defined here, not in types.go, so it stays close to the
// handlers that produce it.
type keepAwakeStatusBody struct {
	Active bool `json:"active"`
}

// handleGetKeepAwake reports the current power-request state.
func (s *Server) handleGetKeepAwake(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, keepAwakeStatusBody{Active: s.keepAwake.Active()})
}
```

- [ ] **Step 4: Run the test**

Run: `go test -race -count=1 -run TestGetKeepAwake_ReturnsCurrentState ./internal/api/...`
Expected: PASS.

- [ ] **Step 5: Run all api tests**

Run: `go test -race -count=1 ./internal/api/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api/handlers.go internal/api/handlers_power_test.go
git commit -m "$(cat <<'EOF'
feat(api): GET /power/keep-awake returns current state

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: `POST /power/keep-awake/enable` handler

**Files:**
- Modify: `internal/api/handlers.go`
- Modify: `internal/api/handlers_power_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/api/handlers_power_test.go`. (Add `"errors"` and `"strings"` to the import block alongside the existing imports.)

```go
// errorKeepAwake is a KeepAwake that returns the supplied error from
// the named operation. Used to exercise the 500 path in the enable /
// disable handlers without relying on real syscall failures.
type errorKeepAwake struct {
	active     bool
	enableErr  error
	disableErr error
}

func (e *errorKeepAwake) Enable(_ string) error {
	if e.enableErr != nil {
		return e.enableErr
	}
	e.active = true
	return nil
}
func (e *errorKeepAwake) Disable() error {
	if e.disableErr != nil {
		return e.disableErr
	}
	e.active = false
	return nil
}
func (e *errorKeepAwake) Active() bool { return e.active }
func (e *errorKeepAwake) Close() error { return nil }

var errSyscallFake = errors.New("synthetic failure")

func powerTestServerWith(t *testing.T, ka power.KeepAwake) http.Handler {
	t.Helper()
	reg := registry.New()
	return New(reg, fakeDiscoverFn(nil, nil), serial.NewFakeOpener(), false, nil, false, ka).Handler()
}

func TestEnableKeepAwake_FlipsActive(t *testing.T) {
	srv, ka := powerTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/power/keep-awake/enable", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp keepAwakeStatusBody
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Active {
		t.Errorf("response active = false")
	}
	if !ka.Active() {
		t.Errorf("ka.Active() = false")
	}
}

func TestEnableKeepAwake_IsIdempotent(t *testing.T) {
	srv, _ := powerTestServer(t)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/power/keep-awake/enable", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("call %d status: got %d", i, rec.Code)
		}
	}
}

func TestEnableKeepAwake_Returns500OnSyscallFailure(t *testing.T) {
	ka := &errorKeepAwake{enableErr: errSyscallFake}
	srv := powerTestServerWith(t, ka)
	req := httptest.NewRequest(http.MethodPost, "/power/keep-awake/enable", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Fatalf("status: got %d, want 500", rec.Code)
	}
	var body ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "keep-awake enable failed" {
		t.Errorf("error code: got %q", body.Error)
	}
	if !strings.Contains(body.Detail, "synthetic failure") {
		t.Errorf("detail: got %q, want substring 'synthetic failure'", body.Detail)
	}
	if ka.Active() {
		t.Errorf("ka.Active() = true after failed Enable")
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test -run TestEnableKeepAwake ./internal/api/...`
Expected: FAIL — POST route 404s.

- [ ] **Step 3: Wire the route and handler**

In `Handler()` add the new route line beside the GET:

```go
mux.HandleFunc("POST /power/keep-awake/enable", s.handlePostKeepAwakeEnable)
```

Add the handler below `handleGetKeepAwake`:

```go
// handlePostKeepAwakeEnable activates the power request. Idempotent.
// On syscall failure returns 500 with the underlying error in `detail`;
// the service-side Active flag stays unchanged on failure.
func (s *Server) handlePostKeepAwakeEnable(w http.ResponseWriter, _ *http.Request) {
	const reason = "SerialHop panel: operator-requested keep-awake"
	if err := s.keepAwake.Enable(reason); err != nil {
		slog.Warn("keep-awake enable failed", "err", err)
		writeError(w, http.StatusInternalServerError, "keep-awake enable failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, keepAwakeStatusBody{Active: s.keepAwake.Active()})
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race -count=1 ./internal/api/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api/handlers.go internal/api/handlers_power_test.go
git commit -m "$(cat <<'EOF'
feat(api): POST /power/keep-awake/enable activates the request

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: `POST /power/keep-awake/disable` handler

**Files:**
- Modify: `internal/api/handlers.go`
- Modify: `internal/api/handlers_power_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/api/handlers_power_test.go`:

```go
func TestDisableKeepAwake_FlipsActive(t *testing.T) {
	srv, ka := powerTestServer(t)
	_ = ka.Enable("test")

	req := httptest.NewRequest(http.MethodPost, "/power/keep-awake/disable", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp keepAwakeStatusBody
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Active {
		t.Errorf("response active = true after Disable")
	}
	if ka.Active() {
		t.Errorf("ka.Active() = true after Disable")
	}
}

func TestDisableKeepAwake_IsIdempotent(t *testing.T) {
	srv, _ := powerTestServer(t)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/power/keep-awake/disable", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("call %d status: got %d", i, rec.Code)
		}
	}
}

func TestDisableKeepAwake_Returns500OnSyscallFailure(t *testing.T) {
	ka := &errorKeepAwake{active: true, disableErr: errSyscallFake}
	srv := powerTestServerWith(t, ka)
	req := httptest.NewRequest(http.MethodPost, "/power/keep-awake/disable", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Fatalf("status: got %d", rec.Code)
	}
	var body ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "keep-awake disable failed" {
		t.Errorf("error code: got %q", body.Error)
	}
	if !ka.Active() {
		t.Errorf("ka.Active() = false after failed Disable; service-side flag must stay aligned with attempted OS state")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test -run TestDisableKeepAwake ./internal/api/...`
Expected: FAIL — 404.

- [ ] **Step 3: Wire the route and handler**

In `Handler()` add:

```go
mux.HandleFunc("POST /power/keep-awake/disable", s.handlePostKeepAwakeDisable)
```

Below `handlePostKeepAwakeEnable`:

```go
// handlePostKeepAwakeDisable clears the power request. Idempotent. On
// syscall failure returns 500; the service-side Active flag is left at
// its current value so the next Enable short-circuits (consistent with
// our best-effort knowledge of OS state).
func (s *Server) handlePostKeepAwakeDisable(w http.ResponseWriter, _ *http.Request) {
	if err := s.keepAwake.Disable(); err != nil {
		slog.Warn("keep-awake disable failed", "err", err)
		writeError(w, http.StatusInternalServerError, "keep-awake disable failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, keepAwakeStatusBody{Active: s.keepAwake.Active()})
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race -count=1 ./internal/api/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api/handlers.go internal/api/handlers_power_test.go
git commit -m "$(cat <<'EOF'
feat(api): POST /power/keep-awake/disable clears the request

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: `ServiceCli` methods (cross-platform; tested on macOS)

**Files:**
- Modify: `internal/panel/servicecli.go`
- Modify: `internal/panel/servicecli_test.go`

The `ServiceCli` source file has no build tag (it's cross-platform), so its tests run on macOS too.

- [ ] **Step 1: Write the failing tests**

The existing `servicecli_test.go` already provides two helpers we reuse here:
- `seedCache(t, port int) string` — writes a bootstrap cache to a temp dir anchored at the given port (user="alice") and returns its path.
- `mustPortFromURL(t, url string) int` — extracts the port from an `httptest.Server.URL`.

Append to `internal/panel/servicecli_test.go`:

```go
func TestServiceCli_GetKeepAwake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/power/keep-awake" || r.Method != http.MethodGet {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"active": true}`))
	}))
	t.Cleanup(srv.Close)

	cli := NewServiceCli(seedCache(t, mustPortFromURL(t, srv.URL)))
	got, status, err := cli.GetKeepAwake(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != StatusOK {
		t.Errorf("status: got %v, want StatusOK", status)
	}
	if !got.Active {
		t.Errorf("active = false")
	}
}

func TestServiceCli_EnableKeepAwake(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/power/keep-awake/enable" || r.Method != http.MethodPost {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		hits++
		_, _ = w.Write([]byte(`{"active": true}`))
	}))
	t.Cleanup(srv.Close)

	cli := NewServiceCli(seedCache(t, mustPortFromURL(t, srv.URL)))
	got, status, err := cli.EnableKeepAwake(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != StatusOK || !got.Active {
		t.Errorf("got %+v, status=%v", got, status)
	}
	if hits != 1 {
		t.Errorf("hits: got %d, want 1", hits)
	}
}

func TestServiceCli_DisableKeepAwake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/power/keep-awake/disable" || r.Method != http.MethodPost {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"active": false}`))
	}))
	t.Cleanup(srv.Close)

	cli := NewServiceCli(seedCache(t, mustPortFromURL(t, srv.URL)))
	got, status, err := cli.DisableKeepAwake(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != StatusOK || got.Active {
		t.Errorf("got %+v, status=%v", got, status)
	}
}

func TestServiceCli_KeepAwake_ServiceDownOn500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"keep-awake enable failed","detail":"boom"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	cli := NewServiceCli(seedCache(t, mustPortFromURL(t, srv.URL)))
	_, status, err := cli.EnableKeepAwake(context.Background())
	if status != StatusServiceDown {
		t.Errorf("status: got %v, want StatusServiceDown", status)
	}
	if err == nil {
		t.Errorf("err = nil; want non-nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -count=1 -run TestServiceCli_(Get|Enable|Disable)KeepAwake -run TestServiceCli_KeepAwake_ServiceDownOn500 ./internal/panel/...`
Expected: FAIL — methods don't exist.

- [ ] **Step 3: Add the three methods**

Append to `internal/panel/servicecli.go`:

```go
// KeepAwakeStatus is the response body shared by the three
// /power/keep-awake endpoints.
type KeepAwakeStatus struct {
	Active bool `json:"active"`
}

// GetKeepAwake proxies GET /power/keep-awake.
func (c *ServiceCli) GetKeepAwake(ctx context.Context) (KeepAwakeStatus, ServiceCliStatus, error) {
	var out KeepAwakeStatus
	status, err := c.do(ctx, "GET", "/power/keep-awake", &out)
	return out, status, err
}

// EnableKeepAwake proxies POST /power/keep-awake/enable.
func (c *ServiceCli) EnableKeepAwake(ctx context.Context) (KeepAwakeStatus, ServiceCliStatus, error) {
	var out KeepAwakeStatus
	status, err := c.do(ctx, "POST", "/power/keep-awake/enable", &out)
	return out, status, err
}

// DisableKeepAwake proxies POST /power/keep-awake/disable.
func (c *ServiceCli) DisableKeepAwake(ctx context.Context) (KeepAwakeStatus, ServiceCliStatus, error) {
	var out KeepAwakeStatus
	status, err := c.do(ctx, "POST", "/power/keep-awake/disable", &out)
	return out, status, err
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race -count=1 ./internal/panel/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/servicecli.go internal/panel/servicecli_test.go
git commit -m "$(cat <<'EOF'
feat(panel): ServiceCli methods for /power/keep-awake

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Wails bindings — DTO + three bound methods

**Files:**
- Modify: `internal/panel/bindings.go`
- Create: `internal/panel/bindings_keepawake_test.go`

The bindings are `//go:build windows`, so the new test file is too. Cross-platform CI (macOS) skips it; Windows CI runs it.

- [ ] **Step 1: Write the failing tests**

Reuses the same `seedCache` / `mustPortFromURL` helpers as Task 8 (they live in the same package). Create `internal/panel/bindings_keepawake_test.go`:

```go
//go:build windows

package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestGetKeepAwake_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"active": true})
	}))
	t.Cleanup(srv.Close)

	a := &App{
		svc: NewServiceCli(seedCache(t, mustPortFromURL(t, srv.URL))),
		ctx: context.Background(),
	}
	got := a.GetKeepAwake()
	if !got.Reachable {
		t.Errorf("Reachable = false; reason=%q", got.Reason)
	}
	if !got.Active {
		t.Errorf("Active = false")
	}
	if got.ErrorMessage != "" {
		t.Errorf("ErrorMessage = %q", got.ErrorMessage)
	}
}

func TestEnableKeepAwake_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"active": true})
	}))
	t.Cleanup(srv.Close)

	a := &App{
		svc: NewServiceCli(seedCache(t, mustPortFromURL(t, srv.URL))),
		ctx: context.Background(),
	}
	got := a.EnableKeepAwake()
	if !got.Reachable || !got.Active {
		t.Errorf("got %+v", got)
	}
}

func TestDisableKeepAwake_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"active": false})
	}))
	t.Cleanup(srv.Close)

	a := &App{
		svc: NewServiceCli(seedCache(t, mustPortFromURL(t, srv.URL))),
		ctx: context.Background(),
	}
	got := a.DisableKeepAwake()
	if !got.Reachable {
		t.Errorf("Reachable = false")
	}
	if got.Active {
		t.Errorf("Active = true after disable")
	}
}

func TestKeepAwake_ServiceDown(t *testing.T) {
	// Server that we close before the test calls in — transport-level
	// failure inside ServiceCli.do.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	port := mustPortFromURL(t, srv.URL)
	srv.Close()

	a := &App{
		svc: NewServiceCli(seedCache(t, port)),
		ctx: context.Background(),
	}
	got := a.EnableKeepAwake()
	if got.Reachable {
		t.Errorf("Reachable = true on closed server")
	}
	if got.Reason != "service_down" {
		t.Errorf("Reason = %q, want service_down", got.Reason)
	}
}

func TestKeepAwake_Unreachable_MissingCache(t *testing.T) {
	a := &App{
		svc: NewServiceCli(filepath.Join(t.TempDir(), "absent.cache.json")),
		ctx: context.Background(),
	}
	got := a.GetKeepAwake()
	if got.Reachable {
		t.Errorf("Reachable = true with missing cache")
	}
	if got.Reason != "unreachable" {
		t.Errorf("Reason = %q, want unreachable", got.Reason)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail (on Windows; on macOS, the file is build-tagged out)**

Run (on Windows): `go test -race -count=1 ./internal/panel/...`
Expected: FAIL — bound methods don't exist.

On macOS the new file is excluded; `go vet ./...` should still be clean.

- [ ] **Step 3: Add the DTO + bound methods**

In `internal/panel/bindings.go`, after the existing DTO block (after line 60ish, near `AdminResult`):

```go
// KeepAwakeResult is the SPA-facing result of GetKeepAwake /
// EnableKeepAwake / DisableKeepAwake. Reachable=false means the
// service couldn't be reached; Reason is "service_down" or
// "unreachable" per the standard panel reachability vocabulary.
// ErrorMessage carries the underlying detail when the service
// returned a 500 (syscall failure).
type KeepAwakeResult struct {
	Active       bool   `json:"active"`
	Reachable    bool   `json:"reachable"`
	Reason       string `json:"reason,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}
```

Near the bottom of the bindings block (after `InstallUpdate`), add:

```go
func (a *App) GetKeepAwake() KeepAwakeResult {
	done := a.logAction("keepawake_get")
	ctx, cancel := a.callCtx()
	defer cancel()
	out, st, err := a.svc.GetKeepAwake(ctx)
	res := keepAwakeResult(out, st, err)
	done(err, slog.Bool("active", res.Active), slog.Bool("reachable", res.Reachable))
	return res
}

func (a *App) EnableKeepAwake() KeepAwakeResult {
	done := a.logAction("keepawake_enable")
	ctx, cancel := a.callCtx()
	defer cancel()
	out, st, err := a.svc.EnableKeepAwake(ctx)
	res := keepAwakeResult(out, st, err)
	done(err, slog.Bool("active", res.Active), slog.Bool("reachable", res.Reachable))
	emitKeepAwakeFooter(a, res, "Keep-awake enabled")
	return res
}

func (a *App) DisableKeepAwake() KeepAwakeResult {
	done := a.logAction("keepawake_disable")
	ctx, cancel := a.callCtx()
	defer cancel()
	out, st, err := a.svc.DisableKeepAwake(ctx)
	res := keepAwakeResult(out, st, err)
	done(err, slog.Bool("active", res.Active), slog.Bool("reachable", res.Reachable))
	emitKeepAwakeFooter(a, res, "Keep-awake disabled")
	return res
}

// keepAwakeResult translates a (status, error) from ServiceCli into the
// SPA-facing DTO. Mirrors the toTabStatus mapping for the Reason field
// but adds an ErrorMessage when the service-side handler returned a 500.
func keepAwakeResult(out KeepAwakeStatus, st ServiceCliStatus, err error) KeepAwakeResult {
	switch st {
	case StatusOK:
		return KeepAwakeResult{Active: out.Active, Reachable: true}
	case StatusServiceDown:
		res := KeepAwakeResult{Reachable: false, Reason: "service_down"}
		if err != nil {
			res.ErrorMessage = err.Error()
		}
		return res
	}
	return KeepAwakeResult{Reachable: false, Reason: "unreachable"}
}

func emitKeepAwakeFooter(a *App, res KeepAwakeResult, okPrefix string) {
	switch {
	case res.Reachable && res.ErrorMessage == "":
		a.emitEvent("footer:set", map[string]interface{}{
			"kind": "ok",
			"text": okPrefix + " at " + time.Now().Format("15:04:05"),
		})
	case res.ErrorMessage != "":
		a.emitEvent("footer:set", map[string]interface{}{
			"kind": "err",
			"text": "Keep-awake failed: " + res.ErrorMessage,
		})
	default:
		a.emitEvent("footer:set", map[string]interface{}{
			"kind": "err",
			"text": "Keep-awake failed: service unreachable",
		})
	}
}
```

Make sure the `KeepAwakeStatus` reference resolves — it lives in `servicecli.go` from Task 8, same package.

- [ ] **Step 4: Run tests (Windows)**

Run: `go test -race -count=1 ./internal/panel/...`
Expected: PASS — including the existing `TestBindings_NoMethodTakesContextContext` regression test, which AST-parses bindings.go to confirm no new method takes a `context.Context`.

On macOS, just run `go test -race -count=1 ./...` — the new test file is build-tagged out, but everything else (api, power, servicecli, ctx-check) must still pass.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/bindings.go internal/panel/bindings_keepawake_test.go
git commit -m "$(cat <<'EOF'
feat(panel): GetKeepAwake/EnableKeepAwake/DisableKeepAwake bindings

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Frontend types + Wails JS stubs

**Files:**
- Modify: `internal/panel/frontend/src/types.ts`
- Modify: `internal/panel/frontend/src/wails/go/main/App.ts`

This task is just type declarations; no tests required. The next two frontend tasks exercise it.

- [ ] **Step 1: Add the payload type**

Append to `internal/panel/frontend/src/types.ts`:

```ts
export interface KeepAwakePayload {
  active: boolean;
  reachable: boolean;
  reason?: string;          // "service_down" | "unreachable" | undefined
  error_message?: string;
}
```

- [ ] **Step 2: Add the JS stubs**

In `internal/panel/frontend/src/wails/go/main/App.ts`, add (near `InstallUpdate`):

```ts
export function GetKeepAwake(): Promise<{ active: boolean; reachable: boolean; reason?: string; error_message?: string }> {
  return call("GetKeepAwake");
}
export function EnableKeepAwake(): Promise<{ active: boolean; reachable: boolean; reason?: string; error_message?: string }> {
  return call("EnableKeepAwake");
}
export function DisableKeepAwake(): Promise<{ active: boolean; reachable: boolean; reason?: string; error_message?: string }> {
  return call("DisableKeepAwake");
}
```

- [ ] **Step 3: Type-check**

Run (from `internal/panel/frontend/`): `npm run build`
Expected: PASS. (Vite's `tsc --noEmit` step gates this.)

- [ ] **Step 4: Commit**

```bash
git add internal/panel/frontend/src/types.ts internal/panel/frontend/src/wails/go/main/App.ts
git commit -m "$(cat <<'EOF'
feat(panel): TS types for keep-awake bindings

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Frontend global store — keep-awake state slice + reconciliation

**Files:**
- Modify: `internal/panel/frontend/src/state/globalStore.ts`
- Modify: `internal/panel/frontend/src/App.tsx`

Place the keep-awake state next to the other shared UI state. Reconciliation rule: any time the `service` lamp transitions to tone `green` from another tone, re-fetch keep-awake.

- [ ] **Step 1: Extend the global store**

In `internal/panel/frontend/src/state/globalStore.ts`:

Add imports at the top:
```ts
import { GetKeepAwake } from "../wails/go/main/App";
import { type KeepAwakePayload } from "../types";
```

Add the default and state alongside the other state:

```ts
const DEFAULT_KEEPAWAKE: KeepAwakePayload = { active: false, reachable: false };
```

Inside `useGlobalUiState`, add:

```ts
const [keepAwake, setKeepAwake] = useState<KeepAwakePayload>(DEFAULT_KEEPAWAKE);
```

Inside the existing `useEffect` (the one that subscribes to events), after the `EventsOn("status:lamp", onLamp)` line, wrap or replace `onLamp` so it also reconciles keep-awake on service-lamp recovery:

```ts
let prevServiceTone: Tone | undefined;
const onLamp = (p: LampPayload) => {
  setLamps(prev => ({ ...prev, [p.which]: { tone: p.tone, label: p.label, sub: p.sub } }));
  if (p.which === "service" && p.tone === "green" && prevServiceTone !== "green") {
    GetKeepAwake().then(res => setKeepAwake(res)).catch(() => { /* ignore */ });
  }
  if (p.which === "service") prevServiceTone = p.tone;
};
```

Just below the existing events, fire the initial fetch (once, on mount):

```ts
GetKeepAwake().then(res => setKeepAwake(res)).catch(() => { /* ignore */ });
```

Place this inside the existing `useEffect` (the one keyed on `[]`), after the `EventsOn` calls.

Update the return value:

```ts
return { warn, footer, lamps, buttons, update, logState, keepAwake, setKeepAwake };
```

- [ ] **Step 2: Pass the state into `StatusTab` via `App.tsx`**

In `internal/panel/frontend/src/App.tsx`:

Destructure `keepAwake` and `setKeepAwake`:

```ts
const { warn, footer, lamps, buttons, update, logState, keepAwake, setKeepAwake } = useGlobalUiState();
```

Update the `<StatusTab .../>` invocation to pass them down:

```tsx
<StatusTab
  lamps={lamps}
  buttons={buttons}
  update={update}
  configDirty={configDirty}
  keepAwake={keepAwake}
  setKeepAwake={setKeepAwake}
/>
```

- [ ] **Step 3: Type-check**

Run: `npm run build`
Expected: a TS error about `StatusTab` not accepting `keepAwake` / `setKeepAwake` props (because we haven't extended its Props yet). That's fine — the next task fixes it.

- [ ] **Step 4: Commit (with build break, since the next task closes it)**

Actually, to keep `main` green, **delay the commit** until Task 12 lands. We're treating Tasks 11 and 12 as a single logical change. Skip this commit step; proceed directly to Task 12.

---

## Task 12: `StatusTab` — Power section with lamp + button + tests

**Files:**
- Modify: `internal/panel/frontend/src/tabs/StatusTab.tsx`
- Create: `internal/panel/frontend/src/tabs/StatusTab.test.tsx`

- [ ] **Step 1: Write the failing test**

Create `internal/panel/frontend/src/tabs/StatusTab.test.tsx`:

```tsx
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { StatusTab } from "./StatusTab";
import { UpdateState } from "../types";
import type { KeepAwakePayload } from "../types";

// Mock the Wails bindings. Each test resets the mocks in beforeEach.
const mocks = vi.hoisted(() => ({
  EnableKeepAwake: vi.fn<[], Promise<KeepAwakePayload>>(),
  DisableKeepAwake: vi.fn<[], Promise<KeepAwakePayload>>(),
}));

vi.mock("../wails/go/main/App", async () => {
  const actual: object = await vi.importActual("../wails/go/main/App");
  return {
    ...actual,
    EnableKeepAwake: mocks.EnableKeepAwake,
    DisableKeepAwake: mocks.DisableKeepAwake,
  };
});

const DEFAULT_LAMPS = {
  service: { tone: "green" as const, label: "OK" },
  server: { tone: "green" as const, label: "Reachable" },
  tunnel: { tone: "green" as const, label: "Up" },
};
const DEFAULT_BUTTONS = { install: false, uninstall: true, restart: true };
const DEFAULT_UPDATE = { state: UpdateState.Idle, release_tag: "" };

function renderTab(overrides?: { keepAwake?: KeepAwakePayload }) {
  const keepAwake: KeepAwakePayload = overrides?.keepAwake ?? { active: false, reachable: true };
  const setKeepAwake = vi.fn();
  render(
    <StatusTab
      lamps={DEFAULT_LAMPS}
      buttons={DEFAULT_BUTTONS}
      update={DEFAULT_UPDATE}
      keepAwake={keepAwake}
      setKeepAwake={setKeepAwake}
    />,
  );
  return { setKeepAwake };
}

beforeEach(() => {
  mocks.EnableKeepAwake.mockReset();
  mocks.DisableKeepAwake.mockReset();
});

describe("StatusTab — Power section", () => {
  it("renders Off lamp + Enable button when inactive", () => {
    renderTab({ keepAwake: { active: false, reachable: true } });
    expect(screen.getByText("Keep system awake")).toBeInTheDocument();
    expect(screen.getByText("Off")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Enable" })).toBeEnabled();
  });

  it("renders On lamp + Disable button when active", () => {
    renderTab({ keepAwake: { active: true, reachable: true } });
    expect(screen.getByText("On")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Disable" })).toBeEnabled();
  });

  it("renders unreachable state with disabled button", () => {
    renderTab({ keepAwake: { active: false, reachable: false } });
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(screen.getByText(/service unreachable/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /enable/i })).toBeDisabled();
  });

  it("calls EnableKeepAwake on click and updates state from response", async () => {
    mocks.EnableKeepAwake.mockResolvedValueOnce({ active: true, reachable: true });
    const { setKeepAwake } = renderTab({ keepAwake: { active: false, reachable: true } });
    fireEvent.click(screen.getByRole("button", { name: "Enable" }));
    await waitFor(() => expect(mocks.EnableKeepAwake).toHaveBeenCalledTimes(1));
    expect(setKeepAwake).toHaveBeenLastCalledWith({ active: true, reachable: true });
  });

  it("calls DisableKeepAwake on click and updates state", async () => {
    mocks.DisableKeepAwake.mockResolvedValueOnce({ active: false, reachable: true });
    const { setKeepAwake } = renderTab({ keepAwake: { active: true, reachable: true } });
    fireEvent.click(screen.getByRole("button", { name: "Disable" }));
    await waitFor(() => expect(mocks.DisableKeepAwake).toHaveBeenCalledTimes(1));
    expect(setKeepAwake).toHaveBeenLastCalledWith({ active: false, reachable: true });
  });

  it("disables button while a toggle is in flight", async () => {
    let resolve: (v: KeepAwakePayload) => void = () => {};
    mocks.EnableKeepAwake.mockReturnValueOnce(new Promise<KeepAwakePayload>(r => { resolve = r; }));
    renderTab({ keepAwake: { active: false, reachable: true } });
    const btn = screen.getByRole("button", { name: "Enable" });
    fireEvent.click(btn);
    expect(btn).toBeDisabled();
    resolve({ active: true, reachable: true });
    await waitFor(() => expect(mocks.EnableKeepAwake).toHaveBeenCalled());
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run (from `internal/panel/frontend/`): `npm test`
Expected: FAIL — `StatusTab` doesn't accept `keepAwake` props yet.

- [ ] **Step 3: Add the Power section to `StatusTab.tsx`**

Open `internal/panel/frontend/src/tabs/StatusTab.tsx`.

Extend the import block:

```ts
import { useEffect, useState } from "react";
import { Button } from "../components/Button";
import { Lamp } from "../components/Lamp";
import { Help } from "../components/Help";
import {
  UpdateState,
  type ButtonStatePayload,
  type KeepAwakePayload,
  type LampWhich,
  type Tone,
  type UpdateStatePayload,
} from "../types";
import {
  InstallService, UninstallService, RestartService,
  DownloadUpdate, CancelDownload, InstallUpdate, OpenReleaseNotes,
  RelaunchPanel,
  EnableKeepAwake, DisableKeepAwake,
} from "../wails/go/main/App";
import { EventsEmit } from "../wails/runtime/runtime";
```

Extend the `Props` interface to accept the new state + setter:

```ts
interface Props {
  lamps: Lamps;
  buttons: ButtonStatePayload;
  update: UpdateStatePayload;
  configDirty?: boolean;
  keepAwake: KeepAwakePayload;
  setKeepAwake: (next: KeepAwakePayload) => void;
}
```

Destructure inside `StatusTab`:

```ts
export function StatusTab({ lamps, buttons, update, configDirty, keepAwake, setKeepAwake }: Props) {
```

Add local state for the toggle being in flight, just below the existing `const [busy, setBusy] = useState(false);` (which is used by the service-control buttons):

```ts
const [paBusy, setPaBusy] = useState(false);

const onToggleKeepAwake = async () => {
  setPaBusy(true);
  try {
    const fn = keepAwake.active ? DisableKeepAwake : EnableKeepAwake;
    const res = await fn();
    setKeepAwake({
      active: res.active,
      reachable: res.reachable,
      reason: res.reason,
      error_message: res.error_message,
    });
  } finally {
    setPaBusy(false);
  }
};

const paLampTone: Tone = !keepAwake.reachable ? "grey" : keepAwake.active ? "green" : "grey";
const paLampLabel = !keepAwake.reachable ? "—" : keepAwake.active ? "On" : "Off";
const paLampSub = !keepAwake.reachable
  ? "Service unreachable"
  : keepAwake.active
    ? "System will not sleep or auto-shutdown."
    : undefined;
const paButtonLabel = keepAwake.active ? "Disable" : "Enable";
const paButtonVariant: "primary" | "default" = keepAwake.active ? "default" : "primary";
const paButtonDisabled = paBusy || !keepAwake.reachable;
```

Insert the Power section in the JSX between "Service health" and "Service control":

```tsx
<div className="shp-h">Power</div>
<section className="shp-lamps">
  <Lamp name="Keep system awake" tone={paLampTone} label={paLampLabel} sub={paLampSub}>
    <Help
      title="Keep system awake"
      what="Prevents Windows from idling into sleep, hibernate, or scheduled automatic shutdown while the SerialHop service is running. Has no effect on user-initiated shutdown, restart, or sign-out. Cleared if the service stops, crashes, or is updated."
    />
  </Lamp>
  <div className="shp-service-actions">
    <Button
      variant={paButtonVariant}
      disabled={paButtonDisabled}
      onClick={onToggleKeepAwake}
    >
      {paButtonLabel}
    </Button>
  </div>
</section>
```

(`<Help>` takes `title: string` and `what: string` — same signature already used by the service-health lamps above.)

- [ ] **Step 4: Run frontend tests**

Run (from `internal/panel/frontend/`): `npm test`
Expected: all six new test cases PASS, all existing tests still PASS.

- [ ] **Step 5: Run TypeScript build to catch any type errors**

Run: `npm run build`
Expected: PASS.

- [ ] **Step 6: Run lint**

Run: `npm run lint`
Expected: clean. Fix any warnings in the new files inline.

- [ ] **Step 7: Commit Tasks 11 + 12 together**

```bash
git add internal/panel/frontend/src/state/globalStore.ts \
        internal/panel/frontend/src/App.tsx \
        internal/panel/frontend/src/tabs/StatusTab.tsx \
        internal/panel/frontend/src/tabs/StatusTab.test.tsx
git commit -m "$(cat <<'EOF'
feat(panel): Power section on Status tab

Adds a Keep-system-awake lamp + Enable/Disable button between
Service health and Service control. State lives in the global
store; refetches on service-lamp recovery so a service restart
reconciles the panel back to the actual state.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: Final whole-repo verification

**Files:** none (verification only).

This task gates the PR. Everything must be green.

- [ ] **Step 1: Run the full Go test matrix**

Run: `go test -race -count=1 ./...`
Expected: PASS.

- [ ] **Step 2: Run gofmt + go vet + govulncheck (as `pr.yml` does)**

Run: `gofmt -l .`
Expected: no output.

Run: `go vet ./...`
Expected: no output.

Run (if installed): `govulncheck ./...`
Expected: no findings.

- [ ] **Step 3: Run golangci-lint if available**

Run: `golangci-lint run`
Expected: clean.

- [ ] **Step 4: Run the frontend test suite + build + lint**

From `internal/panel/frontend/`:

Run: `npm test`
Expected: PASS.

Run: `npm run build`
Expected: PASS.

Run: `npm run lint`
Expected: clean.

- [ ] **Step 5: Confirm the regression gate test ran and passed**

Specifically search for `TestBindings_NoMethodTakesContextContext` in the Go test output. Verify it shows PASS (or simply ran — the AST-parse test fails the build if any new `*App` method accidentally takes `context.Context`).

- [ ] **Step 6: Manual sanity check on a Windows host (post-merge or pre-merge, but required before flagging the feature as shippable)**

This is a written checklist for the reviewer to walk through, not a test step:

1. Build `SerialHop.exe` via `task build` on a Windows VM/host.
2. Install + start the service; open the panel.
3. Confirm the Status tab shows a new "Power" section with the lamp Off.
4. Click `Enable`. Footer should read `Keep-awake enabled at HH:MM:SS`; lamp turns green; button label flips to `Disable`.
5. From an elevated command prompt: `powercfg /requests`. Expect one `System` entry attributed to the SerialHop service exe with reason `SerialHop panel: operator-requested keep-awake`.
6. Click `Disable`. Lamp returns to gray; `powercfg /requests` shows no System entry for SerialHop.
7. Click `Enable`. Then click `Restart` in Service control. After the service comes back, the lamp should land on `Off` automatically (reconciled via the service-lamp recovery hook).
8. Optional: let the box idle past the configured sleep timer with keep-awake enabled. Verify it does NOT sleep. Turn keep-awake off, repeat — verify the box does sleep.

- [ ] **Step 7: PR checklist before opening**

- PR title MUST start with `feat:` (this is a user-visible feature; the release-please pipeline will pick it up as a minor bump).
- Suggested title: `feat: keep-awake button on Status tab (panel + service)`.
- Body: link to `docs/superpowers/specs/2026-05-19-keep-awake-design.md` and summarize the change at a high level (one paragraph each: motivation, design, testing).
- Reviewers: whoever owns the panel side; ideally someone who can also run the Windows manual checklist (step 6) before approving.
