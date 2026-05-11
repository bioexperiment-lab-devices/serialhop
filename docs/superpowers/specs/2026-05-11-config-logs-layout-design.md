# Config & Logs Layout — Design

**Date:** 2026-05-11
**Status:** Approved (brainstorming complete; pending spec review before plan)

## 1. Purpose & scope

Move the config file and all log files out of the install directory into `%ProgramData%\SerialHop\`. The install directory ends up holding only `SerialHop.exe` (plus transient update-staging files). Replace the panel's "Open log file" button with "Open logs folder", which opens the new logs directory in Explorer so the operator can reach `SerialHop.log`, `SerialHop_stderr.log`, rotated backups, and `SerialHop_panel_error.log` from one place.

Motivation: tidiness. Operators inspecting the install directory in Explorer should see the binary, not a pile of rotating logs and a YAML file. The change is a clean break — no users on v0.7.0 yet, so no migration code is shipped.

Out of scope:

- Migrating existing v0.7.0 installs (clean break by request).
- Changing log rotation policy (still 10 MB / 3 backups via lumberjack).
- Moving the update-staging files (`SerialHop-v*.exe`, `SerialHop.exe.old`) — those must live alongside the running `.exe` so the elevated swap can rename intra-directory.
- Per-user data segregation (one install per machine, shared config and logs across operators).
- ACL hardening on the new directory (default `%ProgramData%` inheritance is sufficient — see §6).

## 2. On-disk layout

After this change:

```
%ProgramData%\SerialHop\
├── SerialHop_config.yaml
└── logs\
    ├── SerialHop.log
    ├── SerialHop.log.1                (lumberjack rotation backup; up to 3)
    ├── SerialHop.log.2
    ├── SerialHop.log.3
    ├── SerialHop_stderr.log
    ├── SerialHop_stderr.log.1         (same rotation policy)
    ├── …
    └── SerialHop_panel_error.log      (append-only; no rotation; rare writes only)

C:\Tools\SerialHop\                    (or wherever the operator chose to install)
├── SerialHop.exe
├── SerialHop-vX.Y.Z.exe               (transient, only during auto-update staging)
└── SerialHop.exe.old                  (transient, cleaned on next panel launch)
```

Steady state of the install dir is one file: `SerialHop.exe`.

## 3. New package: `internal/paths`

Single source of truth for on-disk layout. Pure stdlib (env-var reads, `filepath.Join`, `os.MkdirAll`); no Windows-only API calls, so it compiles and tests cleanly on macOS/Linux without build tags or fakes.

```go
// Package paths owns the on-disk layout for the SerialHop client.
//
// Production: DataDir() returns %ProgramData%\SerialHop.
// Tests: set SERIALHOP_DATA_DIR to override the root.
package paths

const (
    ConfigFileName        = "SerialHop_config.yaml"
    ServiceLogFileName    = "SerialHop.log"
    StderrLogFileName     = "SerialHop_stderr.log"
    PanelErrorLogFileName = "SerialHop_panel_error.log"
)

// DataDir returns the SerialHop root data directory.
// Honors SERIALHOP_DATA_DIR (test override) ahead of ProgramData.
// Returns "" if neither is set — callers must check.
func DataDir() string

// LogsDir returns <DataDir>\logs.
func LogsDir() string

// ConfigPath returns <DataDir>\SerialHop_config.yaml.
func ConfigPath() string

// ServiceLogPath / StderrLogPath / PanelErrorLogPath
// return <LogsDir>\<filename>. All composed getters
// (ConfigPath, LogsDir, *LogPath) return "" if DataDir()
// returns "" — so callers can detect "no data dir
// available" with a single empty-string check.
func ServiceLogPath() string
func StderrLogPath() string
func PanelErrorLogPath() string

