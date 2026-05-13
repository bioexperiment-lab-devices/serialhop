# SerialHop Panel — UI contents (what & why)

## Main window

### Status indicators
- **Service lamp** — local Windows service (SerialHop) state: not-installed / stopped / starting / running / stopping. Tells the operator whether the background agent that does all the real work is alive on this machine.
- **Server lamp** — reachability + health of the configured lab-bridge server (HTTPS probe to `/healthz` or equivalent). Tells the operator whether the remote server is up and the network to it works.
- **Tunnel lamp** — state of the Chisel reverse tunnel from this machine back to the lab-bridge (probed by checking the server-side view of the tunnel). Tells the operator whether *their own* connection to the lab-bridge is established and authenticated. Distinguishes auth failure from server error from plain disconnect.

The three lamps exist as a triage tool: when "things don't work", the operator can tell at a glance whether the local service, the remote server, or the link between them is at fault.

### Configuration readouts
Read-only mirror of the live `config.yaml` so the operator can verify settings without opening the file:
- **Chisel server** — host:port the service connects to (port read from the bootstrap cache, hence the `—`/`…` placeholders when the cache is missing/stale).
- **Remote port** — the per-user port assigned by the lab-bridge on its public side (also from cache).
- **REST port** — local port the service exposes on this machine for the lab-bridge to reach back through the tunnel.
- **Discovery** — serial-device include/exclude globs.
- **Raw serial** — whether the unfiltered raw-serial passthrough is on.
- **Log level** — current logger verbosity.

Justification: lets the operator answer "is the service pointing at the right server / using the right port / set to debug?" without leaving the panel.

### Warning label
Surfaces non-fatal startup problems that don't justify aborting the panel:
- Per-user dirs couldn't be created (so "Open config/logs" can't work).
- Config file is malformed (so the service can't run even if installed).

Justification: these are conditions the operator must know about before clicking Install/Restart, but the panel still needs to be usable to fix them.

### Update row (auto-update flow)
Single piece of UI that walks the operator through the update state machine:
- Announce an available version + offer Release notes.
- Download (with cancel), with progress mirrored to the status bar.
- Verify (SHA256SUMS) — failure is surfaced as "download failed" with Retry.
- Install — Retry on failure; "close and reopen" instruction on success because the running panel is the old binary.

Hidden when no update is in flight. Justification: shipping updates to non-technical lab operators automatically is core to the product; this is the human handoff for the steps that need consent (download, install) or recovery (retry).

### Service action buttons
- **Install** — register the Windows service under SCM and start it.
- **Uninstall** — stop and deregister.
- **Restart** — bounce the service (e.g. after editing the config file).

Each runs through a UAC-elevated subprocess because SCM mutations require admin. Enablement is computed from current SCM state + config validity so the operator can't, e.g., click Install when already installed or when the config is broken.

### File buttons
- **Open config file** — opens `config.yaml` in the default editor.
- **Open logs folder** — opens the per-user logs directory.

Justification: the panel deliberately doesn't embed an editor or log viewer. These shortcuts cover the only two "drill-down" things an operator does.

### Status bar
Single line of last-action feedback ("Working…", "Service installed at HH:MM:SS", "Failed: <msg>", download progress). Justification: action buttons are fire-and-forget from the UI's perspective (real work happens in a subprocess or goroutine), so the operator needs a confirmation channel separate from the lamps.

## First-run credentials dialog
Forced modal on first launch (or when creds are missing) before the panel becomes usable. Reason: the service can't establish the tunnel without lab-bridge credentials, and there's no other in-panel way to enter them — the panel reads config but doesn't edit it.
- **Username / Password fields** — written into `config.yaml` (password in plain text, per explicit product requirement).
- **Verify-then-save flow** — submits credentials against the lab-bridge before writing them, so a typo is caught immediately rather than surfacing later as a red Tunnel lamp.
- **"Save anyway" escape hatch** — if the server is unreachable during verify, the operator can still persist the creds (offline first-run, flaky network), with a confirmation prompt so it isn't accidental.
- **Cancel** — leaves the panel running with empty creds; the warning label and Tunnel lamp will then nag the operator into reopening the dialog (currently only by restarting).

## What's deliberately NOT in the UI
Useful to know for redesign — these absences are intentional:
- No in-panel config editor (operators are expected to use Notepad via "Open config file").
- No log viewer (operators use Explorer via "Open logs folder").
- No tray icon / minimize-to-tray (panel is meant to be opened ad-hoc, not run continuously).
- No menus, no settings page, no tabs.
- No way to re-open the creds dialog from the running panel (only fires when stored creds are missing at startup).
- No "View error details" button on failed admin actions — the status bar's one-liner is the only error surface.
