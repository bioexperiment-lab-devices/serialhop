# SerialHop HTTP API reference

`SerialHop` is a Go service running on a lab machine. It controls serial-port lab devices (peristaltic pumps, distribution valves, densitometers) through a high-level JSON command protocol and exposes a REST API. The lab machine sits behind NAT and reaches the rest of the docker-compose network through a chisel reverse tunnel.

This document describes the wire-level behavior of that API.

## Connection

- **Base URL:** `http://chisel:<port>/` — `chisel` is the docker service name; `<port>` is unique per lab machine and is set by the lab machine's local config.
- **Transport:** plain HTTP. No TLS at this layer.
- **Authentication:** none at this layer. Authn/authz is enforced by an upstream service that fronts this URL inside the docker network.
- **Content type:** `application/json` on every request that has a body and on every response.

## Devices

### ID scheme

Every device is identified by an `id` of the form `{type}_{n}`, where:

| `type` | `type_code` | Device |
|---|---|---|
| `pump` | 10 | Peristaltic pump |
| `valve` | 30 | Distribution valve |
| `densitometer` | 70 | Densitometer |

`n` is `1`-based and assigned by the service in the order it discovers ports of that type, sorted by `(type_code, port)`. The same physical device on the same COM port keeps the same `id` across re-discoveries.

The valve's hub `type` is `valve`, but its `identify` block reports `device_type: "distribution_valve"` — the two names differ deliberately (the hub name is the short form used in IDs and routing; `device_type` is the protocol-level name from `JSON_PROTOCOL.md`). The per-device docs directory follows the protocol name: `docs/protocol_translation_docs/distribution_valve/`, not `.../valve/`.