// EnsureDirs creates DataDir and LogsDir with os.MkdirAll (0o755).
// Idempotent. Returns an error if DataDir() is empty
// (e.g., %ProgramData% unset) or MkdirAll fails.
func EnsureDirs() error
```

`DataDir()` precedence: `SERIALHOP_DATA_DIR` env var → `%ProgramData%\SerialHop` → empty string. Production never sets `SERIALHOP_DATA_DIR`. Tests use `t.Setenv("SERIALHOP_DATA_DIR", t.TempDir())`.

## 4. Caller refactor

### 4.1 `internal/logship/logship.go`

- Delete `LogFileName` and `StderrLogFileName` constants (moved to `paths`).
- `Init(dir, version string, level slog.Level)` → `Init(version string, level slog.Level)`. The `dir` parameter is gone. `lumberjack.Logger.Filename` is set from `paths.ServiceLogPath()` / `paths.StderrLogPath()`.
- `Manager.dir` field deleted.

### 4.2 `internal/winsvc/worker.go`

- Delete the `configFileName` constant.
- `RunWorker`:
  - Drop `dir := filepath.Dir(exePath)`.
  - Call `paths.EnsureDirs()`. On error, return the wrapped error (service exits 1; SCM marks the service failed; the error propagates to the Windows Event Log via the normal exit-code path).
  - Call `logship.Init(version.Version, slog.LevelInfo)` (no `dir`).
- `handler` struct: drop the `dir` field.
- `Execute`: build `cfgPath` from `paths.ConfigPath()` instead of `filepath.Join(h.dir, configFileName)`.

### 4.3 `internal/panel/panel.go`

- Delete `configFileName` and `logFileName` constants.
- Rename the local `dir` variable in `Run` to `installDir` for clarity — it stays in scope, used only by update-staging code paths.
- Near the top of `Run`, call `paths.EnsureDirs()`. On error, **do not** return the error from `Run` — keep the panel open so the operator can see what went wrong. Stash the error in a local; the `refresh` closure surfaces it through the warn label, and `ComputeButtons` (or panel code directly) disables both file-action buttons. `writePanelStartupError` is reserved for window-creation failures (see §4.5); a missing data directory is recoverable in-UI.
- `cfgPath := paths.ConfigPath()`. Drop the `logPath` local.
- `refresh`: drop the `os.Stat(logPath)` check and the `logExists` branch.
- Button row 2 → exactly two buttons:
  - **Open config file** → `OpenWithDefaultApp(paths.ConfigPath())`.
  - **Open logs folder** → `OpenWithDefaultApp(paths.LogsDir())` (Windows ShellExecute on a directory opens Explorer at that path — no new helper needed).
  - Delete the old "Open log file" button entirely.
- `writePanelDebugLog(installDir, code, err)` → `writePanelDebugLog(code, err)`. Body uses `paths.PanelErrorLogPath()`. All call sites in this file drop the `installDir` argument.
- Update-staging paths (`cleanupStaleStagedFiles`, `SerialHop.exe.old` cleanup on launch, `ctlDownload`, `ctlInstall`) continue to use `installDir`. Unchanged.

### 4.4 `internal/panel/state.go`

- `ComputeButtons(state, cfgOK, logExists)` → `ComputeButtons(state, cfgOK)`. The `logExists` parameter and the `OpenLog` field on the returned struct are removed.
- `state_test.go`: drop the `logExists` parameter from cases and the `OpenLog` assertions. No new cases.

### 4.5 `cmd/serialhop/main.go`

- Delete the `configFileName` constant.
- `runForeground`:
  - Call `paths.EnsureDirs()` before scaffold creation. Return the error on failure.
  - `cfgPath := paths.ConfigPath()`. The scaffold-on-first-run logic is unchanged otherwise.
  - **Behavior change:** foreground developer mode now reads and writes the same config file as the service. On a dev machine with the service installed, the two modes share configuration instead of using isolated copies. This is the right default (one source of truth on dev machines too); flagged here so reviewers don't miss it.
- `writePanelStartupError`: pick the destination via a pure helper `panelErrorPath(dataDir, exeDir string) string` — returns `paths.PanelErrorLogPath()` when `paths.DataDir() != ""`, else `<exeDir>\SerialHop_panel_error.log`. This is the only remaining log-file write to the install directory, and only as a last-resort breadcrumb when the new layout is unreachable.

## 5. UI before vs. after

```
BEFORE                                AFTER
─── Status ──────                     ─── Status ──────
[●] Running                           [●] Running
─── Configuration ──                  ─── Configuration ──
  (six labels)                          (six labels)
  ⚠ warn label                          ⚠ warn label (now also reports EnsureDirs errors)
  Update row (collapsible)              Update row (collapsible)
