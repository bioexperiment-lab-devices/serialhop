# Remote Admin-Pushed Updates — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a lab-bridge admin push a SerialHop update over the REST API (`POST /agent/update`) with no operator action or UAC, and report the outcome via `GET /agent/update/status`.

**Architecture:** The LocalSystem service downloads + SHA-verifies a new binary in a background goroutine, writes progress to a JSON result file, then spawns a **detached** LocalSystem child (`SerialHop.exe --admin-action=update …`) that performs the existing stop→swap→start-with-rollback and writes the terminal result. Off by default via `remote_update.enabled`; admin-gating is server-side (Authelia), not in SerialHop.

**Tech Stack:** Go stdlib (`net/http`, `encoding/json`, `os/exec`, `log/slog`), existing `internal/updater` + `internal/winsvc` machinery, `golang.org/x/sys/windows` (already in module) for the detached spawn.

**Spec:** `docs/superpowers/specs/2026-07-21-remote-admin-update-design.md`

## Global Constraints

- **Go, cross-platform tests:** every test must pass on macOS/Linux **and** Windows. Windows-only code lives in `*_windows.go`; add a `*_other.go` fake so coverage compiles/runs on non-Windows (CLAUDE.md).
- **Pre-flight (run before every commit that touches Go):** `gofmt -l .` (must print nothing), `go vet ./...`, `go build ./...`, `go test -race -count=1 ./...`. Full gate before PR also runs `golangci-lint run` (errcheck, staticcheck, unused, ineffassign, gosec) and `govulncheck ./...`.
- **Config change ⇒ migration:** any `config.Config` field change bumps `CurrentSchemaVersion` by exactly 1 and appends exactly one `Migration` (append-only history; never edit existing entries). Reuse scaffold comment text verbatim. (CLAUDE.md "Registering config changes".)
- **No new third-party dependencies.** Everything used is stdlib or already in `go.mod`.
- **No secrets in logs** — `tools/forbidsecretlog` guards the log surface; log versions/URLs/hashes only, never `lab_bridge.pass`.
- **Conventional Commits:** individual commits are squashed; the PR title `feat: remote admin-pushed updates` is what release-please reads.
- **Repo:** `github.com/bioexperiment-lab-devices/serialhop`. Import paths are `github.com/bioexperiment-lab-devices/serialhop/internal/<pkg>`.

---

### Task 1: Config — `remote_update` section + schema v2 migration

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/migrations.go`
- Test: `internal/config/config_test.go` (append), `internal/config/migrate_test.go` (append)

**Interfaces:**
- Produces: `config.RemoteUpdateConfig{ Enabled bool }`; `config.Config.RemoteUpdate`; `config.CurrentSchemaVersion == 2`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:

```go
func TestDefault_RemoteUpdateDisabled(t *testing.T) {
	if Default().RemoteUpdate.Enabled {
		t.Error("RemoteUpdate.Enabled should default to false")
	}
	if CurrentSchemaVersion != 2 {
		t.Errorf("CurrentSchemaVersion = %d, want 2", CurrentSchemaVersion)
	}
}
```

Append to `internal/config/migrate_test.go`:

```go
func TestMigrateV1ToV2AddsRemoteUpdate(t *testing.T) {
	src := "schema_version: 1\nlab_bridge:\n  user: \"\"\n"
	out, changes := applyOps(t, src, 2, migrations[len(migrations)-1].Ops...)
	if migrations[len(migrations)-1].To != 2 {
		t.Fatalf("last migration To = %d, want 2", migrations[len(migrations)-1].To)
	}
	if !strings.Contains(out, "remote_update:") || !strings.Contains(out, "enabled: false") {
		t.Errorf("migrated output missing remote_update section:\n%s", out)
	}
	if len(changes) == 0 {
		t.Error("expected at least one change")
	}
}

func TestMigrateV1ToV2PreservesOperatorValue(t *testing.T) {
	src := "schema_version: 1\nremote_update:\n  enabled: true\n"
	out, _ := applyOps(t, src, 2, migrations[len(migrations)-1].Ops...)
	if !strings.Contains(out, "enabled: true") {
		t.Errorf("operator's enabled: true must be preserved:\n%s", out)
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/config/ -run 'RemoteUpdate|V1ToV2' -v`
Expected: FAIL (compile error: `RemoteUpdate` undefined; `CurrentSchemaVersion` is 1).

- [ ] **Step 3: Implement — struct, field, default, scaffold, version bump**

In `internal/config/config.go`: bump the constant and add the type + field + default + scaffold.

```go
const CurrentSchemaVersion = 2
```

Add the struct near the other config structs:

```go
type RemoteUpdateConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
}
```

Add the field to `Config` (after `RawSerial`):

```go
	RemoteUpdate  RemoteUpdateConfig `yaml:"remote_update" json:"remote_update"`
```

Add to `Default()`'s returned struct literal (after `RawSerial: …`):

```go
		RemoteUpdate: RemoteUpdateConfig{Enabled: false},
```

In `scaffoldTemplate`: change the schema line to `schema_version: 2` and append this block at the end of the template (before the closing backtick):

```
remote_update:
  enabled: false          # allow lab-bridge admins to push updates via
                          # POST /agent/update (admin-gated server-side, like
                          # /flash). the update installs with no operator
                          # action. off by default.
```

Also update the scaffold's schema comment line to read `schema_version: 2` (keep the existing "managed automatically" comment).

- [ ] **Step 4: Implement — the migration**

In `internal/config/migrations.go`, replace `var migrations = []Migration{}` with:

```go
var migrations = []Migration{
	{
		To:   2,
		Desc: "add remote_update section (default disabled)",
		Ops: []Op{
			Add("remote_update", `remote_update:
  enabled: false          # allow lab-bridge admins to push updates via
                          # POST /agent/update (admin-gated server-side, like
                          # /flash). the update installs with no operator
                          # action. off by default.`),
		},
	},
}
```

- [ ] **Step 5: Update the migration baseline fixture note**

`internal/config/testdata/migrations/baseline-v1.yaml` stays a **v1** file (it is the pre-migration baseline the drift test replays migrations against — do **not** add `remote_update` to it). No edit needed; confirm it still starts `schema_version: 1`.

- [ ] **Step 6: Run the full config suite (incl. the drift guard)**

Run: `go test ./internal/config/ -v`
Expected: PASS — including `TestScaffoldMatchesMigratedBaseline` (baseline v1 + the new migration must produce exactly the scaffold's key set) and the two new migration tests.

- [ ] **Step 7: gofmt + vet + commit**

```bash
gofmt -w internal/config/ && go vet ./internal/config/
git add internal/config/
git commit -m "feat(config): add remote_update section (schema v2)"
```

---

### Task 2: paths — service staging dir + result path

**Files:**
- Modify: `internal/paths/paths.go`
- Test: `internal/paths/paths_test.go` (append; create if absent)

**Interfaces:**
- Produces: `paths.ServiceUpdateStagingDir() string`, `paths.EnsureServiceUpdateStagingDir() (string, error)`, `paths.UpdateResultPath() string`.

- [ ] **Step 1: Write the failing test**

Append (or create `internal/paths/paths_test.go` with package `paths`):

```go
func TestServiceUpdatePaths_HonorDataDirOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SERIALHOP_DATA_DIR", dir)

	if got := ServiceUpdateStagingDir(); got != filepath.Join(dir, "updates") {
		t.Errorf("ServiceUpdateStagingDir = %q, want %q", got, filepath.Join(dir, "updates"))
	}
	if got := UpdateResultPath(); got != filepath.Join(dir, "update_result.json") {
		t.Errorf("UpdateResultPath = %q, want %q", got, filepath.Join(dir, "update_result.json"))
	}
	staged, err := EnsureServiceUpdateStagingDir()
	if err != nil {
		t.Fatalf("EnsureServiceUpdateStagingDir: %v", err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Errorf("staging dir not created: %v", err)
	}
}
```

Ensure the test file imports `os`, `path/filepath`, `testing`.

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/paths/ -run ServiceUpdatePaths -v`
Expected: FAIL (undefined functions).

- [ ] **Step 3: Implement**

Append to `internal/paths/paths.go` (uses the existing `DataDir()` which already honors `SERIALHOP_DATA_DIR`):

```go
// ServiceUpdateStagingDir is where the LocalSystem service stages a downloaded
// update binary (SerialHop-v*.exe) before spawning the elevated swap child.
// Under %ProgramData% (not %LOCALAPPDATA%, whose LocalSystem expansion is the
// awkward systemprofile path). SERIALHOP_DATA_DIR overrides for tests.
func ServiceUpdateStagingDir() string {
	return filepath.Join(DataDir(), "updates")
}

// EnsureServiceUpdateStagingDir creates ServiceUpdateStagingDir (0o750) and
// returns it.
func EnsureServiceUpdateStagingDir() (string, error) {
	d := ServiceUpdateStagingDir()
	if err := os.MkdirAll(d, 0o750); err != nil {
		return "", fmt.Errorf("create service update staging dir %s: %w", d, err)
	}
	return d, nil
}

// UpdateResultPath is the JSON file recording the last remote-update outcome,
// read by GET /agent/update/status. Written by both the service (progress) and
// the elevated child (terminal state).
func UpdateResultPath() string {
	return filepath.Join(DataDir(), "update_result.json")
}
```

Confirm `paths.go` already imports `fmt`, `os`, `path/filepath` (it does for `EnsureDirs`); if not, add them.

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/paths/ -v`
Expected: PASS.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/paths/ && go vet ./internal/paths/
git add internal/paths/
git commit -m "feat(paths): service update staging dir + result path"
```

---

### Task 3: updater — `ReleasesByTagURL`

**Files:**
- Modify: `internal/updater/release.go`
- Test: `internal/updater/release_test.go` (append)

**Interfaces:**
- Produces: `updater.ReleasesByTagURL(tag string) string`. (The existing `LatestRelease(ctx, hc, url, ua)` already takes a URL, so tag lookup is just `LatestRelease(ctx, hc, ReleasesByTagURL(tag), ua)` — no new fetch fn.)

- [ ] **Step 1: Write the failing test**

Append to `internal/updater/release_test.go`:

```go
func TestReleasesByTagURL(t *testing.T) {
	got := ReleasesByTagURL("v2.3.0")
	want := "https://api.github.com/repos/bioexperiment-lab-devices/serialhop/releases/tags/v2.3.0"
	if got != want {
		t.Errorf("ReleasesByTagURL = %q, want %q", got, want)
	}
}

func TestLatestRelease_TagURL_404Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := LatestRelease(context.Background(), srv.Client(), srv.URL+"/tags/v9.9.9", "ua")
	if err == nil {
		t.Fatal("expected error for 404 tag lookup")
	}
}
```

Ensure imports include `context`, `net/http`, `net/http/httptest`, `testing`.

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/updater/ -run 'ReleasesByTagURL|TagURL' -v`
Expected: FAIL (`ReleasesByTagURL` undefined).

- [ ] **Step 3: Implement**

Add to `internal/updater/release.go` (next to `DefaultReleasesURL`):

```go
// ReleasesByTagURL is the GitHub API endpoint for a specific release tag,
// e.g. ReleasesByTagURL("v2.3.0"). Pass the result to LatestRelease, which
// decodes the same Release shape for a tag as for /releases/latest.
func ReleasesByTagURL(tag string) string {
	return "https://api.github.com/repos/bioexperiment-lab-devices/serialhop/releases/tags/" + tag
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/updater/ -v`
Expected: PASS.

- [ ] **Step 5: commit**

```bash
gofmt -w internal/updater/ && go vet ./internal/updater/
git add internal/updater/
git commit -m "feat(updater): ReleasesByTagURL for tag-pinned lookup"
```

---

### Task 4: `internal/updateresult` — shared result type + atomic IO

**Files:**
- Create: `internal/updateresult/result.go`
- Test: `internal/updateresult/result_test.go`

**Interfaces:**
- Produces: package `updateresult` with `Result` struct, state constants, `Read(path) (Result, error)`, `Write(path string, r Result) error`. Leaf package (imports only stdlib). Consumed by `remoteupdate` and `winsvc`.

- [ ] **Step 1: Write the failing test**

Create `internal/updateresult/result_test.go`:

```go
package updateresult

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "update_result.json")
	in := Result{State: StateInstalling, From: "2.2.0", To: "2.3.0", StartedAt: "2026-07-21T10:00:00Z"}
	if err := Write(p, in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(p)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != in {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, in)
	}
	// no stray .partial left behind
	if _, err := os.Stat(p + ".partial"); !os.IsNotExist(err) {
		t.Errorf(".partial should not persist after Write")
	}
}

