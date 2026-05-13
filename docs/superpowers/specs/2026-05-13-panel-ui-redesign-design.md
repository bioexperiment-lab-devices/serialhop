# Panel UI Redesign — Design

**Date:** 2026-05-13
**Status:** Approved (brainstorming complete; pending spec review before plan)

## 1. Purpose & scope

Replace the current single-pane panel with a tabbed UI that adds first-class editing of configuration, live in-UI log viewing, and live device/port views — turning the panel from a launcher-with-status into a self-contained operations console for non-technical lab operators.

Motivation: today the panel forces operators to open a YAML file in Notepad to change anything, open Explorer to read logs, and trust the lab-bridge UI to see what devices are connected. None of those are appropriate for the non-technical audience the client targets. This redesign moves the common operations into the panel itself while keeping the existing "Open in editor" / "Open logs folder" shortcuts as fallbacks for advanced cases.

In scope:

- Five-tab layout (Status / Config / Devices / Ports / Logs).
- Structured config form with inline validation backed by `config.Validate()`.
- Live log tail of on-disk log files in `%ProgramData%\SerialHop\logs\`.
- Live devices + ports view backed by the existing service REST API.
- First-launch flow folded into Config tab; first-run modal removed.
- `(?)` inline help affordance across the panel.
- Service-side: publish the actual bound REST port via the existing bootstrap cache so the panel can call into the local service when `rest.port: 0`.

Out of scope:

- New service REST endpoints. The panel uses only what's already exposed in `internal/api`.
- Visual / layout design. This spec describes UI **contents** only — what controls exist, what each one does, and why it's there. Per-tab widget layout, fonts, spacing, colors, and window sizing are decided at implementation time.
- UI framework change. Continuing on `lxn/walk` declarative.
- Per-device disconnect endpoint. Bulk `POST /devices/disconnect` continues to be the only disconnect path.
- Power-user device actions (send command, flash) on the Devices tab. Those remain lab-bridge-only.
- Embedded YAML text editor. The structured form covers known fields; "Open in editor" covers everything else.
- Config schema changes. `internal/config.Config` is unchanged.

## 2. Tab structure & global elements

Tabs in order: **Status**, **Config**, **Devices**, **Ports**, **Logs**.

Tabs are always visible and enabled. Empty / error states are handled inside each tab via banners and disabled buttons — no "ghost tabs", no disabled tab headers.

Global UI elements visible from every tab:

- **Window title** — `SerialHop v<X.Y.Z>` (unchanged).
- **Warn label header** — shows `⚠ <error>` when `paths.EnsureDirs()` failed at startup, the config file is missing/malformed, or any panel-wide invariant fails. Hidden when clean. Promoted from today's Status-tab-local warn label to a global header so a config-invalid state is visible from every tab.
- **Status bar footer** — shows the most recent action outcome with timestamp. Persists across tab switches. Examples: `Saved.`, `Service installed at 15:04:23`, `Failed: <msg>`, download progress. Global so a confirmation from one tab does not disappear when the operator clicks over to verify on another tab.

First-launch behavior:

- If `lab_bridge.user` or `lab_bridge.pass` is empty in the loaded config, the panel opens on the **Config** tab. Otherwise it opens on **Status**.
- All other config fields are prefilled with `config.Default()` values on the form so the operator only fills in the credentials.
- A banner at the top of the Config tab reads *"Enter your lab-bridge credentials to enable the service."* It is visible whenever `user` or `pass` is empty; auto-hides once both are non-empty and saved.

## 3. Status tab

Service-health and service-control. No configuration data, no logs, no devices.

### 3.1 Lamps

Three lamps, each with a `(?)` help icon next to the row:

- **Service lamp** — local SerialHop Windows service state (not-installed / stopped / starting / running / stopping). State text + color from `serviceLampPresentation()` (unchanged).
- **Server lamp** — reachability + health of the configured lab-bridge server. State text + color from `serverLampPresentation()` (unchanged).
- **Tunnel lamp** — state of this machine's Chisel reverse tunnel into the lab-bridge. State text + color from `tunnelLampPresentation()` (unchanged).

`(?)` popover content template per lamp: what it checks → what each color means → which color is actionable. Wording finalized at implementation time. Example for the Service lamp:

> Is the SerialHop Windows service installed and running on this machine. Green = running. Red = not installed and config invalid (service can't start). Grey = installed but stopped, or starting/stopping.

### 3.2 Service action buttons

- **Install** — UAC-elevated `install` action. Enabled when SCM state allows + config valid.
- **Uninstall** — UAC-elevated `uninstall`. Enabled when service is installed.
- **Restart** — UAC-elevated `restart`. Enabled when service is installed.

Behavior on click (unchanged from today, just moved from the bottom of the panel into the Status tab): all three buttons disable; lamps grey to `Checking…`; status bar reads `Working…`. On completion: status bar reports outcome + timestamp; lamps re-probe; buttons re-enable per the new SCM state.

### 3.3 Update row

Auto-update state machine. Hidden when no update is in flight. Same logic as today's `applyUpdateRow`:

- `UpdateAvailable` — label + **Download** / **Release notes** buttons.
- `UpdateDownloading` — label + **Cancel** button. Progress mirrors into the global status bar.
- `UpdateDownloadFailed` — label (red) + **Retry**.
- `UpdateReady` — label + **Install update** / **Release notes**.
- `UpdateInstalling` — label only.
- `UpdateInstalled` — label (green): *"Updated to <tag>. Close and reopen this window to load the new panel."*
- `UpdateInstallFailed` — label (red) + **Retry**.

Same update-check cadence as today: on-launch (500 ms delay) + every 6 h when `auto_update.enabled` is true.

## 4. Config tab

Structured form. One widget per `config.Config` field, grouped by YAML section. Every field has a `(?)` icon. Inline validation against `config.Validate()`.

### 4.1 Lab-bridge section

- **Host** — text field. Default `111.88.145.138` (prefilled). Required non-empty.
- **Username** — text field. Required non-empty. Save triggers verify-then-save (see §4.9).
- **Password** — plaintext text field. No masking and no show/hide toggle — matches the existing convention that the password is stored as plain text in the YAML. Required non-empty. Save triggers verify-then-save.

### 4.2 REST section

- **Port** — numeric field, 0..65535. `0` = OS picks a free port.

### 4.3 Discovery section

- **Include list** — editable list of COM port names. Each row: text input + **Remove** button. **Add row** button below the list. Empty list means probe all enumerated ports.
- **Exclude list** — same widget kind. Mutually exclusive with Include — when one list is non-empty, the other is greyed out with an inline note *"Include and Exclude can't be used together"*.
- **Post-open settle (ms)** — numeric field, ≥ 0. Default 2000 (covers the Arduino bootloader reset window).

### 4.4 Log section

- **Level** — dropdown: `debug` / `info` / `warn` / `error`. Default `info`.

### 4.5 Raw serial section

- **Enabled** — checkbox. When on, exposes `GET /serial/ports` + `POST /serial/ports/{port}/command` (bypasses device classification; for diagnostics).

### 4.6 Auto-update section

- **Enabled** — checkbox. When on, the panel checks GitHub Releases on launch + every 6 h and surfaces the update row on the Status tab.

### 4.7 Firmware flashing section

Info block prefacing the field group (rendered as inline text at the top of the subsection, not a popover):

> Firmware flashing is higher risk than raw serial — a bad `.hex` bricks the board (ISP recovery required). Leave disabled unless you're actively flashing devices.

Fields:

- **Enabled** — checkbox.
- **Backup directory** — text field + folder-picker button. Required absolute path when **Enabled** is on. Greyed out when **Enabled** is off. Empty value means the service falls back to `%ProgramData%\SerialHop\backups` (matches today).
- **Keep N backups** — numeric field, ≥ 0. `0` = keep all.

### 4.8 Actions row

- **Save** — runs `config.Validate()`; on success writes YAML; status bar reads *"Saved. Restart the service to apply."* Inline error markers on failing fields. **Save** and **Save & restart** are disabled while any field is invalid.
- **Save & restart** — same as **Save**, then triggers the UAC-elevated service restart. Status bar reports restart progress through the same channel as the Status tab's **Restart** button.
- **Discard changes** — reverts every field to the on-disk YAML. Enabled only when there are unsaved edits.
- **Open in editor** — fallback. Opens `SerialHop_config.yaml` in the default app. Today's "Open config file" behavior, moved into the Config tab.

### 4.9 Verify-then-save on credentials

When the user clicks **Save** or **Save & restart** AND either `lab_bridge.user` or `lab_bridge.pass` differs from the on-disk YAML:

1. Validate non-empty.
2. POST to the lab-bridge with the new credentials using the existing `verifyCredentials()` path.
3. On `CredsOK` — proceed with the save.
4. On `CredsUnauthorized` — inline error next to the credential fields: *"Server rejected these credentials. Check the username and password."* Save is not written.
5. On `CredsNeedsConfirm` (network failure) — prompt *"Couldn't reach `<host>` to verify the credentials (`<detail>`). Save anyway?"* On Yes, proceed; on No, cancel.

Other fields save without any network check. The verify call only fires when at least one of the two credential fields actually changed (re-saving with unchanged credentials does not hit the network).

### 4.10 Unsaved-changes guard

- Tab header reads `Config*` while there are pending edits.
- Switching tabs or closing the window with unsaved edits → prompt *"Discard unsaved configuration changes?"* with **Save** / **Discard** / **Cancel**.
- The Status tab's **Restart** button is **not** blocked by unsaved Config edits — it operates on the on-disk YAML. But after a Restart with unsaved edits pending, the status bar appends a hint: *"Note: unsaved config changes were not applied."*

## 5. Devices tab

Logical-device view. Hardware metadata (VID/PID/serial/product) lives on the Ports tab, not here.

### 5.1 Banner row

- Last-discovery timestamp text: `Discovered at HH:MM:SS` or `Never run`. Sourced from `DevicesResponse.DiscoveredAt`.

### 5.2 Action buttons

- **Rediscover** — `POST /discover`. Buttons disable, table greys; on response the table refreshes.
- **Disconnect all** — `POST /devices/disconnect`. Status bar reports the `released` count. Table contents unchanged — devices stay registered; only connections close.
- **Refresh** — `GET /devices`. No re-probe.

### 5.3 Devices table

Columns:

- **ID** (e.g. `pump_1`) — the logical identifier the lab-bridge addresses.
- **Type** (e.g. `pump`) — the device's logical type.
- **Port** (e.g. `COM5`) — the COM port the device is on. Bridge between the logical abstraction and the physical wire.

Sort by ID by default. No per-row actions.

### 5.4 Empty / error states

- **Service stopped or not installed:** empty table + banner *"Service is not running. Start it from the Status tab."* All buttons disabled.
- **Service running, discovery never run:** empty table + banner *"No devices yet. Click Rediscover to probe serial ports."* Rediscover + Refresh enabled; Disconnect all disabled.
- **Service running but panel can't reach it** (bootstrap cache stale, port mismatch, service still starting): empty table + banner *"Can't reach the local service. It may have just started — wait a few seconds and click Refresh."* Refresh enabled, others disabled.
- **Discovery in progress:** all buttons disabled, table greyed until the response arrives.

## 6. Ports tab

OS-level serial-port enumeration. Mirrors `GET /serial/ports/detailed`.

### 6.1 Action buttons

- **Refresh** — re-fetches the list via `GET /serial/ports/detailed`. No re-probe.
- **Rediscover** — `POST /discover`. Same call as on the Devices tab. Lives here too because looking at the Ports tab is the natural prelude to "now retry discovery on these ports".

### 6.2 Ports table

Columns (all 8 fields from `DetailedPortDTO`):

- **Name** (e.g. `COM5`).
- **Is USB** — boolean, displayed as a checkmark/blank.
- **VID** — USB vendor ID (hex). `(?)` popover.
- **PID** — USB product ID (hex). `(?)` popover.
- **Serial number** — USB serial string if reported by the device. `(?)` popover.
- **Product** — USB product descriptor string. `(?)` popover.
- **Discovered** — boolean. True if discovery matched a SerialHop device on this port. `(?)` popover.
- **Device ID** — the logical device ID this port was bound to. Empty if `Discovered = false`. `(?)` popover.

Sort by Name (COM port number) by default.

### 6.3 Empty / error states

Same policy as Devices tab:

- Service stopped → empty table + *"Service is not running. Start it from the Status tab."* Buttons disabled.
- Service running, zero ports enumerated → empty table + *"No serial ports detected on this machine."* Buttons enabled.
- Service running but panel can't reach it → empty table + *"Can't reach the local service…"* Refresh enabled, others disabled.

## 7. Logs tab

Live tail of the on-disk log files. No service-API dependency.

### 7.1 Top controls

- **Stream dropdown** — three entries, each with its own `(?)` popover:
  - **Service log** → `paths.ServiceLogPath()` (slog JSON, lumberjack-rotated). Rendered as parsed columns: Time / Level / Message.
  - **Stderr** → `paths.StderrLogPath()` (raw text, lumberjack-rotated). Rendered as raw lines.
  - **Panel errors** → `paths.PanelErrorLogPath()` (append-only, no rotation). Rendered as raw lines.
- **Level filter dropdown** — `all` / `debug` / `info` / `warn` / `error`. Hides records below the chosen severity. Greyed out when the selected stream is **Stderr** or **Panel errors** (those have no level metadata).
- **Follow toggle** — when on, auto-scroll to end on every new line. When off, the view stays put as new lines append.
- **Search** — free-text input. Filters visible lines to those containing the substring; highlights matches in shown rows. Re-applied as new lines arrive.

### 7.2 Log view

- **Service log:** three-column table (Time / Level / Message). One row per slog record. Selecting a row exposes its full structured fields (all key/value pairs from the JSON record) in a small panel below the table.
- **Stderr / Panel errors:** single-column scrollable text view.

Tail starts at attach-time end-of-file. New bytes appended live at ~500 ms polling cadence. Lumberjack rotation handled transparently — the panel reopens the file when its inode/size resets.

### 7.3 Bottom actions

- **Open logs folder** — fallback. Opens `paths.LogsDir()` in Explorer (today's "Open logs folder" behavior, moved from the Status tab).

### 7.4 Empty / error states

- **Selected stream's file does not exist** (clean install, service has never started for **Service log**/**Stderr**; panel has never written an error for **Panel errors**): empty view + banner *"No logs yet. Start the service from the Status tab to begin logging."*
- **File permission error** (panel can't read the file): empty view + banner with the OS error message. **Open logs folder** still works.

## 8. Cross-cutting: (?) help icon convention

A small `(?)` icon appears next to labels and column headers where an operator may not immediately know what something is or does. Click opens a small popover.

Locations:

- **Status tab:** each of the three lamp rows.
- **Config tab:** every field label across §4.1–§4.7. (The Firmware flashing info block at §4.7 is inline text, not a popover — it's information all operators of that subsection need to read, not opt-in help.)
- **Ports tab:** column headers for VID, PID, Serial number, Product, Discovered, Device ID.
- **Logs tab:** each entry in the **Stream** dropdown (Service log / Stderr / Panel errors).

Not applied to: Service-action buttons (labels self-explanatory), Devices table columns, Logs tab Follow / Search / Level controls.

Content template per popover:

1. **What it is** (one sentence).
2. **Default or typical value** (where relevant).
3. **When to change it / what it affects.**

Concrete wording for every popover is finalized at implementation time. This spec enumerates locations and the content template.

## 9. Service-side change: ActualRestPort in bootstrap cache

The panel calls the running service over `http://127.0.0.1:<port>` to drive the Devices and Ports tabs. When `rest.port: 0` (OS-assigned), the configured value is `0` and tells the panel nothing about where the service is actually listening.

