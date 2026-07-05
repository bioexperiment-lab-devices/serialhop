# Device Core Runtime Implementation Plan (v2 PR 1 of 5)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `internal/device` — the shared core runtime (envelope, job engine, serial transaction discipline, persistent store, session actor, driver registry) that the pump/densitometer/valve drivers and the v2 API will plug into.

**Architecture:** Per the approved spec (`docs/superpowers/specs/2026-07-05-json-device-protocol-design.md`), everything the three JSON protocols share verbatim lives once in `internal/device`; device quirks live in later driver packages. The session is an actor: one goroutine per device owns the port and all driver state. This PR is pure library + tests — nothing consumes it yet, so it merges as a plain `feat:` with **no** `BREAKING CHANGE` footer.

**Tech Stack:** Go stdlib only (no new dependencies), existing `internal/serial` fakes for tests.

## Global Constraints

- Module path: `github.com/bioexperiment-lab-devices/serialhop`; work on branch `json-device-protocol-v2`.
- Pre-flight before the PR (CLAUDE.md): `gofmt -l .` prints nothing; `go vet ./...`; `golangci-lint run`; `go test -race -count=1 ./...`; `govulncheck ./...`.
- Tests: stdlib `testing` only, no testify. Must pass on macOS and Windows; no Windows-only code in this PR.
- gosec is enabled: file writes use `0o600`, directories `0o700`.
- Every value that clock-drives behavior goes through the injectable `Clock`; real-time is allowed only for serial I/O deadlines.
- Timing knobs (`PerByteTimeout`, `DrainWindow`, `HeartbeatInterval`, `ReattachBase`, `ReattachMax`) are package `var`s so tests can shrink them (repo precedent: `discovery.PostOpenSettle`).
- Canonical behavior source: `docs/protocol_translation_docs/*/TRANSLATION.md` §"Serial primitives" and §"Concurrency & recovery rules"; spec §2–§3.
- Commit messages end with `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.

---

### Task 1: Envelope and error codes

**Files:**
- Create: `internal/device/envelope.go`
- Test: `internal/device/envelope_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `Request{ID, Cmd string; Params json.RawMessage}`, `Response{ID, Status string; Result any; Error *CmdError}`, `CmdError{Code, Message string; Details any}` (implements `error`), constructors `OK(id string, result any) Response`, `Err(id string, e *CmdError) Response`, `ErrInvalidParams(param string, value any, msg string) *CmdError`, `ErrUnknownCommand(cmd string) *CmdError`, `ErrBusy(msg string, details any) *CmdError`, `ErrHardware(msg string) *CmdError`, `ErrInternal(msg string) *CmdError`, and the `Code*` constants listed below.

- [ ] **Step 1: Write the failing test**

```go
// internal/device/envelope_test.go
package device

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOKResponseShape(t *testing.T) {
	b, err := json.Marshal(OK("c9f3", map[string]any{"uptime_ms": 81}))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"id":"c9f3"`, `"status":"ok"`, `"uptime_ms":81`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, `"error"`) {
		t.Errorf("ok response must omit error: %s", s)
	}
}