func TestReadMissingReturnsNone(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("Read missing should not error, got %v", err)
	}
	if got.State != StateNone {
		t.Errorf("missing file State = %q, want %q", got.State, StateNone)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/updateresult/ -v`
Expected: FAIL (package/symbols undefined).

- [ ] **Step 3: Implement**

Create `internal/updateresult/result.go`:

```go
// Package updateresult is the shared, restart-surviving record of the last
// remote-update outcome. Written by the service (progress) and by the elevated
// swap child (terminal state); read by GET /agent/update/status. Leaf package:
// stdlib only, so both internal/remoteupdate and internal/winsvc can import it
// without a cycle.
package updateresult

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Update result states.
const (
	StateNone        = "none"
	StateDownloading = "downloading"
	StateVerifying   = "verifying"
	StateInstalling  = "installing"
	StateSucceeded   = "succeeded"
	StateRolledBack  = "rolled_back"
	StateFailed      = "failed"
)

// Result is the JSON body of GET /agent/update/status.
type Result struct {
	State      string `json:"state"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	Pct        int    `json:"pct,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// Read returns the persisted result, or {State: "none"} if the file is absent.
// A malformed file is an error (surfaced so a status read can report it).
func Read(path string) (Result, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path is a fixed ProgramData location
	if errors.Is(err, os.ErrNotExist) {
		return Result{State: StateNone}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("read update result %s: %w", path, err)
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return Result{}, fmt.Errorf("parse update result %s: %w", path, err)
	}
	return r, nil
}

// Write atomically persists r (write to <path>.partial, fsync, rename) so a
// concurrent Read never sees a torn file.
func Write(path string, r Result) error {
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal update result: %w", err)
	}
	partial := path + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // fixed location
	if err != nil {
		return fmt.Errorf("create %s: %w", partial, err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(partial)
		return fmt.Errorf("write %s: %w", partial, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(partial)
		return fmt.Errorf("fsync %s: %w", partial, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("close %s: %w", partial, err)
	}
	if err := os.Rename(partial, path); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("rename %s -> %s: %w", partial, path, err)
	}
	return nil
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/updateresult/ -v`
Expected: PASS.

- [ ] **Step 5: commit**

```bash
gofmt -w internal/updateresult/ && go vet ./internal/updateresult/
git add internal/updateresult/
git commit -m "feat(updateresult): shared restart-surviving update result record"
```

---

### Task 5: `internal/remoteupdate` — detached spawn shim + Manager read-side

**Files:**
- Create: `internal/remoteupdate/spawn_windows.go` (`//go:build windows`)
- Create: `internal/remoteupdate/spawn_other.go` (`//go:build !windows`)
- Create: `internal/remoteupdate/manager.go`
- Test: `internal/remoteupdate/manager_test.go`

**Interfaces:**
- Produces: `remoteupdate.SpawnDetached(exe string, args []string) error`; `remoteupdate.Config`, `remoteupdate.New(Config) *Manager`; methods `(*Manager).Enabled() bool`, `Status() updateresult.Result`, `Reconcile()`; errors `ErrDisabled`, `ErrInProgress`; the in-flight guard (`tryAcquire`/`release`). (`Trigger` arrives in Task 6.)
- Consumes: `internal/updateresult` (Task 4).

- [ ] **Step 1: Write the failing test**

Create `internal/remoteupdate/manager_test.go`:

```go
package remoteupdate

import (
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/updateresult"
)

func testManager(t *testing.T, enabled bool) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	resultPath := filepath.Join(dir, "update_result.json")
	m := New(Config{
		Enabled:    enabled,
		StagingDir: dir,
		ResultPath: resultPath,
		CurVersion: "2.2.0",
		ExePath:    filepath.Join(dir, "SerialHop.exe"),
		Spawn:      func(string, []string) error { return nil },
		RunBackground: func(f func()) { f() }, // synchronous for tests
	})
	return m, resultPath
}

func TestEnabled(t *testing.T) {
	m, _ := testManager(t, false)
	if m.Enabled() {
		t.Error("Enabled should be false")
	}
}

func TestStatusNoneWhenNoFile(t *testing.T) {
	m, _ := testManager(t, true)
	if got := m.Status(); got.State != updateresult.StateNone {
		t.Errorf("Status = %q, want none", got.State)
	}
}

func TestReconcileInstallingToSucceeded(t *testing.T) {
	m, rp := testManager(t, true)
	_ = updateresult.Write(rp, updateresult.Result{State: updateresult.StateInstalling, From: "2.2.0", To: "2.2.0"})
	m.Reconcile() // CurVersion 2.2.0 == To -> succeeded
	if got := m.Status(); got.State != updateresult.StateSucceeded {
		t.Errorf("reconciled State = %q, want succeeded", got.State)
	}
}

func TestReconcileInstallingToFailed(t *testing.T) {
	m, rp := testManager(t, true)
	_ = updateresult.Write(rp, updateresult.Result{State: updateresult.StateInstalling, From: "2.2.0", To: "2.3.0"})
	m.Reconcile() // CurVersion 2.2.0 == From -> failed
	if got := m.Status(); got.State != updateresult.StateFailed {
		t.Errorf("reconciled State = %q, want failed", got.State)
	}
}

func TestGuardRejectsSecondAcquire(t *testing.T) {
	m, _ := testManager(t, true)
	if !m.tryAcquire() {
		t.Fatal("first acquire should succeed")
	}
	if m.tryAcquire() {
		t.Error("second acquire should fail while in flight")
	}
	m.release()
	if !m.tryAcquire() {
		t.Error("acquire should succeed after release")
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/remoteupdate/ -v`
Expected: FAIL (package/symbols undefined).

- [ ] **Step 3: Implement the spawn shims**

Create `internal/remoteupdate/spawn_windows.go`:

```go
//go:build windows

package remoteupdate

import (
	"fmt"
	"os/exec"

	"golang.org/x/sys/windows"
)

// SpawnDetached launches exe with args as a detached LocalSystem process that
// survives this (service) process being stopped by the SCM. No window, new
// process group, no inherited handles — so when the child stops the service,
// the child keeps running to finish the swap.
func SpawnDetached(exe string, args []string) error {
	cmd := exec.Command(exe, args...) //nolint:gosec // exe is os.Executable() of the service; args are internal flags
	cmd.SysProcAttr = &windows.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn detached update child: %w", err)
	}
	// Do not Wait: the child must outlive us. Release the process handle.
	return cmd.Process.Release()
}
```

Create `internal/remoteupdate/spawn_other.go`:

```go
//go:build !windows

package remoteupdate

import (
	"fmt"
	"runtime"
)

// SpawnDetached is a no-op stub on non-Windows: remote update is a
// Windows-only production feature. Tests inject a fake Spawn into the Manager,
// so this is never exercised by unit tests; it exists only so the package
// compiles and runs cross-platform per CLAUDE.md.
func SpawnDetached(_ string, _ []string) error {
	return fmt.Errorf("detached update spawn not supported on %s", runtime.GOOS)
}
```

- [ ] **Step 4: Implement the Manager read-side**

Create `internal/remoteupdate/manager.go`:

```go
// Package remoteupdate orchestrates admin-pushed updates: resolve source,
// download + SHA-verify, then spawn the detached elevated swap child. Reachable
// via POST /agent/update, gated by remote_update.enabled (default off) and, in
// production, by server-side admin auth. See
// docs/superpowers/specs/2026-07-21-remote-admin-update-design.md.
package remoteupdate

import (
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/bioexperiment-lab-devices/serialhop/internal/updateresult"
)

// Sentinel errors mapped to HTTP status by the api handler.
var (
	ErrDisabled   = errors.New("remote update disabled")
	ErrInProgress = errors.New("update in progress")
)

// Config constructs a Manager. Zero optional fields get production defaults in
// New; tests override HTTPClient/ReleasesURL/TagURL/Spawn/RunBackground.
type Config struct {
	Enabled    bool
	HTTPClient *http.Client
	StagingDir string
	ResultPath string
	CurVersion string // version.Base()
	UserAgent  string
	ExePath    string // service exe to re-launch as the swap child

	// Optional test seams.
	ReleasesURL   string                        // default updater.DefaultReleasesURL
	TagURL        func(tag string) string       // default updater.ReleasesByTagURL
	Spawn         func(exe string, a []string) error // default SpawnDetached
	RunBackground func(func())                  // default: go f()
}

type Manager struct {
	cfg Config

	mu       sync.Mutex
	inFlight bool
}

// New fills defaults and returns a Manager. Safe to call with Enabled=false.
func New(c Config) *Manager {
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{}
	}
	if c.ReleasesURL == "" {
		c.ReleasesURL = defaultReleasesURL
	}
	if c.TagURL == nil {
		c.TagURL = defaultTagURL
	}
	if c.Spawn == nil {
		c.Spawn = SpawnDetached
	}
	if c.RunBackground == nil {
		c.RunBackground = func(f func()) { go f() }
	}
	return &Manager{cfg: c}
}

// Enabled reports whether remote update is turned on.
func (m *Manager) Enabled() bool { return m != nil && m.cfg.Enabled }

// Status returns the last-known result (or {State:"none"}). A malformed result
// file surfaces as a failed record rather than an error to the caller.
func (m *Manager) Status() updateresult.Result {
	r, err := updateresult.Read(m.cfg.ResultPath)
	if err != nil {
		slog.Warn("remote_update status read failed", "err", err.Error())
		return updateresult.Result{State: updateresult.StateFailed, Error: err.Error()}
	}
	return r
}

// Reconcile fixes a result stuck at "installing" (child died before writing a
// terminal state) by comparing the running version to the recorded to/from.
// Called once at service startup.
func (m *Manager) Reconcile() {
	r, err := updateresult.Read(m.cfg.ResultPath)
	if err != nil || r.State != updateresult.StateInstalling {
		return
	}
	switch m.cfg.CurVersion {
	case r.To:
		r.State = updateresult.StateSucceeded
	case r.From:
		r.State = updateresult.StateFailed
		r.Error = "install did not complete (reconciled at startup)"
	default:
		return
	}
	if err := updateresult.Write(m.cfg.ResultPath, r); err != nil {
		slog.Warn("remote_update reconcile write failed", "err", err.Error())
		return
	}
	slog.Info("remote_update reconciled", "state", r.State, "version", m.cfg.CurVersion)
}

// tryAcquire sets inFlight if it is not already; returns false if a job is
// already running.
func (m *Manager) tryAcquire() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inFlight {
		return false
	}
	m.inFlight = true
	return true
}

func (m *Manager) release() {
	m.mu.Lock()
	m.inFlight = false
	m.mu.Unlock()
}
```

Add a tiny indirection file so `manager.go` doesn't import `updater` yet (keeps this task's build minimal) — create `internal/remoteupdate/deps.go`:

```go
package remoteupdate

import "github.com/bioexperiment-lab-devices/serialhop/internal/updater"

// Indirection so the default release URLs live in one place and tests can
// override via Config without importing updater.
var (
	defaultReleasesURL = updater.DefaultReleasesURL
	defaultTagURL      = updater.ReleasesByTagURL
)
```

- [ ] **Step 5: Run, verify pass (all platforms compile)**

Run: `go test ./internal/remoteupdate/ -v && GOOS=windows go build ./internal/remoteupdate/`
Expected: PASS; Windows cross-build succeeds.

- [ ] **Step 6: commit**

```bash
gofmt -w internal/remoteupdate/ && go vet ./internal/remoteupdate/
git add internal/remoteupdate/
git commit -m "feat(remoteupdate): detached spawn shim + manager read-side"
```

---

### Task 6: `internal/remoteupdate` — `Trigger` + source resolution + background job

**Files:**
- Create: `internal/remoteupdate/trigger.go`
- Test: `internal/remoteupdate/trigger_test.go`

**Interfaces:**
- Consumes: `Manager` guard/config (Task 5), `updater.LatestRelease`/`Download`/`VerifyFile` (existing), `updateresult` (Task 4).
- Produces: `Request{Version, URL, SHA256 string}`; `Accepted{To string, Noop bool, Reason string}`; `BadRequestError`, `UpstreamError`; `(*Manager).Trigger(ctx, Request) (Accepted, error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/remoteupdate/trigger_test.go`:

```go
package remoteupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/updateresult"
)

// fakeGitHub serves /latest and /tags/<t> plus asset + sums files.
func fakeGitHub(t *testing.T, version, exeBody string) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256([]byte(exeBody))
	asset := "SerialHop-v" + version + ".exe"
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)
	mux := http.NewServeMux()
	var base string
	relJSON := func() string {
		return fmt.Sprintf(`{"tag_name":"v%s","html_url":"h","assets":[
			{"name":%q,"browser_download_url":%q,"size":%d},
			{"name":"SHA256SUMS.txt","browser_download_url":%q,"size":%d}]}`,
			version, asset, base+"/dl/"+asset, len(exeBody), base+"/dl/SHA256SUMS.txt", len(sums))
	}
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, relJSON()) })
	mux.HandleFunc("/tags/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, relJSON()) })
	mux.HandleFunc("/dl/"+asset, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, exeBody) })
	mux.HandleFunc("/dl/SHA256SUMS.txt", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, sums) })
	srv := httptest.NewServer(mux)
	base = srv.URL
	return srv
}

type spySpawn struct {
	mu   sync.Mutex
	args []string
}

func (s *spySpawn) fn(_ string, a []string) error {
	s.mu.Lock()
	s.args = a
	s.mu.Unlock()
	return nil
}

func triggerManager(t *testing.T, gh *httptest.Server, spawn func(string, []string) error) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	rp := filepath.Join(dir, "update_result.json")
	return New(Config{
		Enabled:       true,
		HTTPClient:    gh.Client(),
		StagingDir:    dir,
		ResultPath:    rp,
		CurVersion:    "2.2.0",
		ExePath:       filepath.Join(dir, "SerialHop.exe"),
		ReleasesURL:   gh.URL + "/latest",
		TagURL:        func(tag string) string { return gh.URL + "/tags/" + tag },
		Spawn:         spawn,
		RunBackground: func(f func()) { f() }, // synchronous
	}), rp
}

func TestTriggerDisabled(t *testing.T) {
	m, _ := testManager(t, false)
	_, err := m.Trigger(context.Background(), Request{})
	if !errors.Is(err, ErrDisabled) {
		t.Errorf("err = %v, want ErrDisabled", err)
	}
}

func TestTriggerLatestSpawnsChild(t *testing.T) {
	gh := fakeGitHub(t, "2.3.0", "new-binary-bytes")
	defer gh.Close()
	spy := &spySpawn{}
	m, rp := triggerManager(t, gh, spy.fn)

	acc, err := m.Trigger(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if acc.To != "2.3.0" || acc.Noop {
		t.Errorf("acc = %+v, want To=2.3.0 Noop=false", acc)
	}
	if got := m.Status(); got.State != updateresult.StateInstalling {
		t.Errorf("post-job State = %q, want installing", got.State)
	}
	spy.mu.Lock()
	defer spy.mu.Unlock()
	assertArg(t, spy.args, "--update-src", "SerialHop-v2.3.0.exe")
	assertArg(t, spy.args, "--update-to", "2.3.0")
	assertArg(t, spy.args, "--update-from", "2.2.0")
	assertHasArg(t, spy.args, "--admin-action=update")
}

func TestTriggerNoopWhenSameVersion(t *testing.T) {
	gh := fakeGitHub(t, "2.2.0", "x") // == CurVersion
	defer gh.Close()
	spy := &spySpawn{}
	m, _ := triggerManager(t, gh, spy.fn)
	acc, err := m.Trigger(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !acc.Noop {
		t.Error("expected Noop for same version")
	}
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if spy.args != nil {
		t.Error("no child should be spawned for noop")
	}
}

func TestTriggerChecksumMismatchFails(t *testing.T) {
	gh := fakeGitHub(t, "2.3.0", "good")
	defer gh.Close()
	m, rp := triggerManager(t, gh, func(string, []string) error { t.Fatal("must not spawn"); return nil })
	// Poison: custom URL to the real asset but a wrong sha256.
	_, err := m.Trigger(context.Background(), Request{
		URL: gh.URL + "/dl/SerialHop-v2.3.0.exe", SHA256: hex.EncodeToString(make([]byte, 32)),
	})
	if err != nil {
		t.Fatalf("Trigger (custom) should be accepted then fail in job: %v", err)
	}
	if got := m.Status(); got.State != updateresult.StateFailed {
		t.Errorf("State = %q, want failed", got.State)
	}
	_ = rp
}

func TestTriggerRejectsHTTPURL(t *testing.T) {
	m, _ := testManager(t, true)
	_, err := m.Trigger(context.Background(), Request{URL: "http://x/SerialHop-v2.3.0.exe", SHA256: "ab"})
	var bad *BadRequestError
	if !errors.As(err, &bad) {
		t.Errorf("err = %v, want BadRequestError", err)
	}
}

func TestTriggerCustomURLNeedsSHA(t *testing.T) {
	m, _ := testManager(t, true)
	_, err := m.Trigger(context.Background(), Request{URL: "https://x/SerialHop-v2.3.0.exe"})
	var bad *BadRequestError
	if !errors.As(err, &bad) {
		t.Errorf("err = %v, want BadRequestError (missing sha256)", err)
	}
}

func TestTriggerInProgress(t *testing.T) {
	m, _ := testManager(t, true)
	if !m.tryAcquire() {
		t.Fatal("acquire")
	}
	defer m.release()
	_, err := m.Trigger(context.Background(), Request{})
	if !errors.Is(err, ErrInProgress) {
		t.Errorf("err = %v, want ErrInProgress", err)
	}
}

func assertArg(t *testing.T, args []string, flag, mustContain string) {
	t.Helper()
	for _, a := range args {
		if len(a) > len(flag) && a[:len(flag)+1] == flag+"=" {
			if mustContain != "" && !contains(a, mustContain) {
				t.Errorf("%s = %q, want to contain %q", flag, a, mustContain)
			}
			return
		}
	}
	t.Errorf("missing arg %s in %v", flag, args)
}
func assertHasArg(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if a == want {
			return
		}
	}
	t.Errorf("missing arg %q in %v", want, args)
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/remoteupdate/ -run Trigger -v`
Expected: FAIL (`Trigger`, `Request`, `Accepted`, `BadRequestError` undefined).

- [ ] **Step 3: Implement**

Create `internal/remoteupdate/trigger.go`:

```go
package remoteupdate

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/updater"
	"github.com/bioexperiment-lab-devices/serialhop/internal/updateresult"
)

// Request selects the update source. Empty => GitHub latest.
type Request struct {
	Version string // optional; "vX.Y.Z" or "X.Y.Z"
	URL     string // optional; https custom mirror
	SHA256  string // required iff URL set (64 hex chars)
}

// Accepted is the trigger outcome. Noop=true means already at the target.
type Accepted struct {
	To     string
	Noop   bool
	Reason string
}

// BadRequestError => HTTP 400.
type BadRequestError struct{ Msg string }

func (e *BadRequestError) Error() string { return e.Msg }

// UpstreamError => HTTP 502 (GitHub release/tag lookup failed synchronously).
type UpstreamError struct{ Err error }

func (e *UpstreamError) Error() string { return "release lookup failed: " + e.Err.Error() }
func (e *UpstreamError) Unwrap() error { return e.Err }

const jobTimeout = 5 * time.Minute

var (
	semverRe   = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	assetNameRe = regexp.MustCompile(`^SerialHop-v(\d+\.\d+\.\d+)\.exe$`)
)

// plan is the fully-resolved work handed to the background job.
type plan struct {
	from, to   string // dotted X.Y.Z (no leading v)
	assetName  string // SerialHop-v<to>.exe
	stagedPath string
	assetURL   string // where to download the .exe from
	sumsBody   string // "<hex>  <assetName>" for VerifyFile
}

// Trigger validates+resolves the request, then (unless noop) launches the
// background download/verify/spawn job. Returns immediately.
func (m *Manager) Trigger(ctx context.Context, req Request) (Accepted, error) {
	if !m.Enabled() {
		return Accepted{}, ErrDisabled
	}
	if !m.tryAcquire() {
		return Accepted{}, ErrInProgress
	}
	pl, acc, err := m.resolve(ctx, req)
	if err != nil || acc.Noop {
		m.release()
		return acc, err
	}
	// Record initial state before the job starts.
	_ = updateresult.Write(m.cfg.ResultPath, updateresult.Result{
		State: updateresult.StateDownloading, From: pl.from, To: pl.to,
		StartedAt: nowRFC3339(),
	})
	slog.Info("remote_update triggered", "to", pl.to, "from", pl.from,
		"custom", req.URL != "")
	m.cfg.RunBackground(func() {
		defer m.release()
		m.runJob(pl)
	})
	return Accepted{To: pl.to}, nil
}

// resolve validates the request and, for GitHub modes, does the synchronous
// release lookup + noop check.
func (m *Manager) resolve(ctx context.Context, req Request) (plan, Accepted, error) {
	if req.URL != "" {
		return m.resolveCustom(req)
	}
	return m.resolveGitHub(ctx, req)
}

func (m *Manager) resolveCustom(req Request) (plan, Accepted, error) {
	if !strings.HasPrefix(req.URL, "https://") {
		return plan{}, Accepted{}, &BadRequestError{Msg: "url must be https://"}
	}
	if len(req.SHA256) != 64 || !isHex(req.SHA256) {
		return plan{}, Accepted{}, &BadRequestError{Msg: "sha256 must be 64 hex chars when url is set"}
	}
	ver, err := customVersion(req)
	if err != nil {
		return plan{}, Accepted{}, err
	}
	asset := "SerialHop-v" + ver + ".exe"
	return plan{
		from: m.cfg.CurVersion, to: ver, assetName: asset,
		stagedPath: filepath.Join(m.cfg.StagingDir, asset),
		assetURL:   req.URL,
		sumsBody:   strings.ToLower(req.SHA256) + "  " + asset,
	}, Accepted{}, nil
}

func (m *Manager) resolveGitHub(ctx context.Context, req Request) (plan, Accepted, error) {
	url := m.cfg.ReleasesURL
	if req.Version != "" {
		v, err := normalizeVersion(req.Version)
		if err != nil {
			return plan{}, Accepted{}, err
		}
		url = m.cfg.TagURL("v" + v)
	}
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rel, err := updater.LatestRelease(lctx, m.cfg.HTTPClient, url, m.cfg.UserAgent)
	if err != nil {
		return plan{}, Accepted{}, &UpstreamError{Err: err}
	}
	ver := strings.TrimPrefix(rel.TagName, "v")
	if !semverRe.MatchString(ver) {
		return plan{}, Accepted{}, &UpstreamError{Err: fmt.Errorf("release tag %q not X.Y.Z", rel.TagName)}
	}
	if cmp, err := updater.Compare(ver, m.cfg.CurVersion); err == nil && cmp == 0 {
		return plan{}, Accepted{To: ver, Noop: true, Reason: "already at " + ver}, nil
	}
	asset := "SerialHop-v" + ver + ".exe"
	a := rel.AssetByName(asset)
	sums := rel.AssetByName("SHA256SUMS.txt")
	if a == nil || sums == nil {
		return plan{}, Accepted{}, &UpstreamError{Err: fmt.Errorf("release %s missing %s or SHA256SUMS.txt", ver, asset)}
	}
	body, err := m.fetchText(ctx, sums.BrowserDownloadURL)
	if err != nil {
		return plan{}, Accepted{}, &UpstreamError{Err: fmt.Errorf("fetch sums: %w", err)}
	}
	return plan{
		from: m.cfg.CurVersion, to: ver, assetName: asset,
		stagedPath: filepath.Join(m.cfg.StagingDir, asset),
		assetURL:   a.BrowserDownloadURL, sumsBody: body,
	}, Accepted{}, nil
}

// runJob downloads, verifies, then spawns the detached swap child. Writes the
// result-file state at each transition. On download/verify failure it is the
// terminal writer (no child spawned).
func (m *Manager) runJob(pl plan) {
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()

	lastPct := -1
	progress := func(recv, total int64) {
		if total <= 0 {
			return
		}
		pct := int(recv * 100 / total)
		if pct/5 == lastPct/5 {
			return
		}
		lastPct = pct
		_ = updateresult.Write(m.cfg.ResultPath, updateresult.Result{
			State: updateresult.StateDownloading, From: pl.from, To: pl.to, Pct: pct,
			StartedAt: m.startedAt(),
		})
	}

	if err := updater.Download(ctx, m.cfg.HTTPClient, pl.assetURL, pl.stagedPath, m.cfg.UserAgent, progress); err != nil {
		m.fail(pl, "download: "+err.Error())
		return
	}
	_ = updateresult.Write(m.cfg.ResultPath, updateresult.Result{
		State: updateresult.StateVerifying, From: pl.from, To: pl.to, StartedAt: m.startedAt(),
	})
	if err := updater.VerifyFile(pl.stagedPath, pl.sumsBody, pl.assetName); err != nil {
		_ = removeQuiet(pl.stagedPath)
		m.fail(pl, "verify: "+err.Error())
		return
	}
	_ = updateresult.Write(m.cfg.ResultPath, updateresult.Result{
		State: updateresult.StateInstalling, From: pl.from, To: pl.to, StartedAt: m.startedAt(),
	})
	args := []string{
		"--admin-action=update",
		"--update-src=" + pl.stagedPath,
		"--update-result=" + m.cfg.ResultPath,
		"--update-from=" + pl.from,
		"--update-to=" + pl.to,
	}
	if err := m.cfg.Spawn(m.cfg.ExePath, args); err != nil {
		m.fail(pl, "spawn: "+err.Error())
		return
	}
	slog.Info("remote_update spawned child", "to", pl.to)
}

func (m *Manager) fail(pl plan, msg string) {
	slog.Warn("remote_update failed", "to", pl.to, "err", msg)
	_ = updateresult.Write(m.cfg.ResultPath, updateresult.Result{
		State: updateresult.StateFailed, From: pl.from, To: pl.to, Error: msg,
		StartedAt: m.startedAt(), FinishedAt: nowRFC3339(),
	})
}

// startedAt preserves the original started_at across state writes.
func (m *Manager) startedAt() string {
	if r, err := updateresult.Read(m.cfg.ResultPath); err == nil && r.StartedAt != "" {
		return r.StartedAt
	}
	return nowRFC3339()
}

func (m *Manager) fetchText(ctx context.Context, url string) (string, error) {
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(fctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", m.cfg.UserAgent)
	resp, err := m.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(b), err
}

func customVersion(req Request) (string, error) {
	if req.Version != "" {
		return normalizeVersion(req.Version)
	}
	base := path.Base(req.URL)
	if mm := assetNameRe.FindStringSubmatch(base); mm != nil {
		return mm[1], nil
	}
	return "", &BadRequestError{Msg: "custom url needs a version: set \"version\" or name the file SerialHop-vX.Y.Z.exe"}
}

func normalizeVersion(s string) (string, error) {
	v := strings.TrimPrefix(s, "v")
	if !semverRe.MatchString(v) {
		return "", &BadRequestError{Msg: fmt.Sprintf("version %q must be X.Y.Z", s)}
	}
	return v, nil
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func removeQuiet(p string) error { return osRemove(p) }
```

Create `internal/remoteupdate/time.go` (isolates the two impure calls so the rest is pure):

```go
package remoteupdate

import (
	"os"
	"time"
)

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

var osRemove = os.Remove
```

- [ ] **Step 4: Run, verify pass (all platforms compile)**

Run: `go test ./internal/remoteupdate/ -race -v && GOOS=windows go build ./internal/remoteupdate/`
Expected: PASS; Windows cross-build succeeds.

- [ ] **Step 5: gofmt + vet + commit**

```bash
gofmt -w internal/remoteupdate/ && go vet ./internal/remoteupdate/
git add internal/remoteupdate/
git commit -m "feat(remoteupdate): Trigger, source resolution, background job"
```

---

### Task 7: winsvc — child writes the result file (+ signature & main flags)

**Files:**
- Modify: `internal/winsvc/control.go`
- Modify: `main.go`
- Test: `internal/winsvc/control_test.go` (append)

**Interfaces:**
- Consumes: `internal/updateresult` (Task 4).
- Produces: `winsvc.RunAdminAction(action, errorFile, updateSrc, resultPath, fromVersion, toVersion string) int`. When `resultPath != ""`, the `update` action writes `installing` on entry and `succeeded`/`rolled_back` at the end. Empty `resultPath` ⇒ no result file (panel path unchanged).

- [ ] **Step 1: Write the failing tests**

Append to `internal/winsvc/control_test.go`:

```go
func TestRunUpdate_WritesSucceededResult(t *testing.T) {
	dir := t.TempDir()
	rp := filepath.Join(dir, "update_result.json")
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{
		state:            StateRunning,
		stateProgression: []ServiceState{StateRunning, StateStopped, StateStartPending, StateRunning},
	}
	fs := newFakeFS(filepath.Join(dir, "SerialHop.exe"), filepath.Join(dir, "SerialHop-v2.3.0.exe"))

	err := runUpdateWithResult(scm, fs, filepath.Join(dir, "SerialHop-v2.3.0.exe"),
		filepath.Join(dir, "SerialHop.exe"), rp, "2.2.0", "2.3.0",
		100*time.Millisecond, time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("runUpdateWithResult: %v", err)
	}
	got, _ := updateresult.Read(rp)
	if got.State != updateresult.StateSucceeded || got.To != "2.3.0" || got.From != "2.2.0" {
		t.Errorf("result = %+v, want succeeded 2.2.0->2.3.0", got)
	}
	if got.FinishedAt == "" {
		t.Error("FinishedAt should be set")
	}
}

func TestRunUpdate_WritesRolledBackOnFailure(t *testing.T) {
	dir := t.TempDir()
	rp := filepath.Join(dir, "update_result.json")
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{state: StateStopped}
	fs := newFakeFS(filepath.Join(dir, "SerialHop.exe"), filepath.Join(dir, "SerialHop-v2.3.0.exe"))
	fs.failRenameTo = filepath.Join(dir, "SerialHop.exe") // src->target rename fails

	_ = runUpdateWithResult(scm, fs, filepath.Join(dir, "SerialHop-v2.3.0.exe"),
		filepath.Join(dir, "SerialHop.exe"), rp, "2.2.0", "2.3.0",
		100*time.Millisecond, time.Millisecond, time.Millisecond)
	got, _ := updateresult.Read(rp)
	if got.State != updateresult.StateRolledBack {
		t.Errorf("result State = %q, want rolled_back", got.State)
	}
	if got.Error == "" {
		t.Error("rolled_back result must include error")
	}
}

func TestRunUpdate_NoResultPathWritesNothing(t *testing.T) {
	dir := t.TempDir()
	scm := newFakeSCM()
	scm.services[ServiceName] = &fakeService{state: StateStopped}
	fs := newFakeFS(filepath.Join(dir, "SerialHop.exe"), filepath.Join(dir, "SerialHop-v2.3.0.exe"))
	err := runUpdateWithResult(scm, fs, filepath.Join(dir, "SerialHop-v2.3.0.exe"),
		filepath.Join(dir, "SerialHop.exe"), "", "2.2.0", "2.3.0",
		100*time.Millisecond, time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("runUpdateWithResult: %v", err)
	}
	// no file at the default location — nothing to assert beyond no panic; the
	// panel path must not write a result file.
}
```

> If `fakeFS` has no `failRenameTo` seam, add it: a `failRenameTo string` field, and in `Rename` return `os.ErrPermission` when `to == f.failRenameTo`. Keep the existing behavior otherwise.

Add `"github.com/bioexperiment-lab-devices/serialhop/internal/updateresult"` and `"path/filepath"` to the test imports if missing.

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/winsvc/ -run RunUpdate -v`
Expected: FAIL (`runUpdateWithResult` undefined).

- [ ] **Step 3: Implement in `control.go`**

Add the import `"github.com/bioexperiment-lab-devices/serialhop/internal/updateresult"` and `"time"` (already present). Extend `RunAdminAction`:

```go
func RunAdminAction(action, errorFile, updateSrc, resultPath, fromVersion, toVersion string) int {
	err := func() error {
		scm, err := DialSCM()
		if err != nil {
			return fmt.Errorf("connect SCM: %w", err)
		}
		defer scm.Disconnect() //nolint:errcheck
		switch action {
		case "install":
			exePath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate executable: %w", err)
			}
			return install(scm, exePath)
		case "uninstall":
			return uninstall(scm, productionStopTimeout, productionPollInterval)
		case "restart":
			return restart(scm, productionStartTimeout, productionPollInterval)
		case "update":
			return runUpdate(scm, updateSrc, resultPath, fromVersion, toVersion)
		default:
			return fmt.Errorf("unknown action %q", action)
		}
	}()
	if err != nil {
		_ = os.WriteFile(errorFile, []byte(err.Error()), 0o600)
		return 1
	}
	return 0
}
```

Replace `runUpdate` with a form that resolves exe/timeouts then delegates to `runUpdateWithResult`:

```go
func runUpdate(scm SCMConn, updateSrc, resultPath, fromVersion, toVersion string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	return runUpdateWithResult(scm, realFS{}, updateSrc, exePath, resultPath, fromVersion, toVersion,
		productionStartTimeout, productionPollInterval, 250*time.Millisecond)
}

// runUpdateWithResult wraps runUpdateWithDeps, writing the update-result file
// when resultPath != "". Empty resultPath preserves the panel path exactly
// (no file written). The result write is best-effort — it never changes the
// update's own success/failure.
func runUpdateWithResult(scm SCMConn, fs FS, updateSrc, exePath, resultPath, fromVersion, toVersion string,
	opTimeout, pollInterval, renameBackoff time.Duration) error {

	if resultPath != "" {
		writeUpdateResult(resultPath, updateresult.StateInstalling, fromVersion, toVersion, "")
	}
	err := runUpdateWithDeps(scm, fs, updateSrc, exePath, opTimeout, pollInterval, renameBackoff)
	if resultPath != "" {
		if err != nil {
			writeUpdateResult(resultPath, updateresult.StateRolledBack, fromVersion, toVersion, err.Error())
		} else {
			writeUpdateResult(resultPath, updateresult.StateSucceeded, fromVersion, toVersion, "")
		}
	}
	return err
}

// writeUpdateResult reads-preserving started_at, sets terminal fields, writes.
func writeUpdateResult(path, state, from, to, errMsg string) {
	r, _ := updateresult.Read(path)
	r.State, r.From, r.To, r.Error = state, from, to, errMsg
	now := time.Now().UTC().Format(time.RFC3339)
	if r.StartedAt == "" {
		r.StartedAt = now
	}
	if state != updateresult.StateInstalling {
		r.FinishedAt = now
	}
	if err := updateresult.Write(path, r); err != nil {
		// best-effort; the update outcome itself is unchanged.
		return
	}
}
```

> Note: the existing `runUpdate(scm, updateSrc)` signature is replaced. Its only caller is `RunAdminAction` (updated above).

- [ ] **Step 4: Update `main.go` call site + flags**

In `main.go`, add three flags next to `flagUpdateSrc`:

```go
	flagUpdateResult = flag.String("update-result", "", "internal: path the update child writes its result JSON to")
	flagUpdateFrom   = flag.String("update-from", "", "internal: version being replaced (for the result record)")
	flagUpdateTo     = flag.String("update-to", "", "internal: version being installed (for the result record)")
```

Update the dispatch call:

```go
		os.Exit(winsvc.RunAdminAction(*flagAdminAction, *flagErrorFile, *flagUpdateSrc,
			*flagUpdateResult, *flagUpdateFrom, *flagUpdateTo))
```

- [ ] **Step 5: Run, verify pass**

Run: `go test ./internal/winsvc/ ./... -run 'RunUpdate|UpdateBinary' -v && go build ./...`
Expected: PASS; whole module builds (main.go call site updated).

- [ ] **Step 6: commit**

```bash
gofmt -w internal/winsvc/ main.go && go vet ./internal/winsvc/ .
git add internal/winsvc/ main.go
git commit -m "feat(winsvc): update child writes restart-surviving result file"
```

---

### Task 8: api — endpoints + Server wiring (app passes nil for now)

**Files:**
- Modify: `internal/api/handlers.go`, `internal/api/types.go`
- Modify: `internal/app/app.go` (pass `nil` for the new param — real manager in Task 9)
- Test: `internal/api/agentupdate_test.go` (create)

**Interfaces:**
- Consumes: `remoteupdate.Manager`, `Request`, `Accepted`, sentinels/typed errors (Task 6).
- Produces: `api.UpdateRequest`, `api.UpdateAcceptedBody`, `api.UpdateNoopBody`; `Server.remoteUpdate *remoteupdate.Manager`; `api.New(…, ru *remoteupdate.Manager)`; routes `POST /agent/update`, `GET /agent/update/status`.

- [ ] **Step 1: Write the failing tests**

Create `internal/api/agentupdate_test.go`:

```go
package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/remoteupdate"
	"github.com/bioexperiment-lab-devices/serialhop/internal/updateresult"
)

func enabledMgr(t *testing.T, cur string) *remoteupdate.Manager {
	t.Helper()
	dir := t.TempDir()
	return remoteupdate.New(remoteupdate.Config{
		Enabled: true, StagingDir: dir,
		ResultPath: filepath.Join(dir, "update_result.json"),
		CurVersion: cur, ExePath: filepath.Join(dir, "SerialHop.exe"),
		Spawn:         func(string, []string) error { return nil },
		RunBackground: func(f func()) { f() },
	})
}

// serverWith builds a Server exposing only the remote-update wiring under test.
func serverWith(mgr *remoteupdate.Manager) *Server {
	return &Server{remoteUpdate: mgr}
}

func TestPostAgentUpdate_DisabledIs404(t *testing.T) {
	s := serverWith(nil) // no manager => disabled
	rr := httptest.NewRecorder()
	s.handlePostAgentUpdate(rr, httptest.NewRequest(http.MethodPost, "/agent/update", strings.NewReader("{}")))
	if rr.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rr.Code)
	}
}

