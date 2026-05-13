# SerialHop Operator Panel — UI Specification

This is the contents specification for the operator panel UI. Each section enumerates the controls, what each one does, and what the user sees in each state. Visual design, layout, typography, and spacing are intentionally not specified.

## Audience and purpose

The panel is opened by a lab operator on a Windows machine that hosts the SerialHop background service. The operator is typically non-technical: they need to install or restart the service, edit a small set of settings, see which lab devices are currently connected, and look at recent logs when something misbehaves. The panel is opened ad-hoc, not run continuously.

## 1. Overall structure

### Tabs

Five tabs, in this order:

1. **Status** — service health and service-control actions.
2. **Config** — editable settings.
3. **Devices** — list of discovered lab devices.
4. **Ports** — OS-level serial-port enumeration.
5. **Logs** — live log viewer.

Tabs are always visible and always enabled. When the data a tab depends on is unavailable, the tab shows its empty state inside (see each tab's section). Tab headers themselves are never disabled or hidden.

### Window-wide elements

Visible from every tab:

- **Window title.** Reads `SerialHop v<version>`.
- **Warning header.** A strip across the top showing `⚠ <error message>` whenever a panel-wide problem exists (couldn't create the data directory, configuration file is missing or malformed, etc.). Hidden when there's no problem.
- **Status footer.** A strip across the bottom showing the most recent action's outcome with a timestamp. Persists across tab switches. Example contents:
  - `Working…`
  - `Saved.`
  - `Saved. Restart the service to apply.`
  - `Service installed at 15:04:23`
  - `Service restarted at 15:07:11`
  - `Cancelled.`
  - `Failed: <message>`
  - `Download complete.`
  - `Downloading 42% (3.1 / 7.4 MB)`
  - `Update applied at 15:09:48`
  - `Note: unsaved config changes were not applied.`

### First-launch behavior

When the panel opens, it inspects the saved configuration:

- If the operator's **Username** or **Password** is empty, the panel opens on the **Config** tab.
- Otherwise it opens on the **Status** tab.

All other config fields are pre-filled with sensible defaults on a fresh install, so the operator only fills in their credentials on day one.

## 2. Status tab

### 2.1 Lamps

Three lamps, top of the tab. Each lamp has:

- A colored dot.
- A short state label.
- A `(?)` help icon (see §7 for the help-icon convention).

**Service lamp.** Tracks the local SerialHop background service.
- States: `Running`, `Starting…`, `Stopping…`, `Stopped`, `Not installed`.
- Colors: green when running; grey while transitioning or stopped; red when not installed and the configuration is invalid (so the service couldn't start anyway).

**Server lamp.** Tracks reachability and health of the remote lab-bridge server.
- States: `Checking…`, `Up`, `Chisel down`, `Unreachable`.
- Colors: green when up; red when the server is reachable but its tunnel daemon isn't responding; grey while checking or when the server can't be reached at all.

**Tunnel lamp.** Tracks this machine's reverse tunnel into the lab-bridge.
- States: `Checking…`, `Connected`, `Disconnected`, `Auth failed`, `Server error`, `Unreachable`, `Not configured`.
- Colors: green when connected; red on disconnect or authentication failure; yellow on a transient server-side error; grey while checking, when nothing is configured yet, or when the server can't be reached.

The lamps re-check periodically in the background and also immediately after any service-control action.

### 2.2 Service action buttons

Three buttons:

- **Install** — installs the background service and starts it. Enabled when the service isn't installed and the configuration is valid.
- **Uninstall** — stops and removes the service. Enabled when the service is installed.
- **Restart** — restarts the service. Enabled when the service is installed.

Each of these actions requires Windows administrator privileges and will trigger a system elevation prompt.

Behavior on click: all three buttons disable; the lamps grey out to `Checking…`; the status footer reads `Working…`. When the action finishes, the footer reports the outcome with a timestamp, the lamps re-check, and the buttons re-enable based on the new state.

### 2.3 Update row

A self-contained band, hidden when no update is in progress. It walks the operator through the update flow:

| Stage | Label | Visible buttons |
|---|---|---|
| Available | `Update: v1.2.3 available` | **Download**, **Release notes** |
| Downloading | `Update: v1.2.3 — downloading…` | **Cancel** |
| Download failed | `Update: v1.2.3 — download failed` (in red) | **Retry** |
| Ready to install | `Update: v1.2.3 — ready to install` | **Install update**, **Release notes** |
| Installing | `Update: installing…` | — |
| Installed | `Updated to v1.2.3. Close and reopen this window to load the new panel.` (in green) | — |
| Install failed | `Update failed — service restored to previous version.` (in red) | **Retry** |

Download progress is mirrored into the status footer (e.g. `Downloading 42% (3.1 / 7.4 MB)`). The **Release notes** button opens the release page in the default browser.

## 3. Config tab

A structured form. One control per setting, grouped into labelled sections. Every field has a `(?)` help icon. Field-level errors appear inline as a red marker next to the offending field. The **Save** and **Save & restart** buttons are disabled while any field is invalid.

### 3.1 First-launch banner

Visible at the top of the tab whenever the **Username** or **Password** is empty:

> Enter your lab-bridge credentials to enable the service.

Auto-clears once both fields are non-empty and saved.

### 3.2 Lab-bridge section

Settings for connecting to the remote lab-bridge server.

- **Host.** Text input. Required. Default `111.88.145.138`.
- **Username.** Text input. Required.
- **Password.** Text input. **Not masked** — shown as plain text. Required.

Saving a change to **Username** or **Password** triggers a verification step (see §3.10).

### 3.3 REST section

Local HTTP server settings.

- **Port.** Numeric input, range 0–65535. `0` means "let the OS pick a free port".

### 3.4 Discovery section

Which serial ports the service probes for devices.

- **Include list.** Editable list of COM port names (e.g. `COM3`, `COM4`). Each row has a **Remove** button; an **Add row** control below the list. Empty list means probe all enumerated ports.
- **Exclude list.** Same kind of control. Mutually exclusive with **Include** — when one list is non-empty, the other is greyed out with an inline note: *"Include and Exclude can't be used together."*
- **Post-open settle (ms).** Numeric input, ≥ 0. Default 2000.

### 3.5 Log section

- **Level.** Dropdown with four options: `debug`, `info`, `warn`, `error`. Default `info`.

### 3.6 Raw serial section

- **Enabled.** Checkbox. Off by default.

### 3.7 Auto-update section

- **Enabled.** Checkbox. On by default.

### 3.8 Firmware flashing section

This section starts with an inline information block (always visible, not a popover):

> Firmware flashing is higher risk than raw serial — a bad firmware file can brick the board, requiring physical recovery. Leave disabled unless you're actively flashing devices.

Then three fields:

- **Enabled.** Checkbox.
- **Backup directory.** Text input plus a folder-picker button. Required (must be an absolute path) when **Enabled** is on. Greyed out when **Enabled** is off. Empty value falls back to a default location.
- **Keep N backups.** Numeric input, ≥ 0. `0` means keep all backups indefinitely.

### 3.9 Actions

Four buttons, after the field groups:

- **Save.** Writes the new settings. On success the status footer reads `Saved. Restart the service to apply.` On validation failure the failing fields are marked red and the save is rejected.
- **Save & restart.** Same as **Save**, but immediately restarts the service afterwards (requires the same elevation prompt as the Status tab's **Restart**).
- **Discard changes.** Reverts every field to the last-saved value. Enabled only when there are unsaved edits.
- **Open in editor.** Opens the configuration file in the operating system's default editor. Fallback for advanced settings the form doesn't expose.

### 3.10 Verify-then-save for credentials

When **Save** or **Save & restart** is clicked AND the **Username** or **Password** has changed since the last save:

1. The panel checks both fields are non-empty.
2. The panel calls the lab-bridge with the new credentials to verify them before writing.
3. If the server accepts them: the save proceeds as normal.
4. If the server rejects them (wrong username or password): an inline error appears next to the credential fields — *"Server rejected these credentials. Check the username and password."* — and the save is **not** written.
5. If the server can't be reached at all: a confirmation prompt appears — *"Couldn't reach `<host>` to verify the credentials (`<detail>`). Save anyway?"* — with **Save anyway** and **Cancel** options. Choosing **Save anyway** writes the credentials without verification.

This verification step only runs when one of the two credential fields actually changed. Re-saving with unchanged credentials does not hit the network.

### 3.11 Unsaved-changes guard

While the form has pending edits:

- The Config tab header shows a modified marker (e.g. `Config*`).
- Switching to another tab or closing the window opens a confirmation prompt — *"Discard unsaved configuration changes?"* — with **Save**, **Discard**, and **Cancel** options.
- The Status tab's **Restart** button is not blocked by unsaved Config edits, but if Restart is clicked while edits are pending, the status footer appends a hint: *"Note: unsaved config changes were not applied."*

## 4. Devices tab

A logical view of the lab devices the service has discovered. Hardware-level information lives on the Ports tab.

### 4.1 Banner row

A single line at the top:

- `Discovered at 15:04:23` — when discovery has run.
- `Never run` — when discovery has not run in this service session.

### 4.2 Action buttons

- **Rediscover.** Closes all existing device connections and re-probes the serial ports. Buttons disable, the table greys, and the status footer shows progress. The table refreshes when discovery completes.
- **Disconnect all.** Closes every active device connection. The status footer reports the number of connections released. Devices remain listed (they aren't removed; only their connections close).
- **Refresh.** Re-fetches the device list without re-probing. For when the operator just wants to re-query state.

### 4.3 Devices table

Three columns:

- **ID** — e.g. `pump_1`. The logical identifier the lab-bridge addresses.
- **Type** — e.g. `pump`. The device's logical category.
- **Port** — e.g. `COM5`. The serial port the device is attached to.

Sorted by **ID** by default. No per-row actions. No `(?)` help on column headers — labels are self-explanatory at this level of abstraction.

### 4.4 Empty and error states

- **Service is not running:** empty table; banner — *"Service is not running. Start it from the Status tab."* All buttons disabled.
- **Service running, discovery not yet performed:** empty table; banner — *"No devices yet. Click Rediscover to probe serial ports."* **Rediscover** and **Refresh** enabled; **Disconnect all** disabled.
- **Service running but unreachable from the panel** (typical right after the service starts): empty table; banner — *"Can't reach the local service. It may have just started — wait a few seconds and click Refresh."* **Refresh** enabled, others disabled.
- **Discovery in progress:** all buttons disabled; the table greys until results arrive.

## 5. Ports tab

A view of every serial port the operating system reports, regardless of whether SerialHop matched a device on it. Useful when the operator wants to answer "why didn't my device show up?".

### 5.1 Action buttons

- **Refresh.** Re-fetches the port list. Reads what the OS currently enumerates without re-probing.
- **Rediscover.** Same effect as the Devices-tab button: closes all existing device connections and re-probes. Placed here because looking at the Ports tab is the natural prelude to "now retry discovery on these ports".

### 5.2 Ports table

Eight columns:

- **Name** — e.g. `COM5`.
- **Is USB** — checkmark or blank.
- **VID** — USB vendor ID, hex.
- **PID** — USB product ID, hex.
- **Serial number** — USB serial string, if the device reports one.
- **Product** — USB product descriptor string.
- **Discovered** — checkmark when SerialHop matched a device on this port.
- **Device ID** — the matched device's ID, empty when **Discovered** is blank.

Sorted by **Name** by default.

`(?)` help icons appear on the column headers for **VID**, **PID**, **Serial number**, **Product**, **Discovered**, and **Device ID**. (The other two columns are self-explanatory.)

### 5.3 Empty and error states

Same shape as the Devices tab:

- Service not running: empty table; *"Service is not running. Start it from the Status tab."* Buttons disabled.
- Service running, zero ports enumerated: empty table; *"No serial ports detected on this machine."* Buttons enabled.
- Service running but unreachable: empty table; *"Can't reach the local service…"* **Refresh** enabled, **Rediscover** disabled.

## 6. Logs tab

Live tailing of the panel's own log files. Works regardless of whether the service is currently running.

### 6.1 Top controls

- **Stream dropdown.** Three entries, each with its own `(?)` help popover:
  - **Service log** — structured log records from the running service. Rendered as a table with **Time**, **Level**, and **Message** columns.
  - **Stderr** — raw stderr output from the service (panic traces, lower-level errors).
  - **Panel errors** — errors logged by the panel itself.
- **Level filter dropdown.** Five entries: `all`, `debug`, `info`, `warn`, `error`. Hides records below the chosen severity. Greyed out when the selected stream is **Stderr** or **Panel errors** (those streams have no level metadata).
- **Follow toggle.** When on, auto-scrolls to the newest line as records arrive. When off, the view holds its scroll position so the operator can read older context without being yanked to the bottom.
- **Search.** Free-text input. Filters visible lines to those containing the substring; matches are highlighted in shown rows. The filter is re-applied as new lines arrive.

### 6.2 Log view

- **Service log** stream: a three-column table (Time / Level / Message). Each row is one structured log record. Selecting a row reveals all of that record's structured fields as a small key/value block below the table.
- **Stderr** and **Panel errors** streams: a single scrollable text view, one line per record.

Logs are tailed live from disk. New lines appear within roughly half a second of being written. Log file rotation is handled transparently — the view doesn't blink or reset when a rotation happens.

### 6.3 Bottom actions

- **Open logs folder.** Opens the folder containing all log files in the operating system's file explorer. Fallback for deeper inspection.

### 6.4 Empty and error states

- **Log file doesn't exist yet** (clean install, service has never started for the Service log / Stderr streams): empty view; banner — *"No logs yet. Start the service from the Status tab to begin logging."*
- **File cannot be read** (permission error): empty view; banner with the OS error message. **Open logs folder** still works.

## 7. The `(?)` help-icon convention

Across the panel, a small `(?)` icon appears next to labels and column headers where an operator may not immediately know what something is or does. Clicking it opens a small popover with help text.

### Where they appear

- **Status tab:** next to each of the three lamp rows.
- **Config tab:** next to every field label in §3.2 through §3.8. (The Firmware-flashing information block in §3.8 is inline text, not a popover — it's information all operators of that subsection need to read.)
- **Ports tab:** on the column headers for VID, PID, Serial number, Product, Discovered, and Device ID.
- **Logs tab:** on each entry in the **Stream** dropdown.

### Where they do not appear

- Service-action buttons on the Status tab.
- Devices-tab table columns.
- Logs-tab **Follow**, **Search**, and **Level** controls.

### Content template

Each popover follows the same three-part structure:

1. **What it is** — one sentence.
2. **Default or typical value** — when relevant.
3. **When to change it / what it affects.**

Example for the Service lamp:

> Is the SerialHop background service installed and running on this machine. Green means running. Red means not installed and the configuration is invalid, so the service can't start. Grey means installed but stopped, or in the middle of starting or stopping.

Example for the Discovery **Post-open settle (ms)** field:

> How long to wait after opening a serial port before sending the discovery probe. Default 2000 ms. Lower this if your devices don't reset when the port is opened; raise it if discovery is silently missing devices.

## 8. State-handling principles (summary)

A few rules apply consistently across the panel:

- **Tabs are always visible and enabled.** If a tab can't show its data, it shows an empty-state banner inside instead.
- **Banners explain why, then suggest the next action.** Every empty/error state combines a short explanation with a pointer to where the operator should go to fix it.
- **The status footer is the action-feedback channel.** Any operator-initiated action (saving config, restarting the service, downloading an update) reports its outcome there with a timestamp.
- **`(?)` icons exist wherever a non-technical operator might be unsure.** They are not a substitute for clear labels — they supplement them.
- **Unsaved-changes are guarded.** The Config tab will not let the operator lose edits accidentally.
- **Destructive or elevated actions are explicit.** Anything that touches the service triggers an elevation prompt; anything that changes saved settings either requires an explicit **Save** click or has its own confirmation.
