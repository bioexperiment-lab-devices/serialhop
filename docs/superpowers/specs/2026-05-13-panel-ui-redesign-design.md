# Panel UI Redesign — Design

**Date:** 2026-05-13
**Status:** Approved (brainstorming complete; pending spec review before plan)

> Supersedes the earlier same-day draft that proposed staying on `lxn/walk`.
> The contents-level decisions (what controls exist on each tab, how
> verify-then-save works, the (?) help convention, the `ActualRestPort`
> service-side change) are unchanged; the framework is now Wails v2 + React.

## 1. Purpose & scope

Replace the current single-pane panel with a tabbed UI that adds first-class
editing of configuration, live in-UI log viewing, and live device/port views —
turning the panel from a launcher-with-status into a self-contained operations
console for non-technical lab operators.

Motivation: today the panel forces operators to open a YAML file in Notepad to
change anything, open Explorer to read logs, and trust the lab-bridge UI to see
what devices are connected. None of those are appropriate for the non-technical
audience the client targets. This redesign moves the common operations into the
panel itself while keeping the existing "Open in editor" / "Open logs folder"
shortcuts as fallbacks for advanced cases.

In scope:

- Five-tab layout (Status / Config / Devices / Ports / Logs).
- Structured config form with inline validation backed by `config.Validate()`.
- Live log tail of on-disk log files in `%ProgramData%\SerialHop\logs\`.
- Live devices + ports view backed by the existing service REST API.
- First-launch flow folded into Config tab; first-run modal removed.
- `(?)` inline help affordance across the panel.
- Service-side: publish the actual bound REST port via the existing bootstrap
  cache so the panel can call into the local service when `rest.port: 0`.
- **Framework swap from `lxn/walk` to Wails v2 + React + TypeScript.** The
  panel becomes a Wails app that embeds Edge WebView2 (already present on
  Windows 10 21H1+ via Microsoft Edge, default on Windows 11). The visual
  vocabulary is the React mockup set in `docs/serialhop-ui/project/` —
  treated as a *style reference and component library*, not a strict
  pixel-perfect contract.

Out of scope:

- New service REST endpoints. The panel uses only what's already exposed in
  `internal/api`.
- Cross-platform support. The panel remains Windows-only. The headless-Pi
  motivation was discussed and deferred — Wails was chosen for its desktop-app
  feel on Windows, not for portability.
- Visual / layout *novelty*. The mockups define palette, typography, the
  title-bar-style chrome, the help-popover treatment, and the footer with
  kind+text+time+progress. The implementer matches that visual vocabulary;
  per-state layouts the mockups don't draw are filled in to spec.
- Remote access. The Wails app renders in an embedded WebView2; no HTTP port
  is exposed by the panel.
- Per-device disconnect endpoint. Bulk `POST /devices/disconnect` continues to
  be the only disconnect path.
- Power-user device actions (send command, flash) on the Devices tab. Those
  remain lab-bridge-only.
- Embedded YAML text editor. The structured form covers known fields;
  "Open in editor" covers everything else.
- Config schema changes. `internal/config.Config` is unchanged.
- Binary split. `SerialHop.exe` stays a single artifact with four runtime
  modes (service / admin-elevated helper / foreground-dev / panel).

## 2. Runtime architecture

`SerialHop.exe` keeps the same four runtime modes selected at startup
(`cmd/serialhop/main.go`):

| Mode | Selector | What changes |
|---|---|---|
| service | `svc.IsWindowsService()` true | unchanged — `internal/app.Run` |
| admin helper | `--admin-action=…` | unchanged — `internal/winsvc.RunAdminAction` |
| foreground-dev | `--foreground` | unchanged — `internal/app.Run` |
| panel | default | **rewritten** — Wails v2 app replacing `lxn/walk` |

The service-mode process and the panel-mode process remain separate.
Install / Uninstall / Restart / Update-Install still go through the existing
UAC-elevated subprocess (`internal/panel/elevate.go` →
`cmd/serialhop --admin-action=…`).

The only new wire between panel and service is the panel's HTTP client to
`http://127.0.0.1:<ActualRestPort>` for the Devices and Ports tabs.

## 3. Tab structure & global elements

Tabs in order: **Status**, **Config**, **Devices**, **Ports**, **Logs**.

Tabs are always visible and enabled. Empty / error states are handled inside
each tab via banners and disabled buttons — no "ghost tabs", no disabled tab
headers.