[Install] [Uninstall] [Restart]       [Install] [Uninstall] [Restart]
[Open config file] [Open log file]    [Open config file] [Open logs folder]
status bar                            status bar
```

Window size unchanged. Only the second button row changes.

**Button gating.** After this change, the "Open config file" and "Open logs folder" buttons are enabled whenever `paths.EnsureDirs()` succeeded at panel startup. Empty `logs\` directory is still a valid Explorer target. If `EnsureDirs` failed, both buttons are disabled and the warn label shows the directory-creation error.

The Install / Uninstall / Restart buttons retain today's gating (SCM state plus `cfgOK`).

## 6. Edge cases & permissions

- **`%ProgramData%` not set.** Defensive branch — on supported Windows versions it's always set. If missing: `paths.DataDir()` returns the empty string, `EnsureDirs()` returns an error, the worker exits 1 with an error in its slog (visible via Event Log), and the panel disables the file-action buttons and shows the error in the warn label. `writePanelStartupError` falls back to `<exeDir>\SerialHop_panel_error.log`.

- **Service starts before panel ever ran.** Possible if someone provisions the service via PowerShell / `sc.exe` without using the panel. The worker calls `paths.EnsureDirs()` (succeeds, creates the directory) then `config.Load(paths.ConfigPath())` (fails with `ENOENT`); the service exits 1. Operator opens the panel, sees "config missing" in the warn label, the panel writes the scaffold, operator clicks Restart. Same failure mode as today; only the file path differs.

- **File ownership and permissions.** Panel (user account) creates the directory and the config file on first run via `paths.EnsureDirs()` + `ensureScaffold`, so the config file is owned by that user with full write access — keeping the "Open config file" → edit → save flow working without bespoke ACLs. Service (LocalSystem) creates the log files on first service start; they're LocalSystem-owned. User-account Explorer can read them via "Open logs folder" → double-click → Notepad opens read-only. That's the intended access pattern. The default `%ProgramData%` ACL on Win10/11 grants `SYSTEM:Full`, `Administrators:Full`, `Users:Read`, and `Authenticated Users:Modify` on objects they create — sufficient for the single-operator lab-machine scenario this client targets. **Limitation:** if a config file created by operator A is later edited by a different Windows account (operator B), B will get a read-only Notepad and cannot save changes. Treated as out of scope (lab machines are single-operator in practice); fix path if it ever bites: set an explicit `Users:Modify` ACL on the data directory at `EnsureDirs` time.

- **Foreground developer mode shares config with the service.** Called out in §4.5. On a dev machine where both modes are used, both read/write the same `%ProgramData%\SerialHop\SerialHop_config.yaml`. Intended.

- **Multiple `SerialHop.exe` installs on one machine.** Out of scope. Two install directories would share `%ProgramData%\SerialHop\` config and logs. Realistic scenarios (lab machines): one install per machine, so not a concern.

- **Lumberjack rotation under LocalSystem.** `os.Rename` on a LocalSystem-owned file by the same LocalSystem process works. Concurrent open by Explorer/Notepad uses share-read mode and does not block the rotation rename. No change from today.

## 7. Testing

- **`internal/paths`** (new tests):
  - `DataDir()` returns the `SERIALHOP_DATA_DIR` value when set.
  - `DataDir()` falls back to `<ProgramData>\SerialHop` when only `ProgramData` is set.
  - `DataDir()` returns `""` when neither is set.
  - `EnsureDirs()` creates both directories; second call is a no-op (idempotent).
  - `EnsureDirs()` returns an error when `DataDir()` is empty.
  - All composed path getters return the expected join under `t.TempDir()`.

- **`internal/logship`**: existing tests in `capture_test.go` / `logship_test.go` already use `t.TempDir()`. They get touched to drop the `dir` argument from `logship.Init` and instead call `t.Setenv("SERIALHOP_DATA_DIR", t.TempDir())`. Coverage stays at parity.

- **`internal/panel/state.go`**: `state_test.go` cases drop the `logExists` parameter and the `OpenLog` assertion. Pure pruning.

- **`writePanelStartupError` fallback.** Extract the path-selection logic into a pure helper, e.g. `panelErrorPath(dataDir, exeDir string) string` that returns `<dataDir>\logs\SerialHop_panel_error.log` when `dataDir != ""`, else `<exeDir>\SerialHop_panel_error.log`. Trivial unit test covers both branches. No need to inject a filesystem.

- **No new Windows-only tests.** The path package is stdlib-only and runs cross-platform. The full suite (`task test`) continues to pass on both macOS and Windows runners.

## 8. Documentation updates

- **`README.md`** — "Install on a Windows lab machine" section:
  - The bullet that says `Logs go to SerialHop.log (slog JSON) and SerialHop_stderr.log (chisel state, panic traces) next to the .exe — both rotated at 10 MB with 3 backups. Click **Open log file** to view the main log.` becomes a sentence pointing at `%ProgramData%\SerialHop\logs\` and the "Open logs folder" button.
  - The "Open config file" mention is unchanged in spirit; the file location is now `%ProgramData%\SerialHop\SerialHop_config.yaml` and worth one sentence on the layout.

- **`docs/superpowers/specs/2026-05-11-auto-update-design.md`** — references on lines 106, 118, 125, 258 mention `SerialHop_panel_error.log` next to the .exe. Update them in place to reference `%ProgramData%\SerialHop\logs\SerialHop_panel_error.log`.

- **`CLAUDE.md`** — no change needed (it describes release/build flow, not runtime file layout).
