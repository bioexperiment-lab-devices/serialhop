# v2 API Cutover (PR 5 of 5) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the raw-byte device API with the `/api/v1` JSON device protocol surface — the breaking cutover PR that wires `internal/device` (runtime + pump/densitometer/valve drivers) into discovery, the registry, the HTTP API, and the panel, and deletes the raw endpoints.

**Architecture:** The HTTP layer becomes a thin translator: it parses the command envelope, looks up a `*device.Session` in a rewritten session registry, calls `Session.Execute`, and mirrors the envelope outcome as an HTTP status. Discovery returns neutral `Match` values (port + conn + probe reply); app wiring builds sessions from them via `device.LookupDriver`. Two session-served commands (`identify`, `get_job`) become memory-served while a device is unreachable (settled amendment to spec §3).

**Tech Stack:** Go 1.22+ method-pattern `http.ServeMux`, `httptest`, `serial.FakePort`/`FakeOpener`, `device.FakeClock`, React/TS (panel, vitest).

**Spec:** `docs/superpowers/specs/2026-07-05-json-device-protocol-design.md` §4 (HTTP surface), §7 (release), §8 (deviations). This plan amends §3 (memory-served commands) — Task 1 updates the spec file.

## Global Constraints

- **NEVER write the string "BREAKING CHANGE" (or "BREAKING-CHANGE") in any commit message on this branch.** Only the PR *body* carries it (Task 12). release-please reads squash commits on main; a stray footer in a branch commit that gets cherry-picked or referenced could mis-bump. Task 12 greps the branch log to verify.
- Pre-flight before the PR (all must pass, on this machine): `gofmt -l .` (prints nothing) · `go vet ./...` · `golangci-lint run` · `go test -race -count=1 ./...` · `$(go env GOPATH)/bin/govulncheck ./...`.
- Tests must be cross-platform (macOS + Windows) and race-clean. No new `_windows.go` code is introduced.
- Driver registration happens at app wiring time — **never** via `init()`.
- Infra routes stay untouched at current paths: `/agent/info`, `/flash/{port}`, `/devices/disconnect`, `/serial/ports/detailed`, `/power/*`.
- API tests use `httptest` + a fake driver under a test type code (spec §6) — no real driver logic in API tests. (The valve round-trip test lives in `internal/registry`, not `internal/api`, for this reason.)
- Branch: `feat/v2-api-cutover` off fresh `main` (use superpowers:using-git-worktrees at execution start).
- Generated artifacts (`assets/manifest.xml`, `*.syso`, `dist/`) are never committed.
- Commit after every task. Conventional-commit style messages (they get squashed; cleanliness is for review).

## Sequencing note

Tasks 1–5 are standalone and keep the whole repo compiling. Task 6 is the atomic cutover: registry rename + API rewrite + app rewiring + discovery rename must land in one commit because they only compile together. Tasks 7–11 are cleanups/tests/docs on top. Task 12 is release mechanics.

---

### Task 1: Memory-served `identify`/`get_job` while unreachable (core + spec amendment)

Settled decision amending spec §3: while a session is unreachable, `identify` is served from cached `Info` whenever a successful Attach has EVER populated it (never attached → `device_unreachable`); `get_job` is ALWAYS served from the jobs engine (including the job the unreachable transition just failed with `hardware_error` "device became unreachable mid-job"); every other command (including `status`) keeps fail-fast `device_unreachable`.

**Files:**
- Modify: `internal/device/session.go:170-185` (the `handle` method)
- Modify: `internal/device/session_resilience_test.go:34-56` (the PR-1 test asserting the old behavior)
- Create: `internal/device/session_memory_served_test.go`
- Modify: `docs/superpowers/specs/2026-07-05-json-device-protocol-design.md` (§3 unreachable bullet, §4 status-mapping list)

**Interfaces:**
- Consumes: existing `stubDriver`, `newFixture`, `waitFor`, `execTransact`, `shrinkTimeoutsExt` test helpers (`session_test.go:16-93`, `session_resilience_test.go:17-32,206-211`).
- Produces: the reordered `handle()` the API layer (Task 6) relies on for its 503 mapping.

- [ ] **Step 1: Update the existing resilience test to the new contract (it will fail)**

In `session_resilience_test.go`, replace the body of `TestTransactDoubleFailureFlipsUnreachableAndFailsJob` from line 41 down with:

```go
	startResp := f.s.Execute(context.Background(), device.Request{ID: "j", Cmd: "job"})
	if startResp.Status != "ok" {
		t.Fatalf("job start: %+v", startResp)
	}
	jobID := startResp.Result.(device.Job).ID

	// nothing fed to the port → both transaction attempts time out
	resp := f.s.Execute(context.Background(), device.Request{ID: "t", Cmd: "tx"})
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("tx: %+v", resp)
	}
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })

	// get_job stays memory-served while unreachable (spec §3): it reports
	// exactly why the job died without waiting for a reattach.
	resp = f.s.Execute(context.Background(), device.Request{
		ID: "g", Cmd: "get_job", Params: json.RawMessage(`{"job_id":"` + jobID + `"}`)})
	if resp.Status != "ok" {
		t.Fatalf("get_job while unreachable must be memory-served: %+v", resp)
	}
	job := resp.Result.(device.Job)
	if job.State != device.JobFailed || job.Error == nil ||
		job.Error.Code != device.CodeHardwareError ||
		job.Error.Message != "device became unreachable mid-job" {
		t.Fatalf("failed job snapshot: %+v", job)
	}

	// driver-served commands still fail fast
	resp = f.s.Execute(context.Background(), device.Request{ID: "p", Cmd: "ping"})
	if resp.Status != "error" || resp.Error.Code != device.CodeDeviceUnreachable {
		t.Fatalf("driver commands while unreachable must fail fast: %+v", resp)
	}
```

- [ ] **Step 2: Write the new semantics tests**

Create `internal/device/session_memory_served_test.go`:

```go
package device_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// TestIdentifyServedFromCacheWhileUnreachable: a successful Attach populated
// the identify cache; the unreachable transition must not empty it (spec §3
// memory-served exception).
func TestIdentifyServedFromCacheWhileUnreachable(t *testing.T) {
	shrinkTimeoutsExt(t)
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		execTransact(drv, 4)
	})
	waitFor(t, "attach", f.s.Connected)

	f.s.Execute(context.Background(), device.Request{ID: "t", Cmd: "tx"}) // → unreachable
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })

	resp := f.s.Execute(context.Background(), device.Request{ID: "i", Cmd: "identify"})
	if resp.Status != "ok" {
		t.Fatalf("identify while unreachable must serve cached info: %+v", resp)
	}
	if info := resp.Result.(device.Info); info.Serial != "26-001" {
		t.Fatalf("cached info: %+v", info)
	}
}

// TestIdentifyUnreachableWhenNeverAttached: no successful Attach has ever
// populated the cache — there is nothing to serve.
func TestIdentifyUnreachableWhenNeverAttached(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.attachErr = errors.New("device silent")
	})
	time.Sleep(20 * time.Millisecond) // let the failed attach land
	resp := f.s.Execute(context.Background(), device.Request{ID: "i", Cmd: "identify"})
	if resp.Status != "error" || resp.Error.Code != device.CodeDeviceUnreachable {
		t.Fatalf("identify with no cached info must be device_unreachable: %+v", resp)
	}
}

// TestGetJobServedWhileNeverAttached: get_job is always jobs-engine-served;
// an unknown job_id stays invalid_params even while unreachable.
func TestGetJobServedWhileNeverAttached(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.attachErr = errors.New("device silent")
	})
	time.Sleep(20 * time.Millisecond)
	resp := f.s.Execute(context.Background(), device.Request{
		ID: "g", Cmd: "get_job", Params: []byte(`{"job_id":"j-1"}`)})
	if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
		t.Fatalf("get_job must be memory-served (invalid_params, not unreachable): %+v", resp)
	}
}
```

- [ ] **Step 3: Run to verify the new tests fail**

Run: `go test -race -count=1 ./internal/device/ -run 'TestTransactDoubleFailure|TestIdentifyServed|TestIdentifyUnreachable|TestGetJobServed'`
Expected: FAIL — `get_job while unreachable must be memory-served`, `identify while unreachable must serve cached info`, `get_job must be memory-served`.

- [ ] **Step 4: Reorder `Session.handle`**

Replace `internal/device/session.go:170-185` with:

```go
func (s *Session) handle(ctx context.Context, req Request) Response {
	// identify and get_job are memory-served (spec §3): they keep answering
	// while the device is unreachable so a client whose job just died can
	// read why without waiting for a reattach.
	switch req.Cmd {
	case "identify":
		if p := s.info.Load(); p != nil {
			return OK(req.ID, *p)
		}
		// No successful Attach has ever populated the cache — nothing to serve.
		return Err(req.ID, errUnreachable("device is not responding"))
	case "get_job":
		return s.handleGetJob(req)
	}
	if !s.connected.Load() {
		return Err(req.ID, errUnreachable("device is not responding"))
	}
	result, cerr := s.driver.Execute(ctx, req.Cmd, req.Params)
	if cerr != nil {
		return Err(req.ID, cerr)
	}
	return OK(req.ID, result)
}
```

- [ ] **Step 5: Run the package tests**

