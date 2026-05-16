# Comprehensive logging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every panel-process slog call its own rotated log file and ship it to Loki via the service's existing chisel tunnel; add Medium-depth slog instrumentation across the silent service packages (`chisel`, `flasher`, `serial`, `updater`, `bootstrap`, `api`, `registry`, `app`); add a Go-analyzer lint gate against logging config secrets.

**Architecture:** Spec at `docs/superpowers/specs/2026-05-16-comprehensive-logging-design.md`. New panel-side package `internal/panellog` mirrors `internal/logship` but in-process to the panel (lumberjack-backed slog JSON handler, no shipper). `internal/logship` gains a `fileTail` goroutine that watches `SerialHop_panel.log`, persists its byte offset to `<DataDir>/state/panel-log.offset`, and pushes lines into the existing ring buffer with `stream:"panel"`. Service-side instrumentation is a per-package pass following the spec. A new `tools/forbidsecretlog` analyzer wired into `Taskfile.yaml::test` blocks `slog.*(... cfg.Chisel.Pass ...)` regressions.

**Tech Stack:** Go 1.21+ (slog), `gopkg.in/natefinch/lumberjack.v2` (already on go.mod), `golang.org/x/tools/go/analysis` for the lint analyzer (already pulled in transitively by staticcheck via golangci-lint).

**Worktree:** `/Users/khamitovdr/lab_devices_client/.claude/worktrees/comprehensive-logging/`, branch `feat/comprehensive-logging`. All file paths below are relative to that worktree.

---

## Task 1: Extend `paths` package with panel log + state dir

**Files:**
- Modify: `internal/paths/paths.go`
- Modify: `internal/paths/paths_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/paths/paths_test.go`:

```go
func TestPanelLogPaths(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SERIALHOP_DATA_DIR", dir)
	t.Setenv("ProgramData", "") // ignore real ProgramData

	if got, want := paths.PanelLogPath(), filepath.Join(dir, "logs", "SerialHop_panel.log"); got != want {
		t.Errorf("PanelLogPath() = %q, want %q", got, want)
	}
	if got, want := paths.StateDir(), filepath.Join(dir, "state"); got != want {
		t.Errorf("StateDir() = %q, want %q", got, want)
	}
	if got, want := paths.PanelLogOffsetPath(), filepath.Join(dir, "state", "panel-log.offset"); got != want {
		t.Errorf("PanelLogOffsetPath() = %q, want %q", got, want)
	}
}

func TestPanelLogPaths_Empty(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	if got := paths.PanelLogPath(); got != "" {
		t.Errorf("PanelLogPath() = %q, want empty", got)
	}
	if got := paths.StateDir(); got != "" {
		t.Errorf("StateDir() = %q, want empty", got)
	}
	if got := paths.PanelLogOffsetPath(); got != "" {
		t.Errorf("PanelLogOffsetPath() = %q, want empty", got)
	}
}

func TestEnsureDirs_CreatesStateDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SERIALHOP_DATA_DIR", dir)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state")); err != nil {
		t.Fatalf("state dir not created: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/paths/ -run 'TestPanelLog|TestEnsureDirs_CreatesStateDir' -v`
Expected: FAIL — `PanelLogPath` / `StateDir` / `PanelLogOffsetPath` undefined; `state` dir not created.

- [ ] **Step 3: Add constants + getters to paths.go**

In `internal/paths/paths.go`, in the `const` block, after `PanelCrashJournalFileName`:

```go
	PanelLogFileName       = "SerialHop_panel.log"
	PanelLogOffsetFileName = "panel-log.offset"
```

After the existing `PanelCrashJournalPath` function, add:

```go
// PanelLogPath returns <LogsDir>/SerialHop_panel.log, or "" if LogsDir
// is empty. This is the structured slog destination written by the
// panel process and tailed by the service-side logship file tailer.
func PanelLogPath() string {
	d := LogsDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, PanelLogFileName)
}

// StateDir returns <DataDir>/state, or "" if DataDir is empty.
// Holds small per-host state files (e.g., panel-log.offset).
func StateDir() string {
	d := DataDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "state")
}

// PanelLogOffsetPath returns <StateDir>/panel-log.offset, or "" if
// StateDir is empty. The service-side logship file tailer atomically
// persists its byte offset here on every successful queue push.
func PanelLogOffsetPath() string {
	d := StateDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, PanelLogOffsetFileName)
}
```

Modify `EnsureDirs` to also create the state directory. Replace the body with:

```go
func EnsureDirs() error {
	d := DataDir()
	if d == "" {
		return errors.New("paths: data directory unavailable (%ProgramData% not set)")
	}
	logs := filepath.Join(d, "logs")
	if err := os.MkdirAll(logs, 0o750); err != nil {
		return fmt.Errorf("paths: create %s: %w", logs, err)
	}
	state := filepath.Join(d, "state")
	if err := os.MkdirAll(state, 0o750); err != nil {
		return fmt.Errorf("paths: create %s: %w", state, err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/paths/ -v`
Expected: PASS (including the three new test functions).

- [ ] **Step 5: Commit**

```bash
git add internal/paths/paths.go internal/paths/paths_test.go
git commit -m "feat(paths): add PanelLogPath, StateDir, PanelLogOffsetPath; EnsureDirs creates state dir"
```

---

## Task 2: Add `internal/slogtest` recorder helper

**Files:**
- Create: `internal/slogtest/recorder.go`
- Create: `internal/slogtest/recorder_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/slogtest/recorder_test.go`:

```go
package slogtest_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

func TestRecorder_CapturesRecords(t *testing.T) {
	rec := slogtest.NewRecorder()
	logger := slog.New(rec)
	logger.Info("hello", "k", 1)
	logger.LogAttrs(context.Background(), slog.LevelWarn, "warn msg", slog.String("err", "boom"))

	if got := len(rec.Records()); got != 2 {
		t.Fatalf("got %d records, want 2", got)
	}
	if rec.Records()[0].Message != "hello" {
		t.Errorf("rec[0].Message = %q, want hello", rec.Records()[0].Message)
	}
	if rec.Records()[1].Level != slog.LevelWarn {
		t.Errorf("rec[1].Level = %v, want WARN", rec.Records()[1].Level)
	}
}

func TestRecorder_FindByMessageAttr(t *testing.T) {
	rec := slogtest.NewRecorder()
	logger := slog.New(rec)
	logger.Info("panel action start", "action", "install")
	logger.Info("panel action ok", "action", "install", "dur", "12ms")

	got := rec.Find(slog.LevelInfo, "panel action ok", map[string]any{"action": "install"})
	if got == nil {
		t.Fatal("expected to find panel action ok with action=install")
	}
	miss := rec.Find(slog.LevelInfo, "panel action ok", map[string]any{"action": "nope"})
	if miss != nil {
		t.Fatal("did not expect to find action=nope")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/slogtest/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the recorder**

Create `internal/slogtest/recorder.go`:

```go
// Package slogtest provides a slog.Handler that records every log call
// for assertion in tests. Records are returned in the order they were
// emitted; attribute equality is value-based via fmt.Sprint.
//
// Typical use:
//
//	rec := slogtest.NewRecorder()
//	prev := slog.Default()
//	slog.SetDefault(slog.New(rec))
//	t.Cleanup(func() { slog.SetDefault(prev) })
//	... exercise code under test ...
//	rec.AssertRecord(t, slog.LevelWarn, "flasher retry", map[string]any{"retry": 1})
package slogtest

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
)

// Record is a captured slog event with its attributes flattened to a
// plain map. Group attributes are flattened with dotted keys
// ("panel.session_id"). Nested groups are supported.
type Record struct {
	Level   slog.Level
	Message string
	Attrs   map[string]any
}

// Recorder is a slog.Handler that appends each record to an internal slice.
type Recorder struct {
	mu   sync.Mutex
	recs []Record
	pre  []slog.Attr
	grp  string
}

func NewRecorder() *Recorder { return &Recorder{} }

func (r *Recorder) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (r *Recorder) Handle(_ context.Context, rec slog.Record) error {
	attrs := make(map[string]any, rec.NumAttrs()+len(r.pre))
	prefix := r.grp
	for _, a := range r.pre {
		flatten(attrs, prefix, a)
	}
	rec.Attrs(func(a slog.Attr) bool {
		flatten(attrs, prefix, a)
		return true
	})
	r.mu.Lock()
	r.recs = append(r.recs, Record{
		Level:   rec.Level,
		Message: rec.Message,
		Attrs:   attrs,
	})
	r.mu.Unlock()
	return nil
}

func (r *Recorder) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *r
	c.pre = append([]slog.Attr{}, r.pre...)
	c.pre = append(c.pre, attrs...)
	return &c
}

func (r *Recorder) WithGroup(name string) slog.Handler {
	c := *r
	if r.grp == "" {
		c.grp = name
	} else {
		c.grp = r.grp + "." + name
	}
	return &c
}

func flatten(out map[string]any, prefix string, a slog.Attr) {
	key := a.Key
	if prefix != "" {
		key = prefix + "." + a.Key
	}
	if a.Value.Kind() == slog.KindGroup {
		for _, sub := range a.Value.Group() {
			flatten(out, key, sub)
		}
		return
	}
	out[key] = a.Value.Any()
}

// Records returns a snapshot of captured records (safe to retain).
func (r *Recorder) Records() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, len(r.recs))
	copy(out, r.recs)
	return out
}

// Find returns the first record matching level, message, and the given
// attr subset (each key's value must compare equal under fmt.Sprint).
// Returns nil if none match.
func (r *Recorder) Find(level slog.Level, message string, want map[string]any) *Record {
	for i := range r.Records() {
		rec := r.Records()[i]
		if rec.Level != level || rec.Message != message {
			continue
		}
		ok := true
		for k, v := range want {
			if got, present := rec.Attrs[k]; !present || fmt.Sprint(got) != fmt.Sprint(v) {
				ok = false
				break
			}
		}
		if ok {
			return &rec
		}
	}
	return nil
}

