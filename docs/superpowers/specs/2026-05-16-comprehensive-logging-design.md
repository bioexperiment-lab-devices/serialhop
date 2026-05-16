# Lab Devices Client — Comprehensive logging for service + panel

**Date:** 2026-05-16
**Status:** Draft (brainstorming complete; pending spec review before plan)
**Builds on:** [`2026-04-28-log-streaming-design.md`](./2026-04-28-log-streaming-design.md)
**Target platform:** Windows (amd64); cross-compile and tests on macOS continue to work.

## 1. Purpose

When a remote client reports "the panel said Install failed" or "the Devices tab is empty", we currently have nothing to look at:

- The service log (`SerialHop.log`) carries a handful of lifecycle events from `winsvc/worker.go` and `main.go`. The remaining ~4 400 LOC across `chisel`, `serial`, `registry`, `flasher`, `updater`, `bootstrap`, `api`, and `app` emit **zero** log calls.
- The panel side (`internal/panel/`) has **no `slog` usage at all**. Failures vanish into UI warn-toasts; a sparse `writePanelDebugLog` writes single-line breadcrumbs to `SerialHop_panel_error.log`, and `appendCrashJournal` records caught React errors in a 64 KiB plaintext journal. Neither file reaches Loki.

This change closes both gaps:

- **Panel side** — every user action, every state transition, every internal error, every external HTTP failure flows through `slog` into a new structured log `SerialHop_panel.log`, then ships to Loki by piggy-backing on the service's existing chisel tunnel via a file-tailing extension to `internal/logship`.
- **Service side** — a Medium-depth instrumentation pass adds INFO at every exported entry point, WARN on every non-fatal error path, and ERROR on every fatal. The two operations that fail most painfully (flashing, chisel session lifecycle) get dense coverage.

The design is purely additive. The on-disk rotated log files remain the durable record.

## 2. Goals and non-goals

### Goals

- A remote operator can filter Grafana by `client="<lab>"` and `stream="panel"` and see every user action that lab took, with outcome and duration, plus every panel-side failure with stack/error context.
- A remote operator can filter `stream="stdout"` and see chisel session transitions, flasher stage progression, updater check/download/install outcomes, bootstrap resolution events, and per-handler `api/` request lines — enough to debug field incidents without asking for files.
- Panel logs are durable on disk even when the service is down (typical case during install failures) and ship in full once the service comes back.
- Log volume in INFO-default mode stays comparable to today's `SerialHop.log` volume — chatty paths (probe ticks, polling success bodies, per-page flash progress) go to DEBUG and are filterable.
- No new public network surface, no new server-side endpoints, no new auth.
- No new Go module dependencies. Stdlib + `lumberjack` (already used).

### Non-goals

- Browser-side (React/TS) `console.log` capture. The crash reporter and `appendCrashJournal` already cover render errors and unhandled rejections.
- Panel logs shipping via a path independent of the service. The chisel tunnel remains the single egress.
- Real-time push from panel to service. The file-tail polling lag (≤500 ms steady-state, ≤service restart in the cold-start case) is acceptable for diagnostic breadcrumbs.
- PII scrubbing or structured-log inference. Line bodies are forwarded verbatim. No secrets are logged anywhere (config password, chisel auth password, etc.); payload fields explicitly enumerate "no secrets ever" in §6.
- A configuration kill switch. The new logging follows `cfg.Log.Level` like everything else.

## 3. Architecture overview

```
PANEL PROCESS (desktop user)               SERVICE PROCESS (LocalSystem)
┌──────────────────────────┐               ┌─────────────────────────────┐
│ panel bindings,          │               │ slog (app, chisel, flasher, │
│ probe loops,             │               │  updater, bootstrap, …)     │
│ scmPollLoop,             │               │           │                 │
│ updater, crash reporter  │               │           ▼                 │
│           │              │               │  slog JSON handler          │
│           ▼              │               │     │           │           │
│   slog JSON handler      │               │     ▼           ▼           │
│           │              │               │ lumberjack   queueWriter    │
│           ▼              │               │ (SerialHop.log)     │       │
│   lumberjack             │               │                     │       │
│   (SerialHop_panel.log)  │ ◄── tail ──── │  panel-log tailer  ─┤       │
│                          │  (offset       │  (new in logship)   │       │
│  Also (unchanged):       │   persisted)   │                     ▼       │
│  crash journal,          │               │              shipQueue ──▶ shipper ──▶ Loki
│  64 KiB plaintext        │               │                     ▲       │
└──────────────────────────┘               │                     │       │
                                           │ os.Stderr ▶ pipe ▶ stderr tap ─┤
                                           │  (SerialHop_stderr.log)        │
                                           └─────────────────────────────┘
```

