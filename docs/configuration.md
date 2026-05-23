# Configuring SerialHop

This is the user-facing guide to configuring SerialHop on a lab PC. For install and architecture, see [`README.md`](../README.md).

## The two ways to edit config — and which to use

SerialHop reads its settings from `%ProgramData%\SerialHop\SerialHop_config.yaml`. That file is the **source of truth** — the service parses it on startup, and a broken YAML file is what stops the service from coming up.

You have two ways to change it:

- **The panel's Config tab** — a typed editor over the same YAML file, with field-level validation, a dirty-state guard when you switch tabs, and a default **Save & restart** flow so changes take effect immediately. This is what you should reach for first.
- **Editing `SerialHop_config.yaml` directly** — needed only if the panel won't open, you're doing a scripted rollout, or a future SerialHop release adds a field that the panel doesn't expose yet. Today every field is in the panel; this is an escape hatch.

The rest of this guide is organized around the panel. The [Field reference](#field-reference) table at the bottom names every YAML key for the times you do go to the file.

## Editing through the panel

Open `SerialHop.exe` (or the Start Menu shortcut). The **Config** tab loads the current values and shows them grouped by section: lab-bridge, REST, discovery, log, raw serial, auto-update, firmware flashing.

- **Validation is per field.** The lab-bridge host must be an IPv4 address or an RFC 1123 hostname (IPv6 is not supported). Integer fields can be cleared. Bad values are flagged inline; you can't save until they're fixed.
- **Switching tabs with unsaved edits opens a confirmation modal** that lists exactly which fields are dirty, so you don't lose work by accident.
- **The default save action is Save & restart.** SerialHop reads config only at service startup, so restarting the service is how new values take effect. The button does both for you.
- **Open config file** reveals `SerialHop_config.yaml` in Windows Explorer, in case you want to look at the file directly or copy it elsewhere.

## Editing the YAML directly

The service reads the YAML only at startup, so the safe edit flow is:

1. Open `%ProgramData%\SerialHop\SerialHop_config.yaml` in any text editor. (The panel's **Open config file** button does this for you.)
2. Make your edit and save.
3. Click **Restart** on the panel's Status tab. From an elevated terminal, `sc stop SerialHop && sc start SerialHop` does the same thing.

A first install writes a scaffold file with inline comments describing every field, so you're editing against a working example.

If the YAML fails validation on next service start, the service won't come up and the panel's Local-service lamp stays red. Open the Logs tab — the validation error is logged with the offending field.

## Common tasks

### Point SerialHop at a different lab-bridge server

Change **lab-bridge → Host** (`lab_bridge.host`). Accepts an IPv4 address or hostname. Save & restart.

### Set or rotate the lab-bridge credentials

Change **lab-bridge → Username** and **Password** (`lab_bridge.user`, `lab_bridge.pass`). Both are required — the service will not start with either empty. They're used both for the chisel reverse tunnel auth and as the Bearer-token identity on the lab-bridge public API.

The initial values are set during install (or via a first-run dialog in the panel). Rotating them later is a normal Config-tab edit.

### Restrict discovery to specific COM ports

If you only want SerialHop to probe a known set of ports, list them in **discovery → Include** (`discovery.include`), e.g. `COM3`, `COM4`. To skip specific ports instead, use **discovery → Exclude** (`discovery.exclude`).

Include and exclude are mutually exclusive — set one, not both. The service refuses to start if both are populated.

### Boards that need more (or less) time to settle after open

When SerialHop opens a serial port, many Arduino-class boards auto-reset on DTR and need ~1–2 seconds before they answer probe bytes. That delay is **discovery → Post-open settle** (`discovery.post_open_settle_ms`, milliseconds, default `2000`).

- **Boards missed by discovery** even though they're plugged in: raise this. They probably need more time to come up before they'll respond.
- **Boards that don't reset on DTR** (custom firmware, USB-CDC bridges): lower it, or set it to `0` to disable the settle entirely. Discovery will be faster.

### Turn on verbose logging for troubleshooting

Change **log → Level** (`log.level`) from `info` to `debug`. Save & restart. The Logs tab will now show probe-by-probe details, request/response byte arrays for serial commands, and other low-level events. Set it back to `info` when you're done — `debug` is noisy.

### Enable raw serial commands to undiscovered ports

By default, you can only send commands to ports where SerialHop has positively identified a device (pump, valve, densitometer). To send raw bytes to any enumerated COM port — useful for diagnosing a board that isn't being discovered, or for talking to instruments outside the supported list — turn on **raw serial → Enabled** (`raw_serial.enabled`).

This unlocks the panel's Ports-tab command box and the `GET /serial/ports` and `POST /serial/ports/{port}/command` REST endpoints. The detailed-listing endpoint (`GET /serial/ports/detailed`) is always available regardless of this flag.

> **Note.** Raw serial bypasses SerialHop's device-classification step. Anyone with access to the panel or the API can send arbitrary bytes to any port. Leave it off unless you're actively diagnosing something.

### Enable firmware flashing

To allow SerialHop to flash a new Intel-HEX firmware to a connected AVR / optiboot board (Arduino Uno R3 today), turn on **firmware flashing → Enabled** (`flashing.enabled`). This unlocks the `POST /flash/{port}` REST endpoint. The panel does not expose a flash button today; flashing is initiated by the lab-bridge UI calling the REST endpoint.

Two related fields:

- **Backup directory** (`flashing.backup_dir`) — absolute path where SerialHop writes a pre-flash backup of the existing firmware (used for auto-rollback if the new firmware fails verification or its test command). Leave blank to use the default, `%ProgramData%\SerialHop\backups`.
- **Keep N backups** (`flashing.keep_n`) — how many backups to retain per COM port. Older backups are pruned after each completed flash. Default `10`. Set to `0` to keep all backups indefinitely.

> **Warning.** Firmware flashing is materially riskier than raw serial: a bad HEX file can leave a board unresponsive ("bricked") until it's recovered with an ISP programmer. SerialHop's byte-verify and auto-rollback flow cover the common failure modes, but they can't cover everything. Only enable flashing on machines where the operators are aware they can re-flash firmware remotely.

## Field reference

Every field in `SerialHop_config.yaml`. Defaults are what a fresh install writes. All fields require a service restart to take effect — the panel's **Save & restart** handles that for you.

### `lab_bridge`

| Field | Type | Default | Validation | Effect |
| --- | --- | --- | --- | --- |
| `host` | string | `111.88.145.138` | IPv4 address or RFC 1123 hostname; IPv6 rejected | The lab-bridge VPS that SerialHop connects to (chisel reverse tunnel + public HTTPS API). |
| `user` | string | *(required)* | non-empty | Chisel auth user; also the Bearer-token identity on `/api/public/clients/{user}`. |
| `pass` | string | *(required)* | non-empty | Chisel password; also the Bearer token. Never logged. |

### `rest`

| Field | Type | Default | Validation | Effect |
| --- | --- | --- | --- | --- |
| `port` | integer | `0` | `0..65535` | Local TCP port for the REST listener on `127.0.0.1`. `0` lets the OS pick a free port (recommended — nothing outside the chisel tunnel reaches this port anyway). |

### `discovery`

| Field | Type | Default | Validation | Effect |
| --- | --- | --- | --- | --- |
| `include` | list of strings | `[]` | mutually exclusive with `exclude` | If non-empty, probe **only** these COM ports during discovery. |
| `exclude` | list of strings | `[]` | mutually exclusive with `include` | If non-empty, **skip** these COM ports during discovery. |
| `post_open_settle_ms` | integer | `2000` | `>= 0` | Milliseconds to wait after opening a port before sending probe bytes. Covers the Arduino auto-reset bootloader window. `0` disables. |

### `log`

| Field | Type | Default | Validation | Effect |
| --- | --- | --- | --- | --- |
| `level` | string | `info` | one of `debug`, `info`, `warn`, `error` | Minimum slog level written to disk and shipped to Loki. `debug` is noisy; reserve for troubleshooting. |

### `raw_serial`

| Field | Type | Default | Validation | Effect |
| --- | --- | --- | --- | --- |
| `enabled` | bool | `false` | — | When `true`, unlocks `GET /serial/ports` and `POST /serial/ports/{port}/command`. Bypasses device classification. |

### `auto_update`

| Field | Type | Default | Validation | Effect |
| --- | --- | --- | --- | --- |
| `enabled` | bool | `true` | — | When `true`, the panel checks GitHub Releases for newer SerialHop versions and offers a guided install (download → SHA-256 verify → install → auto-rollback on service-up failure). |

### `flashing`

| Field | Type | Default | Validation | Effect |
| --- | --- | --- | --- | --- |
| `enabled` | bool | `false` | — | When `true`, unlocks `POST /flash/{port}`. AVR / optiboot only. |
| `backup_dir` | string | `""` | absolute path when `enabled: true`; ignored otherwise | Where pre-flash backups are written. Empty falls back to `%ProgramData%\SerialHop\backups`. |
| `keep_n` | integer | `10` | `>= 0` | How many backups per port to retain. `0` keeps all. |

## When something looks wrong

Start in the panel's **Logs** tab. It tails the same structured logs the service writes, newest first, with a level + free-text filter and an inline detail view for structured fields. If the service failed to come up after a config change, the validation error is right there with the offending field name.

If the panel itself won't open or the Logs tab is empty, the on-disk log is the fallback: `%ProgramData%\SerialHop\logs\SerialHop.log` (slog JSON; rotated at 10 MB × 3 backups). `SerialHop_stderr.log` in the same folder captures chisel output and any panics.