Change:

- Extend `bootstrap.Cache` (the struct serialized to `paths.ServerInfoCachePath()`) with an `ActualRestPort int` field.
- The service writes its bound REST port into the cache once `api.Listen()` returns the actual port.
- The panel reads `ActualRestPort` from the cache before each Devices / Ports tab HTTP call. (Reading per-call rather than caching in-memory keeps the panel robust against service-restart-while-panel-open scenarios.)

Read failure semantics:

- Cache missing, unparseable, or `ActualRestPort == 0`: panel treats this as "service unreachable" and shows the corresponding empty-state banner on the Devices / Ports tabs.
- Cache present but service is down (HTTP call fails): panel shows the "service is not running" banner.

This change is invisible to operators. No new endpoint and no new file.

Alternative considered but not chosen: require `rest.port` to be non-zero (forbid `0` in `Validate()`). Rejected because the YAML scaffold documents `0` as the recommended default and any deployment relying on it would break.

## 10. Removed from today's UI

- **First-run credentials modal** (`internal/panel/credsdialog_windows.go` and its `runCredsDialog` call site) — replaced by the Config-tab first-launch banner + verify-then-save behavior on the credential fields.
- **Configuration read-only label block** on the Status tab (today's six labels: Chisel server, Remote port, REST port, Discovery, Raw serial, Log level) — replaced by the editable Config tab. The cached display logic in `readCacheDisplay` is no longer called from the panel.
- **"Open config file"** button on the Status tab — moved to the Config tab as **Open in editor**.
- **"Open logs folder"** button on the Status tab — moved to the Logs tab.

## 11. Unchanged from today

- Lamp state semantics and colors (`internal/panel/lampstate.go` and the underlying probe functions in `probe.go`).
- Probe cadences: server + tunnel probes every 30 s plus action-triggered re-probe; SCM read-only polling at 1 s.
- Auto-update check cadence: on-launch (500 ms delay) + every 6 h when `auto_update.enabled` is true.
- UAC-elevated subprocess for Install / Uninstall / Restart / Update install.
- Log rotation policy (10 MB × 3 backups via lumberjack) and the Loki shipper.
- Service-side REST API surface. No new endpoints required.
- On-disk layout — `%ProgramData%\SerialHop\` for config + logs, install dir holds only `SerialHop.exe` plus transient update-staging files.
- Config schema — `internal/config.Config` shape unchanged; no new YAML fields.
- UI framework — `lxn/walk` declarative.

## 12. Testing

- **`internal/config`:** existing tests cover `Validate()` and the YAML round-trip. No new tests needed in this package — the panel's form-level validation delegates to `Validate()`.
- **`internal/bootstrap`:** add a test that the new `ActualRestPort` field round-trips through write/read.
- **Service worker:** add a test that the bound REST port is written into the cache after `api.Listen()` returns.
- **`internal/panel`:**
  - `state.go` — `ComputeButtons` keeps its current contract; no test changes.
  - `lampstate.go` — unchanged.
  - **New unit-testable helpers** added as separate files so they compile and run on macOS/Linux without `walk`:
    - Form-state model (tracks per-field dirty + valid; computes save-button enablement and the unsaved-edits flag).
    - File-tail reader (start-from-end on attach; lumberjack rotation reopen on inode/size reset; bounded ring buffer per stream).
    - Credentials-verify-on-save adapter (wraps `verifyCredentials` with the change-detection logic from §4.9).
  - Each helper gets its own focused tests.
- **`walk`-level UI tests are not added** (consistent with today — walk widgets are constructed in `panel.go` but the panel's main flow is exercised by manual smoke).

The redesign keeps every Windows-only file behind `//go:build windows` and provides an `_other.go` fake where one is required for non-Windows builds (consistent with the existing convention).

## 13. Implementation-level details deferred to the plan

These do not affect the contents spec but are flagged so the implementation plan picks values explicitly:

- Window sizing under the tab layout. Today's 480×420 is tight even for the Status tab alone; the Config tab with all field groups needs more vertical room.
- File-tail polling cadence (proposed: 500 ms). Could be replaced with `ReadDirectoryChangesW` if 500 ms feels laggy in manual testing.
- Per-stream tail buffer size (proposed: last 5,000 lines, oldest dropped as new ones arrive).
- Folder picker widget. `lxn/walk` doesn't ship a native folder picker; the plan picks between a `SHBrowseForFolder` syscall wrapper and a vendored helper.
- Initial sort orders, column widths, and tab-key order across the Config tab fields.
