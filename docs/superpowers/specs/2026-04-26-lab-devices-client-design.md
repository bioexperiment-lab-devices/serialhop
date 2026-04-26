# Lab Devices Client — Design

**Date:** 2026-04-26
**Status:** Approved (brainstorming complete; pending spec review before plan)
**Target platform:** Windows (amd64 default; cross-compile from any host via Taskfile)

## 1. Purpose

A single-binary Go application that lets a remote HTTP client control serial-port devices attached to a lab machine. The lab machine sits behind NAT/firewall, so the app dials out to a chisel server and exposes its local REST API as a reverse-tunneled port on that server. An upstream service (running in the chisel server's docker compose) handles authentication and proxies authorized requests to the tunneled port.

Three device types are supported, identified by a universal probe:

| Type code | Device | Probe reply |
|---|---|---|
| 10 | Peristaltic Pump | `[10, cal_1, cal_2, cal_3]` |
| 30 | Distribution Valve | `[30, 1, 1, 6]` |
| 70 | Densitometer | `[70, 0, 0, 2]` |

## 2. High-level architecture

A single Go binary that, on startup, runs three things concurrently:

1. **HTTP REST server** bound to `127.0.0.1:<local_port>`. The local port comes from config; `0` means OS picks a free port.
2. **Chisel client** connecting to the configured chisel server with one reverse-tunnel spec: `R:<remote_port>:127.0.0.1:<local_port>`. The chisel server thereby exposes the REST API on its docker network at `<remote_port>`.
3. **Device registry** — in-memory `map[id]*Device`. Each `Device` owns its open serial port handle and a per-device mutex.

The REST API has no authentication of its own. Authentication is enforced by an upstream service that fronts the tunneled port; the chisel server's reverse-tunnel ports are not reachable from outside its docker network.

### Internal package layout

```
cmd/lab_devices_client/main.go     # config-or-scaffold, wiring, signal handling
internal/config/                   # YAML load + scaffold writer + validation
internal/chisel/                   # thin wrapper around jpillora/chisel/client
internal/serial/                   # open / drain / write-with-gap / read-with-inter-byte-timeout
internal/discovery/                # parallel probe + classification
internal/registry/                 # device map, lifecycle (close-all on next discovery)
internal/api/                      # http.Handlers
```

### Third-party dependencies

- `github.com/jpillora/chisel/client` — reverse tunnel
- `go.bug.st/serial` — cross-platform serial (works on Windows COM ports)
- `gopkg.in/yaml.v3` — config
- stdlib `net/http` (Go 1.22+ routing) — no router framework

## 3. Configuration

### 3.1 Runtime config — `lab_devices_client_config.yaml`

The file lives in **the same directory as the executable** (resolved from `os.Executable()`), so double-clicking the .exe from Explorer behaves predictably.

Schema with defaults:

```yaml
chisel:
  server: "111.88.145.138:7000"   # chisel server host:port
  remote_port: 8081               # REQUIRED — port to expose on the chisel server
  user: "devices_coordinator"     # default; override if your chisel server expects different
  pass: ""                        # optional

rest:
  port: 0                         # local REST port; 0 = OS picks a free one

discovery:
  include: []                     # optional: only probe these COM ports, e.g. ["COM3", "COM4"]
  exclude: []                     # optional: skip these COM ports, e.g. ["COM1"]

log:
  level: "info"                   # debug | info | warn | error
```

### 3.2 Startup flow

1. Resolve config path = `<dir of os.Executable()>/lab_devices_client_config.yaml`.
2. **If file missing** → write the scaffold above (with all defaults and explanatory comments), print `"Config file created at <path>. Please review and edit it, then run again."`, exit 1.
3. **If file present** → parse and validate. On error: print error and exit 1.
4. Open local REST listener (gets the actual port if `rest.port: 0`).
5. Start chisel client with reverse-tunnel spec `R:<remote_port>:127.0.0.1:<actual_local_port>`. Chisel handles its own retry/backoff in the background; REST stays up regardless of chisel state.
6. Block on signal (`os.Interrupt`); on shutdown, close all device serial ports, stop chisel, stop HTTP server.

### 3.3 Validation rules

- `chisel.server` non-empty and parses as `host:port`.
- `chisel.remote_port` in `1..65535`.
- `discovery.include` and `discovery.exclude` are mutually exclusive (only one or the other, or both empty).
- `log.level` is one of `debug | info | warn | error`.

## 4. REST API

### 4.1 `POST /discover`

Triggers a fresh discovery run. No body. Response shape identical to `GET /devices` (see 4.2).

If a discovery is already running → **409 Conflict** `{"error":"discovery in progress"}`.

### 4.2 `GET /devices`

Returns the cached result of the most recent discovery, without re-probing.

```json
{
  "devices": [
    {"id": "pump_1",  "type": "pump",         "type_code": 10, "port": "COM3"},
    {"id": "valve_1", "type": "valve",        "type_code": 30, "port": "COM4"},
    {"id": "densitometer_1", "type": "densitometer", "type_code": 70, "port": "COM7"}
  ],
  "discovered_at": "2026-04-26T12:34:56Z"
}
```

If discovery has never run, returns `{"devices": [], "discovered_at": null}`.

### 4.3 `POST /devices/{id}/command`

Send raw bytes to a discovered device.

**Query params** (all optional):

| Param | Default when `expected_response_bytes=-1` | Default when `expected_response_bytes>0` | Range | Meaning |
|---|---|---|---|---|
| `timeout_ms` | `100` | `1000` | `1..60000` | Initial timeout — max wait for the **first** response byte |
| `inter_byte_ms` | `25` | `50` | `1..1000` | Inter-byte silence that ends the read |
| `wait_for_response` | `true` | `true` | `true` / `false` | If `false`, write and return immediately with empty body (other read params ignored) |
| `expected_response_bytes` | `-1` | `-1` | `-1` or `1..1024` | If `>0`, stop reading as soon as that many bytes are collected; `-1` means use timeout-based termination only |

**Request body:**
```json
{ "command": [1, 2, 3, 4, 0] }
```

Bytes must each be in `0..255`. Out-of-range or non-integer values → 400.

**Response (200):**
```json
{ "response": [10, 1, 2, 3] }
```
or `{ "response": [] }` when the device stayed silent or `wait_for_response=false`.

### 4.4 Read termination rules (when `wait_for_response=true`)

- `expected_response_bytes=-1`: read until either (a) `timeout_ms` fires before any byte arrives, or (b) once at least one byte has arrived, `inter_byte_ms` of silence elapses. Return whatever was collected.
- `expected_response_bytes=N`: read until either (a) `N` bytes collected, (b) `timeout_ms` fires before any byte arrives, or (c) once at least one byte has arrived, `inter_byte_ms` of silence elapses (device hung mid-frame). Return whatever was collected — partial results are not an error.

### 4.5 Send-command handler sequence

1. Look up device by `{id}` in registry → **404** if unknown.
2. Try-lock device mutex → **409** `{"error":"device busy"}` if already held. (No waiting.)
3. Parse + validate query params.
4. Write command bytes (no inter-byte gap; the 10 ms gap is only for the discovery probe).
5. If `wait_for_response=false` → return `200 {"response": []}` immediately.
6. Otherwise read per termination rules above; return collected bytes.
7. **On I/O error during write or read** → reconnect-and-reprobe:
   1. Close current handle.
   2. Re-open the same COM port at 9600/8N1. Failure → **503** `{"error":"device unreachable","detail":"<msg>"}`. Device is left in registry with port closed; the next command will retry.
   3. Drain 200 ms, send probe `[1,2,3,4,0]` (10 ms inter-byte gap), read 4 bytes (1 s overall deadline, 25 ms inter-byte tolerance).
   4. **Probe reply's first byte ≠ stored `type_code`** → close port, **remove device from registry**, return **503** `{"error":"device identity changed","detail":"expected type=10, got type=30"}`. Caller must re-run `/discover`.
   5. Identity matches → retry the original write+read **once**. Any error on retry → **503** `{"error":"device i/o failed","detail":"<msg>"}`.
8. Release mutex.

### 4.6 Uniform error shape

```json
{ "error": "<short_code>", "detail": "<human readable>" }
```

| Status | When |
|---|---|
| 400 | Malformed body, unknown query-param value, byte out of `0..255` |
| 404 | Unknown device id |
| 409 | Discovery already running, or device mutex busy |
| 503 | Device unreachable / i/o failed / identity changed |

## 5. Discovery flow (internal)

Triggered by `POST /discover`. Sequence:

1. **Acquire registry write-lock.** Only one discovery at a time. If another is in progress → return 409. Send-command handlers acquire a registry read-lock during device lookup; while discovery holds the write-lock, new commands park briefly until discovery completes. Commands already past lookup at the moment discovery begins will see their port closed under them and surface as 503 to the caller.
2. **Tear down current devices.** For every device currently in the registry, close its serial port. Drop the map.
3. **Enumerate candidate ports** via `serial.GetPortsList()` from `go.bug.st/serial`. Apply config filter:
   - If `discovery.include` non-empty → intersection of include with enumerated ports (listed-but-missing ports are silently dropped).
   - Else if `discovery.exclude` non-empty → all enumerated minus excluded.
   - Else → everything enumerated.
4. **Probe in parallel** — one goroutine per candidate, no cap. Each goroutine:
   - Open at 9600 / 8N1, no flow control. On open error (locked, missing) → log debug and skip silently.
   - Drain RX buffer: in a loop, read with a short non-blocking deadline and discard whatever arrives, until 200 ms total has elapsed.
   - Write probe `[1, 2, 3, 4, 0]` with **10 ms** sleep between each byte.
   - Read up to 4 bytes with **1 s** overall deadline and **25 ms** inter-byte tolerance.
   - Got fewer than 4 bytes → close the port, return "unknown".
   - Classify by first byte: `10 → pump`, `30 → valve`, `70 → densitometer`. Anything else → close, "unknown".
   - On match: **keep the port open** and return `{port, type, type_code, raw_reply}`.
5. **Collect results**, sort by `(type_code, port)` for determinism, assign IDs `{type}_{n}` starting at `n=1` per type in that sorted order.
6. **Stash** the resulting devices in the registry, set `discovered_at = time.Now().UTC()`.
7. Release lock, return JSON.

## 6. Logging

Single global logger using stdlib `log/slog` with a JSON handler to stdout. Level from `log.level` config.

Required log lines:

- Startup banner with version and config summary (info).
- Every chisel state change: connecting / connected / disconnected / retry (info).
- Every discovery run: per-port outcome (debug), summary line (info).
- Every send-command: id, byte counts, duration, outcome (info); full byte arrays (debug).
- Every reconnect attempt and identity-mismatch event (warn).

## 7. Testing

Unit tests, no live hardware:

- `internal/config`: scaffold round-trip; validation positive and negative cases.
- `internal/discovery`: classification table — given a fake port reply, asserts type and ID assignment. Production code uses `go.bug.st/serial`; tests inject a small fake implementing an interface (`io.ReadWriter` + `Drain()` + `Close()`).
- `internal/api`: handlers driven via `httptest.NewServer`, registry populated with fake-port devices to exercise success / 404 / 409 / 503 / reconnect-success / identity-mismatch paths.

No live serial integration tests in CI. Real-hardware verification is manual on the lab machine.

Coverage target: ≥80% on `discovery`, `api`, `config`. The `chisel` wrapper is thin enough to skip.

## 8. Build (Taskfile)

```yaml
# Taskfile.yaml
version: '3'

vars:
  GOOS: '{{.GOOS | default "windows"}}'
  GOARCH: '{{.GOARCH | default "amd64"}}'
  OUTPUT_DIR: dist
  BINARY_NAME: 'lab_devices_client{{if eq .GOOS "windows"}}.exe{{end}}'

tasks:
  build:
    desc: Build the binary (override target via GOOS=... GOARCH=...)
    cmds:
      - mkdir -p {{.OUTPUT_DIR}}
      - GOOS={{.GOOS}} GOARCH={{.GOARCH}} go build -ldflags="-s -w" -o {{.OUTPUT_DIR}}/{{.BINARY_NAME}} ./cmd/lab_devices_client

  test:
    cmds:
      - go test ./...

  tidy:
    cmds:
      - go mod tidy
```

Default `task build` produces `dist/lab_devices_client.exe` for windows/amd64.
Override target: `task build GOOS=linux GOARCH=arm64`.

## 9. Repo layout

```
lab_devices_client/
├── Taskfile.yaml
├── go.mod
├── go.sum
├── README.md
├── cmd/
│   └── lab_devices_client/
│       └── main.go
├── internal/
│   ├── config/
│   ├── chisel/
│   ├── serial/
│   ├── discovery/
│   ├── registry/
│   └── api/
└── docs/
    └── superpowers/
        └── specs/
            └── 2026-04-26-lab-devices-client-design.md
```
