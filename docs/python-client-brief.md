# lab_devices_client HTTP API reference

`lab_devices_client` is a Go service running on a lab machine. It controls serial-port lab devices and exposes a REST API. The lab machine sits behind NAT and reaches the rest of the docker-compose network through a chisel reverse tunnel.

This document describes the wire-level behavior of that API.

## Connection

- **Base URL:** `http://chisel:<port>/` — `chisel` is the docker service name; `<port>` is unique per lab machine and is set by the lab machine's local config.
- **Transport:** plain HTTP. No TLS at this layer.
- **Authentication:** none at this layer. Authn/authz is enforced by an upstream service that fronts this URL inside the docker network.
- **Content type:** `application/json` on every request that has a body and on every response.

## Devices

Every device is identified by an `id` of the form `{type}_{n}`, where:

| `type` | `type_code` | Device |
|---|---|---|
| `pump` | 10 | Peristaltic pump |
| `valve` | 30 | Distribution valve |
| `densitometer` | 70 | Densitometer |

`n` is `1`-based and assigned by the service in the order it discovers ports of that type, sorted by `(type_code, port)`. The same physical device on the same COM port keeps the same `id` across re-discoveries.

Devices are discovered with `POST /discover` and remain available for commanding until the next discovery (which closes all current ports and re-probes) or until the device's identity changes on the wire (which removes it from the registry — see the 503 section).

## Endpoints

### `POST /discover`

Run a fresh discovery pass. The service closes every currently-open serial port, enumerates candidate COM ports per its config (include/exclude lists), and probes each in parallel with a 5-byte universal probe (`[1, 2, 3, 4, 0]`). Devices that reply with a known type byte are registered; others are dropped.

This is **destructive** — any open device connections owned by the service are closed. Pending commands against those devices fail with 503.

- **Request body:** none.
- **Query parameters:** none.
- **Response (200):** JSON, identical shape to `GET /devices`:
  ```json
  {
    "devices": [
      {"id": "pump_1",         "type": "pump",         "type_code": 10, "port": "COM3"},
      {"id": "valve_1",        "type": "valve",        "type_code": 30, "port": "COM4"},
      {"id": "densitometer_1", "type": "densitometer", "type_code": 70, "port": "COM7"}
    ],
    "discovered_at": "2026-04-26T12:34:56Z"
  }
  ```
  `discovered_at` is RFC 3339 / ISO 8601 in UTC.
- **Errors:**
  - `409 Conflict` — another discovery is already running. Body: `{"error":"discovery in progress","detail":""}`. Discovery typically takes 1–3 seconds.
  - `500 Internal Server Error` — the service could not enumerate ports. Body: `{"error":"discovery failed","detail":"<message>"}`.

### `GET /devices`

Return the cached result of the most recent discovery, without re-probing. Cheap and idempotent.

- **Request body:** none.
- **Query parameters:** none.
- **Response (200):**
  ```json
  {
    "devices": [ {...}, {...} ],
    "discovered_at": "2026-04-26T12:34:56Z"
  }
  ```
  If discovery has never run on the service, the response is:
  ```json
  { "devices": [], "discovered_at": null }
  ```
  `discovered_at` may be `null`.

### `POST /devices/{id}/command`

Send a sequence of raw bytes to a discovered device and (optionally) read its reply.

- **Path parameter:** `{id}` is one of the device IDs returned by `/discover` or `/devices` (e.g. `pump_1`).
- **Request body:**
  ```json
  { "command": [1, 2, 3, 4, 0] }
  ```
  `command` is a non-empty list of integers, each in `0..255`. Out-of-range values, non-integers, or an empty list → 400.