Three streams reach Loki via the existing service-only `logship` shipper:

| stream value | source | path | shipped by |
|---|---|---|---|
| `stdout` | service `slog` | `SerialHop.log` | in-process `queueWriter` (today) |
| `stderr` | service stderr (panic, chisel.Logger) | `SerialHop_stderr.log` | in-process stderr tap (today) |
| `panel` | panel `slog` | `SerialHop_panel.log` | **new** file tailer in `logship` |

All other elements of the 2026-04-28 design are unchanged: labels, push body, backoff, queue capacity, drop-oldest, shipping cadence.

## 4. Components

### 4.1 New package `internal/panellog` (build tag `windows`)

Mirrors the shape of `internal/logship` but in-process to the panel only — no shipper, no queue. Its sole job is to install a JSON slog handler that writes to a rotated file.

```go
package panellog

type Manager struct { /* unexported */ }

func Init(version string, level slog.Level) (*Manager, error)
func (m *Manager) SetLevel(level slog.Level)
func (m *Manager) Shutdown(ctx context.Context) error
func (m *Manager) SessionID() string  // for inclusion in user-facing crash reports
```

Behavior:

- `Init`:
  - Calls `paths.EnsureDirs()` (idempotent with the service-side call).
  - Builds a `lumberjack.Logger` against `paths.PanelLogPath()` with the same policy as `SerialHop.log`: `MaxSize: 10` (MiB), `MaxBackups: 3`, `Compress: false`. Trade-off note — log rotation has a single writer (the panel process), so no cross-process locking is needed. Service-side tailer treats rotation as a normal event (see §4.2).
  - Generates a session id (UUIDv4 from `crypto/rand` — no new dep) and stores it.
  - Builds a `slog.NewJSONHandler` with the shared `slog.LevelVar` for `SetLevel`, wrapped in a handler that pre-attaches `panel.session_id` and `panel.pid` as default attributes (via `slog.Handler.WithAttrs`).
  - Calls `slog.SetDefault`.
  - On the very first call, deletes `paths.PanelErrorLogPath()` if it exists — single-shot migration of the old breadcrumb file (§7).
  - Emits one INFO record: `slog.Info("panel session start", "version", version, "data_dir", paths.DataDir(), "config_present", configPresent)`.
- `SetLevel(l)`: `levelVar.Set(l)`. Cheap and race-free, same pattern as logship.
- `Shutdown(ctx)`: emits `slog.Info("panel session end")` and closes the lumberjack writer.
- `SessionID()`: returns the cached UUID so the crash reporter can include it in the plaintext bundle copied to the clipboard (`buildCrashReport` in `crashReporter.ts` — passed in as a parameter at startup).

`Manager` is held by the panel's `main.App` struct (the binding target in `main`) and `panel.App`. The `Init` call lives in `panel_run_windows.go` *before* `wails.Run`, so a Wails-init failure is itself captured.

### 4.2 Extending `internal/logship` with a file tailer

A new file `internal/logship/file_tail.go` adds:

```go
type fileTail struct {
    q          *queue
    path       string         // paths.PanelLogPath()
    offsetPath string         // paths.PanelLogOffsetPath()
    stream     string         // "panel"
    poll       time.Duration  // 500 * time.Millisecond
}

func (ft *fileTail) run(ctx context.Context)
```

Loop body (one goroutine):

1. `time.Sleep(ft.poll)` (or return on `ctx.Done()`).
2. `os.Stat(ft.path)`. If the file doesn't exist, log INFO once on first miss and continue.
3. Compare the stat result against the persisted `offsetState` (`{size, mtime_unix_nano, byte_offset}` JSON in `offsetPath`):
   - **First run** / offset file missing or corrupt: set `byte_offset` to current EOF, persist, continue. (Cold-start fallback — see §6.3.)
   - **`size < byte_offset`** (file rotated and recreated, or truncated): reset `byte_offset` to 0 and the saved size signature to the new stat, persist.
   - **`size == byte_offset`**: nothing new, continue.
   - **`size > byte_offset`**: proceed to read.