Devices are discovered with `POST /api/v1/discover` and remain available for commanding until the next discovery (which closes every current device session and re-probes), the session is explicitly released via the (non-`/api/v1`) `POST /devices/disconnect` endpoint, or the process restarts. A device that probed successfully but whose driver failed to attach is still listed (`"connected": false`) and is retried in the background — it does not disappear from the list the way a legacy "identity changed" device used to.

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/v1/discover` | Re-probe ports, rebuild every device session, return the new list |
| `GET` | `/api/v1/devices` | Return the cached device list |
| `POST` | `/api/v1/devices/{id}/command` | Execute one JSON protocol command on a device |

### `POST /api/v1/discover`

Closes every current device session (drivers get a chance to persist their state first), re-enumerates candidate COM ports per the service's config (include/exclude lists), and probes each in parallel with the universal identify probe. Waits for each newly-created session's first attach attempt to finish before responding, so the returned list reflects real attach outcomes instead of a transient `connected: false`.

This is **destructive** — any open device sessions are torn down and rebuilt. Run it only when you actually need to re-enumerate hardware, not as a lightweight "give me current state" call (use `GET /api/v1/devices` for that).

- **Request body:** none.
- **Response (200):** JSON, identical shape to `GET /api/v1/devices` (see below).
- **Errors:**
  - `409 Conflict` — another discovery is already running. Body: `{ "error": "discovery in progress" }` (no `detail`).
  - `409 Conflict` — a device has an active job. Body: `{ "error": "job in progress", "detail": "pump_1 has an active job; stop it before re-discovering" }`. Stop the job (`cmd: "stop"`, where supported) before retrying.
  - `500 Internal Server Error` — the service could not enumerate ports. Body: `{ "error": "discovery failed", "detail": "<message>" }`.

### `GET /api/v1/devices`

Return the cached result of the most recent discovery, without re-probing. Cheap and idempotent.

- **Request body:** none.
- **Response (200):**
  ```json
  {
    "devices": [
      {
        "id": "pump_1", "type": "pump", "port": "COM3", "connected": true,
        "identify": {
          "device_type": "pump", "model": "peristaltic-1ch", "serial": "26-025",
          "firmware_version": "legacy", "protocol_version": "1.0", "capabilities": {}
        }
      },
      { "id": "valve_1", "type": "valve", "port": "COM7", "connected": false, "identify": null }
    ],
    "discovered_at": "2026-07-06T12:34:56Z"
  }
  ```
  - `connected` reflects the device session's current attach state, independent of whether it has ever attached before.
  - `identify` is `null` until the device's post-probe attach succeeds at least once; after that it holds the last successful identify block even if the device later goes unreachable (see "Memory-served commands" below).
  - If discovery has never run on the service, the response is `{ "devices": [], "discovered_at": null }`.

### `POST /api/v1/devices/{id}/command`

Execute one command against a device. Body and response are both the **envelope** shared by every device type (`JSON_PROTOCOL.md §2` under `docs/protocol_translation_docs/`):

```json
// request
{ "id": "req-1", "cmd": "dispense", "params": { "volume_ml": 5, "speed_pct": 60 } }
```

```json
// response
{
  "id": "req-1",
  "status": "ok",
  "result": {
    "job_id": "j-7f21",
    "state": "running",
    "progress": 0.35,
    "estimated_duration_s": 200.0,
    "elapsed_s": 70.2,
    "result": null,
    "error": null
  }
}
```

`id` is caller-generated and echoed back verbatim. `cmd` and `params` are device-specific — see `docs/protocol_translation_docs/<device>/JSON_PROTOCOL.md` for the command set, parameter shapes, and result shapes per device type (directory names: `pump`, `distribution_valve`, `densitometer`).

#### HTTP status vs. envelope status

The HTTP status code reflects who decided the outcome, not whether the command "succeeded":

- **200** — the device (or the hub's in-memory job/identify cache) decided the outcome. `status` in the body is `"ok"` or `"error"`; on error, `error.code` is one of `invalid_params`, `busy`, `not_calibrated`, `not_homed`, `hardware_error`, `unknown_command`, `internal_error` (which codes a given device can return is documented per device).
- **404** — unknown `{id}`. Envelope error `unknown_device`.
- **503** — the device is unreachable. Envelope error `device_unreachable`. Exception: `identify` and `get_job` (below).
- **400** — malformed body, or a body missing `id` / `cmd`. Envelope error `invalid_request`.

Worked examples:

- **Device-decided error, still 200** (illustrative — exact commands/messages are per-device):
  ```json
  // request
  { "id": "req-2", "cmd": "dispense", "params": { "volume_ml": -1, "speed_pct": 60 } }
  // response, HTTP 200
  {
    "id": "req-2", "status": "error",
    "error": { "code": "invalid_params", "message": "volume_ml must be positive",
               "details": { "param": "volume_ml", "value": -1 } }
  }
  ```
- **Unknown device, 404:**
  ```json
  // POST /api/v1/devices/pump_9/command  { "id": "req-3", "cmd": "identify" }
  // response, HTTP 404
  { "id": "req-3", "status": "error",
    "error": { "code": "unknown_device", "message": "no device with id pump_9" } }
  ```
- **Device unreachable, 503:**
  ```json
  // response, HTTP 503
  { "id": "req-4", "status": "error",
    "error": { "code": "device_unreachable", "message": "device is not responding" } }
  ```
- **Malformed request, 400** (here: body is missing `"id"`):
  ```json
  // POST /api/v1/devices/pump_1/command  { "cmd": "dispense" }
  // response, HTTP 400
  { "id": "", "status": "error",
    "error": { "code": "invalid_request", "message": "\"id\" and \"cmd\" are required" } }
  ```

#### Memory-served commands: `identify` and `get_job`

Two commands are answered from the hub's in-memory state instead of talking to the device, and so stay at HTTP 200 even while the device is otherwise unreachable:

- **`identify`** returns the cached identify block from the device's last successful attach, regardless of the session's *current* connection state. If no attach has ever succeeded (cache still empty), it returns the normal `device_unreachable` error at 503 — the exception only applies once the cache has been populated at least once.
  ```json
  // device currently disconnected, but attached successfully earlier
  { "id": "req-5", "cmd": "identify" }
  // → HTTP 200
  { "id": "req-5", "status": "ok",
    "result": { "device_type": "pump", "model": "peristaltic-1ch", "serial": "26-025",
                "firmware_version": "legacy", "protocol_version": "1.0", "capabilities": {} } }
  ```
- **`get_job`** looks up a `job_id` in the active job or the last-8 history ring and returns whatever it finds — including a job that just failed with `hardware_error` because the device went unreachable mid-job. It never checks the session's connection state.
  ```json
  { "id": "req-6", "cmd": "get_job", "params": { "job_id": "j-3" } }
  // → HTTP 200, even though the device is currently unreachable
  { "id": "req-6", "status": "ok",
    "result": { "job_id": "j-3", "state": "failed", "progress": 0.62,
                "estimated_duration_s": 200.0, "elapsed_s": 124.0, "result": null,
                "error": { "code": "hardware_error", "message": "…" } } }
  ```
  `params.job_id` is required; a missing or unrecognized `job_id` returns the ordinary `invalid_params` device-decided error (still HTTP 200).

## Job model

Long-running operations (dispense, calibration, a valve move, …) return a **job** immediately and complete asynchronously. Poll it with `get_job`:

```json
{
  "job_id": "j-7f21",
  "state": "running",           // running | paused | succeeded | failed | cancelled
  "progress": 0.35,             // 0.0–1.0; only a verified completion reaches 1.0
  "estimated_duration_s": 200.0,
  "elapsed_s": 70.2,
  "result": null,               // populated when state == "succeeded"
  "error": null                 // populated when state == "failed"
}
```

- A session runs **at most one active job**; starting another while one is active returns the device-decided `busy` error (details include the running job's `job_id`).
- The hub retains the **last 8 completed jobs** per session, newest first, for `get_job` lookups after the job finishes. Older history is dropped.
- Job semantics that are per-device (which commands start jobs, `pause`/`stop` behavior, whether a device also has continuous non-job "state" concepts) are documented in that device's `JSON_PROTOCOL.md`.

## Per-device commands

This document only covers the transport-level envelope, HTTP status mapping, discovery, and job polling — all of which are shared across device types. For the actual command set, parameters, results, and error codes of a specific device, see:

- `docs/protocol_translation_docs/pump/JSON_PROTOCOL.md`
- `docs/protocol_translation_docs/distribution_valve/JSON_PROTOCOL.md`
- `docs/protocol_translation_docs/densitometer/JSON_PROTOCOL.md`

## Raw serial attach

### `GET /serial/ports/{port}/attach`

A separate, non-`/api/v1` endpoint that upgrades to a **WebSocket** and gives a caller direct byte-level access to one serial port — no envelope, no `id`/`cmd`, no job model. It exists for what the JSON device protocol above can't do: bring-up of hardware with no driver yet, firmware/bootloader work (DTR reset, baud switching), and ad-hoc interactive pyserial scripting. It only ever operates on ports with **no discovered device** on them.

Off by default: the whole endpoint is disabled unless the service's config has `raw_serial.enabled: true`.

- **Path param:** `{port}` — a COM port name (e.g. `COM7`), matched against the live port list (the same list behind `GET /serial/ports/detailed`).
- **Query param:** `baud` (int, default `9600`, must be in `1..4000000` if given). There is no `post_open_settle_ms` query param in v1 — unlike the old raw-byte device API, the port is opened at attach time with no configurable settle delay.

#### Pre-upgrade gate

Before the WebSocket handshake, the server runs these checks in order; the first failure wins and the response is a normal HTTP error (the connection never upgrades):

| Order | Condition | Status | Error body |
|---|---|---|---|
| 1 | `raw_serial.enabled` is `false` | 403 | `{"error":"raw serial disabled","detail":"set raw_serial.enabled: true in config"}` |
| 2 | `baud` query param given but not an integer in `1..4000000` | 400 | `{"error":"invalid query param","detail":"<message>"}` |
| 3 | `{port}` is not in the live port list | 404 | `{"error":"port not found","detail":"<port>"}` |
| 4 | `{port}` is owned by a discovered device | 409 | `{"error":"port has discovered device","detail":"owned by <id>"}` |
| 5 | a discovery pass is currently running | 409 | `{"error":"discovery in progress","detail":""}` |
| 6 | another raw session already holds the lease on `{port}` | 409 | `{"error":"port already attached","detail":""}` |

(If the server fails to enumerate ports at all — an internal error, not a normal gate outcome — it returns `500` with `{"error":"list ports failed","detail":"<message>"}` between checks 2 and 3.)

Only once all checks pass does the server take an exclusive lease on the port, complete the WebSocket upgrade, and open it at the requested baud. The lease is released the instant the WebSocket closes, for any reason.

#### Frame protocol

Once upgraded, two frame kinds share the same connection:

- **Binary WebSocket frames** carry raw serial bytes in both directions — a binary frame from the client is written to the port verbatim; bytes read off the port are pushed to the client as binary frames as they arrive.
- **Text WebSocket frames** carry JSON control messages in both directions.

Client → server control ops (`{"op": "...", ...}`):

| `op` | Fields | Effect |
|---|---|---|
| `set_baud` | `baud` (int) | Change the port's baud rate |
| `set_dtr` | `level` (bool) | Set the DTR line |
| `set_rts` | `level` (bool) | Set the RTS line |
| `send_break` | `ms` (int) | Assert a break condition for `ms` milliseconds |
| `drain` | — | Block until pending output has flushed |
| `get_modem` | — | Request current modem status; answered with a `modem` frame |

Server → client frames:

| `op` | Fields | Sent |
|---|---|---|
| `ready` | `port`, `baud` | Once, immediately after a successful open |
| `modem` | `cts`, `dsr`, `ri`, `cd` (bool) | In reply to a `get_modem` request |
| `error` | `detail` | On a malformed control frame, an unrecognized `op` (`"unknown op: <op>"`), or a serial-port write/control failure |

The boolean fields on `modem` are marshaled with `omitempty`, so a `false` line state may be **absent from the JSON object** rather than present with value `false` — treat a missing key as `false`. An unrecognized `op` gets an `error` reply; the connection is **not** closed for this.

#### Session lifetime

- At most one raw session per port at a time (gate check 6 above); discovery also skips any port currently under a raw lease, and a raw attach is refused while discovery is running (gate check 5).
- `raw_serial.idle_timeout_ms` (default `900000` ms / 15 min) closes the session (WebSocket close code 1001, "going away") if neither direction has carried traffic for that long. `0` disables the idle timeout.
- If the underlying serial port itself dies (e.g. device unplugged), the session tears down immediately rather than waiting for the client to notice.

#### Reference client

[`clients/`](../clients/) has a small, documented Python bridge (`serialhop_attach.py`) that exposes this endpoint as a local `rfc2217://` URL, so pyserial code can talk to it with `serial.serial_for_url("rfc2217://127.0.0.1:<port>")`. It translates pyserial's `baudrate` / `dtr` / `rts` / `send_break` operations into the control ops above and streams everything else as binary frames. See `clients/README.md` for setup and the JupyterLab-only reachability note.