func TestPostAgentUpdate_BadURLIs400(t *testing.T) {
	s := serverWith(enabledMgr(t, "2.2.0"))
	rr := httptest.NewRecorder()
	body := `{"url":"http://x/SerialHop-v2.3.0.exe","sha256":"ab"}`
	s.handlePostAgentUpdate(rr, httptest.NewRequest(http.MethodPost, "/agent/update", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rr.Code)
	}
}

func TestPostAgentUpdate_InProgressIs409(t *testing.T) {
	mgr := enabledMgr(t, "2.2.0")
	// Force in-flight via a real trigger that never releases: use a manager
	// whose background runner blocks. Simpler: exercise the sentinel mapping.
	s := serverWith(mgr)
	// Prime a lock by launching a job that blocks the guard.
	// Use the exported behavior: two immediate triggers with a slow spawn.
	// Here we assert the sentinel mapping via a stub error path instead:
	if got := statusForTriggerErr(remoteupdate.ErrInProgress); got != http.StatusConflict {
		t.Errorf("ErrInProgress maps to %d, want 409", got)
	}
	_ = s
}

func TestGetAgentUpdateStatus_Enabled200(t *testing.T) {
	s := serverWith(enabledMgr(t, "2.2.0"))
	rr := httptest.NewRecorder()
	s.handleGetAgentUpdateStatus(rr, httptest.NewRequest(http.MethodGet, "/agent/update/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), updateresult.StateNone) {
		t.Errorf("status body = %s, want state none", rr.Body.String())
	}
}

func TestStatusForTriggerErr_Mapping(t *testing.T) {
	cases := map[error]int{
		remoteupdate.ErrDisabled:              http.StatusNotFound,
		remoteupdate.ErrInProgress:            http.StatusConflict,
		&remoteupdate.BadRequestError{Msg: "x"}: http.StatusBadRequest,
		&remoteupdate.UpstreamError{Err: errors.New("x")}: http.StatusBadGateway,
		errors.New("other"):                   http.StatusInternalServerError,
	}
	for err, want := range cases {
		if got := statusForTriggerErr(err); got != want {
			t.Errorf("statusForTriggerErr(%v) = %d, want %d", err, got, want)
		}
	}
	_ = context.Background
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/api/ -run AgentUpdate -v`
Expected: FAIL (undefined `remoteUpdate` field, handlers, `statusForTriggerErr`, DTOs).

- [ ] **Step 3: Implement DTOs (`types.go`)**

Append to `internal/api/types.go`:

```go
// UpdateRequest is the body of POST /agent/update. Empty => GitHub latest.
type UpdateRequest struct {
	Version string `json:"version,omitempty"`
	URL     string `json:"url,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

// UpdateAcceptedBody is returned 202 when a job starts.
type UpdateAcceptedBody struct {
	Accepted bool   `json:"accepted"`
	To       string `json:"to"`
}

// UpdateNoopBody is returned 200 when the target equals the running version.
type UpdateNoopBody struct {
	Outcome string `json:"outcome"` // "noop"
	Reason  string `json:"reason"`
}
```

- [ ] **Step 4: Implement handlers + wiring (`handlers.go`)**

Add the import `"github.com/bioexperiment-lab-devices/serialhop/internal/remoteupdate"` and `"errors"` and `"io"`. Add the field to `Server` and a param to `New`:

```go
type Server struct {
	reg              *registry.Registry
	discover         DiscoverFn
	opener           labserial.Opener
	flasher          flasher.Flasher
	flashingEnabled  bool
	keepAwake        power.KeepAwake
	rawSerialEnabled bool
	rawIdleTimeout   time.Duration
	remoteUpdate     *remoteupdate.Manager
}
```

```go
func New(
	reg *registry.Registry,
	discover DiscoverFn,
	opener labserial.Opener,
	fl flasher.Flasher,
	flashingEnabled bool,
	keepAwake power.KeepAwake,
	rawSerialEnabled bool,
	rawIdleTimeout time.Duration,
	remoteUpdate *remoteupdate.Manager,
) *Server {
	return &Server{
		reg: reg, discover: discover, opener: opener,
		flasher: fl, flashingEnabled: flashingEnabled, keepAwake: keepAwake,
		rawSerialEnabled: rawSerialEnabled, rawIdleTimeout: rawIdleTimeout,
		remoteUpdate: remoteUpdate,
	}
}
```

Register routes in `Handler()` (next to the `/agent/info` line):

```go
	mux.HandleFunc("POST /agent/update", s.handlePostAgentUpdate)
	mux.HandleFunc("GET /agent/update/status", s.handleGetAgentUpdateStatus)
```

Add the handlers + mapping helper (new file `internal/api/agentupdate.go`):

```go
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/bioexperiment-lab-devices/serialhop/internal/remoteupdate"
)

const maxUpdateBody = 4 * 1024

func (s *Server) handlePostAgentUpdate(w http.ResponseWriter, r *http.Request) {
	if s.remoteUpdate == nil || !s.remoteUpdate.Enabled() {
		writeError(w, http.StatusNotFound, "not found", "")
		return
	}
	var req UpdateRequest
	body := http.MaxBytesReader(w, r.Body, maxUpdateBody)
	if err := json.NewDecoder(body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request", "body is not valid JSON: "+err.Error())
		return
	}
	acc, err := s.remoteUpdate.Trigger(r.Context(), remoteupdate.Request{
		Version: req.Version, URL: req.URL, SHA256: req.SHA256,
	})
	if err != nil {
		writeError(w, statusForTriggerErr(err), triggerErrCode(err), triggerErrDetail(err))
		return
	}
	if acc.Noop {
		writeJSON(w, http.StatusOK, UpdateNoopBody{Outcome: "noop", Reason: acc.Reason})
		return
	}
	writeJSON(w, http.StatusAccepted, UpdateAcceptedBody{Accepted: true, To: acc.To})
}

func (s *Server) handleGetAgentUpdateStatus(w http.ResponseWriter, _ *http.Request) {
	if s.remoteUpdate == nil || !s.remoteUpdate.Enabled() {
		writeError(w, http.StatusNotFound, "not found", "")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.remoteUpdate.Status())
}

func statusForTriggerErr(err error) int {
	switch {
	case errors.Is(err, remoteupdate.ErrDisabled):
		return http.StatusNotFound
	case errors.Is(err, remoteupdate.ErrInProgress):
		return http.StatusConflict
	}
	var bad *remoteupdate.BadRequestError
	if errors.As(err, &bad) {
		return http.StatusBadRequest
	}
	var up *remoteupdate.UpstreamError
	if errors.As(err, &up) {
		return http.StatusBadGateway
	}
	return http.StatusInternalServerError
}

func triggerErrCode(err error) string {
	switch statusForTriggerErr(err) {
	case http.StatusConflict:
		return "update in progress"
	case http.StatusBadRequest:
		return "invalid request"
	case http.StatusBadGateway:
		return "release lookup failed"
	case http.StatusNotFound:
		return "not found"
	default:
		return "internal error"
	}
}

func triggerErrDetail(err error) string {
	var bad *remoteupdate.BadRequestError
	if errors.As(err, &bad) {
		return bad.Msg
	}
	var up *remoteupdate.UpstreamError
	if errors.As(err, &up) {
		return up.Err.Error()
	}
	return ""
}
```

- [ ] **Step 5: Keep the module compiling — update `app.go` call site**

In `internal/app/app.go`, the `api.New(...)` call gains a trailing arg. For now pass `nil` (feature disabled; Task 9 wires the real manager):

```go
	srv := api.New(reg, discoverFn, opener, fl, flashingEnabled, keepAwake,
		cfg.RawSerial.Enabled, time.Duration(cfg.RawSerial.IdleTimeoutMs)*time.Millisecond, nil)
```

Also update any other `api.New(` call sites in tests. Find them:

```bash
grep -rn "api.New(" internal/ | grep -v _test.go
grep -rn "api.New(" internal/ | grep _test.go
```

Add a trailing `, nil` argument to each existing call (tests included) so they compile.

- [ ] **Step 6: Run, verify pass**

Run: `go test ./internal/api/ -v && go build ./...`
Expected: PASS; whole module builds.

- [ ] **Step 7: commit**

```bash
gofmt -w internal/api/ internal/app/ && go vet ./internal/api/ ./internal/app/
git add internal/api/ internal/app/
git commit -m "feat(api): POST /agent/update + GET /agent/update/status"
```

---

### Task 9: app — construct the real Manager + reconcile at startup

**Files:**
- Modify: `internal/app/app.go`
- Test: `internal/app/app_test.go` (append a focused wiring assertion if the file exists; otherwise rely on build + api tests — see Step 1)

**Interfaces:**
- Consumes: `remoteupdate.New`, `remoteupdate.SpawnDetached` (Tasks 5–6); `paths.EnsureServiceUpdateStagingDir`, `paths.UpdateResultPath` (Task 2); `version.Version`/`Base` (existing); `config.RemoteUpdate` (Task 1).

- [ ] **Step 1: Write the failing test (guarded)**

If `internal/app/app_test.go` exists and can construct a `config.Config`, append:

```go
func TestBuildRemoteUpdateManager_DisabledIsNil(t *testing.T) {
	if m := buildRemoteUpdateManager(config.Config{}); m != nil {
		t.Error("disabled config should yield a nil manager")
	}
}

func TestBuildRemoteUpdateManager_EnabledNonNil(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", t.TempDir())
	cfg := config.Config{RemoteUpdate: config.RemoteUpdateConfig{Enabled: true}}
	m := buildRemoteUpdateManager(cfg)
	if m == nil || !m.Enabled() {
		t.Error("enabled config should yield an enabled manager")
	}
}
```

Add imports `config` and `testing` if needed. (If there is no `app_test.go` and creating one is heavy due to `Run`'s dependencies, skip the unit test and verify via `go build ./...` + the api tests — note this in the commit.)

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/app/ -run RemoteUpdateManager -v`
Expected: FAIL (`buildRemoteUpdateManager` undefined).

- [ ] **Step 3: Implement**

In `internal/app/app.go`, add imports:

```go
	"net/http"
	"os"

	"github.com/bioexperiment-lab-devices/serialhop/internal/remoteupdate"
	"github.com/bioexperiment-lab-devices/serialhop/internal/version"
```

Add the builder (a package-level func so it is unit-testable):

```go
// buildRemoteUpdateManager returns a configured Manager when remote update is
// enabled in config, else nil (the api handlers 404 on a nil manager). Best
// effort: if the staging dir can't be created, logs and returns nil so a
// failure here never blocks the service from starting.
func buildRemoteUpdateManager(cfg config.Config) *remoteupdate.Manager {
	if !cfg.RemoteUpdate.Enabled {
		return nil
	}
	staging, err := paths.EnsureServiceUpdateStagingDir()
	if err != nil {
		slog.Warn("remote_update disabled: staging dir", "err", err)
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		slog.Warn("remote_update disabled: executable path", "err", err)
		return nil
	}
	return remoteupdate.New(remoteupdate.Config{
		Enabled:    true,
		HTTPClient: &http.Client{},
		StagingDir: staging,
		ResultPath: paths.UpdateResultPath(),
		CurVersion: version.Base(),
		UserAgent:  "SerialHop/" + version.Version + " (remote-update)",
		ExePath:    exe,
		Spawn:      remoteupdate.SpawnDetached,
	})
}
```

In `Run`, replace the `nil` passed to `api.New` (from Task 8) with the constructed manager and reconcile at startup:

```go
	rum := buildRemoteUpdateManager(cfg)
	if rum != nil {
		rum.Reconcile()
	}
	srv := api.New(reg, discoverFn, opener, fl, flashingEnabled, keepAwake,
		cfg.RawSerial.Enabled, time.Duration(cfg.RawSerial.IdleTimeoutMs)*time.Millisecond, rum)
```

Confirm `slog` and `paths` are already imported in `app.go` (they are used elsewhere; add if not).

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/app/ ./internal/api/ -v && go build ./...`
Expected: PASS; module builds.

- [ ] **Step 5: commit**

```bash
gofmt -w internal/app/ && go vet ./internal/app/
git add internal/app/
git commit -m "feat(app): wire remote-update manager + startup reconcile"
```

---

### Task 10: Docs — README + configuration guide

**Files:**
- Modify: `README.md`
- Modify: `docs/configuration.md`

**Interfaces:** none (docs only).

- [ ] **Step 1: README subsection**

Under the Status/update area of `README.md` (near the existing auto-update mention), add:

```markdown
### Remote updates (admin push)

A lab-bridge **admin** can push a SerialHop update to a lab PC with no operator
action, via the agent API:

- `POST /agent/update` — body `{}` installs the latest GitHub release;
  `{"version":"v2.3.0"}` pins a tag; `{"url":"…","sha256":"…"}` installs from a
  custom mirror. Returns `202` (job accepted) or `200 {"outcome":"noop"}` when
  already current. Downgrade/reinstall is allowed (admin is authoritative).
- `GET /agent/update/status` — reports `downloading` → `verifying` →
  `installing` → `succeeded` | `rolled_back` | `failed` (survives the service
  restart the install causes).

The LocalSystem service downloads + SHA-256-verifies the binary, then a detached
child performs the stop→swap→start with automatic rollback — **no UAC prompt**.
The feature is **off by default**; enable it with `remote_update.enabled: true`.
Access is restricted to admins **server-side** (lab-bridge Authelia), the same
way `/flash` is; SerialHop itself does not authenticate the caller.
```

- [ ] **Step 2: configuration.md field entry**

Add a `remote_update` entry to `docs/configuration.md` matching the style of the existing field docs:

```markdown
### `remote_update`

| Field | Type | Default | Meaning |
|---|---|---|---|
| `remote_update.enabled` | bool | `false` | Allow lab-bridge admins to push updates via `POST /agent/update`. The update installs with no operator action or UAC. Admin-gating is enforced server-side (Authelia), like `/flash`. Leave off unless your lab-bridge deployment manages updates centrally. |

When enabled, the agent also serves `GET /agent/update/status` reporting the
last push outcome. When disabled, both endpoints return `404`.
```

- [ ] **Step 3: Verify + commit**

Run: `gofmt -l .` (docs don't affect Go; ensure nothing else is dirty)
```bash
git add README.md docs/configuration.md
git commit -m "docs: remote admin-pushed updates"
```

---

## Full pre-PR gate

After Task 10, run the complete CI-equivalent gate from the worktree root:

```bash
gofmt -l .
go vet ./...
golangci-lint run
go test -race -count=1 ./...
govulncheck ./...
GOOS=windows go build ./...   # cross-build the Windows target
```

All must be clean before opening the PR (`feat: remote admin-pushed updates`).

## Self-Review (completed during authoring)

- **Spec coverage:** §3 config+migration → Task 1; §5.1 paths → Task 2; §4.1 tag mode → Task 3; §5 result file → Task 4 + Task 7; §1 detached spawn → Task 5; §2/§4 orchestration (resolve/download/verify/spawn, noop, version policy) → Task 6; §4 endpoints + error surface → Task 8; §2 startup reconcile + wiring → Task 5 (`Reconcile`) + Task 9; §11 docs → Task 10; §7 concurrency guard → Tasks 5–6; §8 server prerequisite → documented (out of scope). Custom-URL provenance note (§7) → README/spec only, no code toggle. ✓
- **Placeholder scan:** none — every code step carries complete code. ✓
- **Type consistency:** `remoteupdate.Request/Accepted/Config/Manager`, `updateresult.Result` + state constants, `winsvc.RunAdminAction(6 args)`, `api.New(9 args)`, `paths.*`, `updater.ReleasesByTagURL` are referenced identically across tasks. ✓
</content>