func TestErrorResponseShape(t *testing.T) {
	e := ErrInvalidParams("volume_ml", -1, "volume_ml must be positive")
	b, err := json.Marshal(Err("c9f3", e))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"status":"error"`, `"code":"invalid_params"`,
		`"message":"volume_ml must be positive"`,
		`"param":"volume_ml"`, `"value":-1`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	if strings.Contains(s, `"result"`) {
		t.Errorf("error response must omit result: %s", s)
	}
}

func TestCmdErrorIsError(t *testing.T) {
	var err error = ErrHardware("device not responding")
	if got := err.Error(); got != "hardware_error: device not responding" {
		t.Errorf("Error() = %q", got)
	}
}

func TestRequestDecode(t *testing.T) {
	var r Request
	if err := json.Unmarshal([]byte(`{"id":"a","cmd":"dispense","params":{"volume_ml":10}}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.ID != "a" || r.Cmd != "dispense" || string(r.Params) != `{"volume_ml":10}` {
		t.Errorf("bad decode: %+v", r)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/device/ -run 'TestOK|TestError|TestCmdError|TestRequest' -v`
Expected: FAIL (package does not compile: undefined `OK`, `Err`, …)

- [ ] **Step 3: Write the implementation**

```go
// internal/device/envelope.go

// Package device implements the core runtime for SerialHop's high-level JSON
// device protocol: the shared request/response envelope, job model, serial
// transaction discipline, persistent state store, and the per-device session
// actor hosting a device-type driver.
// See docs/superpowers/specs/2026-07-05-json-device-protocol-design.md and
// docs/protocol_translation_docs/ for the per-device contracts.
package device

import "encoding/json"

// Shared error codes (JSON_PROTOCOL.md §2) plus hub-level codes (spec §4).
const (
	CodeInvalidRequest    = "invalid_request"
	CodeUnknownCommand    = "unknown_command"
	CodeInvalidParams     = "invalid_params"
	CodeBusy              = "busy"
	CodeNotCalibrated     = "not_calibrated"
	CodeNotHomed          = "not_homed"
	CodeHardwareError     = "hardware_error"
	CodeInternalError     = "internal_error"
	CodeUnknownDevice     = "unknown_device"
	CodeDeviceUnreachable = "device_unreachable"
)

// Request is the command envelope every device protocol shares.
type Request struct {
	ID     string          `json:"id"`
	Cmd    string          `json:"cmd"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the reply envelope. Exactly one of Result/Error is set.
type Response struct {
	ID     string    `json:"id"`
	Status string    `json:"status"` // "ok" | "error"
	Result any       `json:"result,omitempty"`
	Error  *CmdError `json:"error,omitempty"`
}

// CmdError is a protocol-level error with a stable code.
type CmdError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (e *CmdError) Error() string { return e.Code + ": " + e.Message }

func OK(id string, result any) Response {
	return Response{ID: id, Status: "ok", Result: result}
}

func Err(id string, e *CmdError) Response {
	return Response{ID: id, Status: "error", Error: e}
}

func ErrInvalidParams(param string, value any, msg string) *CmdError {
	return &CmdError{Code: CodeInvalidParams, Message: msg,
		Details: map[string]any{"param": param, "value": value}}
}

func ErrUnknownCommand(cmd string) *CmdError {
	return &CmdError{Code: CodeUnknownCommand, Message: "unknown command: " + cmd}
}

func ErrBusy(msg string, details any) *CmdError {
	return &CmdError{Code: CodeBusy, Message: msg, Details: details}
}

func ErrHardware(msg string) *CmdError {
	return &CmdError{Code: CodeHardwareError, Message: msg}
}

func ErrInternal(msg string) *CmdError {
	return &CmdError{Code: CodeInternalError, Message: msg}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/device/ -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/device/envelope.go internal/device/envelope_test.go
git commit -m "feat(device): add shared JSON envelope and error codes

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Injectable clock with fake

**Files:**
- Create: `internal/device/clock.go`
- Test: `internal/device/clock_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `Clock interface { Now() time.Time; After(d time.Duration) <-chan time.Time }`, `SystemClock() Clock`, `NewFakeClock(start time.Time) *FakeClock`, `(*FakeClock).Now()`, `(*FakeClock).After(d)`, `(*FakeClock).Advance(d time.Duration)`. `FakeClock` lives in a non-`_test` file: driver packages (later PRs) import it for their own tests, mirroring the `serial.FakePort` pattern.

- [ ] **Step 1: Write the failing test**

```go
// internal/device/clock_test.go
package device

import (
	"testing"
	"time"
)

func TestFakeClockAdvanceFiresDueTimers(t *testing.T) {
	c := NewFakeClock(time.Unix(1000, 0))
	ch := c.After(5 * time.Second)
	select {
	case <-ch:
		t.Fatal("timer fired before Advance")
	default:
	}
	c.Advance(3 * time.Second)
	select {
	case <-ch:
		t.Fatal("timer fired too early")
	default:
	}
	c.Advance(2 * time.Second)
	select {
	case at := <-ch:
		if !at.Equal(time.Unix(1005, 0)) {
			t.Errorf("fired at %v", at)
		}
	default:
		t.Fatal("timer did not fire at its due time")
	}
	if !c.Now().Equal(time.Unix(1005, 0)) {
		t.Errorf("Now() = %v", c.Now())
	}
}

func TestFakeClockNonPositiveAfterFiresImmediately(t *testing.T) {
	c := NewFakeClock(time.Unix(0, 0))
	select {
	case <-c.After(0):
	default:
		t.Fatal("After(0) must fire immediately")
	}
}

func TestSystemClockNow(t *testing.T) {
	before := time.Now()
	got := SystemClock().Now()
	if got.Before(before.Add(-time.Second)) || got.After(before.Add(time.Second)) {
		t.Errorf("SystemClock().Now() = %v, wall = %v", got, before)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/device/ -run TestFakeClock -v`
Expected: FAIL (undefined `NewFakeClock`, `SystemClock`)

- [ ] **Step 3: Write the implementation**

```go
// internal/device/clock.go
package device

import (
	"sync"
	"time"
)

// Clock abstracts time so every clock-driven behavior (job progress,
// completion timers, canaries, reattach backoff) is deterministic in tests.
// Serial I/O deadlines intentionally stay on real time.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type sysClock struct{}

func (sysClock) Now() time.Time                         { return time.Now() }
func (sysClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// SystemClock returns the real-time Clock.
func SystemClock() Clock { return sysClock{} }

// FakeClock is a manually advanced Clock for tests.
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func NewFakeClock(start time.Time) *FakeClock { return &FakeClock{now: start} }

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{at: c.now.Add(d), ch: ch})
	return ch
}

// Advance moves the clock forward and fires every timer due by the new time.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	remaining := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			remaining = append(remaining, w)
		}
	}
	c.waiters = remaining
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/device/ -race -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/device/clock.go internal/device/clock_test.go
git commit -m "feat(device): add injectable clock with deterministic fake

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Persistent state store

**Files:**
- Create: `internal/device/store.go`
- Test: `internal/device/store_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `NewStore(dir, key string) *Store`, `(*Store).Load(v any) (bool, error)` (false, nil when no state exists), `(*Store).Save(v any) error` (atomic: temp file + rename), `(*Store).Path() string`. Key is sanitized: every rune outside `[A-Za-z0-9._-]` becomes `_`.

- [ ] **Step 1: Write the failing test**

```go
// internal/device/store_test.go
package device

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeState struct {
	SchemaVersion int     `json:"schema_version"`
	MlPerStep     float64 `json:"ml_per_step"`
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir, "pump-26-025")
	var got fakeState
	found, err := st.Load(&got)
	if err != nil || found {
		t.Fatalf("Load on missing file: found=%v err=%v", found, err)
	}
	if err := st.Save(fakeState{SchemaVersion: 1, MlPerStep: 0.000424}); err != nil {
		t.Fatal(err)
	}
	found, err = st.Load(&got)
	if err != nil || !found {
		t.Fatalf("Load after Save: found=%v err=%v", found, err)
	}
	if got.MlPerStep != 0.000424 || got.SchemaVersion != 1 {
		t.Errorf("got %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "pump-26-025.json")); err != nil {
		t.Errorf("expected state file: %v", err)
	}
}

func TestStoreSaveOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir, "k")
	if err := st.Save(fakeState{SchemaVersion: 1, MlPerStep: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(fakeState{SchemaVersion: 1, MlPerStep: 2}); err != nil {
		t.Fatal(err)
	}
	var got fakeState
	if _, err := st.Load(&got); err != nil || got.MlPerStep != 2 {
		t.Fatalf("got %+v err %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestStoreKeySanitized(t *testing.T) {
	st := NewStore("/tmp/x", `valve-COM/7:b`)
	base := filepath.Base(st.Path())
	if strings.ContainsAny(base, `/\:`) || base != "valve-COM_7_b.json" {
		t.Errorf("path not sanitized: %s", st.Path())
	}
}

func TestStoreLoadCorruptFileErrors(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir, "bad")
	if err := os.WriteFile(st.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got fakeState
	if _, err := st.Load(&got); err == nil {
		t.Fatal("expected error on corrupt state file")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/device/ -run TestStore -v`
Expected: FAIL (undefined `NewStore`)

- [ ] **Step 3: Write the implementation**

```go
// internal/device/store.go
package device

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store persists one device's driver state as a single JSON file with
// atomic replace-on-save. Drivers own the schema and must include a
// schema_version field (spec §5).
type Store struct {
	path string
}

// NewStore builds a store at dir/<sanitized key>.json.
func NewStore(dir, key string) *Store {
	return &Store{path: filepath.Join(dir, sanitizeKey(key)+".json")}
}

func (st *Store) Path() string { return st.path }

// Load reads the state into v. Returns (false, nil) when no state exists.
func (st *Store) Load(v any) (bool, error) {
	data, err := os.ReadFile(st.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("device store read %s: %w", st.path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("device store decode %s: %w", st.path, err)
	}
	return true, nil
}

// Save writes v atomically: temp file in the same directory, then rename.
func (st *Store) Save(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("device store encode: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(st.path), 0o700); err != nil {
		return fmt.Errorf("device store mkdir: %w", err)
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("device store write: %w", err)
	}
	if err := os.Rename(tmp, st.path); err != nil {
		return fmt.Errorf("device store rename: %w", err)
	}
	return nil
}

func sanitizeKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/device/ -race -run TestStore -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/device/store.go internal/device/store_test.go
git commit -m "feat(device): add atomic per-device state store

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Job engine

**Files:**
- Create: `internal/device/jobs.go`
- Test: `internal/device/jobs_test.go`

**Interfaces:**
- Consumes: `Clock` (Task 2), `CmdError`/`CodeBusy` (Task 1).
- Produces: `Job` wire struct (`job_id`, `state`, `progress`, `estimated_duration_s`, `elapsed_s`, `result`, `error`; `Kind` is `json:"-"`), `JobState` constants (`JobRunning/JobPaused/JobSucceeded/JobFailed/JobCancelled`), `NewJobs(c Clock) *Jobs`, `(*Jobs).Start(kind string, estimate time.Duration) (Job, *CmdError)`, `(*Jobs).Active() *Job`, `(*Jobs).ActiveKind() string`, `(*Jobs).Get(id string) *Job`, `(*Jobs).Complete(result any) *Job`, `(*Jobs).Fail(e *CmdError) *Job`, `(*Jobs).Cancel() *Job`, `(*Jobs).Freeze()`, `(*Jobs).Unfreeze()`. **Not** goroutine-safe: loop-only by design.

- [ ] **Step 1: Write the failing test**

```go
// internal/device/jobs_test.go
package device

import (
	"testing"
	"time"
)

func TestJobsLifecycleWithPause(t *testing.T) {
	c := NewFakeClock(time.Unix(0, 0))
	j := NewJobs(c)

	job, cerr := j.Start("dispense", 100*time.Second)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if job.ID != "j-1" || job.State != JobRunning || job.EstimatedS != 100 {
		t.Fatalf("start: %+v", job)
	}
	if _, cerr := j.Start("other", time.Second); cerr == nil || cerr.Code != CodeBusy {
		t.Fatalf("second Start must be busy, got %v", cerr)
	}

	c.Advance(35 * time.Second)
	a := j.Active()
	if a.ElapsedS != 35 || a.Progress != 0.35 {
		t.Fatalf("at 35s: %+v", a)
	}

	j.Freeze()
	if j.Active().State != JobPaused {
		t.Fatal("freeze must pause")
	}
	c.Advance(10 * time.Second) // paused time must not count
	if got := j.Active(); got.ElapsedS != 35 || got.Progress != 0.35 {
		t.Fatalf("paused clock leaked: %+v", got)
	}
	j.Unfreeze()
	c.Advance(5 * time.Second)
	if got := j.Active(); got.ElapsedS != 40 {
		t.Fatalf("after resume: %+v", got)
	}

	done := j.Complete(map[string]any{"dispensed_ml": 10.0})
	if done.State != JobSucceeded || done.Progress != 1.0 || done.Result == nil {
		t.Fatalf("complete: %+v", done)
	}
	if j.Active() != nil {
		t.Fatal("no active job after completion")
	}
	if got := j.Get("j-1"); got == nil || got.State != JobSucceeded {
		t.Fatalf("history lookup: %+v", got)
	}
}

func TestJobsProgressClampedBelowOneWhileRunning(t *testing.T) {
	c := NewFakeClock(time.Unix(0, 0))
	j := NewJobs(c)
	if _, cerr := j.Start("move", 2*time.Second); cerr != nil {
		t.Fatal(cerr)
	}
	c.Advance(10 * time.Second) // overdue but not verified done
	if got := j.Active(); got.Progress != 0.99 {
		t.Fatalf("overdue progress must clamp to 0.99: %+v", got)
	}
}

func TestJobsFailAndCancelKeepProgress(t *testing.T) {
	c := NewFakeClock(time.Unix(0, 0))
	j := NewJobs(c)
	if _, cerr := j.Start("move", 100*time.Second); cerr != nil {
		t.Fatal(cerr)
	}
	c.Advance(50 * time.Second)
	failed := j.Fail(ErrHardware("device became unreachable mid-job"))
	if failed.State != JobFailed || failed.Progress != 0.5 || failed.Error == nil {
		t.Fatalf("fail: %+v", failed)
	}

	if _, cerr := j.Start("move2", 100*time.Second); cerr != nil {
		t.Fatal(cerr)
	}
	c.Advance(25 * time.Second)
	cancelled := j.Cancel()
	if cancelled.State != JobCancelled || cancelled.Progress != 0.25 {
		t.Fatalf("cancel: %+v", cancelled)
	}
	if j.Cancel() != nil {
		t.Fatal("cancel with no active job must return nil")
	}
}

func TestJobsHistoryRingKeepsEight(t *testing.T) {
	c := NewFakeClock(time.Unix(0, 0))
	j := NewJobs(c)
	for i := 0; i < 10; i++ {
		if _, cerr := j.Start("k", time.Second); cerr != nil {
			t.Fatal(cerr)
		}
		j.Complete(nil)
	}
	if j.Get("j-1") != nil || j.Get("j-2") != nil {
		t.Fatal("oldest jobs must be evicted")
	}
	if j.Get("j-3") == nil || j.Get("j-10") == nil {
		t.Fatal("last 8 jobs must be retained")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/device/ -run TestJobs -v`
Expected: FAIL (undefined `NewJobs`)

- [ ] **Step 3: Write the implementation**

```go
// internal/device/jobs.go
package device

import (
	"fmt"
	"time"
)

type JobState string

const (
	JobRunning   JobState = "running"
	JobPaused    JobState = "paused"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
)

const historyLimit = 8

// Job is the wire shape of the shared job model (JSON_PROTOCOL.md §2).
type Job struct {
	ID         string    `json:"job_id"`
	State      JobState  `json:"state"`
	Progress   float64   `json:"progress"`
	EstimatedS float64   `json:"estimated_duration_s"`
	ElapsedS   float64   `json:"elapsed_s"`
	Result     any       `json:"result"`
	Error      *CmdError `json:"error"`
	Kind       string    `json:"-"` // driver bookkeeping, not on the wire
}

type jobRec struct {
	id            string
	kind          string
	state         JobState
	estimate      time.Duration
	elapsed       time.Duration // accumulated run time, excluding pauses
	runningSince  time.Time     // zero while paused or terminal
	finalProgress float64
	result        any
	err           *CmdError
}

func (r *jobRec) elapsedAt(now time.Time) time.Duration {
	e := r.elapsed
	if !r.runningSince.IsZero() {
		e += now.Sub(r.runningSince)
	}
	return e
}

// Jobs implements the job model for one session: at most one active job,
// history ring of the last 8 completed. Loop-only — not goroutine-safe.
type Jobs struct {
	clock   Clock
	seq     int
	active  *jobRec
	history []*jobRec // newest first
}

func NewJobs(c Clock) *Jobs { return &Jobs{clock: c} }

// Start begins a job; CodeBusy if one is already active.
func (j *Jobs) Start(kind string, estimate time.Duration) (Job, *CmdError) {
	if j.active != nil {
		return Job{}, ErrBusy("a job is already running",
			map[string]any{"job_id": j.active.id})
	}
	j.seq++
	j.active = &jobRec{
		id:           fmt.Sprintf("j-%d", j.seq),
		kind:         kind,
		state:        JobRunning,
		estimate:     estimate,
		runningSince: j.clock.Now(),
	}
	return j.snapshot(j.active), nil
}

func (j *Jobs) Active() *Job {
	if j.active == nil {
		return nil
	}
	job := j.snapshot(j.active)
	return &job
}

func (j *Jobs) ActiveKind() string {
	if j.active == nil {
		return ""
	}
	return j.active.kind
}

func (j *Jobs) Get(id string) *Job {
	if j.active != nil && j.active.id == id {
		job := j.snapshot(j.active)
		return &job
	}
	for _, r := range j.history {
		if r.id == id {
			job := j.snapshot(r)
			return &job
		}
	}
	return nil
}

func (j *Jobs) Complete(result any) *Job {
	return j.finish(JobSucceeded, result, nil, 1.0)
}

func (j *Jobs) Fail(e *CmdError) *Job {
	return j.finish(JobFailed, nil, e, j.currentProgress())
}

func (j *Jobs) Cancel() *Job {
	return j.finish(JobCancelled, nil, nil, j.currentProgress())
}

// Freeze pauses the job clock (pump pause semantics). No-op unless running.
func (j *Jobs) Freeze() {
	r := j.active
	if r == nil || r.state != JobRunning {
		return
	}
	r.elapsed = r.elapsedAt(j.clock.Now())
	r.runningSince = time.Time{}
	r.state = JobPaused
}

func (j *Jobs) Unfreeze() {
	r := j.active
	if r == nil || r.state != JobPaused {
		return
	}
	r.runningSince = j.clock.Now()
	r.state = JobRunning
}

func (j *Jobs) finish(state JobState, result any, e *CmdError, progress float64) *Job {
	if j.active == nil {
		return nil
	}
	r := j.active
	r.elapsed = r.elapsedAt(j.clock.Now())
	r.runningSince = time.Time{}
	r.state = state
	r.result = result
	r.err = e
	r.finalProgress = progress
	j.active = nil
	j.history = append([]*jobRec{r}, j.history...)
	if len(j.history) > historyLimit {
		j.history = j.history[:historyLimit]
	}
	job := j.snapshot(r)
	return &job
}

func (j *Jobs) currentProgress() float64 {
	if j.active == nil {
		return 0
	}
	return clampProgress(j.active.elapsedAt(j.clock.Now()), j.active.estimate)
}

func (j *Jobs) snapshot(r *jobRec) Job {
	job := Job{
		ID:         r.id,
		State:      r.state,
		EstimatedS: r.estimate.Seconds(),
		ElapsedS:   r.elapsedAt(j.clock.Now()).Seconds(),
		Result:     r.result,
		Error:      r.err,
		Kind:       r.kind,
	}
	switch r.state {
	case JobRunning, JobPaused:
		job.Progress = clampProgress(r.elapsedAt(j.clock.Now()), r.estimate)
	default:
		job.Progress = r.finalProgress
	}
	return job
}

// clampProgress keeps clock-simulated progress strictly below 1.0: only a
// verified completion may report 1.0 (spec §2.2).
func clampProgress(elapsed, estimate time.Duration) float64 {
	if estimate <= 0 {
		return 0
	}
	p := float64(elapsed) / float64(estimate)
	if p > 0.99 {
		p = 0.99
	}
	if p < 0 {
		p = 0
	}
	return p
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/device/ -race -run TestJobs -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/device/jobs.go internal/device/jobs_test.go
git commit -m "feat(device): add shared job engine with pause-aware progress

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: Serial transaction primitive

**Files:**
- Create: `internal/device/transact.go`
- Test: `internal/device/transact_test.go`

**Interfaces:**
- Consumes: `serial.Port` (existing), `serial.FakePort` in tests.
- Produces: package vars `PerByteTimeout` (default `500 * time.Millisecond`), `DrainWindow` (default `50 * time.Millisecond`); sentinel `ErrReaderHeld`; unexported `transact(p serial.Port, frame []byte, replyLen int, total time.Duration) ([]byte, error)` — one internal retry of the whole transaction; Task 8 wraps it as `(*Session).Transact`.

- [ ] **Step 1: Write the failing test**

```go
// internal/device/transact_test.go
package device

import (
	"bytes"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// shrinkTimeouts makes transact fail fast in tests; restores on cleanup.
func shrinkTimeouts(t *testing.T) {
	t.Helper()
	oldPB, oldDW := PerByteTimeout, DrainWindow
	PerByteTimeout, DrainWindow = 20*time.Millisecond, 0
	t.Cleanup(func() { PerByteTimeout, DrainWindow = oldPB, oldDW })
}

func TestTransactWritesFrameAndReadsReply(t *testing.T) {
	shrinkTimeouts(t)
	p := serial.NewFakePort("COM9")
	p.Feed([]byte{10, 1, 2, 3}) // DrainWindow=0 → pre-fed reply survives
	frame := []byte{1, 2, 3, 0, 0}
	got, err := transact(p, frame, 4, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{10, 1, 2, 3}) {
		t.Errorf("reply = %v", got)
	}
	if !bytes.Equal(p.Written(), frame) {
		t.Errorf("written = %v", p.Written())
	}
}

func TestTransactDrainsStaleBytesBeforeWrite(t *testing.T) {
	oldPB, oldDW := PerByteTimeout, DrainWindow
	// generous read window so the late feed lands inside the FIRST attempt
	// (a retry would re-drain and discard it)
	PerByteTimeout, DrainWindow = 50*time.Millisecond, 30*time.Millisecond
	t.Cleanup(func() { PerByteTimeout, DrainWindow = oldPB, oldDW })

	p := serial.NewFakePort("COM9")
	p.Feed([]byte{99, 99, 99}) // stale garbage from a previous exchange
	go func() {
		time.Sleep(40 * time.Millisecond) // after the 30 ms drain window
		p.Feed([]byte{30, 1, 1, 4})
	}()
	got, err := transact(p, []byte{33, 1, 0, 0, 0}, 4, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{30, 1, 1, 4}) {
		t.Errorf("stale bytes leaked into reply: %v", got)
	}
}

func TestTransactWriteOnly(t *testing.T) {
	shrinkTimeouts(t)
	p := serial.NewFakePort("COM9")
	got, err := transact(p, []byte{19, 0, 0, 0, 0}, 0, time.Second)
	if err != nil || got != nil {
		t.Fatalf("write-only: got=%v err=%v", got, err)
	}
}

func TestTransactTimeoutRetriesWholeTransactionOnce(t *testing.T) {
	shrinkTimeouts(t)
	p := serial.NewFakePort("COM9")
	frame := []byte{1, 2, 3, 0, 0}
	_, err := transact(p, frame, 4, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if want := append(frame, frame...); !bytes.Equal(p.Written(), want) {
		t.Errorf("expected exactly two write attempts, written = %v", p.Written())
	}
}

func TestTransactSecondAttemptCanSucceed(t *testing.T) {
	shrinkTimeouts(t)
	p := serial.NewFakePort("COM9")
	go func() {
		time.Sleep(30 * time.Millisecond) // first attempt (20 ms silence) already failed
		p.Feed([]byte{70, 0, 0, 2})
	}()
	got, err := transact(p, []byte{1, 2, 3, 4, 0}, 4, 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{70, 0, 0, 2}) {
		t.Errorf("reply = %v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/device/ -run TestTransact -v`
Expected: FAIL (undefined `transact`, `PerByteTimeout`, `DrainWindow`)

- [ ] **Step 3: Write the implementation**

```go
// internal/device/transact.go
package device

import (
	"errors"
	"fmt"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// Timing knobs are vars so tests can shrink them.
var (
	// PerByteTimeout is the max silence between reply bytes. Devices insert
	// 10–20 ms gaps; 500 ms per the TRANSLATION docs' serial primitives.
	PerByteTimeout = 500 * time.Millisecond
	// DrainWindow is how long to spend discarding stale RX bytes pre-write.
	DrainWindow = 50 * time.Millisecond
)

// ErrReaderHeld: a reply-expecting transaction was attempted while a watcher
// goroutine holds the port's read side (spec §3). Driver bug by definition.
var ErrReaderHeld = errors.New("device: port read side is held by a watcher")

// transact implements the shared serial discipline (TRANSLATION docs §2):
// drain RX → write the whole frame in one write → read exactly replyLen
// bytes → on any failure retry the whole transaction once.
func transact(p serial.Port, frame []byte, replyLen int, total time.Duration) ([]byte, error) {
	reply, err := transactOnce(p, frame, replyLen, total)
	if err == nil {
		return reply, nil
	}
	return transactOnce(p, frame, replyLen, total)
}

func transactOnce(p serial.Port, frame []byte, replyLen int, total time.Duration) ([]byte, error) {
	if err := p.Drain(DrainWindow); err != nil {
		return nil, fmt.Errorf("drain: %w", err)
	}
	if _, err := p.Write(frame); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	if replyLen == 0 {
		return nil, nil
	}
	if minTotal := time.Duration(replyLen) * 30 * time.Millisecond; total < minTotal {
		total = minTotal
	}
	if err := p.SetReadTimeout(PerByteTimeout); err != nil {
		return nil, fmt.Errorf("set read timeout: %w", err)
	}
	buf := make([]byte, 0, replyLen)
	deadline := time.Now().Add(total)
	for len(buf) < replyLen {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("read: total timeout after %d/%d bytes", len(buf), replyLen)
		}
		chunk := make([]byte, replyLen-len(buf))
		n, err := p.Read(chunk)
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		if n == 0 { // per-byte timeout expired with no data
			return nil, fmt.Errorf("read: silence after %d/%d bytes", len(buf), replyLen)
		}
		buf = append(buf, chunk[:n]...)
	}
	return buf, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/device/ -race -run TestTransact -v`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/device/transact.go internal/device/transact_test.go
git commit -m "feat(device): add shared serial transaction primitive

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: Driver contract and factory registry

**Files:**
- Create: `internal/device/registry.go`
- Test: `internal/device/registry_test.go`

**Interfaces:**
- Consumes: `CmdError` (Task 1); forward-references `*Session` (defined in Task 7 — write this task and Task 7's session struct stub in the same change if the compiler needs it; see Step 3 note).
- Produces: `Driver` interface (`Attach(ctx, probeReply []byte) (Info, error)`, `Execute(ctx, cmd string, params json.RawMessage) (any, *CmdError)`, `Tick(now time.Time)`, `Detach()`), `Factory func(s *Session) Driver`, `Info` struct, `Register(code byte, name string, f Factory)`, `LookupDriver(code byte) (name string, f Factory, ok bool)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/device/registry_test.go
package device

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type nopDriver struct{}

func (nopDriver) Attach(context.Context, []byte) (Info, error) { return Info{}, nil }
func (nopDriver) Execute(context.Context, string, json.RawMessage) (any, *CmdError) {
	return nil, nil
}
func (nopDriver) Tick(time.Time) {}
func (nopDriver) Detach()        {}

func TestRegisterAndLookup(t *testing.T) {
	Register(201, "testdev", func(*Session) Driver { return nopDriver{} })
	name, factory, ok := LookupDriver(201)
	if !ok || name != "testdev" || factory == nil {
		t.Fatalf("lookup: %q %v %v", name, factory, ok)
	}
	if d := factory(nil); d == nil {
		t.Fatal("factory returned nil driver")
	}
	if _, _, ok := LookupDriver(202); ok {
		t.Fatal("unregistered code must not resolve")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/device/ -run TestRegisterAndLookup -v`
Expected: FAIL (undefined `Register`, `Info`, `Driver`)

- [ ] **Step 3: Write the implementation**

Note: `Factory` references `*Session`, which Task 7 fully implements. To keep this task compiling on its own, declare the empty struct placeholder `type Session struct{}` at the top of `internal/device/session.go` now; Task 7 replaces it.

```go
// internal/device/session.go  (placeholder — Task 7 replaces this file)
package device

// Session is the per-device actor. Implemented in the session task.
type Session struct{}
```

```go
// internal/device/registry.go
package device

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Driver is the per-device-type contract (spec §2.4). One instance per
// attached device; every method runs on the session goroutine.
type Driver interface {
	// Attach performs post-probe setup per the device's TRANSLATION.md §3:
	// read the serial number, push config mirrors, recover persistent state.
	// probeReply is the 4-byte identify reply discovery consumed (pump:
	// calibration bytes; valve: position count; densitometer: channels).
	// The returned Info is cached and served for `identify` and GET /devices.
	Attach(ctx context.Context, probeReply []byte) (Info, error)
	// Execute handles one JSON command. `identify` and `get_job` are served
	// by the session before reaching the driver.
	Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *CmdError)
	// Tick runs ~1/s while attached: canaries, monitoring schedulers.
	Tick(now time.Time)
	// Detach persists state and drops watchers; the session closes the port.
	Detach()
}

// Factory builds the driver bound to its session.
type Factory func(s *Session) Driver

// Info is the cached identify block (JSON_PROTOCOL.md §3 `identify`).
type Info struct {
	DeviceType      string `json:"device_type"`
	Model           string `json:"model"`
	Serial          string `json:"serial,omitempty"`
	FirmwareVersion string `json:"firmware_version"`
	ProtocolVersion string `json:"protocol_version"`
	Capabilities    any    `json:"capabilities"`
}

type driverEntry struct {
	name    string
	factory Factory
}

var (
	regMu   sync.RWMutex
	drivers = map[byte]driverEntry{}
)

// Register binds a probe type code to a driver factory. Called at app wiring
// time (not package init), so tests may register fakes under unused codes.
func Register(code byte, name string, f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	drivers[code] = driverEntry{name: name, factory: f}
}

// LookupDriver resolves a probe type code to its registered driver.
func LookupDriver(code byte) (string, Factory, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	e, ok := drivers[code]
	return e.name, e.factory, ok
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/device/ -race -run TestRegisterAndLookup -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/device/registry.go internal/device/registry_test.go internal/device/session.go
git commit -m "feat(device): add driver contract and factory registry

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: Session actor — loop, dispatch, driver services

**Files:**
- Modify: `internal/device/session.go` (replace the Task 6 placeholder entirely)
- Test: `internal/device/session_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1–6.
- Produces:
  - `SessionConfig{ID, Type string, TypeCode byte, PortName string, Conn serial.Port, Opener serial.Opener, Clock Clock, StateDir string, Factory Factory, ProbeReply []byte, Reprobe func(serial.Port) ([]byte, error)}`
  - `NewSession(cfg SessionConfig) *Session`, `(*Session).Start(ctx context.Context)`, `(*Session).Close()`
  - `(*Session).Execute(ctx context.Context, req Request) Response` — thread-safe API entry
  - Accessors (thread-safe): `ID() string`, `TypeName() string`, `PortName() string`, `Connected() bool`, `CachedInfo() (Info, bool)`
  - Driver services (session-goroutine only unless noted): `Jobs() *Jobs`, `Now() time.Time`, `Store(key string) *Store`, `SetInfo(info Info)`, `Post(fn func())` (thread-safe), `After(d time.Duration, fn func())`, `Go(fn func())`, `Conn() serial.Port`, `HoldReader()`, `ReleaseReader()`
  - `var HeartbeatInterval = time.Second`
  - Session-served commands: `identify` (from cached `Info`), `get_job` (params `{job_id}`; unknown id → `invalid_params`).
- Task 8 adds: `(*Session).Transact`, unreachable/backoff/reattach.

- [ ] **Step 1: Write the failing test**

```go
// internal/device/session_test.go
package device_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// stubDriver is a scriptable Driver for session tests.
type stubDriver struct {
	s         *device.Session
	attachErr error
	attaches  atomic.Int32
	ticks     atomic.Int32
	detached  atomic.Bool
	exec      func(cmd string, params json.RawMessage) (any, *device.CmdError)
}

func (d *stubDriver) Attach(ctx context.Context, probeReply []byte) (device.Info, error) {
	d.attaches.Add(1)
	if d.attachErr != nil {
		return device.Info{}, d.attachErr
	}
	return device.Info{DeviceType: "stub", Model: "stub-1", Serial: "26-001",
		FirmwareVersion: "legacy", ProtocolVersion: "1.0"}, nil
}

func (d *stubDriver) Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *device.CmdError) {
	if d.exec != nil {
		return d.exec(cmd, params)
	}
	return nil, device.ErrUnknownCommand(cmd)
}

func (d *stubDriver) Tick(now time.Time) { d.ticks.Add(1) }
func (d *stubDriver) Detach()            { d.detached.Store(true) }

type sessionFixture struct {
	s      *device.Session
	drv    *stubDriver
	clock  *device.FakeClock
	port   *serial.FakePort
	opener *serial.FakeOpener
}

func newFixture(t *testing.T, mutate func(*device.SessionConfig, *stubDriver)) *sessionFixture {
	t.Helper()
	drv := &stubDriver{}
	clock := device.NewFakeClock(time.Unix(1000, 0))
	port := serial.NewFakePort("COM9")
	opener := serial.NewFakeOpener()
	opener.Add(port)
	conn, err := opener.Open("COM9")
	if err != nil {
		t.Fatal(err)
	}
	cfg := device.SessionConfig{
		ID: "stub_1", Type: "stub", TypeCode: 201, PortName: "COM9",
		Conn: conn, Opener: opener, Clock: clock, StateDir: t.TempDir(),
		Factory:    func(s *device.Session) device.Driver { drv.s = s; return drv },
		ProbeReply: []byte{201, 0, 0, 1},
		Reprobe:    func(p serial.Port) ([]byte, error) { return []byte{201, 0, 0, 1}, nil },
	}
	if mutate != nil {
		mutate(&cfg, drv)
	}
	s := device.NewSession(cfg)
	s.Start(context.Background())
	t.Cleanup(s.Close)
	return &sessionFixture{s: s, drv: drv, clock: clock, port: port, opener: opener}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSessionAttachesAndServesIdentify(t *testing.T) {
	f := newFixture(t, nil)
	waitFor(t, "attach", f.s.Connected)
	resp := f.s.Execute(context.Background(), device.Request{ID: "r1", Cmd: "identify"})
	if resp.Status != "ok" || resp.ID != "r1" {
		t.Fatalf("resp: %+v", resp)
	}
	info, ok := resp.Result.(device.Info)
	if !ok || info.Serial != "26-001" {
		t.Fatalf("identify result: %#v", resp.Result)
	}
	if got, ok := f.s.CachedInfo(); !ok || got.Model != "stub-1" {
		t.Fatalf("CachedInfo: %+v %v", got, ok)
	}
}

func TestSessionRoutesCommandsToDriver(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			if cmd != "ping" {
				return nil, device.ErrUnknownCommand(cmd)
			}
			return map[string]any{"uptime_ms": 5}, nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	resp := f.s.Execute(context.Background(), device.Request{ID: "r2", Cmd: "ping"})
	if resp.Status != "ok" {
		t.Fatalf("resp: %+v", resp)
	}
	resp = f.s.Execute(context.Background(), device.Request{ID: "r3", Cmd: "nope"})
	if resp.Status != "error" || resp.Error.Code != device.CodeUnknownCommand {
		t.Fatalf("resp: %+v", resp)
	}
}

func TestSessionGetJob(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			job, cerr := drv.s.Jobs().Start("dispense", 100*time.Second)
			if cerr != nil {
				return nil, cerr
			}
			return map[string]any{"job": job}, nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	if resp := f.s.Execute(context.Background(), device.Request{ID: "r4", Cmd: "dispense"}); resp.Status != "ok" {
		t.Fatalf("start: %+v", resp)
	}
	resp := f.s.Execute(context.Background(), device.Request{
		ID: "r5", Cmd: "get_job", Params: json.RawMessage(`{"job_id":"j-1"}`)})
	if resp.Status != "ok" {
		t.Fatalf("get_job: %+v", resp)
	}
	job, ok := resp.Result.(device.Job)
	if !ok || job.ID != "j-1" || job.State != device.JobRunning {
		t.Fatalf("job: %#v", resp.Result)
	}
	resp = f.s.Execute(context.Background(), device.Request{
		ID: "r6", Cmd: "get_job", Params: json.RawMessage(`{"job_id":"j-99"}`)})
	if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
		t.Fatalf("unknown job: %+v", resp)
	}
}

func TestSessionSerializesCommands(t *testing.T) {
	release := make(chan struct{})
	var inFlight, maxInFlight atomic.Int32
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			cur := inFlight.Add(1)
			if cur > maxInFlight.Load() {
				maxInFlight.Store(cur)
			}
			<-release
			inFlight.Add(-1)
			return "done", nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	results := make(chan device.Response, 2)
	for i := 0; i < 2; i++ {
		go func() {
			results <- f.s.Execute(context.Background(), device.Request{ID: "x", Cmd: "slow"})
		}()
	}
	waitFor(t, "first command entered", func() bool { return inFlight.Load() == 1 })
	close(release)
	<-results
	<-results
	if maxInFlight.Load() != 1 {
		t.Fatalf("commands overlapped: max in flight = %d", maxInFlight.Load())
	}
}

func TestSessionHeartbeatTicksDriver(t *testing.T) {
	f := newFixture(t, nil)
	waitFor(t, "attach", f.s.Connected)
	f.clock.Advance(device.HeartbeatInterval)
	waitFor(t, "tick", func() bool { return f.drv.ticks.Load() >= 1 })
}

func TestSessionAfterRunsOnLoop(t *testing.T) {
	f := newFixture(t, nil)
	waitFor(t, "attach", f.s.Connected)
	var fired atomic.Bool
	done := make(chan struct{})
	f.s.Post(func() {
		f.drv.s.After(10*time.Second, func() { fired.Store(true) })
		close(done)
	})
	<-done
	f.clock.Advance(9 * time.Second)
	time.Sleep(10 * time.Millisecond)
	if fired.Load() {
		t.Fatal("After fired early")
	}
	f.clock.Advance(time.Second)
	waitFor(t, "after callback", fired.Load)
}

func TestSessionUnreachableWhenAttachFails(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.attachErr = context.DeadlineExceeded // any error
	})
	waitFor(t, "first attach attempt", func() bool { return f.drv.attaches.Load() >= 1 })
	resp := f.s.Execute(context.Background(), device.Request{ID: "r7", Cmd: "ping"})
	if resp.Status != "error" || resp.Error.Code != device.CodeDeviceUnreachable {
		t.Fatalf("resp: %+v", resp)
	}
	if f.s.Connected() {
		t.Fatal("must not report connected")
	}
}

func TestSessionCloseDetachesDriver(t *testing.T) {
	f := newFixture(t, nil)
	waitFor(t, "attach", f.s.Connected)
	f.s.Close()
	if !f.drv.detached.Load() {
		t.Fatal("Detach not called on Close")
	}
	resp := f.s.Execute(context.Background(), device.Request{ID: "r8", Cmd: "ping"})
	if resp.Status != "error" || resp.Error.Code != device.CodeDeviceUnreachable {
		t.Fatalf("Execute after Close: %+v", resp)
	}
	if !strings.Contains(resp.Error.Message, "closed") {
		t.Fatalf("message should say session closed: %+v", resp.Error)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/device/ -run TestSession -v`
Expected: FAIL (Session has no fields/methods yet)

- [ ] **Step 3: Write the implementation (replace session.go entirely)**

```go
// internal/device/session.go
package device

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// HeartbeatInterval is how often an attached driver's Tick runs.
var HeartbeatInterval = time.Second

// SessionConfig carries everything a Session needs at construction.
type SessionConfig struct {
	ID       string
	Type     string // registered driver type name, e.g. "pump"
	TypeCode byte   // probe type code, e.g. 10
	PortName string
	Conn     serial.Port // open port handed over by discovery
	Opener   serial.Opener
	Clock    Clock  // nil → SystemClock()
	StateDir string // devicestate directory for Store
	Factory  Factory
	// ProbeReply is the 4-byte identify reply discovery consumed.
	ProbeReply []byte
	// Reprobe re-identifies the device on a freshly opened port during
	// background re-attach. Wired to discovery.Probe by the caller.
	Reprobe func(p serial.Port) ([]byte, error)
}

type mailMsg struct {
	req  Request
	resp chan Response
}

// Session is the per-device actor: it owns the serial port and the driver,
// and runs all driver code on a single goroutine (spec §3).
type Session struct {
	cfg    SessionConfig
	jobs   *Jobs
	driver Driver

	mail   chan mailMsg
	posts  chan func()
	done   chan struct{}
	cancel context.CancelFunc

	// cross-goroutine mirrors for API reads
	connected atomic.Bool
	info      atomic.Pointer[Info]

	// loop-owned state — touched only by the session goroutine
	conn       serial.Port
	readerHeld bool
	backoff    time.Duration
	loopCtx    context.Context
}

func NewSession(cfg SessionConfig) *Session {
	if cfg.Clock == nil {
		cfg.Clock = SystemClock()
	}
	return &Session{
		cfg:   cfg,
		jobs:  NewJobs(cfg.Clock),
		mail:  make(chan mailMsg),
		posts: make(chan func(), 64),
		done:  make(chan struct{}),
		conn:  cfg.Conn,
	}
}

// Start launches the session goroutine; the initial attach runs on it.
func (s *Session) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.loopCtx = ctx
	s.driver = s.cfg.Factory(s)
	go s.loop(ctx)
}

// Close stops the loop; the driver is detached and the port closed.
// Blocks until shutdown completes. Safe to call more than once.
func (s *Session) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	<-s.done
}

func (s *Session) loop(ctx context.Context) {
	defer close(s.done)
	s.attach(s.cfg.ProbeReply)
	heartbeat := s.cfg.Clock.After(HeartbeatInterval)
	for {
		select {
		case <-ctx.Done():
			if s.connected.Load() {
				s.driver.Detach()
			}
			if s.conn != nil {
				_ = s.conn.Close()
			}
			return
		case m := <-s.mail:
			m.resp <- s.handle(ctx, m.req)
		case fn := <-s.posts:
			fn()
		case <-heartbeat:
			if s.connected.Load() {
				s.driver.Tick(s.cfg.Clock.Now())
			}
			heartbeat = s.cfg.Clock.After(HeartbeatInterval)
		}
	}
}

// attach runs driver.Attach and publishes the result. Loop-only.
func (s *Session) attach(probeReply []byte) {
	info, err := s.driver.Attach(s.loopCtx, probeReply)
	if err != nil {
		slog.Warn("device attach failed", "device", s.cfg.ID, "err", err)
		s.connected.Store(false)
		s.scheduleReattach()
		return
	}
	s.info.Store(&info)
	s.connected.Store(true)
	s.backoff = 0
	slog.Info("device attached", "device", s.cfg.ID, "port", s.cfg.PortName)
}

// Execute submits one envelope command; thread-safe API entry point.
func (s *Session) Execute(ctx context.Context, req Request) Response {
	resp := make(chan Response, 1)
	select {
	case s.mail <- mailMsg{req: req, resp: resp}:
	case <-s.done:
		return Err(req.ID, &CmdError{Code: CodeDeviceUnreachable, Message: "device session closed"})
	case <-ctx.Done():
		return Err(req.ID, ErrInternal("request cancelled"))
	}
	select {
	case r := <-resp:
		return r
	case <-s.done:
		return Err(req.ID, &CmdError{Code: CodeDeviceUnreachable, Message: "device session closed"})
	case <-ctx.Done():
		return Err(req.ID, ErrInternal("request cancelled"))
	}
}

func (s *Session) handle(ctx context.Context, req Request) Response {
	if !s.connected.Load() {
		return Err(req.ID, &CmdError{Code: CodeDeviceUnreachable, Message: "device is not responding"})
	}
	switch req.Cmd {
	case "identify":
		return OK(req.ID, *s.info.Load())
	case "get_job":
		return s.handleGetJob(req)
	}
	result, cerr := s.driver.Execute(ctx, req.Cmd, req.Params)
	if cerr != nil {
		return Err(req.ID, cerr)
	}
	return OK(req.ID, result)
}

func (s *Session) handleGetJob(req Request) Response {
	var p struct {
		JobID string `json:"job_id"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return Err(req.ID, ErrInvalidParams("params", nil, "params is not valid JSON"))
		}
	}
	if p.JobID == "" {
		return Err(req.ID, ErrInvalidParams("job_id", p.JobID, "job_id is required"))
	}
	job := s.jobs.Get(p.JobID)
	if job == nil {
		return Err(req.ID, ErrInvalidParams("job_id", p.JobID, "unknown job"))
	}
	return OK(req.ID, *job)
}

// --- thread-safe accessors (API/DTO reads) ---

func (s *Session) ID() string       { return s.cfg.ID }
func (s *Session) TypeName() string { return s.cfg.Type }
func (s *Session) PortName() string { return s.cfg.PortName }
func (s *Session) Connected() bool  { return s.connected.Load() }

// CachedInfo returns the identify block from the last successful attach.
func (s *Session) CachedInfo() (Info, bool) {
	p := s.info.Load()
	if p == nil {
		return Info{}, false
	}
	return *p, true
}

// --- driver services ---

// Jobs, Now, Store, SetInfo, Conn, HoldReader, ReleaseReader: session-goroutine only.
func (s *Session) Jobs() *Jobs    { return s.jobs }
func (s *Session) Now() time.Time { return s.cfg.Clock.Now() }

// Store returns the persistent store for this device; drivers call it with
// their state key (serial number, or port name for serial-less devices).
func (s *Session) Store(key string) *Store {
	return NewStore(s.cfg.StateDir, s.cfg.Type+"-"+key)
}

// SetInfo refreshes the cached identify block (e.g. capabilities derived
// from a new calibration).
func (s *Session) SetInfo(info Info) { s.info.Store(&info) }

// Conn exposes the raw port for watcher-goroutine reads (pump opcode-18).
func (s *Session) Conn() serial.Port { return s.conn }

// HoldReader marks the port's read side as owned by a watcher goroutine;
// ReleaseReader clears it. Reply-expecting Transact calls fail with
// ErrReaderHeld while held.
func (s *Session) HoldReader()    { s.readerHeld = true }
func (s *Session) ReleaseReader() { s.readerHeld = false }

// Post schedules fn on the session goroutine. Thread-safe.
func (s *Session) Post(fn func()) {
	select {
	case s.posts <- fn:
	case <-s.done:
	}
}

// After runs fn on the session goroutine after d (via the injectable clock).
func (s *Session) After(d time.Duration, fn func()) {
	ch := s.cfg.Clock.After(d)
	go func() {
		select {
		case <-ch:
			s.Post(fn)
		case <-s.done:
		}
	}()
}

// Go runs fn on a watcher goroutine (blocking port reads). fn reports back
// to the loop via Post.
func (s *Session) Go(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("device watcher panic", "device", s.cfg.ID, "panic", r)
			}
		}()
		fn()
	}()
}

// scheduleReattach and Transact are implemented in the resilience task.
func (s *Session) scheduleReattach() {}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/device/ -race -run TestSession -v`
Expected: PASS (8 tests). Note `TestSessionUnreachableWhenAttachFails` passes with the stub `scheduleReattach`; Task 8 makes it real.

- [ ] **Step 5: Run the full package suite**

Run: `go test ./internal/device/ -race -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/device/session.go internal/device/session_test.go
git commit -m "feat(device): add per-device session actor with command dispatch

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: Session resilience — Transact wrapper, unreachable, reattach

**Files:**
- Modify: `internal/device/session.go` (replace the `scheduleReattach` stub; add `Transact`, `markUnreachable`, `tryReattach`, backoff vars)
- Test: `internal/device/session_resilience_test.go`

**Interfaces:**
- Consumes: `transact` (Task 5), session internals (Task 7).
- Produces: `(*Session).Transact(frame []byte, replyLen int, timeout time.Duration) ([]byte, error)` (session-goroutine only; double-failure → session unreachable, active job failed, backoff reattach), package vars `ReattachBase = 5 * time.Second`, `ReattachMax = 60 * time.Second`. Reattach opens the port fresh via `cfg.Opener`, verifies identity via `cfg.Reprobe` (first reply byte must equal `cfg.TypeCode`), then re-runs `driver.Attach`.

- [ ] **Step 1: Write the failing test**

```go
// internal/device/session_resilience_test.go
package device_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// execTransact makes the stub driver run one Transact on command "tx".
func execTransact(drv *stubDriver, replyLen int) {
	drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
		if cmd == "job" {
			job, cerr := drv.s.Jobs().Start("move", 100*time.Second)
			if cerr != nil {
				return nil, cerr
			}
			return job, nil
		}
		reply, err := drv.s.Transact([]byte{33, 1, 0, 0, 0}, replyLen, 50*time.Millisecond)
		if err != nil {
			return nil, device.ErrHardware(err.Error())
		}
		return reply, nil
	}
}

func TestTransactDoubleFailureFlipsUnreachableAndFailsJob(t *testing.T) {
	shrinkTimeoutsExt(t)
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		execTransact(drv, 4)
	})
	waitFor(t, "attach", f.s.Connected)

	if resp := f.s.Execute(context.Background(), device.Request{ID: "j", Cmd: "job"}); resp.Status != "ok" {
		t.Fatalf("job start: %+v", resp)
	}
	// nothing fed to the port → both transaction attempts time out
	resp := f.s.Execute(context.Background(), device.Request{ID: "t", Cmd: "tx"})
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("tx: %+v", resp)
	}
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })

	// active job must have been failed by the transition
	resp = f.s.Execute(context.Background(), device.Request{ID: "g", Cmd: "get_job"})
	if resp.Status != "error" || resp.Error.Code != device.CodeDeviceUnreachable {
		t.Fatalf("commands while unreachable must fail fast: %+v", resp)
	}
}

func TestSessionReattachesAfterBackoff(t *testing.T) {
	shrinkTimeoutsExt(t)
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		execTransact(drv, 4)
	})
	waitFor(t, "attach", f.s.Connected)
	if f.drv.attaches.Load() != 1 {
		t.Fatalf("attaches = %d", f.drv.attaches.Load())
	}

	f.s.Execute(context.Background(), device.Request{ID: "t", Cmd: "tx"}) // → unreachable
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })

	f.clock.Advance(device.ReattachBase)
	waitFor(t, "reattach", f.s.Connected)
	if f.drv.attaches.Load() != 2 {
		t.Fatalf("attaches = %d", f.drv.attaches.Load())
	}
}

func TestSessionReattachRejectsIdentityChange(t *testing.T) {
	shrinkTimeoutsExt(t)
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		execTransact(drv, 4)
		cfg.Reprobe = func(p serial.Port) ([]byte, error) {
			return []byte{70, 0, 0, 2}, nil // a densitometer appeared on our port
		}
	})
	waitFor(t, "attach", f.s.Connected)
	f.s.Execute(context.Background(), device.Request{ID: "t", Cmd: "tx"})
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })

	f.clock.Advance(device.ReattachBase)
	time.Sleep(20 * time.Millisecond)
	if f.s.Connected() {
		t.Fatal("must not attach to a different device type")
	}
	if f.drv.attaches.Load() != 1 {
		t.Fatalf("driver.Attach must not run on identity mismatch, attaches = %d", f.drv.attaches.Load())
	}
}

func TestSessionReattachFailureBacksOffExponentially(t *testing.T) {
	shrinkTimeoutsExt(t)
	reprobeErr := errors.New("still dead")
	var reprobes atomic.Int32 // written on the session goroutine, read by the test
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		execTransact(drv, 4)
		cfg.Reprobe = func(p serial.Port) ([]byte, error) {
			reprobes.Add(1)
			return nil, reprobeErr
		}
	})
	waitFor(t, "attach", f.s.Connected)
	f.s.Execute(context.Background(), device.Request{ID: "t", Cmd: "tx"})
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })

	f.clock.Advance(device.ReattachBase) // 5s: attempt 1
	waitFor(t, "first reprobe", func() bool { return reprobes.Load() >= 1 })
	f.clock.Advance(device.ReattachBase) // only 5s more — attempt 2 needs 10s
	time.Sleep(20 * time.Millisecond)
	if reprobes.Load() != 1 {
		t.Fatalf("backoff not doubled: reprobes = %d", reprobes.Load())
	}
	f.clock.Advance(device.ReattachBase) // total 10s since attempt 1
	waitFor(t, "second reprobe", func() bool { return reprobes.Load() >= 2 })
}

func TestHoldReaderBlocksReplyExpectingTransact(t *testing.T) {
	shrinkTimeoutsExt(t)
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			drv.s.HoldReader()
			defer drv.s.ReleaseReader()
			if _, err := drv.s.Transact([]byte{19, 0, 0, 0, 0}, 0, time.Second); err != nil {
				return nil, device.ErrInternal("write-only must pass: " + err.Error())
			}
			_, err := drv.s.Transact([]byte{1, 2, 3, 0, 0}, 4, time.Second)
			if !errors.Is(err, device.ErrReaderHeld) {
				return nil, device.ErrInternal("expected ErrReaderHeld")
			}
			return "ok", nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	resp := f.s.Execute(context.Background(), device.Request{ID: "h", Cmd: "tx"})
	if resp.Status != "ok" {
		t.Fatalf("resp: %+v", resp)
	}
	if f.s.Connected() != true {
		t.Fatal("ErrReaderHeld must not flip the session unreachable")
	}
}

// shrinkTimeoutsExt shrinks transact knobs for the resilience tests
// (session_test.go's fixture does not touch them).
func shrinkTimeoutsExt(t *testing.T) {
	t.Helper()
	oldPB, oldDW := device.PerByteTimeout, device.DrainWindow
	device.PerByteTimeout, device.DrainWindow = 10*time.Millisecond, 0
	t.Cleanup(func() { device.PerByteTimeout, device.DrainWindow = oldPB, oldDW })
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/device/ -run 'TestTransactDouble|TestSessionReattach|TestHoldReader' -v`
Expected: FAIL (no `Transact` method; stub `scheduleReattach` does nothing)

- [ ] **Step 3: Write the implementation**

In `internal/device/session.go`, add near `HeartbeatInterval`:

```go
// Reattach backoff bounds (doubling from base to max).
var (
	ReattachBase = 5 * time.Second
	ReattachMax  = 60 * time.Second
)
```

Replace the `scheduleReattach` stub with:

```go
// Transact runs one serial transaction with the shared discipline. A
// double failure flips the session to unreachable, fails the active job,
// and schedules a backoff reattach. Session-goroutine only.
func (s *Session) Transact(frame []byte, replyLen int, timeout time.Duration) ([]byte, error) {
	if replyLen > 0 && s.readerHeld {
		return nil, ErrReaderHeld
	}
	reply, err := transact(s.conn, frame, replyLen, timeout)
	if err != nil {
		s.markUnreachable(err)
	}
	return reply, err
}

// markUnreachable transitions ready → unreachable. No-op when already
// unreachable or still attaching: those paths own their own retries.
func (s *Session) markUnreachable(cause error) {
	if !s.connected.Load() {
		return
	}
	slog.Warn("device unreachable", "device", s.cfg.ID, "port", s.cfg.PortName, "err", cause)
	s.connected.Store(false)
	s.readerHeld = false
	if s.jobs.Active() != nil {
		s.jobs.Fail(ErrHardware("device became unreachable mid-job"))
	}
	s.scheduleReattach()
}

func (s *Session) scheduleReattach() {
	if s.backoff == 0 {
		s.backoff = ReattachBase
	} else {
		s.backoff *= 2
		if s.backoff > ReattachMax {
			s.backoff = ReattachMax
		}
	}
	s.After(s.backoff, s.tryReattach)
}

// tryReattach reopens the port, re-verifies device identity, and re-runs
// driver.Attach. Loop-only (scheduled via After).
func (s *Session) tryReattach() {
	if s.connected.Load() {
		return
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	conn, err := s.cfg.Opener.Open(s.cfg.PortName)
	if err != nil {
		slog.Warn("device reattach: open failed", "device", s.cfg.ID, "err", err)
		s.scheduleReattach()
		return
	}
	s.conn = conn
	reply, err := s.cfg.Reprobe(conn)
	if err != nil {
		slog.Warn("device reattach: probe failed", "device", s.cfg.ID, "err", err)
		s.scheduleReattach()
		return
	}
	if len(reply) == 0 || reply[0] != s.cfg.TypeCode {
		slog.Warn("device reattach: identity changed on port",
			"device", s.cfg.ID, "port", s.cfg.PortName, "reply", reply)
		s.scheduleReattach()
		return
	}
	s.attach(reply)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/device/ -race -count=1 -v`
Expected: PASS — the whole package, including Task 7's tests, still green.

- [ ] **Step 5: Commit**

```bash
git add internal/device/session.go internal/device/session_resilience_test.go
git commit -m "feat(device): add session transact wrapper with unreachable recovery

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: Verification sweep and PR

**Files:**
- No new files; fixes only if checks fail.

**Interfaces:**
- Consumes: everything above.
- Produces: the merged PR that driver plans (PRs 2–4) build on.

- [ ] **Step 1: Run the full pre-flight (CLAUDE.md)**

```bash
gofmt -l .            # must print nothing
go vet ./...
golangci-lint run
go test -race -count=1 ./...
govulncheck ./...
```

Expected: all clean. Fix anything that surfaces (gosec on file modes, errcheck on ignored errors — `_ =` deliberate discards are fine) and amend/commit the fixes.

- [ ] **Step 2: Push and open the PR**

```bash
git push -u origin json-device-protocol-v2
gh pr create \
  --title "feat: add device runtime core for JSON device protocol" \
  --body "$(cat <<'EOF'
First of five PRs implementing the v2 JSON device protocol
(docs/superpowers/specs/2026-07-05-json-device-protocol-design.md).

Adds internal/device: shared envelope + error codes, job engine,
serial transaction primitive, atomic per-device state store, driver
factory registry, and the per-device session actor (command dispatch,
heartbeat ticks, watcher/timer scheduling, unreachable-with-backoff
recovery). Includes the approved design spec. Pure library — nothing
consumes it yet; driver PRs (pump, densitometer, valve) follow, then
the breaking API cutover.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Note: title is a plain `feat:` — **no** `BREAKING CHANGE:` anywhere in the body; the major bump is reserved for the cutover PR (PR 5).

- [ ] **Step 3: Verify CI is green**

Run: `gh pr checks --watch`
Expected: the `verify` job passes on both platforms.

---

## Follow-on plans (not in this document)

- PR 2 plan: pump driver (`internal/device/pump`) — written after this PR merges, against the real core API.
- PR 3 plan: densitometer driver. PR 4 plan: valve driver.
- PR 5 plan: API cutover (`/api/v1`, discovery/registry integration, raw surface removal, panel bindings) — carries the `BREAKING CHANGE:` footer.