Run: `go test -race -count=1 ./internal/device/...`
Expected: PASS (all — including pump/densitometer/valve, which don't touch this path).

- [ ] **Step 6: Amend the spec**

In `docs/superpowers/specs/2026-07-05-json-device-protocol-design.md`:

(a) In §3, extend the **Unreachable devices** bullet (line ~176) — after "Subsequent commands return `device_unreachable` immediately" insert:

> Exception — memory-served commands (amended 2026-07-06): `identify` is served from the cached `Info` whenever a successful `Attach` has ever populated it (HTTP 200, envelope `ok`); if `Attach` has never succeeded it returns `device_unreachable`. `get_job` is always served from the jobs engine — including the job the unreachable transition just failed with `hardware_error` "device became unreachable mid-job"; unknown `job_id` remains `invalid_params`. Every other command, including `status` (driver-served), fails fast with `device_unreachable`.

(b) In §4's status-mapping list, extend the 503 bullet: "session unreachable → **503** `device_unreachable` — except `identify` (with cached info) and `get_job`, which stay memory-served at **200** per §3."

- [ ] **Step 7: Commit**

```bash
git add internal/device/session.go internal/device/session_resilience_test.go internal/device/session_memory_served_test.go docs/superpowers/specs/2026-07-05-json-device-protocol-design.md
git commit -m "feat: serve identify and get_job from memory while device unreachable"
```

---

### Task 2: `Jobs.HasActive`, `Session.HasActiveJob`, `Session.WaitFirstAttach`

Three small thread-safe additions the API layer needs: a cross-goroutine active-job probe (discover's 409 check) and a way to wait out the initial attach attempt (so a discover response reflects real attach outcomes instead of transient `connected=false`).

**Files:**
- Modify: `internal/device/jobs.go` (struct + `Start` + `finish` + new method)
- Modify: `internal/device/session.go` (struct + `NewSession` + `loop` + two new methods)
- Modify: `internal/device/jobs_test.go`, `internal/device/session_test.go` (append tests)

**Interfaces:**
- Produces: `func (j *Jobs) HasActive() bool` (thread-safe), `func (s *Session) HasActiveJob() bool` (thread-safe), `func (s *Session) WaitFirstAttach(ctx context.Context)` — blocks until the initial attach attempt completes (success or failure), ctx cancels, or the session closes. Task 6's discover handler and test helpers consume all three.

- [ ] **Step 1: Write the failing tests**

Append to `internal/device/jobs_test.go`:

```go
func TestHasActiveIsThreadSafeMirror(t *testing.T) {
	j := device.NewJobs(device.NewFakeClock(time.Unix(0, 0)))
	if j.HasActive() {
		t.Fatal("no job yet")
	}
	if _, cerr := j.Start("work", time.Minute); cerr != nil {
		t.Fatal(cerr)
	}
	if !j.HasActive() {
		t.Fatal("job started")
	}
	j.Freeze()
	if !j.HasActive() {
		t.Fatal("paused is still active")
	}
	j.Unfreeze()
	j.Complete(nil)
	if j.HasActive() {
		t.Fatal("completed")
	}
	j.Start("work", time.Minute)
	j.Fail(device.ErrHardware("x"))
	if j.HasActive() {
		t.Fatal("failed")
	}
}
```

Append to `internal/device/session_test.go`:

```go
func TestWaitFirstAttachSuccess(t *testing.T) {
	f := newFixture(t, nil)
	f.s.WaitFirstAttach(context.Background()) // must not hang
	if !f.s.Connected() {
		t.Fatal("attach outcome must be published before WaitFirstAttach returns")
	}
}

func TestWaitFirstAttachFailure(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.attachErr = errAttachBoom
	})
	f.s.WaitFirstAttach(context.Background()) // must not hang on failure either
	if f.s.Connected() {
		t.Fatal("attach failed")
	}
}

func TestHasActiveJobViaSession(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			job, cerr := drv.s.Jobs().Start("work", time.Minute)
			if cerr != nil {
				return nil, cerr
			}
			return job, nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	if f.s.HasActiveJob() {
		t.Fatal("no job yet")
	}
	if resp := f.s.Execute(context.Background(), device.Request{ID: "j", Cmd: "start"}); resp.Status != "ok" {
		t.Fatalf("start: %+v", resp)
	}
	if !f.s.HasActiveJob() {
		t.Fatal("job is active")
	}
}
```

Note: if `session_test.go` has no `errAttachBoom`, declare `var errAttachBoom = errors.New("attach boom")` near the top of the test file (check for an existing equivalent first — `TestSessionUnreachableWhenAttachFails` around line 210 already sets `attachErr`; reuse its error variable if one exists, otherwise add this one and reuse it there is NOT required).

- [ ] **Step 2: Run to verify failure**

Run: `go test -count=1 ./internal/device/ -run 'TestHasActive|TestWaitFirstAttach'`
Expected: FAIL — compile errors: `j.HasActive undefined`, `f.s.WaitFirstAttach undefined`, `f.s.HasActiveJob undefined`.

- [ ] **Step 3: Implement**

`internal/device/jobs.go` — add the import `"sync/atomic"`, add field to `Jobs`:

```go
type Jobs struct {
	clock   Clock
	seq     int
	active  *jobRec
	history []*jobRec // newest first

	// hasActive mirrors active != nil for cross-goroutine reads (the API's
	// discover-conflict check); every other method stays loop-only.
	hasActive atomic.Bool
}
```

In `Start` (after `j.active = &jobRec{...}`): `j.hasActive.Store(true)`.
In `finish` (after `j.active = nil`): `j.hasActive.Store(false)`.
Add:

```go
// HasActive reports whether a job is running or paused. Unlike every other
// Jobs method it is safe to call from any goroutine.
func (j *Jobs) HasActive() bool { return j.hasActive.Load() }
```

`internal/device/session.go` — add `firstAttach chan struct{}` to the `Session` struct (next to `done`); in `NewSession` add `firstAttach: make(chan struct{}),`; in `loop` (line ~106) change:

```go
	s.attach(s.cfg.ProbeReply)
	close(s.firstAttach)
```

Add next to the thread-safe accessors:

```go
// HasActiveJob reports whether the session has a running or paused job.
// Thread-safe; the API's discover-conflict check uses it.
func (s *Session) HasActiveJob() bool { return s.jobs.HasActive() }

// WaitFirstAttach blocks until the initial attach attempt completes
// (success or failure), ctx is cancelled, or the session shuts down.
// Discover uses it so the device list it returns reflects real attach
// outcomes instead of a transient connected=false.
func (s *Session) WaitFirstAttach(ctx context.Context) {
	select {
	case <-s.firstAttach:
	case <-s.done:
	case <-ctx.Done():
	}
}
```

- [ ] **Step 4: Run package tests**

Run: `go test -race -count=1 ./internal/device/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/device/jobs.go internal/device/session.go internal/device/jobs_test.go internal/device/session_test.go
git commit -m "feat: add session first-attach wait and thread-safe active-job probe"
```

---

### Task 3: `paths.DeviceStateDir`

**Files:**
- Modify: `internal/paths/paths.go` (new function after `StateDir` at line ~139; add to `EnsureDirs` at line ~172)
- Modify: `internal/paths/paths_test.go` (mirror the existing `StateDir` test pattern — read the file first)

**Interfaces:**
- Produces: `func DeviceStateDir() string` — `<DataDir>/devicestate`, `""` when `DataDir()` is unset. Task 6's app wiring consumes it. (`device.Store.Save` does its own `MkdirAll`, so `EnsureDirs` inclusion is belt-and-braces consistency, not a hard dependency.)

- [ ] **Step 1: Write the failing test** — read `internal/paths/paths_test.go`, find the `StateDir` test, and add the equivalent:

```go
func TestDeviceStateDir(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", filepath.Join("some", "root"))
	want := filepath.Join("some", "root", "devicestate")
	if got := paths.DeviceStateDir(); got != want {
		t.Fatalf("DeviceStateDir() = %q, want %q", got, want)
	}
	t.Setenv("SERIALHOP_DATA_DIR", "")
	// with no data dir the helper must return "" (never a relative path)
	if runtime.GOOS != "windows" {
		if got := paths.DeviceStateDir(); got != "" {
			t.Fatalf("DeviceStateDir() with no data dir = %q, want empty", got)
		}
	}
}
```

Adapt assertions to match how the existing `StateDir`/`LogsDir` tests handle the env override and the windows/non-windows split — copy their exact structure.

- [ ] **Step 2: Run to verify failure** — `go test -count=1 ./internal/paths/ -run TestDeviceStateDir` → FAIL (undefined).

- [ ] **Step 3: Implement** — in `paths.go`, after `StateDir()`:

```go
// DeviceStateDir returns the directory for per-device persistent state
// (devicestate/pump-26-025.json, devicestate/valve-COM7.json). Empty when
// no data dir is available.
func DeviceStateDir() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "devicestate")
}
```

In `EnsureDirs()`, add `DeviceStateDir()` to the list of directories created (same 0o750 MkdirAll pattern as logs/state).

- [ ] **Step 4: Run** — `go test -race -count=1 ./internal/paths/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paths/paths.go internal/paths/paths_test.go
git commit -m "feat: add device state directory to paths"
```

---

### Task 4: `discovery.Match` + `RunMatches` (staging)

Discovery must hand the API layer everything a `device.SessionConfig` needs — including the probe reply bytes it currently discards (`runner.go:108-133`). Staging: add `Match` + `RunMatches`; the old `Run` becomes a thin wrapper so `internal/api`/`internal/app` keep compiling until Task 6 deletes it.

**Files:**
- Modify: `internal/discovery/runner.go`
- Modify: `internal/discovery/runner_test.go` (add `RunMatches` tests; existing `Run` tests keep passing)

**Interfaces:**
- Produces:
  ```go
  type Match struct {
      ID       string // ordinal per (type code, port): "pump_1"
      Type     string // classification name: "pump" | "valve" | "densitometer"
      TypeCode byte
      Port     string
      Conn     serial.Port
      Reply    []byte // the identify reply the probe consumed (≥4 bytes)
  }
  func RunMatches(ctx context.Context, opener serial.Opener, candidates []string) ([]Match, error)
  ```
  Task 6 renames `RunMatches` → `Run` after deleting the wrapper.

- [ ] **Step 1: Write the failing test** — read `internal/discovery/runner_test.go` first to reuse its fake-port scripting helpers, then add:

```go
func TestRunMatchesCapturesProbeReply(t *testing.T) {
	// script one port that answers the probe as a pump: reply {10, 1, 2, 3}
	// (reuse the existing test's FakeOpener/FakePort scripting — the file
	// already builds exactly this for TestRun*; copy that setup)
	matches, err := discovery.RunMatches(context.Background(), opener, []string{"COM3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches: %+v", matches)
	}
	m := matches[0]
	if m.ID != "pump_1" || m.Type != "pump" || m.TypeCode != 10 || m.Port != "COM3" {
		t.Fatalf("match fields: %+v", m)
	}
	if len(m.Reply) < 4 || m.Reply[0] != 10 {
		t.Fatalf("probe reply must be captured for SessionConfig.ProbeReply: %v", m.Reply)
	}
	if m.Conn == nil {
		t.Fatal("matched port must stay open")
	}
	_ = m.Conn.Close()
}
```

Fill the port scripting from the file's existing pattern (there is a working "one pump on COM3" setup in the current tests — mirror it exactly, including the `init()` that zeroes `PostOpenSettle` if present, or `t.Cleanup` var swap).

- [ ] **Step 2: Run to verify failure** — `go test -count=1 ./internal/discovery/ -run TestRunMatchesCapturesProbeReply` → FAIL (undefined `RunMatches`).

- [ ] **Step 3: Implement** — in `runner.go`:
  - Add `reply []byte` to `probeOutcome` (line 63) and store `reply` in the outcome at line 133.
  - Add the `Match` type as specified above.
  - Move the body of `Run` into `RunMatches`, building `[]Match` instead of `[]*registry.Device` (same sort, same `counts` ordinal-ID loop; set `Reply: m.reply`).
  - Reduce `Run` to:

```go
// Run adapts RunMatches to the legacy registry shape. Deleted in the v2
// API cutover once nothing consumes *registry.Device.
func Run(ctx context.Context, opener serial.Opener, candidates []string) ([]*registry.Device, error) {
	matches, err := RunMatches(ctx, opener, candidates)
	if err != nil {
		return nil, err
	}
	devs := make([]*registry.Device, 0, len(matches))
	for _, m := range matches {
		devs = append(devs, &registry.Device{
			ID: m.ID, Type: m.Type, TypeCode: m.TypeCode, Port: m.Port,
			Conn: m.Conn, Opener: opener,
		})
	}
	return devs, nil
}
```

  - Update the `bytesToInts` doc comment (line 72): "…matching the convention used by command response logging." (drop the `/devices/{id}/command` route reference).

- [ ] **Step 4: Run** — `go test -race -count=1 ./internal/discovery/` → PASS (old `Run` tests + new one).

- [ ] **Step 5: Commit**

```bash
git add internal/discovery/runner.go internal/discovery/runner_test.go
git commit -m "feat: expose discovery matches with captured probe replies"
```

---

### Task 5: Session registry (staged as `SessionRegistry`)

**Decision (scope item 5): replace `internal/registry`'s contents.** The package name, import path, and `Replace`/`CloseAll` semantics survive; the `Device`-with-open-conn model does not. Staged: `SessionRegistry` lands beside the old `Registry` so everything compiles; Task 6 deletes the old code and renames.

**Files:**
- Create: `internal/registry/sessions.go`
- Create: `internal/registry/sessions_test.go`

**Interfaces:**
- Consumes: `device.NewSession`, `Session.Start/Close/ID/TypeName/PortName/HasActiveJob`, `device.FakeClock`, `serial.NewFakePort/NewFakeOpener`.
- Produces (consumed by Task 6's API handlers and app wiring — names after the Task 6 rename in parentheses):
  ```go
  func NewSessionRegistry() *SessionRegistry            // → New() *Registry
  func (r *SessionRegistry) LockDiscovery() bool
  func (r *SessionRegistry) UnlockDiscovery()
  func (r *SessionRegistry) IsDiscovering() bool
  func (r *SessionRegistry) Replace(sessions []*device.Session) // closes old set; stamps discoveredAt when sessions != nil
  func (r *SessionRegistry) CloseAll()                  // closes all; preserves discoveredAt
  func (r *SessionRegistry) DisconnectAll() int
  func (r *SessionRegistry) DisconnectByPort(port string) bool
  func (r *SessionRegistry) Get(id string) (*device.Session, bool)
  func (r *SessionRegistry) List() []*device.Session    // Replace-order copy
  func (r *SessionRegistry) HasPort(name string) (string, bool)
  func (r *SessionRegistry) DiscoveredAt() *time.Time
  ```

- [ ] **Step 1: Write the failing tests**

Create `internal/registry/sessions_test.go`. It needs a local stub driver + session builder (the `device_test` helpers aren't importable):

```go
package registry_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

type nullDriver struct {
	detached atomic.Bool
}

func (d *nullDriver) Attach(ctx context.Context, probeReply []byte) (device.Info, error) {
	return device.Info{DeviceType: "stub", Model: "stub-1", FirmwareVersion: "legacy", ProtocolVersion: "1.0"}, nil
}
func (d *nullDriver) Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *device.CmdError) {
	return nil, device.ErrUnknownCommand(cmd)
}
func (d *nullDriver) Tick(now time.Time) {}
func (d *nullDriver) Detach()            { d.detached.Store(true) }

// newStubSession returns a started session on the named port and its driver.
func newStubSession(t *testing.T, id, port string) (*device.Session, *nullDriver) {
	t.Helper()
	drv := &nullDriver{}
	fp := serial.NewFakePort(port)
	opener := serial.NewFakeOpener()
	opener.Add(fp)
	conn, err := opener.Open(port)
	if err != nil {
		t.Fatal(err)
	}
	s := device.NewSession(device.SessionConfig{
		ID: id, Type: "stub", TypeCode: 201, PortName: port,
		Conn: conn, Opener: opener, Clock: device.NewFakeClock(time.Unix(1000, 0)),
		StateDir: t.TempDir(),
		Factory:  func(*device.Session) device.Driver { return drv },
		Reprobe:  func(serial.Port) ([]byte, error) { return []byte{201, 0, 0, 1}, nil },
	})
	s.Start(context.Background())
	s.WaitFirstAttach(context.Background())
	t.Cleanup(s.Close)
	return s, drv
}
```

Tests to write (each 5–15 lines, straightforward):

- `TestReplaceInstallsAndStampsDiscoveredAt` — `Replace([s1])`; `List()` has s1; `Get("id1")` found; `DiscoveredAt()` non-nil.
- `TestReplaceClosesPreviousSessions` — `Replace([s1])` then `Replace([s2])`; assert `drv1.detached.Load()` true, `Get("id1")` gone, `List()` == [s2].
- `TestReplaceNilClosesButKeepsTimestamp` — `Replace([s1])`, note `DiscoveredAt`, `Replace(nil)`; s1 detached; `List()` empty; `DiscoveredAt()` unchanged (non-nil).
- `TestCloseAllPreservesDiscoveredAt` — same but via `CloseAll()`.
- `TestDisconnectAllReturnsCount` — two sessions → `DisconnectAll()` == 2; both detached; registry empty.
- `TestDisconnectByPort` — two sessions on COM3/COM4; `DisconnectByPort("COM3")` true; only that one detached/removed; `DisconnectByPort("COM9")` false.
- `TestHasPort` — returns (id, true) for a held port, ("", false) otherwise.
- `TestListPreservesReplaceOrder` — `Replace([s2, s1])` (deliberately not ID-sorted) → `List()` returns them in that order (discovery already sorts by (type code, port); the registry must not re-sort).
- `TestDiscoveryGate` — `LockDiscovery()` true then false while held; `IsDiscovering()` mirrors; `UnlockDiscovery()` releases.

- [ ] **Step 2: Run to verify failure** — `go test -count=1 ./internal/registry/ -run 'TestReplace|TestCloseAll|TestDisconnect|TestHasPort|TestListPreserves|TestDiscoveryGate'` → FAIL (undefined `SessionRegistry`).

- [ ] **Step 3: Implement `internal/registry/sessions.go`**

```go
package registry

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// SessionRegistry tracks the device sessions created by the most recent
// discovery, keyed by ordinal device ID. List preserves Replace order
// (discovery's (type code, port) sort). Sessions handed to Replace must
// already be Start()ed — Close on an unstarted session blocks forever.
type SessionRegistry struct {
	mu           sync.RWMutex
	ordered      []*device.Session
	byID         map[string]*device.Session
	discoveredAt *time.Time
	discoverGate atomic.Bool
}

func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{byID: map[string]*device.Session{}}
}

// LockDiscovery acquires the single-discovery gate; false if held.
func (r *SessionRegistry) LockDiscovery() bool { return r.discoverGate.CompareAndSwap(false, true) }

// UnlockDiscovery releases the gate.
func (r *SessionRegistry) UnlockDiscovery() { r.discoverGate.Store(false) }

// IsDiscovering reports whether a discovery pass is in flight.
func (r *SessionRegistry) IsDiscovering() bool { return r.discoverGate.Load() }

// Replace installs a new session set and closes every session of the old
// set (graceful: Close runs Detach, which persists driver state). A non-nil
// sessions slice stamps discoveredAt; Replace(nil) closes everything but
// keeps the timestamp (shutdown path).
func (r *SessionRegistry) Replace(sessions []*device.Session) {
	r.mu.Lock()
	old := r.ordered
	r.ordered = append([]*device.Session(nil), sessions...)
	r.byID = make(map[string]*device.Session, len(sessions))
	for _, s := range sessions {
		r.byID[s.ID()] = s
	}
	if sessions != nil {
		now := time.Now()
		r.discoveredAt = &now
	}
	r.mu.Unlock()
	// Close outside the lock: Close blocks on graceful detach, which may do
	// serial I/O (pump safety stop).
	for _, s := range old {
		s.Close()
	}
}

// CloseAll closes every session and empties the registry, preserving
// discoveredAt (used before a re-probe and at shutdown).
func (r *SessionRegistry) CloseAll() { r.removeAll() }

// DisconnectAll closes every session and returns how many were released.
func (r *SessionRegistry) DisconnectAll() int { return r.removeAll() }

func (r *SessionRegistry) removeAll() int {
	r.mu.Lock()
	old := r.ordered
	r.ordered = nil
	r.byID = map[string]*device.Session{}
	r.mu.Unlock()
	for _, s := range old {
		s.Close()
	}
	return len(old)
}

// DisconnectByPort closes and removes the session holding the named port.
func (r *SessionRegistry) DisconnectByPort(port string) bool {
	r.mu.Lock()
	var victim *device.Session
	for i, s := range r.ordered {
		if s.PortName() == port {
			victim = s
			r.ordered = append(r.ordered[:i:i], r.ordered[i+1:]...)
			delete(r.byID, s.ID())
			break
		}
	}
	r.mu.Unlock()
	if victim == nil {
		return false
	}
	victim.Close()
	return true
}

// Get returns the session with the given device ID.
func (r *SessionRegistry) Get(id string) (*device.Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byID[id]
	return s, ok
}

// List returns the sessions in Replace order.
func (r *SessionRegistry) List() []*device.Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]*device.Session(nil), r.ordered...)
}

// HasPort returns the ID of the session holding the named port, if any.
func (r *SessionRegistry) HasPort(name string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.ordered {
		if s.PortName() == name {
			return s.ID(), true
		}
	}
	return "", false
}

// DiscoveredAt returns the time of the last discovery, or nil if never run.
func (r *SessionRegistry) DiscoveredAt() *time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.discoveredAt == nil {
		return nil
	}
	t := *r.discoveredAt
	return &t
}
```

- [ ] **Step 4: Run** — `go test -race -count=1 ./internal/registry/` → PASS (new + old registry tests).

- [ ] **Step 5: Commit**

```bash
git add internal/registry/sessions.go internal/registry/sessions_test.go
git commit -m "feat: add session registry alongside legacy device registry"
```

---

### Task 6: THE CUTOVER — `/api/v1` surface, deletions, registry rename, app rewiring

The atomic task: everything below only compiles together. Order of operations inside the task keeps the diff reviewable; run the full test suite only at the end.

**Files:**
- Delete: `internal/api/raw_serial.go`, `internal/api/raw_serial_test.go`, `internal/registry/registry.go`, `internal/registry/registry_test.go`
- Create: `internal/api/v1.go`, `internal/api/v1_test.go`
- Modify: `internal/api/handlers.go` (Server/New/Handler; delete old device handlers), `internal/api/types.go`, `internal/api/handlers_test.go` (rewrite), `internal/api/flash_test.go`, `internal/api/handlers_power_test.go`, `internal/api/log_middleware_test.go` (constructor/route updates)
- Modify: `internal/registry/sessions.go` + `sessions_test.go` (rename `SessionRegistry`→`Registry`, `NewSessionRegistry`→`New`; `git mv` to `registry.go`/`registry_test.go`)
- Modify: `internal/discovery/runner.go` + `runner_test.go` (delete legacy `Run` wrapper; rename `RunMatches`→`Run`; drop the `registry` import)
- Modify: `internal/app/app.go` (driver registration, session-building discover fn, shutdown)
- Modify: `internal/device/registry.go:17` (comment: `GET /devices` → `GET /api/v1/devices`)

**Interfaces:**
- Consumes: Task 1–5 outputs (`WaitFirstAttach`, `HasActiveJob`, `DeviceStateDir`, `Match`, `SessionRegistry`), `device.LookupDriver`, `pump.Register`/`densitometer.Register`/`valve.Register` (`internal/device/pump/pump.go:37`, `densitometer/densitometer.go:132`, `valve/valve.go:83`).
- Produces: the final public HTTP surface; `api.New(reg *registry.Registry, discover DiscoverFn, opener serial.Opener, fl flasher.Flasher, flashingEnabled bool, keepAwake power.KeepAwake)`; `type DiscoverFn func(ctx context.Context) ([]*device.Session, error)`.

- [ ] **Step 1: Registry rename**

```bash
git rm internal/registry/registry.go internal/registry/registry_test.go
```

In `sessions.go`/`sessions_test.go`: rename `SessionRegistry` → `Registry`, `NewSessionRegistry` → `New` (plain find-replace; also update the doc comments). Then:

```bash
git mv internal/registry/sessions.go internal/registry/registry.go
git mv internal/registry/sessions_test.go internal/registry/registry_test.go
```

`go build ./internal/registry/` must pass; the rest of the repo is now broken until Step 6 — expected.

- [ ] **Step 2: Discovery rename**

In `internal/discovery/runner.go`: delete the legacy `Run` wrapper (added in Task 4), rename `RunMatches` → `Run`, delete the now-unused `registry` import. Update `runner_test.go`: the old `Run` tests become `Run`-returns-`[]Match` tests (merge with the Task 4 test — keep coverage of: multiple devices sorted ordinal IDs, non-matching port closed, open-failure skipped). `go test -race -count=1 ./internal/discovery/` must pass.

- [ ] **Step 3: API types — rewrite `internal/api/types.go`**

Delete `CommandRequest`, `CommandResponse`, `PortDTO`, `PortsResponse`, and the old `DeviceDTO`. Keep `ErrorBody`, `DetailedPortDTO`, `DetailedPortsResponse`, `DisconnectResponse`, and all Flash types. New device DTOs:

```go
// DeviceDTO is one entry in the /api/v1 device list (spec §4).
type DeviceDTO struct {
	ID        string       `json:"id"`
	Type      string       `json:"type"` // hub type name; the valve registers as "valve" while its identify.device_type is "distribution_valve"
	Port      string       `json:"port"`
	Connected bool         `json:"connected"`
	Identify  *device.Info `json:"identify"` // null until Attach succeeds
}

// DevicesResponse is the body of GET /api/v1/devices and POST /api/v1/discover.
type DevicesResponse struct {
	Devices      []DeviceDTO `json:"devices"`
	DiscoveredAt *time.Time  `json:"discovered_at"`
}
```

- [ ] **Step 4: API handlers**

**`internal/api/handlers.go`** — new Server/New/Handler; delete `handleGetDevices`, `handlePostDiscover`, `handlePostCommand`, `executeCommand`, `tryReconnect`, `errIdentityChanged`, `probeAdapter`, `cmdParams`, `parseCmdParams`, `parseCommandBody`, `bytesToInts`, `toDTOs`, `maxCommandBodyBytes`, `maxCommandLen`. Keep `portSettleDelay`, `handleGetAgentInfo`, the three power handlers, `keepAwakeStatusBody`.

```go
// DiscoverFn probes ports and returns started device sessions; wired to
// discovery.Run + device.LookupDriver by the app.
type DiscoverFn func(ctx context.Context) ([]*device.Session, error)

type Server struct {
	reg             *registry.Registry
	discover        DiscoverFn
	opener          labserial.Opener
	flasher         flasher.Flasher
	flashingEnabled bool
	keepAwake       power.KeepAwake
}

func New(
	reg *registry.Registry,
	discover DiscoverFn,
	opener labserial.Opener,
	fl flasher.Flasher,
	flashingEnabled bool,
	keepAwake power.KeepAwake,
) *Server {
	return &Server{
		reg: reg, discover: discover, opener: opener,
		flasher: fl, flashingEnabled: flashingEnabled, keepAwake: keepAwake,
	}
}

// Handler returns the HTTP routing table. Device control lives under
// /api/v1; infra routes keep their original paths (external contracts).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/devices", s.handleV1Devices)
	mux.HandleFunc("POST /api/v1/discover", s.handleV1Discover)
	mux.HandleFunc("POST /api/v1/devices/{id}/command", s.handleV1Command)
	mux.HandleFunc("POST /devices/disconnect", s.handlePostDevicesDisconnect)
	mux.HandleFunc("GET /serial/ports/detailed", s.handleGetSerialPortsDetailed)
	mux.HandleFunc("POST /flash/{port}", s.handlePostFlashPort)
	mux.HandleFunc("GET /agent/info", s.handleGetAgentInfo)
	mux.HandleFunc("GET /power/keep-awake", s.handleGetKeepAwake)
	mux.HandleFunc("POST /power/keep-awake/enable", s.handlePostKeepAwakeEnable)
	mux.HandleFunc("POST /power/keep-awake/disable", s.handlePostKeepAwakeDisable)
	return logMiddleware(mux)
}
```

**Create `internal/api/v1.go`:**

```go
package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// maxEnvelopeBytes caps the JSON body of POST /api/v1/devices/{id}/command.
const maxEnvelopeBytes = 32 * 1024

func (s *Server) handleV1Devices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.deviceList())
}

func (s *Server) deviceList() DevicesResponse {
	sessions := s.reg.List()
	out := DevicesResponse{
		Devices:      make([]DeviceDTO, 0, len(sessions)),
		DiscoveredAt: s.reg.DiscoveredAt(),
	}
	for _, sess := range sessions {
		dto := DeviceDTO{
			ID:        sess.ID(),
			Type:      sess.TypeName(),
			Port:      sess.PortName(),
			Connected: sess.Connected(),
		}
		if info, ok := sess.CachedInfo(); ok {
			dto.Identify = &info
		}
		out.Devices = append(out.Devices, dto)
	}
	return out
}

func (s *Server) handleV1Discover(w http.ResponseWriter, r *http.Request) {
	if !s.reg.LockDiscovery() {
		writeError(w, http.StatusConflict, "discovery in progress", "")
		return
	}
	defer s.reg.UnlockDiscovery()
	for _, sess := range s.reg.List() {
		if sess.HasActiveJob() {
			writeError(w, http.StatusConflict, "job in progress",
				sess.ID()+" has an active job; stop it before re-discovering")
			return
		}
	}
	s.reg.CloseAll()
	time.Sleep(portSettleDelay)
	sessions, err := s.discover(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "discovery failed", err.Error())
		return
	}
	s.reg.Replace(sessions)
	// Wait out each session's initial attach attempt so the response
	// reflects real attach outcomes instead of transient connected=false.
	// The attaches run concurrently on their own session goroutines, so
	// this sequential wait costs one slowest-attach, not a sum.
	for _, sess := range sessions {
		sess.WaitFirstAttach(r.Context())
	}
	writeJSON(w, http.StatusOK, s.deviceList())
}

func (s *Server) handleV1Command(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeEnvelope(w, r)
	if !ok {
		return
	}
	sess, found := s.reg.Get(r.PathValue("id"))
	if !found {
		writeJSON(w, http.StatusNotFound, device.Err(req.ID, &device.CmdError{
			Code:    device.CodeUnknownDevice,
			Message: "no device with id " + r.PathValue("id"),
		}))
		return
	}
	resp := sess.Execute(r.Context(), req)
	writeJSON(w, httpStatusFor(resp), resp)
}

// decodeEnvelope parses and validates the command envelope. On failure it
// writes the 400 invalid_request response itself and returns ok=false.
func decodeEnvelope(w http.ResponseWriter, r *http.Request) (device.Request, bool) {
	var req device.Request
	body := http.MaxBytesReader(w, r.Body, maxEnvelopeBytes)
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, device.Err(req.ID, &device.CmdError{
			Code:    device.CodeInvalidRequest,
			Message: "body is not a valid command envelope: " + err.Error(),
		}))
		return device.Request{}, false
	}
	if req.ID == "" || req.Cmd == "" {
		writeJSON(w, http.StatusBadRequest, device.Err(req.ID, &device.CmdError{
			Code:    device.CodeInvalidRequest,
			Message: `"id" and "cmd" are required`,
		}))
		return device.Request{}, false
	}
	return req, true
}

// httpStatusFor mirrors the envelope outcome as an HTTP status (spec §4).
// Device-decided outcomes are 200; hub-level unreachability is 503.
func httpStatusFor(resp device.Response) int {
	if resp.Error != nil && resp.Error.Code == device.CodeDeviceUnreachable {
		return http.StatusServiceUnavailable
	}
	return http.StatusOK
}
```

Delete `internal/api/raw_serial.go` and `internal/api/raw_serial_test.go` (`git rm`). `flash.go` compiles unchanged — the new registry deliberately keeps `List`/`IsDiscovering`/`DisconnectAll`/`DisconnectByPort`/`HasPort` signatures.

- [ ] **Step 5: API tests**

**Create `internal/api/v1_test.go`** with the shared fake-driver infra:

```go
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/power"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// fakeDriver is the spec-§6 test driver: no device logic, scriptable Execute.
type fakeDriver struct {
	s         *device.Session
	attachErr error
	exec      func(cmd string, params json.RawMessage) (any, *device.CmdError)
}

func (d *fakeDriver) Attach(ctx context.Context, probeReply []byte) (device.Info, error) {
	if d.attachErr != nil {
		return device.Info{}, d.attachErr
	}
	return device.Info{DeviceType: "fake-device", Model: "fake-1",
		FirmwareVersion: "legacy", ProtocolVersion: "1.0"}, nil
}
func (d *fakeDriver) Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *device.CmdError) {
	if d.exec != nil {
		return d.exec(cmd, params)
	}
	return nil, device.ErrUnknownCommand(cmd)
}
func (d *fakeDriver) Tick(now time.Time) {}
func (d *fakeDriver) Detach()            {}

// newFakeSession starts a session hosting drv under test type code 240.
func newFakeSession(t *testing.T, id string, drv *fakeDriver) *device.Session {
	t.Helper()
	port := serial.NewFakePort("TEST-" + id)
	opener := serial.NewFakeOpener()
	opener.Add(port)
	conn, err := opener.Open(port.Name())
	if err != nil {
		t.Fatal(err)
	}
	s := device.NewSession(device.SessionConfig{
		ID: id, Type: "fake", TypeCode: 240, PortName: port.Name(),
		Conn: conn, Opener: opener, StateDir: t.TempDir(),
		Factory: func(sess *device.Session) device.Driver { drv.s = sess; return drv },
		Reprobe: func(serial.Port) ([]byte, error) { return nil, errors.New("no reprobe in tests") },
	})
	s.Start(context.Background())
	s.WaitFirstAttach(context.Background())
	t.Cleanup(s.Close)
	return s
}

func newV1Server(t *testing.T, reg *registry.Registry, disc DiscoverFn) http.Handler {
	t.Helper()
	ka, err := power.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ka.Close() })
	return New(reg, disc, serial.NewFakeOpener(), nil, false, ka).Handler()
}

func postEnvelope(t *testing.T, srv http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}
```

Then the tests (write each in full; expected shapes given):

1. `TestV1DevicesEmpty` — fresh `registry.New()`; GET `/api/v1/devices` → 200; raw body contains `"devices":[]` (NOT `null`) and `"discovered_at":null`.
2. `TestV1DevicesListsSessions` — registry with one attached fake session (`fake_1`) and one never-attached (`attachErr` set, `fake_2`); GET → 200; decode into `DevicesResponse`; `fake_1` has `Connected: true`, `Identify.DeviceType == "fake-device"`; `fake_2` has `Connected: false`, `Identify == nil`; `DiscoveredAt != nil` (Replace stamped it).
3. `TestV1CommandOK` — driver `exec` returns `map[string]any{"echo": cmd}`; POST `{"id":"r1","cmd":"ping"}` → 200; envelope `id=="r1"`, `status=="ok"`, `result.echo=="ping"`.
4. `TestV1CommandDriverErrorIs200` — driver returns `device.ErrInvalidParams("volume_ml", -1, "must be positive")`; POST → HTTP 200 with `status=="error"`, `error.code=="invalid_params"` (device-decided outcomes are 200).
5. `TestV1CommandUnknownDeviceIs404` — empty registry; POST to `/api/v1/devices/nope/command` with valid envelope `{"id":"r9","cmd":"ping"}` → 404; body is an envelope: `status=="error"`, `error.code=="unknown_device"`, `id=="r9"`.
6. `TestV1CommandUnreachableIs503` — never-attached session; POST `{"id":"r2","cmd":"status"}` → 503, `error.code=="device_unreachable"`. Also `{"id":"r3","cmd":"identify"}` → 503 (never attached ⇒ no cached info). And `{"id":"r4","cmd":"get_job","params":{"job_id":"j-1"}}` → **200** with `error.code=="invalid_params"` (memory-served).
7. `TestV1CommandMalformedBodyIs400` — body `"{"` → 400, envelope `error.code=="invalid_request"`. Body `{}` → 400 (missing id+cmd). Body `{"id":"x"}` → 400 (missing cmd).
8. `TestV1DiscoverReplacesSessions` — seed registry with session A (driver has a `detached atomic.Bool` — extend `fakeDriver` with it, set in `Detach()`); `disc` returns a fresh session B built inside the DiscoverFn closure (build it with `newFakeSession` semantics but WITHOUT `t.Cleanup(s.Close)` double-close concerns — Close is idempotent, keep the cleanup); POST `/api/v1/discover` → 200; A's driver `detached` true; response lists only B with `connected:true`.
9. `TestV1DiscoverBusyIs409` — `disc` blocks on a channel; fire first discover in a goroutine, wait until `reg.IsDiscovering()`, second discover → 409 `{"error":"discovery in progress"}`; release channel, join goroutine. (Mirror the old `handlers_test.go` discovery-conflict test structure — read it before deleting.)
10. `TestV1DiscoverActiveJobIs409` — session whose driver `exec` on `"start"` calls `d.s.Jobs().Start("work", time.Minute)`; POST command `start`; then POST discover → 409 `{"error":"job in progress"}`; registry still lists the session (not cleared).
11. `TestV1DiscoverErrorIs500` — `disc` returns `errors.New("boom")` → 500 `{"error":"discovery failed","detail":"boom"}`.

**Rewrite `internal/api/handlers_test.go`:** delete everything covering the removed handlers/helpers (`newTestServer`, `fakeDiscoverFn`, `makeFakeDevice`, `postCmd`, all `/devices`+`/discover`+`/devices/{id}/command` tests, the parse helpers' tests). Keep `decode()` if other files use it. What remains: possibly nothing but shared helpers — if the file becomes empty, delete it and move any still-used helper into `v1_test.go`.

**Update `internal/api/flash_test.go`:** `newTestServerForFlash`/`newTestServerWithFlash` drop the `rawSerialEnabled` constructor arg (`New(reg, disc, opener, fl, enabled, ka)`); the "registry not empty → 409" test seeds via `reg.Replace([]*device.Session{newFakeSession(t, "fake_1", &fakeDriver{})})` instead of a `registry.Device`.

**Update `internal/api/handlers_power_test.go`:** constructor arg removal only.

**Update `internal/api/log_middleware_test.go`:** `/devices` → `/api/v1/devices` (route string + logged-route assertion).

- [ ] **Step 6: App wiring — `internal/app/app.go`**

Read the current file first. Changes:

(a) Imports: add `internal/device`, `internal/device/pump`, `internal/device/densitometer`, `internal/device/valve`, `internal/paths` (already imported for BackupsDir — verify).

(b) At the top of `Run`, before the registry is built:

```go
	// Driver registration happens here, at wiring time — never in init()
	// (spec §2.4), so tests can register fakes under unused type codes.
	pump.Register()
	densitometer.Register()
	valve.Register()
```

(c) Replace the `discoverFn` block (`app.go:54-62`) with:

```go
	stateDir := paths.DeviceStateDir()
	if stateDir == "" {
		slog.Warn("device state: no data dir available; state files will land in the working directory")
	}
	// reprobe re-identifies a device during background reattach. Opening a
	// port pulses DTR and reboots Arduino-class boards (see
	// discovery.PostOpenSettle) — a reattach reopens the port, so probing
	// before the settle would hit the bootloader window on every retry.
	reprobe := func(p labserial.Port) ([]byte, error) {
		time.Sleep(discovery.PostOpenSettle)
		reply, _, err := discovery.Probe(p)
		return reply, err
	}
	discoverFn := func(reqCtx context.Context) ([]*device.Session, error) {
		all, err := opener.List()
		if err != nil {
			return nil, fmt.Errorf("list ports: %w", err)
		}
		ports := discovery.FilterPorts(all, include, exclude)
		slog.Info("discovery: starting", "candidates", ports)
		matches, err := discovery.Run(reqCtx, opener, ports)
		if err != nil {
			return nil, err
		}
		sessions := make([]*device.Session, 0, len(matches))
		for _, m := range matches {
			name, factory, ok := device.LookupDriver(m.TypeCode)
			if !ok {
				// The probe classified it, so a missing factory is a wiring bug.
				slog.Error("discovery: no driver registered", "type_code", int(m.TypeCode), "port", m.Port)
				_ = m.Conn.Close()
				continue
			}
			sess := device.NewSession(device.SessionConfig{
				ID: m.ID, Type: name, TypeCode: m.TypeCode, PortName: m.Port,
				Conn: m.Conn, Opener: opener, StateDir: stateDir,
				Factory: factory, ProbeReply: m.Reply, Reprobe: reprobe,
			})
			sess.Start(ctx) // app-lifetime ctx: sessions die on shutdown
			sessions = append(sessions, sess)
		}
		return sessions, nil
	}
```

`ctx` here must be the cancellable app context used by the rest of `Run` (the one `cancel()` acts on) — check what the surrounding code names it.

(d) Constructor call (`app.go:85`): `srv := api.New(reg, discoverFn, opener, fl, flashingEnabled, keepAwake)` — `cfg.RawSerial.Enabled` argument gone.

(e) Shutdown (`app.go:125`): replace `reg.Replace(nil)` with `reg.CloseAll()` and update its comment: sessions detach gracefully (drivers persist state), then ports close.

(f) `internal/device/registry.go:17`: update the comment's `GET /devices` reference to `GET /api/v1/devices`.

- [ ] **Step 7: Build, test, lint the world**

```bash
go build ./... && gofmt -l . && go vet ./...
go test -race -count=1 ./...
golangci-lint run
```
Expected: all pass, gofmt prints nothing. Panel Go tests (`internal/panel`) still pass — `servicecli.go` paths change in Task 8, and until then its tests still point at old paths against its own test server (self-consistent). Fix anything that falls out; `handlers_test.go`-adjacent compile errors mean a missed deletion.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "feat: cut HTTP device API over to /api/v1 JSON protocol"
```

---

### Task 7: Delete the `raw_serial` config field

Safe for deployed configs: `config.Load` uses non-strict `yaml.Unmarshal` (`internal/config/load.go:21`), so a stale `raw_serial:` key in an existing YAML is ignored on load and dropped on the panel's next save.

**Files:**
- Modify: `internal/config/config.go` (drop `Config.RawSerial` at line 13, `RawSerialConfig` at 38-40, default at 66, scaffold lines 98-101)
- Modify: `internal/config/testdata/scaffold.golden.yaml` (drop lines 27-30)
- Modify: `internal/config/*_test.go` (any raw_serial references — grep)
- Modify: `internal/panel/frontend/src/tabs/ConfigTab.tsx` (type at line 22, section at 438-452, dirty-label at 584)
- Modify: `internal/panel/frontend/src/tabs/ConfigTab.test.tsx:23`, `internal/panel/frontend/src/preview-shim/seed.ts:20,30`
- Modify: `docs/configuration.md` (delete "Enable raw serial commands" section at ~66-72 and the `### raw_serial` table at ~119-121)

**Interfaces:** none new; pure deletion. Task 6 already removed the only production reader.

- [ ] **Step 1: Go side** — remove the field, type, default, and scaffold lines; run `grep -rn "raw_serial\|RawSerial" internal/config/` to catch test references; update the golden file to match the new scaffold exactly. Run: `go test -race -count=1 ./internal/config/` → PASS.
- [ ] **Step 2: Panel side** — remove the `raw_serial` member from the config type, the "Raw serial" `<Section>`, the dirty-label row, and the two seed entries. Run: `npm test --prefix internal/panel/frontend` → PASS.
- [ ] **Step 3: docs/configuration.md** — delete the two sections; check the doc's intro/index doesn't reference them.
- [ ] **Step 4: Repo-wide grep** — `grep -rn "raw_serial" --include='*.go' --include='*.ts' --include='*.tsx' --include='*.yaml' . | grep -v worktrees | grep -v docs/superpowers` → only historical plans/specs remain (they are dated records; leave them).
- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat: remove raw_serial config option"
```

---### Task 8: Panel bindings to `/api/v1`

**Files:**
- Modify: `internal/panel/servicecli.go:98-110` (paths), comments
- Modify: `internal/panel/servicecli_test.go:39-40,98` (path assertions)
- Modify: `internal/panel/frontend/src/tabs/DevicesTab.tsx` (DTO interface + Connected column)
- Modify: `internal/panel/frontend/src/preview-shim/bindings.ts:53,58` (mock shapes)

**Interfaces:**
- Consumes: `api.DevicesResponse` (new shape) — the Go binding already round-trips it typed.

- [ ] **Step 1: Update servicecli_test.go expectations first** — `r.URL.Path == "/devices"` → `"/api/v1/devices"`; the discover assertion → `"/api/v1/discover"`. Run `go test -count=1 ./internal/panel/ -run TestServiceCli` (check actual test names) → FAIL.
- [ ] **Step 2: Update `servicecli.go`** — `GetDevices` does `c.do(ctx, "GET", "/api/v1/devices", &out)`; `Discover` does `c.do(ctx, "POST", "/api/v1/discover", &out)`; update the two "proxies …" doc comments. `DisconnectAll`/`DisconnectPort`/`GetPorts` are untouched (infra routes). Run the panel Go tests → PASS.
- [ ] **Step 3: Frontend DTO + rendering** — in `DevicesTab.tsx` replace the `DeviceDTO` interface and add a Connected column:

```ts
interface DeviceDTO {
  id: string;
  type: string;
  port: string;
  connected: boolean;
  identify: {
    device_type: string;
    model: string;
    serial?: string;
    firmware_version: string;
    protocol_version: string;
    capabilities: unknown;
  } | null;
}
```

Table: add a `<th style={{ width: "15%" }}>Connected</th>` after Port (shrink the other widths to 25/25/25) and a `<td>{d.connected ? "yes" : "no"}</td>` in the row. No other rendering changes.

- [ ] **Step 4: Preview-shim mocks** — update the mocked `GetDevices`/`Discover` device entries in `preview-shim/bindings.ts`: drop `type_code`, add `connected: true` and an `identify` object matching the interface (one entry may use `connected: false, identify: null` to exercise the column).
- [ ] **Step 5: Frontend checks** — `npm test --prefix internal/panel/frontend` and `npm run lint --prefix internal/panel/frontend` → PASS. Also `npm run build --prefix internal/panel/frontend` (runs `tsc --noEmit`) to catch type errors.
- [ ] **Step 6: Commit**

```bash
git add internal/panel
git commit -m "feat: move panel device bindings to /api/v1"
```

---

### Task 9: Valve homed-state discover-rebuild integration test

Recovery-gate test the valve PR's final review asked for: homed state must round-trip the registry's Close-then-rebuild flow (Detach persists `{physical_position, device_belief_at_shutdown}`, fresh Attach recovers them only when the live counter matches). Lives in `internal/registry` (NOT `internal/api` — spec §6 keeps real drivers out of API tests) and mirrors the production wiring: `valve.Register()` + `device.LookupDriver`.

**Files:**
- Create: `internal/registry/valve_roundtrip_test.go`

**Interfaces:**
- Consumes: `valve.Register`/`valve.TypeCode` (`internal/device/valve/valve.go:35,83`), `device.LookupDriver`, registry `Replace`/`CloseAll`. Frame bytes mirror `internal/device/valve/valve_test.go:46-74` and `home_test.go:12-20,75-86` — read both before writing.

- [ ] **Step 1: Write the test**

```go
package registry_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/valve"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// buildValveSession mirrors the app's discover wiring: factory via
// LookupDriver, probe reply from the fake device, shared state dir.
func buildValveSession(t *testing.T, opener *serial.FakeOpener, portName, stateDir string) *device.Session {
	t.Helper()
	name, factory, ok := device.LookupDriver(valve.TypeCode)
	if !ok {
		t.Fatal("valve driver not registered")
	}
	conn, err := opener.Open(portName)
	if err != nil {
		t.Fatal(err)
	}
	s := device.NewSession(device.SessionConfig{
		ID: "valve_1", Type: name, TypeCode: valve.TypeCode, PortName: portName,
		Conn: conn, Opener: opener, Clock: device.NewFakeClock(time.Unix(1000, 0)),
		StateDir: stateDir, Factory: factory,
		ProbeReply: []byte{30, 1, 1, 6}, // radial-6 build
		Reprobe:    func(p serial.Port) ([]byte, error) { return []byte{30, 1, 1, 6}, nil },
	})
	s.Start(context.Background())
	s.WaitFirstAttach(context.Background())
	return s
}

func exec(t *testing.T, s *device.Session, cmd, params string) device.Response {
	t.Helper()
	req := device.Request{ID: "t-" + cmd, Cmd: cmd}
	if params != "" {
		req.Params = json.RawMessage(params)
	}
	return s.Execute(context.Background(), req)
}

func resultMap(t *testing.T, resp device.Response) map[string]any {
	t.Helper()
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestValveHomedStateSurvivesDiscoverRebuild drives the production
// re-discovery flow end to end: CloseAll detaches the session (the driver
// persists homed state), a fresh session attaches on the same port + state
// dir (as a re-probe of the same device would), and the recovered state is
// visible through the new session.
func TestValveHomedStateSurvivesDiscoverRebuild(t *testing.T) {
	oldPB, oldDW := device.PerByteTimeout, device.DrainWindow
	device.PerByteTimeout, device.DrainWindow = 10*time.Millisecond, 0
	t.Cleanup(func() { device.PerByteTimeout, device.DrainWindow = oldPB, oldDW })

	valve.Register()
	stateDir := t.TempDir()
	port := serial.NewFakePort("COM9")
	opener := serial.NewFakeOpener()
	opener.Add(port)
	reg := registry.New()

	// Session 1: attach (position query answers counter 0), home at 4.
	port.Feed([]byte{30, 1, 1, 0}) // Attach's position-query reply
	s1 := buildValveSession(t, opener, "COM9", stateDir)
	reg.Replace([]*device.Session{s1})
	if !s1.Connected() {
		t.Fatal("session 1 must attach")
	}
	port.Feed([]byte{30, 1, 1, 0}) // home's belief-resync reply
	if resp := exec(t, s1, "home", `{"position":4}`); resp.Status != "ok" {
		t.Fatalf("home: %+v", resp)
	}

	// Re-discovery: CloseAll persists via Detach; rebuild on same port+dir.
	reg.CloseAll()
	port.Feed([]byte{30, 1, 1, 0}) // new Attach's position query: counter unchanged
	s2 := buildValveSession(t, opener, "COM9", stateDir)
	reg.Replace([]*device.Session{s2})
	t.Cleanup(reg.CloseAll)
	if !s2.Connected() {
		t.Fatal("session 2 must attach")
	}

	port.Feed([]byte{30, 1, 1, 0}) // status's idle CHECK_BELIEF reply
	sm := resultMap(t, exec(t, s2, "status", ""))
	if sm["state"] != "idle" || sm["homed"] != true || sm["position"] != 4.0 {
		t.Fatalf("homed state must survive the rebuild: %v", fmt.Sprintf("%v", sm))
	}
}
```

Before running, verify the fed frames against the current `valve_test.go` fixture (`newFixture`, `newHomedFixture`) — if the driver's attach sequence changed since this plan was written, mirror the fixture's exact `Feed` calls.

- [ ] **Step 2: Run** — `go test -race -count=1 ./internal/registry/ -run TestValveHomedStateSurvivesDiscoverRebuild` → PASS. If it fails on frames, diff against `valve_test.go`'s fixture feeds; do NOT change driver code to make it pass.
- [ ] **Step 3: Commit**

```bash
git add internal/registry/valve_roundtrip_test.go
git commit -m "test: valve homed state survives a discover rebuild"
```

---

### Task 10: Completion-window audit backstop — API-level serialization test

**Audit conclusion (verified 2026-07-06, goes in the PR body):** the API layer cannot inject serial traffic into any driver's completion window, because the session mailbox serializes every command with attach/timer/watcher callbacks on one goroutine, and each driver guards its own window on top of that:
- **Densitometer** — `serialGate()` (`densitometer.go:286-295`) rejects port-touching commands while `d.sweep != nil` **or** `now < busy_until`; the `sweep != nil` half covers the post-`busy_until` completion After-chain (the PR-3 review fix). Covered by `TestStatusDuringSlowSweepStaysBusy` (`sweep_test.go:232`).
- **Pump** — during an opcode-18 watch, every mailbox command is memory-served (`ping`/`status`), busy-rejected (motion), or write-only (`pause`/`resume`/`stop`); `Session.Transact` refuses reply-expecting transactions outright while the reader is held (`ErrReaderHeld`, `session.go:297`). Covered by `watch_test.go:44,112`.
- **Valve** — `moveJob != nil` gates new motion/config until `verifyMove` (which runs loop-serialized via After→Post) clears it; mid-move `ping` is deliberate, side-effect-free, and never feeds belief while moving (`commands.go:34-36`). Covered by `move_failure_test.go:64,85`.

No gap found → no new driver tests. What IS added: an API-level test pinning the serialization guarantee the audit rests on (concurrent HTTP requests to one device never overlap in the driver).

**Files:**
- Modify: `internal/api/v1_test.go` (append)

- [ ] **Step 1: Write the test**

```go
// TestV1CommandsSerializePerSession pins the guarantee the driver
// completion-window guards rest on: concurrent HTTP requests to one device
// execute strictly one at a time on the session goroutine.
func TestV1CommandsSerializePerSession(t *testing.T) {
	var inFlight, maxSeen atomic.Int32
	drv := &fakeDriver{}
	drv.exec = func(cmd string, _ json.RawMessage) (any, *device.CmdError) {
		cur := inFlight.Add(1)
		for {
			m := maxSeen.Load()
			if cur <= m || maxSeen.CompareAndSwap(m, cur) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		return "done", nil
	}
	sess := newFakeSession(t, "fake_1", drv)
	reg := registry.New()
	reg.Replace([]*device.Session{sess})
	srv := newV1Server(t, reg, nil)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"id":"c%d","cmd":"work"}`, n)
			rec := postEnvelope(t, srv, "/api/v1/devices/fake_1/command", body)
			if rec.Code != http.StatusOK {
				t.Errorf("request %d: status %d", n, rec.Code)
			}
		}(i)
	}
	wg.Wait()
	if maxSeen.Load() != 1 {
		t.Fatalf("commands overlapped in the driver: max in-flight %d", maxSeen.Load())
	}
}
```

(Add the `fmt`, `sync`, `sync/atomic` imports.)

- [ ] **Step 2: Run** — `go test -race -count=1 ./internal/api/ -run TestV1CommandsSerializePerSession` → PASS.
- [ ] **Step 3: Commit**

```bash
git add internal/api/v1_test.go
git commit -m "test: pin per-session command serialization under concurrent API traffic"
```

---

### Task 11: Docs — README, SECURITY.md, python-client-brief

**Files:**
- Modify: `README.md:48-54` (panel tab prose), `README.md:60-158` (API sections), `README.md:214` (canonical-contract pointer), `README.md:46` ("enable raw serial or flashing" → "enable flashing")
- Modify: `SECURITY.md:31-37` (control-plane route list + the "No file system access" bullet)
- Modify: `docs/python-client-brief.md` (rewrite to v2)

- [ ] **Step 1: README panel prose** — `### Devices` (line 48-50): "Discovered devices with **type** (`pump` / `valve` / `densitometer`), **port**, and connection state. Per-row Disconnect releases that one port without tearing down the rest of the registry." `### Ports` (52-54): drop the raw-bytes sentence and the `raw_serial.enabled` mention; keep the descriptor/filter sentence.

- [ ] **Step 2: README REST API section** — replace lines 60-158 with:

````markdown
## REST API

The REST API is bound to `127.0.0.1` on the lab machine; it is reachable from outside **only** through the chisel reverse tunnel that the lab-bridge auth proxy fronts. All requests and responses are JSON. Infra endpoints report errors as `{ "error": "<short>", "detail": "<long>" }`; device commands use the envelope described below.

| Method | Path | Purpose | Gate |
| --- | --- | --- | --- |
| `POST` | `/api/v1/discover` | Re-probe ports, rebuild device sessions, return the new list | — |
| `GET`  | `/api/v1/devices` | Return the cached device list | — |
| `POST` | `/api/v1/devices/{id}/command` | Execute one JSON protocol command on a device | — |
| `POST` | `/devices/disconnect` | Release all device sessions; with `?port=<name>` release just that one | — |
| `GET`  | `/serial/ports/detailed` | List enumerated COM ports with USB descriptors | — |
| `POST` | `/flash/{port}` | Pre-backup → flash → byte-verify → optional test → auto-rollback | `flashing.enabled` |
| `GET`  | `/agent/info` | Agent self-description for server-pulled state | — |
| `GET`  | `/power/keep-awake` | Report keep-awake state | — |
| `POST` | `/power/keep-awake/enable` | Activate keep-awake (idempotent) | — |
| `POST` | `/power/keep-awake/disable` | Clear keep-awake (idempotent) | — |

Device types: `pump` (type code `10`), `valve` (`30`), `densitometer` (`70`). Device IDs are ordinal per type in `(type code, port)` order: `pump_1`, `valve_1`, … Note the valve's hub type name is `valve` while its `identify.device_type` is `distribution_valve`.

<details>
<summary><b>Devices &amp; commands</b> — <code>POST /api/v1/discover</code>, <code>GET /api/v1/devices</code>, <code>POST /api/v1/devices/{id}/command</code></summary>

`POST /api/v1/discover` closes every current device session (drivers persist their state), re-probes the candidate ports, and builds fresh sessions. `GET /api/v1/devices` serves the same list from cache.

```json
{
  "devices": [
    {
      "id": "pump_1", "type": "pump", "port": "COM7", "connected": true,
      "identify": {
        "device_type": "pump", "model": "peristaltic-1ch", "serial": "26-025",
        "firmware_version": "legacy", "protocol_version": "1.0", "capabilities": {}
      }
    }
  ],
  "discovered_at": "2026-07-06T12:34:56Z"
}
```

`identify` is `null` until the device's post-probe attach succeeds; a device that probed but failed to attach is listed with `"connected": false` and retried in the background.

`POST /api/v1/devices/{id}/command` executes one command. Body and response are the protocol envelope:

```json
{ "id": "req-1", "cmd": "dispense", "params": { "volume_ml": 5, "speed_pct": 60 } }
```

```json
{ "id": "req-1", "status": "ok", "result": { "job_id": "j-3", "state": "running" } }
```

The per-device command sets (envelope, error codes, job model) are documented canonically in [`docs/protocol_translation_docs/`](docs/protocol_translation_docs/) — one `JSON_PROTOCOL.md` per device type.

HTTP status mirrors the envelope outcome:

- Device-decided outcomes (`ok`, `busy`, `invalid_params`, `not_calibrated`, `not_homed`, `hardware_error`, `unknown_command`, `internal_error`) → **200** with the envelope.
- Unknown device id → **404**, envelope error `unknown_device`.
- Device unreachable → **503**, envelope error `device_unreachable`. Exception: `identify` (served from cache once a first attach has succeeded) and `get_job` (always served from the jobs engine — including a job that just failed with `hardware_error` when the device became unreachable mid-job) stay at **200**.
- Malformed body / missing `id` or `cmd` → **400**, envelope error `invalid_request`.
- `POST /api/v1/discover` while another discovery runs, or while any device has an active job → **409** with `{ "error": "...", "detail": "..." }`; stop jobs first.

</details>
````

Keep the existing Disconnect details block (retitle its intro from "serial handles" to "device sessions"), the Firmware flashing / Agent info / Keep-awake blocks unchanged, and replace the "Raw serial" details block with a short "Ports" block documenting only `GET /serial/ports/detailed` (reuse the existing JSON example). Update line 214's canonical-contract pointer to `docs/superpowers/specs/2026-07-05-json-device-protocol-design.md`. Update line 46: "…(rotate credentials, restrict discovery, enable flashing)…".

- [ ] **Step 3: SECURITY.md** — replace the three-route list (lines ~31-35): control plane is `GET /api/v1/devices`, `POST /api/v1/discover`, `POST /api/v1/devices/{id}/command` — "commands are JSON, validated per device protocol, and translated to fixed 5-byte frames on the serial port; raw byte passthrough no longer exists." Update the "No file system access" bullet: the binary's file I/O now also includes per-device state JSON files under its own data dir (`devicestate/`). Keep the handlers.go line reference accurate (re-check line numbers after Task 6).

- [ ] **Step 4: python-client-brief.md** — rewrite for v2. Keep the Connection section; replace Devices/Endpoints with: ID scheme (unchanged ordinals + valve naming note), the three `/api/v1` endpoints, envelope request/response with one worked example per outcome (ok, device-decided error at 200, 404/503/400/409 with bodies), the device-list DTO with `connected`/`identify`, memory-served `identify`/`get_job` semantics, job model pointer (`get_job` + job states + history of 8), and a pointer to `docs/protocol_translation_docs/<device>/JSON_PROTOCOL.md` for per-device commands. Delete everything about `{"command":[...]}` byte arrays, query params, and raw serial.

- [ ] **Step 5: Check for stragglers** — `grep -rn '"/devices"\|POST /discover\|/devices/{id}/command\|GET /serial/ports\b\|serial/ports/{port}' README.md SECURITY.md docs/*.md | grep -v superpowers` → nothing unexpected remains (historical plans/specs under docs/superpowers are dated records; leave them).

- [ ] **Step 6: Commit**

```bash
git add README.md SECURITY.md docs/python-client-brief.md docs/configuration.md
git commit -m "docs: document the /api/v1 JSON device protocol"
```

(`docs/configuration.md` was staged in Task 7 — include here only if it still has changes.)

---

### Task 12: Release mechanics

- [ ] **Step 1: Full pre-flight**

```bash
gofmt -l .                      # must print nothing
go vet ./...
golangci-lint run
go test -race -count=1 ./...
"$(go env GOPATH)/bin/govulncheck" ./...
npm test --prefix internal/panel/frontend
```
All must pass.

- [ ] **Step 2: BREAKING CHANGE hygiene** — the string must appear in NO branch commit:

```bash
git log origin/main..HEAD --format='%B' | grep -ci "breaking.change" || echo CLEAN
```
Expected: `CLEAN` (grep exits 1). If any commit message contains it, reword with `git commit --amend` / rebase before pushing.

- [ ] **Step 3: Verify no dead references** — `grep -rn 'rawSerialEnabled\|CommandRequest\|parseCmdParams' internal/ | grep -v worktrees` → empty.

- [ ] **Step 4: Push and open the PR**

```bash
git push -u origin feat/v2-api-cutover
gh pr create --title "feat!: replace raw-byte device API with per-device JSON protocol" --body "$(cat <<'EOF'
## Summary

PR 5 of 5 of the v2 JSON device protocol effort (spec: docs/superpowers/specs/2026-07-05-json-device-protocol-design.md §4/§7/§8).

- New surface: `GET /api/v1/devices`, `POST /api/v1/discover`, `POST /api/v1/devices/{id}/command` (command envelope in/out, HTTP status mirrors the envelope).
- Discovery builds `device.Session`s via the driver registry (pump/densitometer/valve registered at app wiring); attach failures list the device `connected:false` with background retry.
- `internal/registry` is now a session registry (same Replace/CloseAll semantics); re-discovery and shutdown detach sessions gracefully so driver state persists.
- Spec §3 amendment: `identify`/`get_job` are memory-served while a device is unreachable; everything else stays fail-fast 503.
- Panel bindings moved to `/api/v1`; README/SECURITY/client-brief rewritten.
- Completion-window audit: densitometer serialGate, pump reader-held discipline, and valve move-window guards verified against concurrent /command traffic (no gap; per-session serialization pinned by TestV1CommandsSerializePerSession).

## Test plan

- `go test -race -count=1 ./...` green on macOS and Windows CI.
- New: memory-served semantics tests (core), /api/v1 handler matrix (fake driver, spec §6), session-registry tests, valve homed-state discover-rebuild round-trip, API serialization test.

BREAKING CHANGE: the raw-byte device API is removed. Deleted endpoints: `GET /devices`, `POST /discover`, `POST /devices/{id}/command` (raw bytes), `GET /serial/ports`, `POST /serial/ports/{port}/command`. The `raw_serial.enabled` config option is gone (stale keys in existing configs are ignored). Device control now goes through `POST /api/v1/devices/{id}/command` with the per-device JSON protocol; the device list moved to `GET /api/v1/devices` with a new entry shape (`connected`, `identify`; `type_code` removed).
EOF
)"
```

- [ ] **Step 5: Watch CI** — both macOS and Windows legs of `verify` must pass, plus the semantic-PR title check (`feat!:` is valid for `amannn/action-semantic-pull-request@v6`). Report the PR URL and stop — do NOT merge; the human reviews and merges, which triggers release-please to cut the v2.0.0 release PR.

---

## Self-review notes

- **Spec §4 coverage:** routes/DTO/discovered_at (Task 6), status mapping incl. 409 (Task 6 tests 4-11), discovery lifecycle + connected:false on attach failure (Tasks 2+6), registry semantics (Tasks 5+6), deletions (Tasks 6+7), infra routes untouched (Task 6 Handler), flash precondition preserved (Task 6 flash_test update).
- **Scope item 3 (settled decision):** Task 1 (reorder + 3 semantics + PR-1 test update + spec §3/§4 text).
- **Scope item 4:** LookupDriver wiring, no init(), SessionConfig fields incl. Reprobe-with-settle, StateDir via paths, ordinal IDs preserved in discovery, valve naming split documented (Task 6 + Task 11).
- **Scope item 5:** decision = replace internal/registry (Task 5/6); valve round-trip (Task 9).
- **Cross-cutting audit:** Task 10 (conclusion + backstop test).
- **Release mechanics:** Task 12; no-BREAKING-in-commits guarded twice (global constraint + Step 2 grep).
- **Type consistency spot-checks:** `DiscoverFn` returns `[]*device.Session` (Task 6 handlers + app wiring + tests agree); registry method set used by flash.go unchanged (`List`/`IsDiscovering`/`DisconnectAll`/`DisconnectByPort`/`HasPort`); `WaitFirstAttach(ctx)` signature consistent across Tasks 2/5/6/9.
