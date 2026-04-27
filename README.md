# lab_devices_client

Single-binary Go application that exposes serial-port lab devices to a remote HTTP client through a chisel reverse tunnel.

## Build

Default target is Windows / amd64:
```
task build
```

Override target via env variables:
```
task build GOOS=linux GOARCH=arm64
```

Output: `dist/lab_devices_client[.exe]`.

## First run

The binary expects a `lab_devices_client_config.yaml` next to itself. On first run the binary writes a scaffold and exits:

```
> lab_devices_client.exe
Config file created at C:\path\to\lab_devices_client_config.yaml. Please review and edit it, then run again.
```

Edit the file (set `chisel.remote_port` to a unique port for this machine) and run again.

## REST API

The REST API is bound to `127.0.0.1` locally; it is reachable from outside the lab machine **only** through the chisel reverse tunnel.

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/discover` | Run a fresh discovery and return the device list |
| `GET`  | `/devices`  | Return the cached device list |
| `POST` | `/devices/{id}/command` | Send raw bytes; optionally read a reply |

See `docs/superpowers/specs/2026-04-26-lab-devices-client-design.md` for full request/response shapes and behavior.

### `POST /devices/{id}/command`

Send raw bytes to a discovered device and (optionally) read its reply. The `{id}` path parameter is a device ID returned by `/discover` or `/devices` (e.g. `pump_1`, `valve_1`, `densitometer_1`).

**Request body** — JSON object with a single `command` field whose value is a list of integers in `0..255`:

```json
{ "command": [1, 2, 3, 4, 0] }
```

**Query parameters** (all optional):

| Param | Default (when `expected_response_bytes=-1`) | Default (when `expected_response_bytes>0`) | Range | Meaning |
|---|---|---|---|---|
| `timeout_ms` | `100` | `1000` | `1..60000` | Max wait (ms) for the **first** response byte |
| `inter_byte_ms` | `25` | `50` | `1..1000` | Inter-byte silence (ms) that ends the read |
| `wait_for_response` | `true` | `true` | `true` / `false` | If `false`, write and return immediately with empty `response` |
| `expected_response_bytes` | `-1` | `-1` | `-1` or `1..1024` | If `>0`, stop reading as soon as that many bytes are collected |

**Response (200)** — JSON object with the bytes the device returned, as integers:

```json
{ "response": [10, 1, 2, 3] }
```

If the device stayed silent (or `wait_for_response=false`) the response is `{ "response": [] }`.

**Error responses** all share the same shape:

```json
{ "error": "<short_code>", "detail": "<human readable>" }
```

| Status | When |
|---|---|
| 400 | Body malformed, command bytes out of `0..255`, or query param invalid |
| 404 | Unknown device id |
| 409 | Another command is already running against this device |
| 503 | Device unreachable / I/O failed / device identity changed (re-run `/discover`) |

#### Read-termination rules

When `wait_for_response=true`:

- `expected_response_bytes=-1` (default): read until either (a) `timeout_ms` fires before any byte arrives, or (b) once at least one byte has arrived, `inter_byte_ms` of silence elapses. Return whatever was collected.
- `expected_response_bytes=N`: read until either (a) `N` bytes collected, (b) `timeout_ms` fires before any byte arrives, or (c) once at least one byte has arrived, `inter_byte_ms` of silence elapses. Partial results are returned without error.

#### Examples

Send a 5-byte probe to `pump_1`, default timeouts, return whatever the device replies:

```bash
curl -X POST http://127.0.0.1:8080/devices/pump_1/command \
  -H 'Content-Type: application/json' \
  -d '{"command":[1,2,3,4,0]}'
# → {"response":[10,12,34,56]}
```

Fire-and-forget — send bytes, don't wait for a reply:

```bash
curl -X POST 'http://127.0.0.1:8080/devices/valve_1/command?wait_for_response=false' \
  -H 'Content-Type: application/json' \
  -d '{"command":[20,3]}'
# → {"response":[]}
```

Read exactly 4 bytes back with the longer defaults that come with `expected_response_bytes`:

```bash
curl -X POST 'http://127.0.0.1:8080/devices/densitometer_1/command?expected_response_bytes=4' \
  -H 'Content-Type: application/json' \
  -d '{"command":[5]}'
# → {"response":[70,0,0,2]}
```

Override the timeouts for a long-running command:

```bash
curl -X POST 'http://127.0.0.1:8080/devices/pump_1/command?timeout_ms=2000&inter_byte_ms=100' \
  -H 'Content-Type: application/json' \
  -d '{"command":[40,1,2]}'
```

Note: in production this app's REST port is reachable only from inside the chisel server's docker network. The examples above target `127.0.0.1:8080` for local testing on the lab machine itself.

## Tests

```
task test
```