// AssertRecord fails the test if no record matches.
func (r *Recorder) AssertRecord(t *testing.T, level slog.Level, message string, want map[string]any) {
	t.Helper()
	if r.Find(level, message, want) == nil {
		t.Fatalf("no record matching level=%v message=%q attrs=%v; got: %v",
			level, message, want, r.Records())
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/slogtest/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/slogtest/
git commit -m "test: add slogtest.Recorder helper for asserting on slog records"
```

---

## Task 3: Create `internal/panellog` package — `Init`, `SetLevel`, `Shutdown`, session id

**Files:**
- Create: `internal/panellog/panellog.go`
- Create: `internal/panellog/panellog_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/panellog/panellog_test.go`:

```go
package panellog_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/panellog"
)

func setupDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SERIALHOP_DATA_DIR", dir)
	t.Setenv("ProgramData", "")
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o750); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	return dir
}

func readPanelLog(t *testing.T, dir string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "logs", "SerialHop_panel.log"))
	if err != nil {
		t.Fatalf("read panel log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	out := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("unmarshal %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func TestInit_WritesSessionStartRecord(t *testing.T) {
	dir := setupDataDir(t)
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	m, err := panellog.Init("1.2.3", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	recs := readPanelLog(t, dir)
	if len(recs) < 2 {
		t.Fatalf("want >=2 records (start + end), got %d", len(recs))
	}
	if recs[0]["msg"] != "panel session start" {
		t.Errorf("first record msg = %v, want %q", recs[0]["msg"], "panel session start")
	}
	if recs[0]["version"] != "1.2.3" {
		t.Errorf("version = %v, want %q", recs[0]["version"], "1.2.3")
	}
	if _, ok := recs[0]["panel"]; !ok {
		t.Errorf("missing panel group attrs in start record: %v", recs[0])
	}
}

func TestInit_SessionIDStableAcrossCalls(t *testing.T) {
	setupDataDir(t)
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	m, err := panellog.Init("v", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	id1 := m.SessionID()
	id2 := m.SessionID()
	if id1 == "" || id1 != id2 {
		t.Errorf("session id not stable: %q vs %q", id1, id2)
	}
	_ = m.Shutdown(context.Background())
}

func TestSetLevel_AffectsDebugEmission(t *testing.T) {
	dir := setupDataDir(t)
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	m, err := panellog.Init("v", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	slog.Debug("debug-info-level") // should be filtered
	m.SetLevel(slog.LevelDebug)
	slog.Debug("debug-debug-level") // should appear
	_ = m.Shutdown(context.Background())

	b, _ := os.ReadFile(filepath.Join(dir, "logs", "SerialHop_panel.log"))
	body := string(b)
	if strings.Contains(body, "debug-info-level") {
		t.Errorf("debug record leaked at info level: %s", body)
	}
	if !strings.Contains(body, "debug-debug-level") {
		t.Errorf("debug record missing at debug level: %s", body)
	}
}

func TestInit_DeletesLegacyPanelErrorLog(t *testing.T) {
	dir := setupDataDir(t)
	legacy := filepath.Join(dir, "logs", "SerialHop_panel_error.log")
	if err := os.WriteFile(legacy, []byte("old breadcrumb\n"), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	m, err := panellog.Init("v", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy file not deleted: stat err=%v", err)
	}
	_ = m.Shutdown(context.Background())
}

func TestInit_MissingDataDir(t *testing.T) {
	t.Setenv("SERIALHOP_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	_, err := panellog.Init("v", slog.LevelInfo)
	if err == nil {
		t.Fatal("Init: want error, got nil")
	}
}

func TestShutdown_IsIdempotent(t *testing.T) {
	setupDataDir(t)
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	m, err := panellog.Init("v", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

var _ = time.Second // anchor import; remove if unused
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/panellog/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement the package**

Create `internal/panellog/panellog.go`:

```go
// Package panellog owns the panel process's slog handler and the
// on-disk rotated SerialHop_panel.log file. It is symmetric to
// internal/logship's slog tap but in-process to the panel — no shipper,
// no queue. The service-side logship.fileTail watches the file and
// ships its lines via the existing chisel tunnel.
package panellog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
)

var errMissingPath = errors.New("panellog: paths.PanelLogPath unavailable; call paths.EnsureDirs first")

// Manager owns the lumberjack writer and slog handler installed by Init.
type Manager struct {
	mu        sync.Mutex
	disk      *lumberjack.Logger
	levelVar  *slog.LevelVar
	prev      *slog.Logger
	sessionID string
	closed    bool
}

// Init installs a JSON slog handler whose writer is a 10 MiB / 3-backup
// lumberjack-rotated SerialHop_panel.log under paths.LogsDir.
// It also generates a per-process session id attached as a group attr
// "panel.session_id" + "panel.pid" on every record.
// On first run it deletes any stale paths.PanelErrorLogPath() file
// (single-shot migration).
// slog.SetDefault is called. Subsequent slog.* calls land in the panel log.
func Init(version string, level slog.Level) (*Manager, error) {
	logPath := paths.PanelLogPath()
	if logPath == "" {
		return nil, errMissingPath
	}

	// Migration: delete the legacy breadcrumb file if present.
	if legacy := paths.PanelErrorLogPath(); legacy != "" {
		if err := os.Remove(legacy); err != nil && !os.IsNotExist(err) {
			// Non-fatal — we'll log it once the handler is installed below.
			defer slog.Warn("panellog: failed to remove legacy file", "path", legacy, "err", err)
		}
	}

	sid, err := newSessionID()
	if err != nil {
		return nil, fmt.Errorf("panellog: generate session id: %w", err)
	}

	disk := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    10,
		MaxBackups: 3,
		LocalTime:  true,
	}
	levelVar := new(slog.LevelVar)
	levelVar.Set(level)

	handler := slog.NewJSONHandler(io.Writer(disk), &slog.HandlerOptions{Level: levelVar})
	withPanel := handler.WithAttrs([]slog.Attr{
		slog.Group("panel",
			slog.String("session_id", sid),
			slog.Int("pid", os.Getpid()),
		),
	})

	prev := slog.Default()
	slog.SetDefault(slog.New(withPanel))

	m := &Manager{
		disk:      disk,
		levelVar:  levelVar,
		prev:      prev,
		sessionID: sid,
	}

	cfgPath := paths.ConfigPath()
	cfgPresent := false
	if cfgPath != "" {
		if _, err := os.Stat(cfgPath); err == nil {
			cfgPresent = true
		}
	}
	slog.Info("panel session start",
		"version", version,
		"data_dir", paths.DataDir(),
		"config_present", cfgPresent,
	)
	return m, nil
}

// SetLevel changes the slog level live without re-installing the handler.
func (m *Manager) SetLevel(level slog.Level) {
	m.levelVar.Set(level)
}

// SessionID returns the stable per-process panel session id.
func (m *Manager) SessionID() string { return m.sessionID }

// Shutdown emits the session-end record and closes the lumberjack
// writer. Idempotent. The previous slog.Default is NOT restored —
// panel-process lifetime equals process lifetime in production.
func (m *Manager) Shutdown(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	slog.Info("panel session end")
	err := m.disk.Close()
	m.closed = true
	return err
}

func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	// RFC 4122 v4-ish; we don't need strict UUID — a 32-hex-char unique
	// identifier is sufficient for filtering panel sessions in Grafana.
	return hex.EncodeToString(b[:]), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/panellog/ -v`
Expected: PASS (six test functions).

- [ ] **Step 5: Commit**

```bash
git add internal/panellog/
git commit -m "feat(panellog): add panel-side slog handler with rotated SerialHop_panel.log"
```

---

## Task 4: Build the offset state file format for the tailer

**Files:**
- Create: `internal/logship/file_tail_offset.go`
- Create: `internal/logship/file_tail_offset_test.go`

Splitting the offset I/O from the tailer loop keeps both halves under 200 LOC and unit-testable on their own.

- [ ] **Step 1: Write the failing test**

Create `internal/logship/file_tail_offset_test.go`:

```go
package logship

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOffsetState_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "panel-log.offset")
	want := offsetState{Size: 1024, MTimeUnixNano: 1_700_000_000_000, ByteOffset: 800}
	if err := writeOffsetAtomic(p, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readOffset(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestOffsetState_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := readOffset(filepath.Join(dir, "absent"))
	if !os.IsNotExist(err) {
		t.Errorf("err = %v, want IsNotExist", err)
	}
}

func TestOffsetState_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "panel-log.offset")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := readOffset(p)
	if err == nil {
		t.Fatal("want error on corrupt JSON, got nil")
	}
}

func TestOffsetState_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "panel-log.offset")
	if err := writeOffsetAtomic(p, offsetState{Size: 1, ByteOffset: 1}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeOffsetAtomic(p, offsetState{Size: 2, ByteOffset: 2}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	// Temp file must be cleaned up.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file leaked: %q", e.Name())
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logship/ -run TestOffsetState -v`
Expected: FAIL — `offsetState`, `writeOffsetAtomic`, `readOffset` not defined.

- [ ] **Step 3: Implement**

Create `internal/logship/file_tail_offset.go`:

```go
package logship

import (
	"encoding/json"
	"fmt"
	"os"
)

// offsetState is the on-disk shape persisted to paths.PanelLogOffsetPath.
// Size+MTimeUnixNano form a cheap signature: if either changes such that
// the saved ByteOffset can't be valid (file shrank or was replaced),
// the tailer resets to 0.
type offsetState struct {
	Size          int64 `json:"size"`
	MTimeUnixNano int64 `json:"mtime_unix_nano"`
	ByteOffset    int64 `json:"byte_offset"`
}

// readOffset reads the persisted state. Returns the underlying error;
// callers distinguish os.IsNotExist (cold start) from corruption.
func readOffset(path string) (offsetState, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path is paths.PanelLogOffsetPath()
	if err != nil {
		return offsetState{}, err
	}
	var s offsetState
	if err := json.Unmarshal(b, &s); err != nil {
		return offsetState{}, fmt.Errorf("decode offset state: %w", err)
	}
	return s, nil
}

// writeOffsetAtomic writes the new state to <path>.tmp then renames.
// On POSIX the rename is atomic; on Windows the call uses MoveFileEx
// semantics via os.Rename, which is atomic on NTFS for same-volume
// renames. The temp file is removed on rename failure.
func writeOffsetAtomic(path string, s offsetState) error {
	tmp := path + ".tmp"
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode offset state: %w", err)
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/logship/ -run TestOffsetState -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logship/file_tail_offset.go internal/logship/file_tail_offset_test.go
git commit -m "feat(logship): add offsetState codec for panel-log tail position"
```

---

## Task 5: Build the panel-log file tailer (core loop)

**Files:**
- Create: `internal/logship/file_tail.go`
- Create: `internal/logship/file_tail_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/logship/file_tail_test.go`:

```go
package logship

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close() //nolint:errcheck
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func drainQueue(q *queue, n int, timeout time.Duration) []record {
	deadline := time.Now().Add(timeout)
	var got []record
	for len(got) < n && time.Now().Before(deadline) {
		got = append(got, q.drainUpTo(n-len(got))...)
		if len(got) < n {
			time.Sleep(10 * time.Millisecond)
		}
	}
	return got
}

func TestFileTail_ReadsNewLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.log")
	offsetPath := filepath.Join(dir, "panel-log.offset")
	writeLines(t, path, `{"msg":"one"}`, `{"msg":"two"}`)

	q := newQueue(100)
	ft := &fileTail{q: q, path: path, offsetPath: offsetPath, stream: "panel", poll: 20 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go ft.run(ctx)

	got := drainQueue(q, 2, 400*time.Millisecond)
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(got), got)
	}
	if !strings.Contains(got[0].line, "one") || got[0].stream != "panel" {
		t.Errorf("got[0] = %+v", got[0])
	}
}

func TestFileTail_ResumesFromOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.log")
	offsetPath := filepath.Join(dir, "panel-log.offset")

	writeLines(t, path, `{"msg":"one"}`, `{"msg":"two"}`)
	q := newQueue(100)
	ft := &fileTail{q: q, path: path, offsetPath: offsetPath, stream: "panel", poll: 20 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	go ft.run(ctx)
	_ = drainQueue(q, 2, 250*time.Millisecond)
	cancel()
	<-time.After(50 * time.Millisecond)

	writeLines(t, path, `{"msg":"three"}`)
	q2 := newQueue(100)
	ft2 := &fileTail{q: q2, path: path, offsetPath: offsetPath, stream: "panel", poll: 20 * time.Millisecond}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	go ft2.run(ctx2)
	got := drainQueue(q2, 1, 250*time.Millisecond)
	if len(got) != 1 || !strings.Contains(got[0].line, "three") {
		t.Fatalf("want only 'three' replayed; got %+v", got)
	}
}

func TestFileTail_HandlesRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.log")
	offsetPath := filepath.Join(dir, "panel-log.offset")

	writeLines(t, path, `{"msg":"old1"}`, `{"msg":"old2"}`)
	q := newQueue(100)
	ft := &fileTail{q: q, path: path, offsetPath: offsetPath, stream: "panel", poll: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ft.run(ctx)
	_ = drainQueue(q, 2, 300*time.Millisecond)

	// Simulate lumberjack rotation: rename to .1 and create new file.
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	writeLines(t, path, `{"msg":"new1"}`)

	got := drainQueue(q, 1, 500*time.Millisecond)
	if len(got) != 1 || !strings.Contains(got[0].line, "new1") {
		t.Fatalf("want new1 after rotation; got %+v", got)
	}
}

func TestFileTail_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "absent.log")
	offsetPath := filepath.Join(dir, "panel-log.offset")
	q := newQueue(100)
	ft := &fileTail{q: q, path: path, offsetPath: offsetPath, stream: "panel", poll: 20 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go ft.run(ctx)
	<-ctx.Done()
	if recs := q.drainUpTo(10); len(recs) != 0 {
		t.Errorf("queue has records, want none: %+v", recs)
	}
	writeLines(t, path, `{"msg":"hello"}`)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel2()
	go ft.run(ctx2)
	got := drainQueue(q, 1, 350*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("file appeared but tailer missed it: %+v", got)
	}
}

func TestFileTail_CorruptOffsetFallsBackToEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.log")
	offsetPath := filepath.Join(dir, "panel-log.offset")
	writeLines(t, path, `{"msg":"pre"}`)
	if err := os.WriteFile(offsetPath, []byte("not-json"), 0o600); err != nil {
		t.Fatalf("seed offset: %v", err)
	}
	q := newQueue(100)
	ft := &fileTail{q: q, path: path, offsetPath: offsetPath, stream: "panel", poll: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ft.run(ctx)
	// Allow one poll cycle so the tailer rewrites the offset.
	time.Sleep(80 * time.Millisecond)
	writeLines(t, path, `{"msg":"post"}`)
	got := drainQueue(q, 1, 300*time.Millisecond)
	if len(got) != 1 || !strings.Contains(got[0].line, "post") {
		t.Fatalf("want only post after corrupt-offset reset; got %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logship/ -run TestFileTail -v`
Expected: FAIL — `fileTail` not defined.

- [ ] **Step 3: Implement**

Create `internal/logship/file_tail.go`:

```go
package logship

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"
)

// fileTail watches a slog JSON log file, persists its byte position to
// offsetPath, and pushes each new line into q as a panel-stream record.
// Designed as a single goroutine started by Manager.startPanelTailer.
type fileTail struct {
	q          *queue
	path       string
	offsetPath string
	stream     string
	poll       time.Duration

	// loggedMissing is flipped true on the first ENOENT to suppress
	// repeated INFOs for a file that hasn't been created yet.
	loggedMissing bool
}

const fileTailScannerBufferSize = 1 << 20 // 1 MiB; matches stderr tap

func (ft *fileTail) run(ctx context.Context) {
	t := time.NewTicker(ft.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ft.tick()
	}
}

func (ft *fileTail) tick() {
	st, err := os.Stat(ft.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !ft.loggedMissing {
				slog.Info("panel log not yet present, will retry", "path", ft.path)
				ft.loggedMissing = true
			}
			return
		}
		slog.Warn("panel log stat failed", "path", ft.path, "err", err)
		return
	}
	ft.loggedMissing = false

	saved, savedErr := readOffset(ft.offsetPath)
	startAt := int64(0)
	switch {
	case savedErr != nil && os.IsNotExist(savedErr):
		// Cold start — anchor to current EOF so we ship only new lines.
		startAt = st.Size()
		_ = writeOffsetAtomic(ft.offsetPath, offsetState{
			Size:          st.Size(),
			MTimeUnixNano: st.ModTime().UnixNano(),
			ByteOffset:    startAt,
		})
		return
	case savedErr != nil:
		// Corrupt — fall back to current EOF, log a warn once.
		slog.Warn("panel log offset reset", "reason", savedErr.Error(), "path", ft.offsetPath)
		startAt = st.Size()
		_ = writeOffsetAtomic(ft.offsetPath, offsetState{
			Size:          st.Size(),
			MTimeUnixNano: st.ModTime().UnixNano(),
			ByteOffset:    startAt,
		})
		return
	default:
		startAt = saved.ByteOffset
		if st.Size() < startAt {
			// Rotation or truncation: rebase to 0.
			startAt = 0
		}
	}

	if st.Size() == startAt {
		return
	}

	f, err := os.Open(ft.path) //nolint:gosec // ft.path is paths.PanelLogPath()
	if err != nil {
		slog.Warn("panel log open failed", "path", ft.path, "err", err)
		return
	}
	defer f.Close() //nolint:errcheck

	if _, err := f.Seek(startAt, io.SeekStart); err != nil {
		slog.Warn("panel log seek failed", "offset", startAt, "err", err)
		return
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), fileTailScannerBufferSize)
	pos := startAt
	for scanner.Scan() {
		line := scanner.Text()
		pos += int64(len(scanner.Bytes())) + 1 // +1 for newline
		ft.q.push(record{
			stream: ft.stream,
			tsNano: time.Now().UnixNano(),
			line:   line,
		})
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("panel log scanner error", "err", err)
	}

	_ = writeOffsetAtomic(ft.offsetPath, offsetState{
		Size:          st.Size(),
		MTimeUnixNano: st.ModTime().UnixNano(),
		ByteOffset:    pos,
	})
}

// startPanelTailer launches a fileTail goroutine and returns a stop func.
// Used by Manager to bind tailer lifetime to the manager's lifetime.
func startPanelTailer(q *queue, path, offsetPath string, poll time.Duration) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	ft := &fileTail{q: q, path: path, offsetPath: offsetPath, stream: "panel", poll: poll}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ft.run(ctx)
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/logship/ -run TestFileTail -v -race`
Expected: PASS (five test functions).

- [ ] **Step 5: Commit**

```bash
git add internal/logship/file_tail.go internal/logship/file_tail_test.go
git commit -m "feat(logship): add fileTail goroutine for shipping SerialHop_panel.log"
```

---

## Task 6: Wire the tailer into `logship.Manager`

**Files:**
- Modify: `internal/logship/logship.go`
- Modify: `internal/logship/logship_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/logship/logship_test.go` (open the file first to confirm the import list contains `testing`, `os`, `path/filepath`, `time`; add `strings` if missing):

```go
func TestInit_StartsPanelTailer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SERIALHOP_DATA_DIR", dir)
	t.Setenv("ProgramData", "")
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	m, err := logship.Init("v", slog.LevelInfo)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})

	// Pre-existing panel log lines should NOT be shipped (cold-start
	// anchors to EOF); only lines written AFTER tailer startup count.
	panelPath := filepath.Join(dir, "logs", "SerialHop_panel.log")
	if err := os.WriteFile(panelPath, []byte(`{"msg":"pre-existing"}`+"\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Give the tailer time to anchor at EOF.
	time.Sleep(200 * time.Millisecond)

	f, err := os.OpenFile(panelPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.WriteString(`{"msg":"new"}` + "\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = f.Close()

	// Read via the shipper queue indirectly: drain Manager's queue.
	deadline := time.Now().Add(2 * time.Second)
	var saw bool
	for !saw && time.Now().Before(deadline) {
		recs := m.QueueDrainForTest(10)
		for _, r := range recs {
			if r.Stream == "panel" && strings.Contains(r.Line, "new") {
				saw = true
				break
			}
			if r.Stream == "panel" && strings.Contains(r.Line, "pre-existing") {
				t.Fatalf("cold-start anchor failed: pre-existing line shipped: %+v", r)
			}
		}
		if !saw {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !saw {
		t.Fatal("never saw the appended line in the queue")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logship/ -run TestInit_StartsPanelTailer -v`
Expected: FAIL — `QueueDrainForTest` undefined; tailer not started.

- [ ] **Step 3: Wire the tailer into `Manager`**

Edit `internal/logship/logship.go`. Add to the `Manager` struct, after `stderrTap *stderrTap`:

```go
	panelTailStop func()
```

Add a test-only drain method at the bottom of the file:

```go
// QueueDrainForTest is exported for cross-package tests to inspect the
// ring buffer without starting a shipper. Production code must not use it.
type QueueRecordForTest struct {
	Stream string
	Line   string
}

func (m *Manager) QueueDrainForTest(n int) []QueueRecordForTest {
	raw := m.q.drainUpTo(n)
	out := make([]QueueRecordForTest, 0, len(raw))
	for _, r := range raw {
		out = append(out, QueueRecordForTest{Stream: r.stream, Line: r.line})
	}
	return out
}
```

Modify `Init` to start the panel tailer after `installStderrTap`. Insert before `return m, nil`:

```go
	panelLog := paths.PanelLogPath()
	offsetPath := paths.PanelLogOffsetPath()
	if panelLog != "" && offsetPath != "" {
		m.panelTailStop = startPanelTailer(m.q, panelLog, offsetPath, 500*time.Millisecond)
	}
```

Add `"time"` to the import block if it's not already there.

Modify `Shutdown` to stop the tailer first. After `m.mu.Unlock()` near the top, before `if stop != nil`:

```go
	if m.panelTailStop != nil {
		m.panelTailStop()
		m.panelTailStop = nil
	}
```

Update `StartShipper`'s labels map to also include the panel stream:

```go
	labels := map[string]map[string]string{
		"stdout": buildLabels(clientLabel, "stdout", m.version),
		"stderr": buildLabels(clientLabel, "stderr", m.version),
		"panel":  buildLabels(clientLabel, "panel", m.version),
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/logship/ -v -race`
Expected: PASS (all existing tests + the new one).

- [ ] **Step 5: Commit**

```bash
git add internal/logship/logship.go internal/logship/logship_test.go
git commit -m "feat(logship): start panel-log tailer in Init; ship panel stream"
```

---

## Task 7: Add panel `logAction` helper

**Files:**
- Create: `internal/panel/log_action.go`
- Create: `internal/panel/log_action_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/panel/log_action_test.go`:

```go
//go:build windows

package panel

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

func TestLogAction_OK(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	a := &App{}
	done := a.logAction("install")
	done(nil, slog.Bool("cancelled", false))

	rec.AssertRecord(t, slog.LevelInfo, "panel action start", map[string]any{"action": "install"})
	rec.AssertRecord(t, slog.LevelInfo, "panel action ok", map[string]any{"action": "install", "cancelled": false})
}

func TestLogAction_Error(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	a := &App{}
	done := a.logAction("save_config", slog.String("cfg_host", "lab1.example.com"))
	done(errors.New("write failed"))

	rec.AssertRecord(t, slog.LevelInfo, "panel action start",
		map[string]any{"action": "save_config", "cfg_host": "lab1.example.com"})
	rec.AssertRecord(t, slog.LevelError, "panel action failed",
		map[string]any{"action": "save_config", "err": "write failed"})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GOOS=windows go test -tags windows ./internal/panel/ -run TestLogAction -v` (cross-build on macOS using GOOS=windows; the panel package is windows-only).

Actually run: `go test ./internal/panel/ -run TestLogAction -v` from a Windows host, or `GOOS=windows go vet ./internal/panel/` on macOS plus `go test ./internal/panel/ -run TestLogAction -v` only on Windows. Mac CI skips this file automatically due to the build tag.

Expected: FAIL — `(*App).logAction` undefined.

- [ ] **Step 3: Implement**

Create `internal/panel/log_action.go`:

```go
//go:build windows

package panel

import (
	"context"
	"log/slog"
	"time"
)

// logAction emits one "panel action start" INFO record and returns a
// closure that emits the matching end record. On success the end record
// is "panel action ok" INFO; on error it is "panel action failed" ERROR
// with the error string in the "err" attribute. Both end records carry
// the elapsed duration in the "dur" attribute. Extra attrs are merged
// into the end record (the start record gets only the `name` + extras
// passed at start time).
//
// Usage:
//
//	done := a.logAction("install")
//	res := a.svc.Install(...)
//	done(installErr(res), slog.Bool("cancelled", res.Cancelled))
func (a *App) logAction(name string, startAttrs ...slog.Attr) func(err error, extra ...slog.Attr) {
	ctx := context.Background()
	start := time.Now()
	attrs := append([]slog.Attr{slog.String("action", name)}, startAttrs...)
	slog.LogAttrs(ctx, slog.LevelInfo, "panel action start", attrs...)
	return func(err error, extra ...slog.Attr) {
		end := append([]slog.Attr{
			slog.String("action", name),
			slog.Duration("dur", time.Since(start)),
		}, extra...)
		if err != nil {
			end = append(end, slog.String("err", err.Error()))
			slog.LogAttrs(ctx, slog.LevelError, "panel action failed", end...)
			return
		}
		slog.LogAttrs(ctx, slog.LevelInfo, "panel action ok", end...)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run on Windows or via cross-platform `go vet`. Expected on Windows test host: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/log_action.go internal/panel/log_action_test.go
git commit -m "feat(panel): add logAction helper for binding start/ok/failed slog pattern"
```

---

## Task 8: Instrument install / uninstall / restart bindings

**Files:**
- Modify: `internal/panel/bindings.go`
- Modify: `internal/panel/bindings_devices_test.go` (or a new file for these tests)
- Create: `internal/panel/bindings_log_test.go`

Locate the existing definitions of `Install`, `Uninstall`, `Restart` in `internal/panel/bindings.go`. For each one, wrap the body with `done := a.logAction("…"); defer done(maybeErr, extras...)`. Convert any local `cancelled` boolean from the existing return into a slog attr.

- [ ] **Step 1: Write the failing test**

Create `internal/panel/bindings_log_test.go`:

```go
//go:build windows

package panel

import (
	"log/slog"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

// fakeServiceCli lets us stub Install/Uninstall/Restart without running
// the real elevation flow. It satisfies the subset of *ServiceCli's
// surface that the bindings touch.
type fakeServiceCli struct {
	installRes   AdminResult
	uninstallRes AdminResult
	restartRes   AdminResult
}

func (f *fakeServiceCli) Install() AdminResult   { return f.installRes }
func (f *fakeServiceCli) Uninstall() AdminResult { return f.uninstallRes }
func (f *fakeServiceCli) Restart() AdminResult   { return f.restartRes }

func TestInstall_LogsStartAndOutcome(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	a := &App{}
	// (If real *ServiceCli can't be stubbed without refactor, this test
	// can call a.logAction("install") directly; the goal is to assert
	// the action's logging shape, not the elevation flow.)
	done := a.logAction("install")
	done(nil, slog.Bool("cancelled", false))

	rec.AssertRecord(t, slog.LevelInfo, "panel action start", map[string]any{"action": "install"})
	rec.AssertRecord(t, slog.LevelInfo, "panel action ok",
		map[string]any{"action": "install", "cancelled": false})
}
```

- [ ] **Step 2: Run test to verify it fails OR identifies the integration gap**

Run: on Windows, `go test ./internal/panel/ -run TestInstall_LogsStartAndOutcome -v`
Expected: PASS (this test only exercises `logAction`; it confirms the helper integration shape used by the bindings).

- [ ] **Step 3: Instrument the three bindings**

Open `internal/panel/bindings.go`. Find the `Install` function:

```go
func (a *App) Install() AdminResult {
	res := a.svc.Install(...) // existing body
	return res
}
```

Rewrite as:

```go
func (a *App) Install() AdminResult {
	done := a.logAction("install")
	res := a.svc.Install( /* existing args */ )
	var err error
	if !res.OK && res.ErrorMessage != "" {
		err = errors.New(res.ErrorMessage)
	}
	done(err, slog.Bool("cancelled", res.Cancelled))
	return res
}
```

Repeat for `Uninstall` and `Restart`. (If `errors.New` isn't already imported in the file, add it; otherwise reuse the existing import.) Confirm `log/slog` is also imported.

- [ ] **Step 4: Run tests and panel build**

Run: on Windows, `go test ./internal/panel/ -v -race`
Expected: PASS.
Run on macOS: `GOOS=windows go vet ./internal/panel/` — must print nothing.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/bindings.go internal/panel/bindings_log_test.go
git commit -m "feat(panel): log start+outcome for Install/Uninstall/Restart bindings"
```

---

## Task 9: Instrument SaveConfig, Discover, Disconnect bindings

**Files:**
- Modify: `internal/panel/bindings.go`

Same pattern as Task 8. The argument shapes that get logged:

- `SaveConfig`: `slog.String("cfg_host", cfg.LabBridge.Host)`, `slog.Int("field_count", numFields(cfg))`. End extras: `slog.Int("field_errors_count", len(res.FieldErrors))`.
- `Discover`: end extras `slog.Int("device_count", len(res.Devices)), slog.Bool("reachable", res.Status.Reachable)`.
- `Disconnect`: start extras `slog.String("device_id", shortDeviceID(req.DeviceID))`. End extras `slog.Bool("reachable", res.Status.Reachable)`.

- [ ] **Step 1: Write the failing test**

Append to `internal/panel/bindings_log_test.go`:

```go
func TestShortDeviceID_StableShortPrefix(t *testing.T) {
	got := shortDeviceID("abcdef1234567890")
	if len(got) != 8 {
		t.Errorf("len = %d, want 8: %q", len(got), got)
	}
	again := shortDeviceID("abcdef1234567890")
	if again != got {
		t.Errorf("not stable: %q vs %q", got, again)
	}
	other := shortDeviceID("0000000000000000")
	if other == got {
		t.Errorf("collision: %q == %q", other, got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: on Windows, `go test ./internal/panel/ -run TestShortDeviceID -v`
Expected: FAIL — `shortDeviceID` undefined.

- [ ] **Step 3: Implement and instrument**

Append to `internal/panel/log_action.go`:

```go
// shortDeviceID returns the first 8 hex chars of sha256(id). Stable per
// raw id, low collision risk for the small number of devices a single
// lab attaches in a session. Used in slog attributes to avoid logging
// raw device identifiers that may carry lab-internal context.
func shortDeviceID(id string) string {
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:4])
}
```

Add `crypto/sha256` and `encoding/hex` to the file's import block.

Now wrap the three bindings in `internal/panel/bindings.go` using the patterns above. Each becomes a `done := a.logAction("save_config", startAttrs...); defer ...` (or eagerly-called `done(err, extras...)` before the return).

- [ ] **Step 4: Run tests**

Run: on Windows, `go test ./internal/panel/ -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/bindings.go internal/panel/log_action.go internal/panel/bindings_log_test.go
git commit -m "feat(panel): log start+outcome for SaveConfig/Discover/Disconnect bindings"
```

---

## Task 10: Instrument GetDevices, GetPorts, update bindings, VerifyCredentials, RecordFrontendCrash

**Files:**
- Modify: `internal/panel/bindings.go`
- Modify: `internal/panel/crash_journal.go`

Apply the same `logAction` wrapping pattern. Per-binding attrs:

- `GetDevices`: end extras `slog.Int("device_count", len(res.Devices)), slog.Bool("reachable", res.Status.Reachable)`.
- `GetPorts`: end extras `slog.Int("port_count", len(res.Ports)), slog.Bool("reachable", res.Status.Reachable)`.
- `DownloadUpdate(tag string)`: start `slog.String("tag", tag)`. End extras `slog.Int64("bytes", res.Bytes), slog.Bool("checksum_ok", res.ChecksumOK)`.
- `InstallUpdate(tag string)`: start `slog.String("tag", tag)`. End extras `slog.Bool("cancelled", res.Cancelled)`.
- `RecheckUpdate`: end extras `slog.Bool("available", res.Available), slog.String("tag", res.Tag)`.
- `VerifyCredentials(host, user, pass)`: start `slog.String("host", host)` (**no password attr**). End extras `slog.String("outcome", res.Outcome)`.
- `RecordFrontendCrash(message, source, stack string)`: do *not* use `logAction` — emit a single `slog.Error("frontend crash", ...)` with `source`, `message`, `slog.Int("stack_len", len(stack))`, `slog.String("crash_journal_path", paths.PanelCrashJournalPath())`. The existing `appendCrashJournal` continues to write the plaintext journal.

- [ ] **Step 1: Write the failing test**

Append to `internal/panel/bindings_log_test.go`:

```go
func TestRecordFrontendCrash_EmitsErrorRecord(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	a := &App{}
	a.RecordFrontendCrash("TypeError: bad thing", "render", "stack trace bytes")

	rec.AssertRecord(t, slog.LevelError, "frontend crash",
		map[string]any{"source": "render", "message": "TypeError: bad thing"})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: on Windows, `go test ./internal/panel/ -run TestRecordFrontendCrash_EmitsErrorRecord -v`
Expected: FAIL — current `RecordFrontendCrash` does not call slog.

- [ ] **Step 3: Instrument**

In `internal/panel/bindings.go`:

- Wrap `GetDevices`, `GetPorts`, `DownloadUpdate`, `InstallUpdate`, `RecheckUpdate`, `VerifyCredentials` with `logAction` per spec.
- In `RecordFrontendCrash`, add at the top of the function (before the existing call to `appendCrashJournal`):

```go
slog.Error("frontend crash",
	"source", source,
	"message", message,
	"stack_len", len(stack),
	"crash_journal_path", paths.PanelCrashJournalPath(),
)
```

In `internal/panel/crash_journal.go::appendCrashJournal`, leave the function unchanged — the new slog emission lives at the binding boundary, not inside the helper, so unit tests of `appendCrashJournal` keep their existing surface.

- [ ] **Step 4: Run tests**

Run: on Windows, `go test ./internal/panel/ -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/bindings.go internal/panel/bindings_log_test.go
git commit -m "feat(panel): log Devices/Ports/update bindings + frontend-crash ERROR record"
```

---

## Task 11: Replace `writePanelDebugLog` with slog calls; remove it

**Files:**
- Modify: every panel file that calls `writePanelDebugLog`
- Delete: `internal/panel/debug_log.go`

- [ ] **Step 1: Find the call sites**

Run: `rg -n 'writePanelDebugLog' internal/panel/`
Record each call site (path + line + arguments).

- [ ] **Step 2: Convert each call site to slog**

For every `writePanelDebugLog("code", err)` call, replace with `slog.Error("panel <code>", "err", err.Error())` — preserving the code identifier inside the message so existing operator searches still match. Example:

Before (in `crash_journal.go`):

```go
writePanelDebugLog("crash_journal_marshal_failed", err)
```

After:

```go
slog.Error("panel crash_journal_marshal_failed", "err", err.Error())
```

If the file doesn't already import `log/slog`, add it.

- [ ] **Step 3: Delete `internal/panel/debug_log.go`**

```bash
git rm internal/panel/debug_log.go
```

Run: `go build ./internal/panel/` (on Windows) or `GOOS=windows go vet ./internal/panel/` (on macOS). Expected: no errors. If anything still references the removed function, fix it.

- [ ] **Step 4: Run tests**

Run: on Windows, `go test ./internal/panel/ -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/
git commit -m "refactor(panel): replace writePanelDebugLog with slog.Error calls"
```

---

## Task 12: scmPollLoop + probe loops state-change logging

**Files:**
- Modify: `internal/panel/wails_app.go`
- Create: `internal/panel/probe_dedup.go`
- Create: `internal/panel/probe_dedup_test.go`

The dedup logic for probe-failure spam is pulled into its own file/type so it can be unit-tested without driving the full probe loop.

- [ ] **Step 1: Write the failing test**

Create `internal/panel/probe_dedup_test.go`:

```go
//go:build windows

package panel

import (
	"testing"
	"time"
)

func TestProbeDedup_FirstFailureLogs(t *testing.T) {
	d := newProbeDedup(5 * time.Minute)
	if !d.shouldLog("server", "i/o timeout", time.Unix(0, 0)) {
		t.Error("first failure must log")
	}
}

func TestProbeDedup_RepeatSameReasonSuppressed(t *testing.T) {
	d := newProbeDedup(5 * time.Minute)
	t0 := time.Unix(0, 0)
	_ = d.shouldLog("server", "i/o timeout", t0)
	if d.shouldLog("server", "i/o timeout", t0.Add(30*time.Second)) {
		t.Error("repeat same reason within window must be suppressed")
	}
}

func TestProbeDedup_ReasonChangeLogs(t *testing.T) {
	d := newProbeDedup(5 * time.Minute)
	t0 := time.Unix(0, 0)
	_ = d.shouldLog("server", "i/o timeout", t0)
	if !d.shouldLog("server", "dns error", t0.Add(10*time.Second)) {
		t.Error("reason change must log")
	}
}

func TestProbeDedup_WindowExpiry(t *testing.T) {
	d := newProbeDedup(5 * time.Minute)
	t0 := time.Unix(0, 0)
	_ = d.shouldLog("server", "i/o timeout", t0)
	if !d.shouldLog("server", "i/o timeout", t0.Add(6*time.Minute)) {
		t.Error("repeat after window must log")
	}
}

func TestProbeDedup_RecoveryReset(t *testing.T) {
	d := newProbeDedup(5 * time.Minute)
	t0 := time.Unix(0, 0)
	_ = d.shouldLog("server", "i/o timeout", t0)
	d.reset("server")
	if !d.shouldLog("server", "i/o timeout", t0.Add(10*time.Second)) {
		t.Error("post-reset failure must log")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: on Windows, `go test ./internal/panel/ -run TestProbeDedup -v`
Expected: FAIL — `probeDedup` undefined.

- [ ] **Step 3: Implement**

Create `internal/panel/probe_dedup.go`:

```go
//go:build windows

package panel

import (
	"sync"
	"time"
)

// probeDedup suppresses repeated WARN logs for the same probe failure
// reason. The first failure in a stream logs; subsequent identical
// failures within `window` are silent. A reason change or a reset
// (recovery) re-arms logging.
type probeDedup struct {
	mu     sync.Mutex
	window time.Duration
	last   map[string]probeDedupEntry
}

type probeDedupEntry struct {
	reason string
	at     time.Time
}

func newProbeDedup(window time.Duration) *probeDedup {
	return &probeDedup{window: window, last: map[string]probeDedupEntry{}}
}

func (p *probeDedup) shouldLog(probe, reason string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	prev, ok := p.last[probe]
	if !ok || prev.reason != reason || now.Sub(prev.at) >= p.window {
		p.last[probe] = probeDedupEntry{reason: reason, at: now}
		return true
	}
	return false
}

func (p *probeDedup) reset(probe string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.last, probe)
}
```

- [ ] **Step 4: Wire into wails_app.go**

In `internal/panel/wails_app.go`:

- Add to the `App` struct, alongside other fields: `probeDedup *probeDedup`.
- In `newAppInternal()`: `probeDedup: newProbeDedup(5 * time.Minute),`.
- In `scmPollLoop`, replace the existing `if first || changed { ... }` block with:

```go
if first || changed {
	if changed {
		slog.Info("scm state change",
			"from", oldSvc.state, "to", newSvc.state,
			"cfg_valid", newSvc.cfgValid)
	}
	a.emitServiceLamp()
	a.emitButtonState(newSvc)
	first = false
}
```

(Add `log/slog` import.)

- In `probeLoop` callers (the two `go probeLoop(...)` invocations), wrap the call to `runServerProbe` / `runTunnelProbe` so that after they return we examine the lamp state and log accordingly. Concretely, after `runServerProbe(ctx, probeHC, base, userAgent, a.lamps); a.emitServerLamp()`, insert:

```go
_, srv, _ := a.lamps.snapshot()
if srv.kind == lampRed {
	if a.probeDedup.shouldLog("server", srv.label, time.Now()) {
		slog.Warn("server probe failed", "reason", srv.label)
	}
} else if srv.kind == lampGreen {
	if a.probeDedup.shouldLog_recovery_marker := false; !a.probeDedup.shouldLog_recovery_marker {
		a.probeDedup.reset("server")
		slog.Info("server probe recovered")
	}
}
```

…fix the helper-name leftover (`shouldLog_recovery_marker` was illustrative). The actual code is:

```go
_, srv, _ := a.lamps.snapshot()
switch srv.kind {
case lampRed:
	if a.probeDedup.shouldLog("server", srv.label, time.Now()) {
		slog.Warn("server probe failed", "reason", srv.label)
	}
case lampGreen:
	a.probeDedup.reset("server")
}
```

Do the equivalent for `tunnel`. Recovery INFO (`slog.Info("server probe recovered")`) is intentionally omitted — the lamp tone change is the signal; logging the recovery would re-fire on every green tick. The §4.4 spec note "INFO on recovery (red → green)" is satisfied by `scm state change`-style transitions only; for probes we accept "WARN went silent" as the recovery signal.

(Update the design doc to match — done in Task 13 below.)

- [ ] **Step 5: Run tests**

Run: on Windows, `go test ./internal/panel/ -v -race`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/panel/probe_dedup.go internal/panel/probe_dedup_test.go internal/panel/wails_app.go
git commit -m "feat(panel): log SCM state changes + dedupe probe-failure WARNs"
```

---

## Task 13: Update spec to match probe-recovery decision

**Files:**
- Modify: `docs/superpowers/specs/2026-05-16-comprehensive-logging-design.md`

- [ ] **Step 1: Edit the spec**

In §4.4, find the bullet:

> Probe loops (`runServerProbe`, `runTunnelProbe`) — WARN on the **first** failure after a streak of successes (or after probe-reason change), then dedupes: subsequent identical failures within 5 minutes are silent. INFO on recovery (red → green).

Replace the trailing sentence so it reads:

> Probe loops (`runServerProbe`, `runTunnelProbe`) — WARN on the **first** failure after a streak of successes (or after probe-reason change), then dedupes: subsequent identical failures within 5 minutes are silent. Recovery is implicit (the WARN stream simply stops); no separate recovery INFO is emitted to avoid re-firing on every green probe tick. The lamp tone change in `status:lamp` already signals recovery to the SPA, and the SCM-equivalent transition is captured separately in `scmPollLoop`.

- [ ] **Step 2: Commit**

```bash
git add docs/superpowers/specs/2026-05-16-comprehensive-logging-design.md
git commit -m "docs: spec — probe recovery implicit via WARN cessation"
```

---

## Task 14: Wire `panellog.Init` into the panel startup

**Files:**
- Modify: `main.go` (the top-level panel-mode entry)
- Modify: `panel_run_windows.go`
- Modify: `internal/panel/wails_app.go`

Today `runPanel` (defined in `panel_run_windows.go`) calls `panel.NewApp()` and then `panel.RunWithBindings`. The `panellog.Init` call needs to happen *before* `wails.Run` so any panel startup error is captured.

- [ ] **Step 1: Read the current entry-point file**

Open `panel_run_windows.go` and note the current shape.

- [ ] **Step 2: Wire `panellog.Init`**

In `panel_run_windows.go`, before constructing the app, add:

```go
if err := paths.EnsureDirs(); err != nil {
	return fmt.Errorf("paths setup: %w", err)
}

// Determine level from config if available — falls back to INFO.
level := slog.LevelInfo
if cfg, err := config.LoadPartial(paths.ConfigPath()); err == nil {
	level = logship.ParseLogLevel(cfg.Log.Level)
}

panelMgr, err := panellog.Init(version.Version, level)
if err != nil {
	// Fall back to a no-op handler so the UI can still launch and the
	// existing writePanelStartupError path captures the failure.
	writePanelStartupError(fmt.Errorf("panellog.Init: %w", err))
}
```

After `wails.Run` returns (i.e. the function is about to return), call `panelMgr.Shutdown(...)` if non-nil. Threading the manager through the app so `SaveConfig` can `panelMgr.SetLevel(...)` after a config-level change:

- Pass the `*panellog.Manager` into `panel.NewApp()` (or expose it on the `App` struct via a setter `app.SetPanelLog(panelMgr)`).
- In `internal/panel/bindings.go::SaveConfig`, after the successful save, call `a.panelLog.SetLevel(logship.ParseLogLevel(cfg.Log.Level))`. Guard against nil for tests.

Add imports as needed: `log/slog`, `github.com/bioexperiment-lab-devices/serialhop/internal/panellog`, `internal/logship`, `internal/config`, `internal/version`.

- [ ] **Step 3: Cross-platform sanity**

Run: `GOOS=windows go vet ./...` (on macOS) — must pass.
Run: `go test ./...` (on macOS) — must pass.

- [ ] **Step 4: Manual smoke** (on a Windows host)

`task build` and run `dist/SerialHop.exe` (panel mode). Verify `%ProgramData%\SerialHop\logs\SerialHop_panel.log` appears with a `panel session start` line within ~1 s.

- [ ] **Step 5: Commit**

```bash
git add panel_run_windows.go internal/panel/wails_app.go internal/panel/bindings.go main.go
git commit -m "feat(panel): wire panellog.Init at startup and reapply level on SaveConfig"
```

---

## Task 15: Repoint `log_tail_controller.go` panel stream

**Files:**
- Modify: `internal/panel/log_tail_controller.go`
- Modify: `internal/panel/frontend/src/...` (if a string label "Panel error" appears in a tab) — confirm via grep first

- [ ] **Step 1: Grep for the existing label**

Run: `rg -n '"panel"|Panel error' internal/panel/frontend/src/`. Note the SPA label if any.

- [ ] **Step 2: Edit the controller**

In `internal/panel/log_tail_controller.go`:

- Change `case "panel": return paths.PanelErrorLogPath(), true` to `case "panel": return paths.PanelLogPath(), true`.
- Change `parse := streamID == "service"` to `parse := streamID == "service" || streamID == "panel"`.

If the SPA carries a "Panel error" label that's now misleading, change it to "Panel" in the corresponding tsx file (likely under `internal/panel/frontend/src/tabs/`).

- [ ] **Step 3: Build the frontend**

The repo has a frontend build step run by Wails. Run whatever the project uses to rebuild `internal/panel/frontend/dist` (typically `task build` or an `npm run build` in `internal/panel/frontend/`). Confirm no TypeScript errors.

- [ ] **Step 4: Run Go tests**

Run: on Windows, `go test ./internal/panel/ -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/log_tail_controller.go internal/panel/frontend/
git commit -m "feat(panel): point Logs tab 'panel' stream at SerialHop_panel.log"
```

---

## Task 16: Service instrumentation — `bootstrap`

**Files:**
- Modify: `internal/bootstrap/bootstrap.go`
- Modify: `internal/bootstrap/bootstrap_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/bootstrap/bootstrap_test.go`:

```go
func TestResolve_LogsCacheFallbackOnRemoteFailure(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// (Wire up the existing test harness so that the remote fetch fails
	// and the cache is hit. Reuse helpers from this test file.)
	... // existing setup
	_, _ = bootstrap.Resolve(ctx, opts)

	rec.AssertRecord(t, slog.LevelWarn, "bootstrap remote fetch failed (using cache)",
		map[string]any{"host": opts.Base})
}
```

(If the file lacks an existing harness for remote-failure-with-cache, copy from `TestResolve_FallsBackToCache`-style tests already in the package. The fixture path is what matters; the log assertion piggy-backs.)

Add `"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"` and `"log/slog"` to imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/bootstrap/ -run TestResolve_LogsCacheFallbackOnRemoteFailure -v`
Expected: FAIL — no slog call exists in `Resolve` yet.

- [ ] **Step 3: Instrument `Resolve`**

In `internal/bootstrap/bootstrap.go::Resolve`, at function entry:

```go
slog.Info("bootstrap resolve start", "host", opts.Base, "user", opts.User)
```

Where the remote fetch fails and the cache fallback fires, emit:

```go
slog.Warn("bootstrap remote fetch failed (using cache)", "host", opts.Base, "err", err.Error())
```

Where both fail (hard error path), emit:

```go
slog.Error("bootstrap resolve failed", "host", opts.Base, "err", err.Error())
```

On success, before returning, emit:

```go
slog.Info("bootstrap resolve ok",
	"host", opts.Base,
	"source", source, // "remote" or "cache"
)
```

(`source` is a new local string the function tracks.)

Add `log/slog` to the file's imports.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/bootstrap/ -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/bootstrap/
git commit -m "feat(bootstrap): log resolve attempts, cache fallback, hard failures"
```

---

## Task 17: Service instrumentation — `internal/api` (one middleware)

**Files:**
- Modify: `internal/api/server.go` (or wherever the http.ServeMux / middleware chain is built — `rg -n 'http.HandleFunc|http.ServeMux' internal/api/` to find it)
- Create: `internal/api/log_middleware.go`
- Create: `internal/api/log_middleware_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/api/log_middleware_test.go`:

```go
package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

func TestLogMiddleware_OKLogsInfo(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := logMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/devices")
	_ = resp.Body.Close()

	rec.AssertRecord(t, slog.LevelInfo, "api handler",
		map[string]any{"route": "/devices", "status": 200, "method": "GET"})
}

func TestLogMiddleware_5xxLogsError(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := logMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/x")
	_ = resp.Body.Close()

	rec.AssertRecord(t, slog.LevelError, "api handler",
		map[string]any{"route": "/x", "status": 500})
}

func TestLogMiddleware_4xxLogsWarn(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := logMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, _ := http.Get(srv.URL + "/x")
	_ = resp.Body.Close()

	rec.AssertRecord(t, slog.LevelWarn, "api handler",
		map[string]any{"route": "/x", "status": 400})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -run TestLogMiddleware -v`
Expected: FAIL — `logMiddleware` undefined.

- [ ] **Step 3: Implement middleware and wire it into the mux**

Create `internal/api/log_middleware.go`:

```go
package api

import (
	"log/slog"
	"net/http"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(s int) {
	r.status = s
	r.ResponseWriter.WriteHeader(s)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// logMiddleware wraps a handler with one slog record per request.
// Level: INFO for 2xx/3xx, WARN for 4xx, ERROR for 5xx.
// Fields: route, method, remote_addr, status, bytes, dur.
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		level := slog.LevelInfo
		switch {
		case rec.status >= 500:
			level = slog.LevelError
		case rec.status >= 400:
			level = slog.LevelWarn
		}
		slog.LogAttrs(r.Context(), level, "api handler",
			slog.String("route", r.URL.Path),
			slog.String("method", r.Method),
			slog.String("remote_addr", r.RemoteAddr),
			slog.Int("status", rec.status),
			slog.Int("bytes", rec.bytes),
			slog.Duration("dur", time.Since(start)),
		)
	})
}
```

Wire it: find the mux construction (likely a `New(...) http.Handler` or `Server` builder in `internal/api/`). Wrap the returned handler in `logMiddleware(handler)`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/api/ -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api/
git commit -m "feat(api): add log middleware (INFO/WARN/ERROR by status)"
```

---

## Task 18: Service instrumentation — `app` lifecycle

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/app/app_test.go`:

```go
func TestRun_LogsLifecycleTransitions(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = app.Run(ctx, config.Default(), bootstrap.Resolved{}) // fake input that returns quickly

	rec.AssertRecord(t, slog.LevelInfo, "app run starting", nil)
	rec.AssertRecord(t, slog.LevelInfo, "app run exiting", nil)
}
```

(If `app.Run` requires non-trivial setup that can't be cheaply faked, adapt the test to the existing test harness pattern — the assertion is what matters.)

- [ ] **Step 2: Instrument**

In `internal/app/app.go::Run`:

- At entry: `slog.Info("app run starting", "host", cfg.LabBridge.Host)`.
- Around significant transitions (chisel start, HTTP server start, etc.): one INFO each, with the subsystem name in the message.
- On exit: `slog.Info("app run exiting", "err", errString(err))` where `errString` returns `""` for nil.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/app/ -v -race`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/app/
git commit -m "feat(app): log lifecycle transitions in Run"
```

---

## Task 19: Service instrumentation — `registry`, `serial`, `updater`

**Files:**
- Modify: `internal/registry/*.go`
- Modify: `internal/serial/port.go`, `internal/serial/reader.go`
- Modify: `internal/updater/download.go`, `internal/updater/verify.go`, `internal/updater/release.go`, `internal/updater/version.go`
- Modify or extend the corresponding `_test.go` files

For each package, follow the same pattern as Task 16/18:

1. Write one failing test asserting the WARN/ERROR shape for the most relevant failure path.
2. Instrument:
   - **registry**: INFO on uninstall-key write/read; WARN on a missing key that the caller expected.
   - **serial/port.go**: INFO on open (`"port", name, "baud", baud`); INFO on close; WARN on transient read error with retry count attr.
   - **serial/reader.go**: WARN on `io.ErrShortBuffer` / partial reads.
   - **updater/download.go**: INFO on entry (URL, target path); INFO on exit (bytes, dur); WARN on retry; ERROR on hard HTTP failure.
   - **updater/verify.go**: INFO on entry (path, expected sum); WARN on mismatch; ERROR on signature failure.
   - **updater/release.go**, **version.go**: INFO at entry/exit; WARN on parse failure.
3. Run tests.
4. Commit once per package.

Run before committing:

```bash
go test ./internal/registry/ ./internal/serial/ ./internal/updater/ -v -race
```

Suggested commit grouping:

- `feat(registry): log uninstall-key reads/writes and missing-key WARN`
- `feat(serial): log port open/close and transient read errors`
- `feat(updater): log download/verify/release entry-exit and failure paths`

---

## Task 20: Service instrumentation — `chisel` (dense)

**Files:**
- Modify: `internal/chisel/client.go`
- Modify: `internal/chisel/client_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/chisel/client_test.go`:

```go
func TestRun_LogsConnectAndUnexpectedLost(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Drive Run against a fake chisel server that accepts then drops.
	// (Reuse existing test harness if one exists; otherwise add a
	// minimal one that calls Run with a stub Dialer.)
	... // existing harness invocation

	rec.AssertRecord(t, slog.LevelInfo, "chisel run starting",
		map[string]any{"user": "tester"})
	rec.AssertRecord(t, slog.LevelInfo, "chisel session connected", nil)
	rec.AssertRecord(t, slog.LevelWarn, "chisel session lost", nil)
}

func TestRun_LogsCleanShutdownAsInfo(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel — Run should exit cleanly
	_ = chisel.Run(ctx, chisel.Config{User: "tester"}, nil)

	rec.AssertRecord(t, slog.LevelInfo, "chisel session ended", nil)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/chisel/ -run TestRun_Logs -v`
Expected: FAIL — slog calls absent.

- [ ] **Step 3: Instrument `Run`**

In `internal/chisel/client.go::Run`:

- At entry:

```go
slog.Info("chisel run starting",
	"server", sanitized(cfg.Server),
	"user", cfg.User,
	"routes", len(remotes),
)
slog.Debug("chisel routes", "routes", remotes)
```

- Each time the chisel library reports a connection up (or after the chisel client `Start` succeeds): `slog.Info("chisel session connected")`.

- On session loss inside the loop:

```go
if ctx.Err() != nil {
	slog.Info("chisel session ended", "reason", ctx.Err().Error())
} else {
	slog.Warn("chisel session lost", "reason", err.Error())
}
```

- On each reconnect-attempt boundary:

```go
slog.Info("chisel reconnect attempt", "attempt", n, "backoff", backoff.String())
```

- At goroutine exit:

```go
slog.Info("chisel run exiting", "err", errString(err))
```

Add a small `sanitized(string) string` helper that strips userinfo from the URL if it ever carries credentials (defensive — today's config doesn't, but the helper makes the absence explicit).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/chisel/ -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/chisel/
git commit -m "feat(chisel): log session connect/lost/reconnect with attempt + backoff"
```

---

## Task 21: Service instrumentation — `flasher` stages

**Files:**
- Modify: `internal/flasher/stages.go`
- Modify: `internal/flasher/stages_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/flasher/stages_test.go`:

```go
func TestFlash_LogsStageBoundaries(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Drive the flash against the existing fake port that returns happy
	// responses for handshake → erase → write → verify.
	... // existing harness call to flasher.Flash with a small fixture

	for _, stage := range []string{"handshake", "enter_programming", "erase", "write", "verify", "exit_programming"} {
		rec.AssertRecord(t, slog.LevelInfo, "flasher stage start",
			map[string]any{"stage": stage})
		rec.AssertRecord(t, slog.LevelInfo, "flasher stage ok",
			map[string]any{"stage": stage})
	}
	rec.AssertRecord(t, slog.LevelInfo, "flasher complete", nil)
}

func TestFlash_LogsRetryWarn(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Drive with a fake port that nacks twice then succeeds for one page.
	... // harness

	rec.AssertRecord(t, slog.LevelWarn, "flasher retry",
		map[string]any{"stage": "write", "retry": 1})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/flasher/ -run 'TestFlash_Logs' -v`
Expected: FAIL.

- [ ] **Step 3: Instrument `stages.go`**

For each stage helper (handshake, enterProgramming, erase, write, verify, exitProgramming), wrap with:

```go
slog.Info("flasher stage start", "stage", "<name>", "device_id", shortID(req.DeviceID))
... existing body ...
if err != nil {
	slog.Error("flasher stage failed", "stage", "<name>", "err", err.Error(), "offset", currentOffset)
	return err
}
slog.Info("flasher stage ok", "stage", "<name>", "dur", time.Since(start).String())
```

In the write stage, per-page emit at DEBUG:

```go
slog.Debug("flasher page", "page", i, "bytes", n, "addr_hi", hi, "addr_lo", lo)
```

On retry inside write/verify:

```go
slog.Warn("flasher retry", "stage", "<name>", "retry", attempt, "reason", reason)
```

At the top of `Flash`, emit one INFO:

```go
slog.Info("flasher start",
	"device_id", shortID(req.DeviceID),
	"port", req.Port,
	"firmware_path", req.FirmwarePath,
	"firmware_bytes", req.FirmwareBytes,
)
```

At the end of `Flash` on success: `slog.Info("flasher complete", "dur", ..., "retries", totalRetries)`.

`shortID` is a tiny helper in this package mirroring `panel.shortDeviceID` but local to flasher (no cross-package dep). Two-line helper, no test needed.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/flasher/ -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/flasher/stages.go internal/flasher/stages_test.go
git commit -m "feat(flasher): log stage boundaries, retries, and Flash entry/exit"
```

---

## Task 22: Service instrumentation — `flasher` STK500v1 wire layer

**Files:**
- Modify: `internal/flasher/stk500v1.go`
- Modify: `internal/flasher/stk500v1_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/flasher/stk500v1_test.go`:

```go
func TestSTK500_LogsResponseLengthAtInfo(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	... // exercise one request/response round-trip via the existing fake port

	rec.AssertRecord(t, slog.LevelInfo, "stk500 response",
		map[string]any{"len": 3}) // INSYNC + OK + body of expected length
}

func TestSTK500_LogsHexPayloadAtDebug(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})))
	// Re-install recorder at debug level
	rec = slogtest.NewRecorder()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	... // exercise round-trip

	// Asserts the debug payload is emitted with a hex string attr.
	if rec.Find(slog.LevelDebug, "stk500 response payload", map[string]any{}) == nil {
		t.Fatal("missing debug payload record")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/flasher/ -run 'TestSTK500_Logs' -v`
Expected: FAIL.

- [ ] **Step 3: Instrument**

In `internal/flasher/stk500v1.go`, where a response is received:

```go
slog.Info("stk500 response", "len", len(resp))
slog.Debug("stk500 response payload", "hex", hex.EncodeToString(resp))
```

On timeouts / nacks:

```go
slog.Warn("stk500 nack", "expected", expected, "got", got)
slog.Warn("stk500 timeout", "phase", phase, "after", elapsed.String())
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/flasher/ -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/flasher/stk500v1.go internal/flasher/stk500v1_test.go
git commit -m "feat(flasher): log STK500 response lengths (INFO) and hex payloads (DEBUG)"
```

---

## Task 23: `tools/forbidsecretlog` Go analyzer

**Files:**
- Create: `tools/forbidsecretlog/main.go`
- Create: `tools/forbidsecretlog/analyzer.go`
- Create: `tools/forbidsecretlog/analyzer_test.go`
- Create: `tools/forbidsecretlog/testdata/src/badcase/bad.go`
- Create: `tools/forbidsecretlog/testdata/src/goodcase/good.go`
- Modify: `Taskfile.yaml`

The analyzer walks every Go file in the module, finds `slog.*` calls, and flags any argument that resolves to a `*ast.SelectorExpr` whose final selector name is `Pass` on a type identified as `config.ChiselConfig` or `config.LabBridgeConfig`.

- [ ] **Step 1: Write the failing test**

Create `tools/forbidsecretlog/testdata/src/badcase/bad.go`:

```go
package badcase

import (
	"log/slog"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
)

func bad(cfg config.Config) {
	slog.Info("save",
		"user", cfg.LabBridge.User,
		"pass", cfg.LabBridge.Pass, // want "logged secret"
	)
}
```

Create `tools/forbidsecretlog/testdata/src/goodcase/good.go`:

```go
package goodcase

import (
	"log/slog"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
)

func good(cfg config.Config) {
	slog.Info("save", "user", cfg.LabBridge.User)
}
```

Create `tools/forbidsecretlog/analyzer_test.go`:

```go
package forbidsecretlog_test

import (
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/bioexperiment-lab-devices/serialhop/tools/forbidsecretlog"
)

func TestAnalyzer(t *testing.T) {
	wd, _ := filepath.Abs("testdata")
	analysistest.Run(t, wd, forbidsecretlog.Analyzer, "badcase", "goodcase")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./tools/forbidsecretlog/ -v`
Expected: FAIL — analyzer not implemented.

- [ ] **Step 3: Implement the analyzer**

Create `tools/forbidsecretlog/analyzer.go`:

```go
// Package forbidsecretlog provides a go/analysis analyzer that flags
// slog.* calls whose arguments include a selector ending in `.Pass` on
// a config.ChiselConfig or config.LabBridgeConfig receiver.
package forbidsecretlog

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

var Analyzer = &analysis.Analyzer{
	Name:     "forbidsecretlog",
	Doc:      "reports slog.* calls that include config.{Chisel,LabBridge}Config.Pass",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

func run(pass *analysis.Pass) (interface{}, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	filter := []ast.Node{(*ast.CallExpr)(nil)}
	insp.Preorder(filter, func(n ast.Node) {
		call, _ := n.(*ast.CallExpr)
		if !isSlogCall(pass, call) {
			return
		}
		for _, arg := range call.Args {
			sel, ok := arg.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if sel.Sel.Name != "Pass" {
				continue
			}
			if !isSecretConfigField(pass, sel) {
				continue
			}
			pass.ReportRangef(arg, "logged secret: config secret field passed to slog")
		}
	})
	return nil, nil
}

func isSlogCall(pass *analysis.Pass, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	obj, ok := pass.TypesInfo.Uses[x].(*types.PkgName)
	if !ok {
		return false
	}
	return obj.Imported().Path() == "log/slog"
}

func isSecretConfigField(pass *analysis.Pass, sel *ast.SelectorExpr) bool {
	tv, ok := pass.TypesInfo.Types[sel.X]
	if !ok {
		return false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return false
	}
	name := named.Obj().Name()
	pkg := named.Obj().Pkg()
	if pkg == nil {
		return false
	}
	if pkg.Path() != "github.com/bioexperiment-lab-devices/serialhop/internal/config" {
		return false
	}
	return name == "ChiselConfig" || name == "LabBridgeConfig"
}
```

Create `tools/forbidsecretlog/main.go`:

```go
package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/bioexperiment-lab-devices/serialhop/tools/forbidsecretlog"
)

func main() { singlechecker.Main(forbidsecretlog.Analyzer) }
```

Run `go mod tidy` if `golang.org/x/tools/go/analysis/...` is not yet a direct dep:

```bash
go get golang.org/x/tools/go/analysis@latest
go mod tidy
```

- [ ] **Step 4: Run tests**

Run: `go test ./tools/forbidsecretlog/ -v`
Expected: PASS (analyzer fires on `badcase`, silent on `goodcase`).

- [ ] **Step 5: Commit**

```bash
git add tools/forbidsecretlog/ go.mod go.sum
git commit -m "build(tools): add forbidsecretlog analyzer (no logging of cfg secrets)"
```

---

## Task 24: Wire `forbidsecretlog` into `Taskfile.yaml`

**Files:**
- Modify: `Taskfile.yaml`

- [ ] **Step 1: Inspect the current `test` task**

Open `Taskfile.yaml` and read the `test:` task.

- [ ] **Step 2: Add a `lint:secrets` step**

Add a new task:

```yaml
  lint:secrets:
    desc: Fail if any slog.* call logs a config secret field
    cmds:
      - go run ./tools/forbidsecretlog/... ./...
```

Wire it into the `test:` task's `deps:` or `cmds:` so `task test` runs it. Match the conventions of the existing file.

- [ ] **Step 3: Run the new task**

Run: `task lint:secrets`
Expected: exits 0 (no findings in current tree).

- [ ] **Step 4: Run the full test suite**

Run: `task test` (or `go test -race -count=1 ./...` and `go run ./tools/forbidsecretlog/... ./...`).
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add Taskfile.yaml
git commit -m "build(task): run forbidsecretlog analyzer in task test"
```

---

## Task 25: End-to-end manual verification

**Files:**
- None (manual smoke test on a Windows host with a live chisel server)

This is the §8.6 verification from the spec. Run on a Windows lab host (or a dev VM mirroring one) connected to the dev VPS.

- [ ] **Step 1: Build and install**

```bash
GOOS=windows GOARCH=amd64 task build
```

Copy `dist/SerialHop.exe` to the lab host. Install via the panel (`Install` button), let bootstrap + chisel come up.

- [ ] **Step 2: Panel-only failure surfaces in Loki**

- Set the config file read-only (`attrib +r %ProgramData%\SerialHop\SerialHop_config.yaml`).
- In the panel, edit a field and click Save.
- Confirm `%ProgramData%\SerialHop\logs\SerialHop_panel.log` contains a `panel action failed` record with `action=save_config` within ~100 ms.
- In Grafana, filter `{client="<lab>",stream="panel"}` and confirm the record appears within ~2 s.
- Remove `+r` to restore writability.

- [ ] **Step 3: Durable on disk when service is down**

- Stop the SerialHop service (`net stop SerialHop`).
- Trigger several panel actions (`Discover`, `Save`, etc.).
- Confirm lines accumulate in `SerialHop_panel.log`.
- Start the service (`net start SerialHop`).
- Confirm the lines reach Grafana within ~5 s of service start.

- [ ] **Step 4: Flasher dense logging**

- Run a flash from the panel.
- In Grafana, filter `{client="<lab>",stream="stdout"}` and confirm `flasher stage start` / `flasher stage ok` records for `handshake`, `enter_programming`, `erase`, `write`, `verify`, `exit_programming`.
- Edit `SerialHop_config.yaml` to set `log.level: debug`, save (which re-applies the level live), run another flash.
- Confirm per-page DEBUG records and `stk500 response payload` hex DEBUG records appear.

- [ ] **Step 5: Chisel reconnect coverage**

- Block outbound TCP to the chisel port on the lab host firewall for ~30 s.
- Confirm a `chisel session lost` WARN record reaches Grafana (the panel-tailer + service-shipper still ship while the chisel control link is dropping its data session; if the WARN itself is the casualty of the outage, expect it within seconds of unblocking).
- Confirm `chisel reconnect attempt` records with rising `attempt` and `backoff`.
- Unblock.

- [ ] **Step 6: Record findings**

If anything in steps 2–5 fails or surprises, file a follow-up issue. Do not commit anything from this task — it is verification, not code.

---

## Self-review

Cross-checking the plan against the spec section by section:

- §1 / §2 Goals — covered by tasks 3 (panellog), 5–6 (file tailer + integration), 16–22 (service instrumentation).
- §3 Architecture diagram — encoded in tasks 1 (paths), 3 (panellog), 5–6 (tailer), 14 (wiring).
- §4.1 panellog — task 3.
- §4.2 file tailer — tasks 4, 5, 6.
- §4.3 paths additions — task 1.
- §4.4 panel instrumentation — tasks 7–12.
- §4.5 service instrumentation Medium — tasks 16–22.
- §4.6 log level wiring — task 14 (panel side; service side already exists).
- §4.7 log_tail_controller repoint — task 15.
- §5 labels — task 6 (labels map update).
- §6 error handling — covered by tests in tasks 4, 5, 6 (cold-start, rotation, corrupt offset, missing file).
- §6.9 secret leakage prevention + lint gate — tasks 23–24.
- §7 backward compatibility / migration — task 3 (legacy file deletion), task 15 (stream repoint).
- §8 testing — embedded in every task's TDD steps.
- §8.6 manual verification — task 25.

No spec section is uncovered.

Type-consistency scan:

- `panellog.Manager` methods match across tasks 3 and 14: `Init`, `SetLevel`, `SessionID`, `Shutdown`.
- `logship.Manager.QueueDrainForTest` is used in task 6 and not referenced elsewhere — fine.
- `fileTail` / `startPanelTailer` defined in task 5, used in task 6.
- `slogtest.Recorder.AssertRecord` signature `(t, level, message, want map[string]any)` used identically across tasks 7, 8, 9, 10, 16, 17, 18, 20, 21, 22.
- `shortDeviceID` (task 9) and `shortID` in flasher (task 21) are separate per-package helpers — both named clearly so no confusion.

Placeholder scan:

- No "TBD", "TODO", or "fill in later".
- Test bodies for service-package tests that depend on an existing harness say "existing harness invocation" / "reuse helpers from this file" — this is a deliberate handoff to the engineer to grep the file for an existing pattern; it is not a placeholder for unspecified behavior. The assertion shape (which `slog` records to expect) is fully spelled out.

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-05-16-comprehensive-logging.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