Global UI elements visible from every tab (per `docs/serialhop-ui/project/`):

- **Title bar** — the real OS window chrome shows `SerialHop v<X.Y.Z>`; the
  Wails app uses native window controls (the `— ▢ ✕` icons drawn in the
  mockup's `TitleBar` component are illustrative — we don't reimplement
  window-frame controls inside the WebView).
- **Tab bar** — five tabs as above. The Config tab shows a small dot indicator
  while there are unsaved edits.
- **Warn header** — shows `⚠ <error>` when `paths.EnsureDirs()` failed at
  startup, the config file is missing/malformed, or any panel-wide invariant
  fails. Hidden when clean. Promoted from today's Status-tab-local warn label
  to a global header so a config-invalid state is visible from every tab.
  Driven by the `warn:set` / `warn:clear` Wails events (§11).
- **Footer status line** — shows the most recent action outcome with timestamp
  and an optional progress bar (auto-update download). Persists across tab
  switches. Driven by `footer:set` events (§11). Examples: `Saved.`,
  `Service installed at 15:04:23`, `Failed: <msg>`.

First-launch behavior:

- If `lab_bridge.user` or `lab_bridge.pass` is empty in the loaded config, the
  panel opens on the **Config** tab. Otherwise it opens on **Status**.
- All other config fields are prefilled with `config.Default()` values on the
  form so the operator only fills in the credentials.
- A banner at the top of the Config tab reads *"Enter your lab-bridge
  credentials to enable the service."* It is visible whenever `user` or
  `pass` is empty; auto-hides once both are non-empty and saved.

## 4. Status tab

Service-health and service-control. No configuration data, no logs, no devices.

### 4.1 Lamps

Three lamps, each with a `(?)` help icon next to the row:

- **Service lamp** — local SerialHop Windows service state (not-installed /
  stopped / starting / running / stopping). State text + color from
  `serviceLampPresentation()` (unchanged).
- **Server lamp** — reachability + health of the configured lab-bridge server.
  State text + color from `serverLampPresentation()` (unchanged).
- **Tunnel lamp** — state of this machine's Chisel reverse tunnel into the
  lab-bridge. State text + color from `tunnelLampPresentation()` (unchanged).

`(?)` popover content template per lamp: what it checks → what each color
means → which color is actionable. Wording finalized at implementation time.

### 4.2 Service action buttons

- **Install** — UAC-elevated `install` action. Enabled when SCM state allows
  + config valid.
- **Uninstall** — UAC-elevated `uninstall`. Enabled when service is installed.
- **Restart** — UAC-elevated `restart`. Enabled when service is installed.

Behavior on click (unchanged from today, just moved from the bottom of the
panel into the Status tab): all three buttons disable; lamps grey to
`Checking…`; footer reads `Working…`. On completion: footer reports
outcome + timestamp; lamps re-probe; buttons re-enable per the new SCM state.

The Go-side `state.ComputeButtons(scmState, cfgValid)` function continues to
own the enablement matrix; its boolean output drives the `disabled` prop on
the TS side.

### 4.3 Update row

Auto-update state machine. Hidden when no update is in flight. Same logic as
today's `applyUpdateRow`; only the rendering changes:

- `UpdateAvailable` — label + **Download** / **Release notes** buttons.
- `UpdateDownloading` — label + **Cancel** button. Progress mirrors into the
  footer via `footer:set` with `progress`.
- `UpdateDownloadFailed` — label (red) + **Retry**.
- `UpdateReady` — label + **Install update** / **Release notes**.
- `UpdateInstalling` — label only.
- `UpdateInstalled` — label (green): *"Updated to <tag>. Close and reopen this
  window to load the new panel."*
- `UpdateInstallFailed` — label (red) + **Retry**.

Same update-check cadence as today: on-launch (500 ms delay) + every 6 h
when `auto_update.enabled` is true.

## 5. Config tab

Structured form. One widget per `config.Config` field, grouped by YAML section.
Every field has a `(?)` icon. Inline validation against `config.Validate()`.

Form state lives in React (`useState`). Go side only validates and persists —
it does not mirror the in-flight form.

### 5.1 Lab-bridge section

- **Host** — text field. Default `111.88.145.138` (prefilled). Required.
- **Username** — text field. Required. Save triggers verify-then-save (§5.9).
- **Password** — plaintext text field. No masking and no show/hide toggle —
  matches the existing convention that the password is stored as plain text in
  the YAML. Required. Save triggers verify-then-save.