4. `os.Open` the file, `Seek(byte_offset, io.SeekStart)`, wrap in `bufio.Scanner` with `Buffer` set to 1 MiB (same as stderr tap).
5. For each `Scan()` call: `q.push(record{stream:"panel", tsNano: time.Now().UnixNano(), line: <line>})`. Update an in-memory `byte_offset` accumulator from `Scanner.Bytes()` length + 1 for newline.
6. On `Scanner.Err()`: WARN and break to outer loop — next tick reopens.
7. On EOF: persist new `byte_offset` to `offsetPath` via atomic write (write to `<path>.tmp`, then `os.Rename`). Close the file. Outer loop iterates.

The tailer is started unconditionally inside `logship.Init` (alongside the other taps). `StartShipper` later turns shipping on; until then, panel records pile into the same queue as service records and drop-oldest harmlessly.

Implementation note: the existing panel-side `internal/panel/filetail.go` (`FileTail`, used by the log viewer) is unrelated — it lives in the panel process and emits Wails events; the service-side `fileTail` is a fresh implementation living in `internal/logship`. Different ownership, different purpose; the name overlap is unfortunate but the package qualifier disambiguates.

### 4.3 `paths` package additions

```go
const (
    PanelLogFileName       = "SerialHop_panel.log"
    PanelLogOffsetFileName = "panel-log.offset"
)

func PanelLogPath() string         // <LogsDir>/SerialHop_panel.log
func PanelLogOffsetPath() string   // <DataDir>/state/panel-log.offset
func StateDir() string             // <DataDir>/state
```

`EnsureDirs()` extended to also mkdir `<DataDir>/state` with 0o750. (Same pattern as the existing `logs/` mkdir.)

The old `PanelErrorLogPath` and `PanelErrorLogFileName` are **kept** in the API surface so `panellog.Init` can call them at migration time, but no code path writes to that file anymore. A follow-up PR can remove them once the migration window is past; leaving them in this change keeps the diff focused on the additive bits.

### 4.4 Panel-side instrumentation

A small helper threads through every binding that performs an action:

```go
// internal/panel/bindings_helpers.go (new function)
func (a *App) logAction(name string, attrs ...slog.Attr) func(err error, extra ...slog.Attr) {
    start := time.Now()
    slog.LogAttrs(context.Background(), slog.LevelInfo,
        "panel action start", append([]slog.Attr{slog.String("action", name)}, attrs...)...)
    return func(err error, extra ...slog.Attr) {
        attrs := append([]slog.Attr{
            slog.String("action", name),
            slog.Duration("dur", time.Since(start)),
        }, extra...)
        if err != nil {
            slog.LogAttrs(context.Background(), slog.LevelError, "panel action failed",
                append(attrs, slog.String("err", err.Error()))...)
            return
        }
        slog.LogAttrs(context.Background(), slog.LevelInfo, "panel action ok", attrs...)
    }
}
```

Usage per binding:

```go
func (a *App) Install() AdminResult {
    done := a.logAction("install")
    res := a.svc.Install(...)
    done(installErr(res), slog.Bool("cancelled", res.Cancelled))
    return res
}
```

Instrumented sites (every binding listed produces exactly one start + one end record):

| Binding | Args logged | Outcome extras |
|---|---|---|
| `Install` | none | `cancelled` |
| `Uninstall` | none | `cancelled` |
| `Restart` | none | `cancelled` |
| `SaveConfig` | `cfg_host` (host only), `field_count` | `field_errors_count` |
| `Discover` | none | `device_count`, `reachable` |
| `Disconnect` | `device_id` (sha256-prefix, not raw — see §6) | `reachable` |
| `GetDevices` | none | `device_count`, `reachable` |
| `GetPorts` | none | `port_count`, `reachable` |
| `DownloadUpdate` | `tag` | `bytes`, `checksum_ok` |
| `InstallUpdate` | `tag` | `cancelled` |
| `RecheckUpdate` | none | `available`, `tag` |
| `VerifyCredentials` | `host` | `outcome` |
| `RecordFrontendCrash` | `source`, `message_len` | (no end record — fire-and-forget; emits an ERROR with stack instead) |

Non-binding panel sites:

- `wails_app.go::startup` — emits `panel session start` (via `panellog.Init`) plus DEBUG `startup completed`.
- `wails_app.go::shutdown` — emits `panel session end`.
- `scmPollLoop` — INFO only on state transition: `slog.Info("scm state change", "from", old.state, "to", new.state, "cfg_valid", new.cfgValid)`. No log on no-change ticks.
- `probeLoop` for server and tunnel — WARN on the **first** failure after a streak of successes (or after probe-reason change), then dedupes: subsequent identical failures within 5 minutes are silent. INFO on recovery (red → green).
- `appendCrashJournal` — keeps writing the plaintext journal (unchanged behavior); additionally calls `slog.Error("frontend crash", "source", source, "message", message, "stack_len", len(stack))`. Stack body is **not** included in the log line (it's already in the crash journal); a `crash_journal_path` attribute points the operator at the local file.
- `writePanelDebugLog` — **removed**. The five remaining call sites switch to direct `slog.Error("…", "err", err)` calls.

### 4.5 Service-side instrumentation (Medium)

Light layer (every package):

- `internal/bootstrap/bootstrap.go::Resolve` — INFO on entry (host); INFO on cache-hit / cache-miss; WARN on remote-fetch failure followed by cache fallback; ERROR when both fail.
- `internal/api/*.go` — INFO on each handler entry (route, remote addr, method); INFO with `status` and `dur` on exit for 2xx/3xx; WARN with `status` and `err` for 4xx; ERROR for 5xx. Implemented as a single middleware wrapper, not per-handler edits.
- `internal/app/app.go::Run` — INFO at each lifecycle transition.
- `internal/registry/*.go` — INFO on uninstall-key write/read; WARN on missing key when one is expected.
- `internal/serial/port.go` — INFO on open with port + baud; INFO on close; WARN on transient read error with retry count; ERROR on unrecoverable open failure.
- `internal/updater/{download,verify,release,version}.go` — INFO on entry (URL or path), INFO on exit (bytes, duration); WARN on checksum mismatch; ERROR on signature failure or hard HTTP failure.

Dense layer (two packages):

- `internal/chisel/client.go::Run`:
  - INFO at entry with sanitized server URL, user, and route count (routes themselves at DEBUG — they contain port numbers but no secrets).
  - INFO `chisel session connected` once.
  - WARN `chisel session lost` with reason on each unexpected disconnect. Disconnects driven by `ctx.Done()` (clean shutdown) emit INFO `chisel session ended` instead.
  - INFO `chisel reconnect attempt` with attempt number and backoff duration.
  - INFO `chisel run exiting` with reason at goroutine exit.
  - Today the chisel library logs to a custom `chisel.Logger` that writes to stderr; that pathway continues to work and reaches `stream:"stderr"`. The new `slog` calls are *our* events around the library, not a replacement.
- `internal/flasher/stages.go` + `internal/flasher/stk500v1.go`:
  - INFO at each stage boundary: `handshake`, `enter_programming`, `erase`, `write`, `verify`, `exit_programming` — with `device_id` (sha256-prefix), `port`, `firmware_path`, `firmware_bytes`.
  - DEBUG per page write: `page_index`, `bytes`, `addr_hi`, `addr_lo`. Payload bodies (the response from the bootloader to a `R` read-page command, or the `T` echo) **may** be logged at DEBUG as hex strings — they are protocol data, not secrets. INFO never carries payload bytes.
  - WARN on retry with retry index and reason (nack, timeout, length mismatch).
  - ERROR on terminal failure with stage and last-attempted offset.
  - INFO on success with total bytes, total duration, retry count.

### 4.6 Log level wiring

- `cfg.Log.Level` (existing) drives both managers. On `SaveConfig`, the panel side calls `panellog.SetLevel(parseLogLevel(cfg.Log.Level))` (`parseLogLevel` already exists in `logship`; promoted to a small shared helper if needed).
- The service side already does this via `manager.SetLevel(logship.ParseLogLevel(cfg.Log.Level))` on each config load. No change.
- Default level INFO. Setting `log.level: debug` in the config produces: per-page flasher progress, raw serial payloads (hex), per-tick probe results, raw chisel route list, full HTTP handler entry/exit pairs for every poll.

### 4.7 `log_tail_controller.go` repoint

`streamPath` extends to:

```go
case "panel":
    return paths.PanelLogPath(), true
case "panel-error":  // legacy; only present if the file still exists
    return paths.PanelErrorLogPath(), true
```

The viewer in the SPA already parses slog JSON when `streamID == "service"`; the `parse := streamID == "service"` line in `start()` extends to `streamID == "service" || streamID == "panel"`. The new panel log is JSON; the legacy `panel-error` (if shown for historical files) is raw.

## 5. Data flow and labels

Labels reuse the existing 4-label scheme from the 2026-04-28 spec exactly. The only change is that the `stream` label gains a third value: `"panel"`.

| label | value | source |
|---|---|---|
| `client` | chisel auth username | `cfg.Chisel.User`, passed into `StartShipper` |
| `stream` | `"stdout"` / `"stderr"` / `"panel"` | from queue record |
| `service` | `"lab_devices_client"` | hardcoded constant |
| `version` | client semver | `version.Version` |

`panel.session_id` and `panel.pid` are JSON fields inside each panel line, **not** Loki labels. This keeps cardinality bounded (Loki rejects high-cardinality label values).

Push body unchanged from §6 of the 2026-04-28 spec. Up to three `streams[]` entries per push instead of two.

## 6. Error handling and edge cases

### 6.1 Panel log file missing when service starts

`fileTail.run` `os.Stat` returns `ErrNotExist`. INFO once: `panel log not yet present, will retry`. Subsequent missing-file ticks are silent. When the panel later creates the file (its first slog write), the next 500 ms tick picks it up at offset 0.

### 6.2 Lumberjack rotation

Lumberjack renames `SerialHop_panel.log` → `SerialHop_panel.log.1` and opens a new `SerialHop_panel.log`. Next service-side tail tick sees `stat.Size() < saved.byte_offset`: reset `byte_offset` to 0, persist, continue reading the new file from the start. The rotated-to backup file's tail end is lost from the *shipped* stream, but the file is on disk and the operator can copy it manually. Acceptable trade-off for a code path that fires every ~10 MiB of panel logs (in practice, hours-to-days).

### 6.3 Offset file corrupt, missing, or signature mismatch

`offsetState` JSON fails to parse, file unreadable, or the saved `{size, mtime}` signature implies the file has been replaced wholesale (e.g., manual operator intervention, clock skew): fall back to current EOF, log `slog.Warn("panel log offset reset", "reason", reason)`, persist new offset, continue. Worst case we drop unshipped panel lines from one service-down window — they remain on disk.

### 6.4 Panel writes fail

`lumberjack.Logger` swallows transient errors and returns an error to `slog`, which discards. Same behavior as today's service log. No new failure modes introduced.

### 6.5 Panel runs without `%ProgramData%` writable

`panellog.Init` returns an error from `paths.EnsureDirs`. `panel_run_windows.go` falls back to a no-op slog handler (`slog.New(slog.NewTextHandler(io.Discard, nil))`) and proceeds to `wails.Run`. UI still works; we just lose panel logs for that session. The existing `writePanelStartupError` path in `main.go` still records this failure to the install-dir fallback file.

### 6.6 Service down while panel logs

Panel file keeps growing locally. Tailer resumes from `byte_offset` when the service starts. Up to `10 MiB × (1 active + 3 backups) = 40 MiB` of panel log lines are recoverable; older history is lost to rotation but remains on disk.

### 6.7 Panel emits faster than shipper drains

Queue overflow path is unchanged: drop-oldest, `dropped` counter increments, surfaced via `slog.Warn("logs dropped", "count", n)` after the next successful push. The Warn carries no `stream` distinction — operators see how many lines were dropped overall, not which stream they came from. Acceptable: drops are rare and the indication that *any* drops happened is the signal to investigate.

### 6.8 Cardinality

Three stream values, four labels total. Well below Loki's 15-label / 1024-char-value defensive ceiling.

### 6.9 Secret leakage prevention

Three classes of "looks like data, must not be logged":

- **Config secrets** — `cfg.Chisel.Pass`, `cfg.LabBridge.Pass`. Never reach a log line. `SaveConfig` logs `cfg_host`, not the whole struct. `VerifyCredentials` logs `host` and `outcome`, not the password.
- **Serial response payloads at INFO** — `stk500v1.go` logs response *lengths* at INFO. Hex payloads only at DEBUG, where the operator has explicitly opted in.
- **Device identifiers** — full device IDs (which can encode lab-specific metadata) are hashed to an 8-char sha256 prefix before logging. The hash is stable within a session for cross-referencing.

A lint gate is added as part of this change: a small Go analyzer under `tools/forbidsecretlog` that walks `slog.*` call expressions and fails if any argument resolves to a selector ending in `.Pass` on a `config.ChiselConfig` or `config.LabBridgeConfig` receiver. Run from `Taskfile.yaml::test` alongside `golangci-lint`. The repo already prefers Go tools over shell (`tools/render-manifest` precedent); shell-grep is rejected for the cross-platform reasons documented in `CLAUDE.md`.

### 6.10 Test slog handler isolation

Existing tests do not call `slog.SetDefault`. The new instrumentation in service packages means tests that exercise those code paths will write to the default slog handler — which under `go test` is `slog.NewTextHandler(os.Stderr, ...)`. That's noisy but harmless. Tests that *assert* on log records install a per-test handler via `slog.New(testHandler).` then defer-restore the original. See §8.4.

## 7. Backward compatibility and migration

- `SerialHop.log` and `SerialHop_stderr.log` formats and rotation: **unchanged**.
- `SerialHop_panel_error.log`: deleted by `panellog.Init` on first run if present. The five existing `writePanelDebugLog` call sites are converted to `slog.Error`. `paths.PanelErrorLogPath` and `PanelErrorLogFileName` remain in the API surface for the migration window; a follow-up PR removes them.
- `SerialHop_panel_crash.log`: **unchanged** — kept for "copy-paste into a bug report" ergonomics. Each crash additionally emits a `slog.Error` in the new panel log so Loki sees it.
- `log_tail_controller.go` "panel" stream: repoints to the new `SerialHop_panel.log`. The viewer auto-parses JSON because the stream is in the JSON-known list now. No SPA change required beyond a one-line label tweak (existing UI string `"Panel error"` → `"Panel"`).
- New `<DataDir>/state/` subdirectory and `panel-log.offset` file: created by `paths.EnsureDirs`. Survives uninstall/reinstall the same way `<DataDir>/logs/` does (the installer preserves `%ProgramData%\SerialHop` by design).
- `cfg.Log.Level` semantics: unchanged; now drives both managers.
- Older clients running pre-change binaries: no panel log file is created, service-side tailer sees the file missing and silently waits. Zero impact.
- Older clients with a stale `SerialHop_panel_error.log`: deleted by `panellog.Init` on first new-binary start. Pre-change content is lost — acceptable because the file holds only single-line breadcrumbs and was rarely useful.

## 8. Testing strategy

The whole new code surface is platform-neutral Go where possible. Tests run on macOS and Windows alike.

### 8.1 `internal/panellog/panellog_test.go`

- `Init` then `slog.Info("test", "k", "v")` produces one JSON record on disk with expected fields (including `panel.session_id`, `panel.pid`).
- `SetLevel(slog.LevelDebug)` flips debug emission live without re-installing the handler.
- Session id remains stable across many calls within one `Init`.
- `Shutdown` emits the closing INFO and flushes lumberjack.
- Migration: with a stale `SerialHop_panel_error.log` present, `Init` deletes it.
- `Init` failure (write-protected dir) returns an error; subsequent fall-back path (no-op handler) is exercised in a separate test for `panel_run_windows.go`'s startup.

### 8.2 `internal/logship/file_tail_test.go`

- Write 100 JSON lines to a tempdir file, run one tail cycle, assert all 100 reach the queue with `stream:"panel"`.
- Persisted offset round-trip: write 100 lines, tail, kill, append 50 more, restart tail with same offset file — only the 50 new lines reach the queue.
- Rotation: write 100 lines, rename-and-recreate, write 50 — assert the 50 reach the queue and the offset resets to past those 50.
- Corrupt offset file (truncated JSON) → fall back to EOF, WARN logged, continue.
- File missing → single INFO, then silent polling; creates file mid-test, lines from offset 0 reach the queue.
- Line longer than 1 MiB: WARN, line skipped, scanner recreated, subsequent lines OK.

### 8.3 `internal/logship/logship_test.go` (extensions)

- The shipper push body, when both stdout and panel records are in the same batch, decodes as `streams[]` with two entries, both carrying the correct stream label.
- Empty `cfg.Chisel.User` (no shipper started): panel-log tailer still pushes into the queue and drops oldest; no error.

### 8.4 Per-package instrumentation tests

For each instrumented service package, add a small table-driven test asserting the WARN-on-error and ERROR-on-fatal paths fire. Pattern:

```go
func TestFlasher_LogsRetryWarn(t *testing.T) {
    h := slogtest.NewRecorder()
    prev := slog.Default()
    slog.SetDefault(slog.New(h))
    t.Cleanup(func() { slog.SetDefault(prev) })

    // drive the flasher with a fake port that nacks twice then succeeds
    ...
    h.AssertRecord(t, slog.LevelWarn, "flasher retry", "retry", 1)
    h.AssertRecord(t, slog.LevelWarn, "flasher retry", "retry", 2)
}
```

A tiny new test helper `internal/slogtest/recorder.go` (no new module dep — pure stdlib) implements the recorder.

### 8.5 Panel binding tests

`internal/panel/bindings_*_test.go` files extend to assert one INFO `panel action start` + one INFO `panel action ok` (or one ERROR `panel action failed`) per binding call. Reuses the same `slogtest.NewRecorder` helper.

### 8.6 Manual verification (dev VPS)

1. **Panel-only failure surfaces in Loki.**
   - Make `SaveConfig` fail by `attrib +r` on the config file. Trigger save from the UI.
   - Confirm: line in `SerialHop_panel.log` within ~100 ms; reaches Grafana within ~2 s with `stream="panel"` and `action="save_config"`.
2. **Durable on disk when service is down.**
   - Stop the SerialHop service. Trigger several panel actions. Verify lines accumulate in `SerialHop_panel.log`.
   - Start the service. Verify the lines reach Grafana within ~5 s.
3. **Flasher dense logging.**
   - Run a flash. Confirm Grafana shows the stage boundaries (handshake/erase/write/verify) at INFO. Switch `log.level` to `debug`, restart, run another flash, confirm per-page records and hex payloads appear in `stream="stdout"`.
4. **Chisel reconnect coverage.**
   - Block outbound TCP to the chisel port for 30 s. Confirm a `chisel session lost` WARN + `chisel reconnect attempt` INFO records in Grafana, including attempt count and backoff.

## 9. Risks and trade-offs

- **Service-side instrumentation is broad.** ~25–35 new `slog` call sites across eight packages, plus one HTTP middleware. The risk is a noisy INFO baseline that floods Loki when many labs are connected. Mitigation: per-tick probe results and per-page flasher progress are explicitly DEBUG. We can re-tune any specific call site post-rollout by demoting INFO → DEBUG without changing behavior.
- **Panel-log file growing under sustained UI use.** With ~10 KB/day estimated baseline panel volume, 10 MiB lasts months. Bursty crash loops could rotate faster, but the rotation policy caps disk at 40 MiB. Acceptable.
- **Tailer polling adds 500 ms of latency.** Diagnostic latency, not control-path latency. Already discussed in §2.
- **Slog default handler change in panel.** Once `panellog.Init` runs, every package the panel imports that calls `slog.*` will write to `SerialHop_panel.log`. The panel imports `internal/winsvc` (for SCM queries) and `internal/api` (for DTOs); their slog calls — when invoked from the panel process — will end up in the panel log. This is the right outcome: it means panel-initiated calls to those packages get logged in panel context, not service context. No special handling needed; just a consequence to be aware of when reading Grafana.
- **Lint gate for secret leakage.** The added grep gate has false-positive risk if someone names a non-secret field `Pass`. The gate is narrowly scoped (`cfg.Chisel.Pass` / `cfg.LabBridge.Pass` literal patterns) to minimize this.

## 10. Out of scope

Reiterating §2 in checklist form so the implementation plan stays bounded:

- Browser-side `console.log` capture
- Server-side ingest changes (the existing Loki endpoint is already accepting from the chisel forward tunnel)
- Per-stream label refinement (e.g., adding `subsystem="flasher"` as a label)
- Structured field extraction / metrics from log lines
- Real-time IPC between panel and service
- Replacing the plaintext crash journal with the new structured log
- Removing the deprecated `paths.PanelErrorLogPath` API (deferred to follow-up)