- **Query parameters** (all optional):

  | Param | Default when `expected_response_bytes=-1` | Default when `expected_response_bytes>0` | Range | Meaning |
  |---|---|---|---|---|
  | `timeout_ms` | `100` | `1000` | `1..60000` | Max wait (ms) for the **first** response byte |
  | `inter_byte_ms` | `25` | `50` | `1..1000` | Inter-byte silence (ms) that ends the read |
  | `wait_for_response` | `true` | `true` | `true` / `false` | If `false`, write and return immediately with empty `response`; other read params are ignored |
  | `expected_response_bytes` | `-1` | `-1` | `-1` or `1..1024` | If `>0`, stop reading as soon as that many bytes are collected |

  The defaults are context-dependent: when the caller supplies `expected_response_bytes` (i.e. they know how long the reply is), the service waits longer for the first byte and tolerates longer inter-byte gaps. When the caller doesn't supply it, the service prefers to fail fast.

- **Response (200):**
  ```json
  { "response": [10, 1, 2, 3] }
  ```
  `response` is always present and always a list of integers in `0..255`. It is `[]` when the device stayed silent within the configured timeout, or when `wait_for_response=false`.

#### Read-termination rules (when `wait_for_response=true`)

- `expected_response_bytes=-1` (default): the read ends when either
  - `timeout_ms` elapses before any byte arrives, or
  - once at least one byte has arrived, `inter_byte_ms` of silence elapses.

  Whatever has been collected is returned.
- `expected_response_bytes=N`: the read ends when any of
  - `N` bytes have been collected,
  - `timeout_ms` fires before any byte arrives,
  - once at least one byte has arrived, `inter_byte_ms` of silence elapses.

  Partial results (`len(response) < N`) are returned without an error status.

#### Per-device concurrency

Each device has a per-device mutex. Two simultaneous commands against the same device cannot both proceed — the second gets **409**, never queues. Different devices are independent and may be commanded in parallel.

#### Internal reconnect-and-reprobe

If the service detects an I/O error while writing or reading the device's serial port, it transparently:

1. Closes the current handle and re-opens the same COM port at 9600 / 8N1.
2. Sends the universal probe `[1, 2, 3, 4, 0]` and reads 4 bytes.
3. Verifies the first byte equals the device's stored `type_code`.
4. Retries the original write+read once.

The outcomes visible to the caller:

- **Reconnect succeeds and identity matches** → the original command completes and the caller gets a normal `200` response. The reconnect is invisible.
- **Re-open fails** → `503 device unreachable`. The device stays in the registry; the next command will retry.
- **Re-probe returns a different `type_code` (or no reply)** → `503 device identity changed`. The device is **removed from the registry**. Subsequent commands to this `id` return `404` until `/discover` is run again.

## Error response shape

All error responses share this body shape:

```json
{ "error": "<short_code>", "detail": "<human readable string, may be empty>" }
```

| Status | `error` codes |
|---|---|
| 400 | `invalid request body`, `invalid query param` |
| 404 | `device not found` |
| 409 | `discovery in progress`, `device busy` |
| 500 | `discovery failed` |
| 503 | `device unreachable`, `device i/o failed`, `device identity changed` |

`device busy` (409) means another caller currently holds the device's mutex. `device identity changed` (503) means the device was removed from the registry; further calls to the same `id` return `404` until `/discover` runs again.

## Byte encoding

Bytes are always represented as JSON **integers** in `0..255`, both inbound (`command`) and outbound (`response`). No base64, no hex strings.

## Request duration

The service holds the HTTP connection open while it talks to the device, so a request's wall-clock duration tracks the configured read timeouts:

- `POST /devices/{id}/command` — at most `timeout_ms + inter_byte_ms × len(response)` milliseconds, plus a small constant for write + framing. With defaults this is well under 1 second; with overrides it can be up to ~60 s.
- `POST /discover` — bounded by 200 ms drain + 50 ms probe writes + 1 s read deadline per port; ports are probed in parallel, so the total is roughly 1.3 s regardless of port count, plus a small constant.
- `GET /devices` — sub-millisecond; serves cached state.