### 5.2 REST section

- **Port** — numeric field, 0..65535. `0` = OS picks a free port.

### 5.3 Discovery section

- **Include list** — editable list of COM port names. Each row: text input
  + **Remove** button. **Add row** button below the list. Empty list means
  probe all enumerated ports.
- **Exclude list** — same widget kind. Mutually exclusive with Include — when
  one list is non-empty, the other is greyed out with an inline note
  *"Include and Exclude can't be used together"*.
- **Post-open settle (ms)** — numeric field, ≥ 0. Default 2000 (covers the
  Arduino bootloader reset window).

### 5.4 Log section

- **Level** — dropdown: `debug` / `info` / `warn` / `error`. Default `info`.

### 5.5 Raw serial section

- **Enabled** — checkbox.

### 5.6 Auto-update section

- **Enabled** — checkbox.

### 5.7 Firmware flashing section

Info block prefacing the field group (rendered as inline text at the top of
the subsection, not a popover):

> Firmware flashing is higher risk than raw serial — a bad `.hex` bricks the
> board (ISP recovery required). Leave disabled unless you're actively
> flashing devices.

Fields:

- **Enabled** — checkbox.
- **Backup directory** — text field + folder-picker button. Required absolute
  path when **Enabled** is on. Greyed out when **Enabled** is off. Empty value
  means the service falls back to `%ProgramData%\SerialHop\backups`. The
  folder-picker uses Wails' `runtime.OpenDirectoryDialog`.
- **Keep N backups** — numeric field, ≥ 0. `0` = keep all.

### 5.8 Actions row

- **Save** — calls `ValidateConfig(form)` first; on success calls
  `SaveConfig(form)` which writes YAML. Footer reads *"Saved. Restart the
  service to apply."* Inline error markers on failing fields. **Save** and
  **Save & restart** are disabled while any field is invalid client-side.
- **Save & restart** — same as **Save**, then triggers `RestartService()`.
  Footer reports restart progress through the same channel as the Status
  tab's **Restart** button.
- **Discard changes** — re-calls `LoadConfigFromDisk()` and replaces the form
  state. Enabled only when there are unsaved edits.
- **Open in editor** — fallback. Calls `OpenConfigInEditor()` which runs the
  existing `OpenWithDefaultApp(paths.ConfigPath())`.

### 5.9 Verify-then-save on credentials

When the user clicks **Save** or **Save & restart** AND either
`lab_bridge.user` or `lab_bridge.pass` differs from the value the form was
initialized with:

1. Validate non-empty client-side.
2. Call `VerifyCredentials(host, user, pass)` — Go side runs the existing
   `verifyCredentials()` path.
3. On `CredsOK` — proceed with the save.
4. On `CredsUnauthorized` — inline error next to the credential fields:
   *"Server rejected these credentials. Check the username and password."*
   Save is not written.
5. On `CredsNeedsConfirm` (network failure) — prompt *"Couldn't reach
   `<host>` to verify the credentials (`<detail>`). Save anyway?"* On Yes,
   proceed; on No, cancel.

Other fields save without any network check. The verify call only fires when
at least one of the two credential fields actually changed.

The Go-side `credverify.go` helper wraps this five-outcome state machine for
unit-test isolation; the binding `VerifyCredentials` is its thin entrypoint.

### 5.10 Unsaved-changes guard

- Tab bar Config indicator shown while there are pending edits (any form
  field differs from the last-loaded YAML).
- Switching tabs or closing the window with unsaved edits → modal *"Discard
  unsaved configuration changes?"* with **Save** / **Discard** / **Cancel**.
- The Status tab's **Restart** button is **not** blocked by unsaved Config
  edits — it operates on the on-disk YAML. But after a Restart with unsaved
  edits pending, the footer appends a hint: *"Note: unsaved config changes
  were not applied."*

## 6. Devices tab

Logical-device view. Hardware metadata (VID/PID/serial/product) lives on the
Ports tab, not here.

### 6.1 Banner row

- Last-discovery timestamp text: `Discovered at HH:MM:SS` or `Never run`.
  Sourced from `DevicesResponse.DiscoveredAt`.

### 6.2 Action buttons

- **Rediscover** — calls `Discover()` (Go binding → `POST /discover`).
  Buttons disable, table greys; on response the table refreshes.
- **Disconnect all** — calls `DisconnectAll()` (→ `POST /devices/disconnect`).
  Footer reports the `released` count. Table contents unchanged — devices
  stay registered; only connections close.
- **Refresh** — calls `GetDevices()` (→ `GET /devices`). No re-probe.

### 6.3 Devices table

Columns:

- **ID** (e.g. `pump_1`) — the logical identifier the lab-bridge addresses.
- **Type** (e.g. `pump`) — the device's logical type.
- **Port** (e.g. `COM5`) — the COM port the device is on.

Sort by ID by default. No per-row actions.

### 6.4 Empty / error states

- **Service stopped or not installed:** empty table + banner *"Service is not
  running. Start it from the Status tab."* All buttons disabled.
- **Service running, discovery never run:** empty table + banner *"No devices
  yet. Click Rediscover to probe serial ports."* Rediscover + Refresh enabled;
  Disconnect all disabled.
- **Service running but panel can't reach it** (bootstrap cache stale, port
  mismatch, service still starting): empty table + banner *"Can't reach the
  local service. It may have just started — wait a few seconds and click
  Refresh."* Refresh enabled, others disabled.
- **Discovery in progress:** all buttons disabled, table greyed until the
  response arrives.

The "can-the-panel-reach-the-service" decision lives in the Go-side
`servicecli.go` — it reads `ActualRestPort` from the bootstrap cache on every
call (per §10) and reports the empty/error state via the binding return.

## 7. Ports tab

OS-level serial-port enumeration. Mirrors `GET /serial/ports/detailed`.

### 7.1 Action buttons

- **Refresh** — `GetPorts()` (→ `GET /serial/ports/detailed`). No re-probe.
- **Rediscover** — `Discover()` (→ `POST /discover`). Same call as on the
  Devices tab. Lives here too because looking at the Ports tab is the natural
  prelude to "now retry discovery on these ports".

### 7.2 Ports table

Columns (all 8 fields from `DetailedPortDTO`):

- **Name** (e.g. `COM5`).
- **Is USB** — boolean, displayed as a checkmark/blank.
- **VID** — USB vendor ID (hex). `(?)` popover.
- **PID** — USB product ID (hex). `(?)` popover.
- **Serial number** — USB serial string if reported by the device. `(?)`.
- **Product** — USB product descriptor string. `(?)` popover.
- **Discovered** — boolean. True if discovery matched a SerialHop device on
  this port. `(?)` popover.
- **Device ID** — the logical device ID this port was bound to. Empty if
  `Discovered = false`. `(?)` popover.

Sort by Name (COM port number) by default.

### 7.3 Empty / error states

Same policy as Devices tab:

- Service stopped → empty + *"Service is not running…"* Buttons disabled.
- Service running, zero ports enumerated → empty + *"No serial ports detected
  on this machine."* Buttons enabled.
- Service running but panel can't reach it → empty + *"Can't reach the local
  service…"* Refresh enabled, others disabled.

## 8. Logs tab

Live tail of the on-disk log files. No service-API dependency.

### 8.1 Top controls

- **Stream dropdown** — three entries, each with its own `(?)` popover:
  - **Service log** → `paths.ServiceLogPath()` (slog JSON,
    lumberjack-rotated). Rendered as parsed columns: Time / Level / Message.
  - **Stderr** → `paths.StderrLogPath()` (raw text, lumberjack-rotated).
    Rendered as raw lines.
  - **Panel errors** → `paths.PanelErrorLogPath()` (append-only, no rotation).
    Rendered as raw lines.
- **Level filter dropdown** — `all` / `debug` / `info` / `warn` / `error`.
  Hides records below the chosen severity. Greyed out when the selected
  stream is **Stderr** or **Panel errors** (those have no level metadata).
- **Follow toggle** — when on, auto-scroll to end on every new line. When
  off, the view stays put as new lines append.
- **Search** — free-text input. Filters visible lines to those containing the
  substring; highlights matches in shown rows. Re-applied as new lines arrive.

### 8.2 Log view

- **Service log:** three-column table (Time / Level / Message). One row per
  slog record. Selecting a row exposes its full structured fields (all
  key/value pairs from the JSON record) in a small panel below the table.
- **Stderr / Panel errors:** single-column scrollable text view.

The tailing pipeline:

- Operator selects a stream → TS calls `StartLogStream(id)`.
- Go-side `filetail.go` opens the file, seeks to end, starts a 500 ms
  poll-and-emit loop.
- Each new line (or slog record) is emitted as `log:line` to TS.
- On lumberjack rotation (inode/size reset detected), Go emits
  `log:rotated` then reopens the file.
- Selecting a different stream → TS calls `StartLogStream(id2)` which
  internally calls `StopLogStream()` first.
- The buffer of last-N lines lives TS-side (last 5,000 default, oldest
  dropped); the Go side only forwards bytes, it doesn't retain history.

### 8.3 Bottom actions

- **Open logs folder** — fallback. Calls `OpenLogsFolder()` which runs the
  existing `OpenWithDefaultApp(paths.LogsDir())`.

### 8.4 Empty / error states

- **Selected stream's file does not exist** (clean install, service has never
  started for **Service log**/**Stderr**; panel has never written an error
  for **Panel errors**): empty view + banner *"No logs yet. Start the service
  from the Status tab to begin logging."*
- **File permission error**: empty view + banner with the OS error message.
  **Open logs folder** still works.

## 9. Cross-cutting: (?) help icon convention

A small `(?)` icon appears next to labels and column headers where an
operator may not immediately know what something is or does. Click opens a
small popover (the `Help` component from `panel-shell.jsx`).

Locations:

- **Status tab:** each of the three lamp rows.
- **Config tab:** every field label across §5.1–§5.7. (The Firmware flashing
  info block at §5.7 is inline text, not a popover.)
- **Ports tab:** column headers for VID, PID, Serial number, Product,
  Discovered, Device ID.
- **Logs tab:** each entry in the **Stream** dropdown.

Not applied to: Service-action buttons, Devices table columns, Logs tab
Follow / Search / Level controls.

Content template per popover:

1. **What it is** (one sentence).
2. **Default or typical value** (where relevant).
3. **When to change it / what it affects.**

Concrete wording for every popover is finalized at implementation time.

## 10. Service-side change: ActualRestPort in bootstrap cache

The panel calls the running service over `http://127.0.0.1:<port>` to drive
the Devices and Ports tabs. When `rest.port: 0` (OS-assigned), the configured
value tells the panel nothing about where the service is actually listening.

Change:

- Extend `bootstrap.Cache` (the struct serialized to
  `paths.ServerInfoCachePath()`) with an `ActualRestPort int` field.
- The service writes its bound REST port into the cache once `api.Listen()`
  returns the actual port.
- The panel reads `ActualRestPort` from the cache before each Devices /
  Ports tab HTTP call. Reading per-call rather than caching in-memory keeps
  the panel robust against service-restart-while-panel-open scenarios.

Read failure semantics:

- Cache missing, unparseable, or `ActualRestPort == 0`: panel treats this as
  "service unreachable" and shows the corresponding empty-state banner on
  the Devices / Ports tabs.
- Cache present but service is down (HTTP call fails): panel shows the
  "service is not running" banner.

This change is invisible to operators. No new endpoint and no new file.

Alternative considered but not chosen: require `rest.port` to be non-zero
(forbid `0` in `Validate()`). Rejected because the YAML scaffold documents
`0` as the recommended default.

## 11. Go ↔ TypeScript contract

The only structurally new code surface. Two halves: **bindings** (TS → Go,
request/response) and **events** (Go → TS, fire-and-forget). All bindings
live as methods on a single Wails `App` struct in `internal/panel/bindings.go`;
Wails auto-generates the matching TS shims at build time into
`internal/panel/frontend/src/wails/`.

### 11.1 Bindings

```
// Global
GetVersion()                       → string
LoadConfigFromDisk()               → config.Config
ValidateConfig(cfg)                → []FieldError
SaveConfig(cfg)                    → SaveResult
VerifyCredentials(host,user,pass)  → CredsResult
OpenConfigInEditor()               → error
OpenLogsFolder()                   → error
OpenReleaseNotes()                 → error                 // uses the
                                                            // currently-tracked release URL
PickBackupDir()                    → string                // OpenDirectoryDialog

// Status tab
InstallService()                   → AdminResult
UninstallService()                 → AdminResult
RestartService()                   → AdminResult
TriggerProbe(which)                → void                  // "server" | "tunnel"
CheckForUpdate()                   → void
DownloadUpdate()                   → void
CancelDownload()                   → void
InstallUpdate()                    → AdminResult

// Devices tab
GetDevices()                       → DevicesDTO
Discover()                         → DevicesDTO
DisconnectAll()                    → DisconnectDTO

// Ports tab
GetPorts()                         → DetailedPortsDTO

// Logs tab
StartLogStream(id)                 → void                  // "service" | "stderr" | "panel"
StopLogStream()                    → void
```

DTOs reused directly from existing packages (Wails introspects them into TS
interfaces; we don't define parallel panel DTOs):

- `config.Config`, all sub-structs.
- `api.DeviceDTO`, `api.DetailedPortDTO`, `api.DevicesResponse`,
  `api.DetailedPortsResponse`, `api.DisconnectResponse`.

New types declared in `internal/panel/bindings.go`:

- `FieldError { Field string; Detail string }` — flat list returned by
  `ValidateConfig`.
- `SaveResult { OK bool; FieldErrors []FieldError }`.
- `CredsResult { Outcome string; Detail string }` — outcome ∈ {`ok`,
  `unauthorized`, `needs_confirm`}.
- `AdminResult { OK bool; ErrorMessage string; Cancelled bool }` — wraps the
  existing `RunElevatedAdminAction` return.

### 11.2 Events

```
"warn:set"          { message }
"warn:clear"
"status:lamp"       { which, tone, label, sub? }
"update:state"      { state, releaseTag, progressPct? }
"config:saved"
"config:save-failed"{ detail }
"log:line"          { stream, raw?, record? }
"log:rotated"       { stream }
"footer:set"        { kind, text, time?, progress? }
```

`status:lamp` is emitted only on tone-or-label change to avoid 1 Hz event
spam from the SCM-poll goroutine.

### 11.3 What runs where

- **Probe loops** (server + tunnel, every 30 s, action-triggered re-probe) —
  Go side. The existing `probe.go` is reused; the sink flips from
  `mw.Synchronize(repaintLamps)` to
  `runtime.EventsEmit(ctx, "status:lamp", …)`.
- **SCM polling** (every 1 s) — Go side. Emits `status:lamp` for the
  service lamp only on change.
- **Update state machine** — Go side. The existing `update_state.go` keeps
  its transitions; emits `update:state` on each transition; download
  progress double-emits to `footer:set`.
- **Log tailing** — Go side `filetail.go`, started/stopped by
  `StartLogStream` / `StopLogStream`. One tailer at a time (whichever stream
  the operator selected); switching streams cancels the previous tailer.
- **Form state, dirty flag, unsaved-changes guard** — TS side. React
  `useState` is the natural store; Go does not mirror.
- **Devices / Ports table state** — TS side. Refresh is operator-triggered;
  Go does no caching.

## 12. Code reuse from existing `internal/panel`

| File | Disposition |
|---|---|
| `lampstate.go` + `lampstate_test.go` | **kept** unchanged |
| `state.go` + `state_test.go` | **kept** unchanged |
| `probe.go` + `probe_test.go` | **kept**; the goroutines now emit Wails events instead of calling `mw.Synchronize` |
| `update_state.go` + `update_state_test.go` | **kept** unchanged |
| `firstrun.go` + `firstrun_test.go` | **kept** as logic-only; the dialog call site goes |
| `elevate.go` + `elevate_other.go` | **kept** unchanged |
| `debug_log.go` | **kept** unchanged |
| `panel.go` (24 KB, walk-based) | **removed**; the package's external entry point (`func panel.Run() error`, called from `cmd/serialhop/main.go`) survives but is reimplemented in `wails_app.go` |
| `panel_other.go` | **removed** — Wails app is also Windows-only, but the helpers it relies on are all OS-independent so non-Windows CI builds still pass |
| `credsdialog_windows.go` + `credsdialog_other.go` | **removed** — first-run modal is gone |
| `timer_windows.go` | **removed** — walk-specific |

New files in `internal/panel/`:

- `wails_app.go` (`//go:build windows`) — Wails `App` struct,
  `startup(ctx)` / `shutdown(ctx)` lifecycle, owns the probe / SCM / update
  goroutines. Plus a corresponding `wails_app_other.go` (`//go:build
  !windows`) that stubs `Run()` so the package builds on non-Windows CI.
- `bindings.go` (`//go:build windows`) — bound methods (the API in §11.1).
- `events.go` (`//go:build windows`) — typed event payloads + a thin
  emitter wrapper. Build-tagged because the Wails runtime import only
  resolves cleanly when paired with the Windows-only WebView2 host code.
- `servicecli.go` — typed HTTP client to `http://127.0.0.1:<ActualRestPort>`
  for Devices / Ports. Reads the bootstrap cache per call; returns a
  three-way status (ok / unreachable / service-down) so bindings can map to
  the empty-state banners. Tested with `httptest`.
- `filetail.go` — bounded-ring file tail reader with rotation detection
  (inode/size reset on Windows is detected via `os.SameFile` plus a size
  drop heuristic; the lumberjack rotation case is the only one we need to
  handle, and that case writes a new file at the same path). Emits via a
  `func(line)` callback so it's unit-testable without Wails.
- `credverify.go` — verify-then-save state machine wrapping the existing
  `verifyCredentials` plus the change-detection from §5.9. Returns a
  `CredsResult`.

## 13. Frontend layout & build

The React app lives under `internal/panel/frontend/` (the Wails default).

```
internal/panel/frontend/
├── package.json                         — pinned versions
├── tsconfig.json
├── vite.config.ts
├── index.html
├── src/
│   ├── main.tsx                         — Wails runtime init + React root
│   ├── App.tsx                          — tab router + global state
│   ├── components/                      — Lamp, Field, Section, Help, Modal,
│   │                                      Footer, TitleBar, TabBar, Warning,
│   │                                      Button, Checkbox  (from mockup)
│   ├── tabs/
│   │   ├── StatusTab.tsx
│   │   ├── ConfigTab.tsx
│   │   ├── DevicesTab.tsx
│   │   ├── PortsTab.tsx
│   │   └── LogsTab.tsx
│   ├── wails/                           — auto-generated (gitignored)
│   └── styles/                          — adapted from
│                                          docs/serialhop-ui/project/styles.css
└── dist/                                — Vite output (gitignored;
                                           embedded into binary via go:embed)
```

Stack: React 18 + TypeScript + Vite. Plain CSS (no Tailwind / no CSS-in-JS) —
matches the mockup's CSS approach and keeps the bundle small. No state-
management library beyond `useState` / `useContext` — the data surface is
small and per-tab. No routing library — `useState`-driven tab switching is
sufficient.

The mockups in `docs/serialhop-ui/project/` (`panel-shell.jsx`,
`panel-status.jsx`, `panel-config.jsx`, `panel-tables.jsx`, `styles.css`)
are the visual reference. Components are re-implemented as TSX inside the
project; the prototype JSX is not imported directly.

## 14. Build pipeline & release

Today's `task build` runs a Go-only build via `tools/buildcmd`. With Wails it
becomes a two-step build:

1. `wails build` (or `npm run build` for frontend dev iteration).
   - Invokes Vite (`npm run build`) to produce `internal/panel/frontend/dist/`.
   - `go build`-equivalent invocation produces `dist/SerialHop.exe` with the
     frontend embedded via `go:embed`.
2. `cmd/serialhop/resource_windows.syso` continues to provide the manifest /
   icon / version resource. The render-manifest + assets/manifest.template.xml
   flow is unchanged.

CI changes (`.github/workflows/pr.yml`, `release-build.yml`):

- Add `actions/setup-node@v4` (pinned to whatever Node major matches Wails v2
  requirements — to be picked in the plan).
- Install `wails` CLI via `go install github.com/wailsapp/wails/v2/cmd/wails@<pinned>`.
- The `verify` job runs:
  - `gofmt`, `go vet`, `golangci-lint`, `go test -race -count=1`, `govulncheck`
    — unchanged.
  - `npm ci` + `npm run build` inside `internal/panel/frontend/` to fail-fast
    on TS / lint errors.
  - Frontend unit tests (Vitest) under `internal/panel/frontend/`.
- The `release-build` job runs `wails build -ldflags=…` instead of
  `go build`. Artifact name and SHA256SUMS flow are unchanged.

WebView2 runtime requirement: Windows 10 21H1+ ships Edge with the WebView2
runtime; Windows 11 always has it. For the rare older-Win10 case the plan
documents the manual install path (Microsoft Evergreen Bootstrapper). No
auto-install logic in v1.

## 15. Removed from today's UI

- **First-run credentials modal** (`internal/panel/credsdialog_windows.go`
  and its `runCredsDialog` call site) — replaced by the Config-tab
  first-launch banner + verify-then-save behavior on the credential fields.
- **Configuration read-only label block** on the Status tab (today's six
  labels: Chisel server, Remote port, REST port, Discovery, Raw serial,
  Log level) — replaced by the editable Config tab. The cached display
  logic in `readCacheDisplay` is no longer called from the panel.
- **"Open config file"** button on the Status tab — moved to the Config tab
  as **Open in editor**.
- **"Open logs folder"** button on the Status tab — moved to the Logs tab.
- **`lxn/walk` dependency** — removed from `go.mod`. All walk-specific code
  in `internal/panel/panel.go` is replaced.

## 16. Unchanged from today

- Lamp state semantics and colors (`internal/panel/lampstate.go` and the
  underlying probe functions in `probe.go`).
- Probe cadences: server + tunnel probes every 30 s plus action-triggered
  re-probe; SCM read-only polling at 1 s.
- Auto-update check cadence: on-launch (500 ms delay) + every 6 h when
  `auto_update.enabled` is true.
- UAC-elevated subprocess for Install / Uninstall / Restart / Update install.
- Log rotation policy (10 MB × 3 backups via lumberjack) and the Loki shipper.
- Service-side REST API surface. No new endpoints required.
- On-disk layout — `%ProgramData%\SerialHop\` for config + logs, install dir
  holds only `SerialHop.exe` plus transient update-staging files.
- Config schema — `internal/config.Config` shape unchanged; no new YAML
  fields.
- Auto-update + release-please pipeline. Wails build still produces a single
  `SerialHop-vX.Y.Z.exe` artifact + `SHA256SUMS.txt`; the auto-updater swaps
  the binary the same way as today.
- Single binary, four runtime modes. SCM still launches `SerialHop.exe`
  as a service; the WebView2 runtime is not loaded in that mode.

## 17. Testing

- **`internal/config`:** existing tests cover `Validate()` and the YAML
  round-trip. No new tests needed in this package — the panel's form-level
  validation delegates to `Validate()`.
- **`internal/bootstrap`:** add a test that the new `ActualRestPort` field
  round-trips through write/read.
- **Service worker:** add a test that the bound REST port is written into
  the cache after `api.Listen()` returns.
- **`internal/panel` (Go side):**
  - Preserved tests (`lampstate_test.go`, `state_test.go`, `probe_test.go`,
    `update_state_test.go`, `firstrun_test.go`) — kept as-is.
  - New focused tests for the new helpers:
    - `filetail.go` — start-from-end on attach; lumberjack rotation reopen
      on inode/size reset; bounded ring buffer; emit callback ordering.
    - `credverify.go` — five-outcome decision tree exercised with a fake
      `verifyCredentials`.
    - `servicecli.go` — `httptest.Server` doubles for the three-way
      status (ok / unreachable / service-down).
- **Frontend (`internal/panel/frontend/`):**
  - Vitest + React Testing Library.
  - Form-state behavior: dirty flag tracking, unsaved-changes guard prompt,
    verify-then-save inline error mapping, save-button enablement under
    invalid fields.
  - Lamp / banner / footer rendering on representative event payloads.
  - No tests for tab navigation / styling — covered by manual smoke.
- **No Wails-level end-to-end tests** in v1. Manual smoke covers the
  integration boundary (consistent with today — walk widgets are
  constructed in `panel.go` but the panel's main flow is exercised by
  manual smoke).

The redesign keeps every Windows-only file behind `//go:build windows` and
provides `_other.go` fakes where required for non-Windows builds (consistent
with the existing convention). Pure-Go helpers (lampstate, state,
update_state, filetail, credverify, servicecli) build and test on all
platforms.

## 18. Implementation-level details deferred to the plan

These do not affect the contents spec but are flagged so the implementation
plan picks values explicitly:

- Pinned Wails v2 version + Node major version + React version.
- Window default size and minimum size for the Wails main window.
- File-tail polling cadence (proposed: 500 ms). Could be replaced with
  `ReadDirectoryChangesW` via a Wails-side wrapper if 500 ms feels laggy in
  manual testing.
- Per-stream tail buffer size in TS (proposed: last 5,000 lines).
- Initial sort orders, column widths, and tab-key order across the Config
  tab fields.
- Whether to suppress `status:lamp` events when the panel window is
  minimized — probably not worth the complexity in v1.
- ESLint / Prettier configuration for the frontend project.
