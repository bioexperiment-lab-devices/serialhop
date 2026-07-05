# Distribution Valve (Radial Switch) — JSON Protocol v1.0

High-level JSON-over-HTTP protocol for the radial flow-distribution valve (replaces the legacy 5-byte binary protocol, see `PROTOCOL.md`). Transport details are out of scope — every exchange is one JSON request and one JSON response.

The valve routes flow to one of N outputs (N = 6 or 2 depending on the build); position `0` means all outputs closed, positions `1..N` open the corresponding output.

## 1. Transport

* All commands: `POST /api/v1` with `Content-Type: application/json`; the body is the request envelope, the response body is the response envelope.
* One command is processed at a time; the device is single-client.

## 2. Envelope (shared across all devices)

**Request:**

```json
{
  "id": "c9f3a2e0-...",        // client-generated UUID, echoed back
  "cmd": "set_position",
  "params": { }                 // optional, command-specific
}
```

**Success response:**

```json
{
  "id": "c9f3a2e0-...",
  "status": "ok",
  "result": { }
}
```

**Error response:**

```json
{
  "id": "c9f3a2e0-...",
  "status": "error",
  "error": {
    "code": "invalid_params",
    "message": "position must be between 0 and 6",
    "details": { "param": "position", "value": 9 }
  }
}
```

### Shared error codes

| Code | Meaning |
|---|---|
| `invalid_request` | Body is not valid JSON or missing `cmd` |
| `unknown_command` | `cmd` not recognized |
| `invalid_params` | Missing/out-of-range parameter (`details` says which) |
| `busy` | A move is in progress and the command conflicts with it |
| `not_homed` | Position commanded while the current position is unknown |
| `hardware_error` | Motor/driver fault |
| `internal_error` | Firmware bug / unexpected state |

### Job model (long-running operations)

Moves return a **job** immediately and complete asynchronously:

```json
{
  "job_id": "j-7f21",
  "state": "running",           // running | succeeded | failed | cancelled
  "progress": 0.6,
  "estimated_duration_s": 1.8,
  "result": null,
  "error": null
}
```

* Poll with `get_job`; the active/last job is also embedded in `status`.
* Only **one job at a time**; a `set_position` during a move returns `busy` (no mid-move retargeting — position bookkeeping must stay exact).
* The device retains the last 8 completed jobs for polling.

## 3. Common commands

### `ping`

```json
// → {"cmd": "ping"}
// ← result:
{ "uptime_ms": 8123456 }
```

### `identify`

```json
// → {"cmd": "identify"}
// ← result:
{
  "device_type": "distribution_valve",
  "model": "radial-6",
  "serial": "26-003",
  "firmware_version": "2.0.0",
  "protocol_version": "1.0",
  "capabilities": {
    "positions": 6,               // outputs; valid positions are 0..positions
    "rotation_modes": ["shortest", "direct", "wrap"],
    "seconds_per_position": 0.9
  }
}
```

### `status`

```json
// → {"cmd": "status"}
// ← result:
{
  "state": "idle",                // idle | moving | unhomed
  "homed": true,
  "position": 4,                  // null while moving or unhomed
  "target_position": null,        // set while moving
  "job": null,                    // active job object while moving
  "config": {
    "default_rotation": "shortest",
    "hold_torque": false
  }
}
```

`position` is reported only when the valve is physically settled at it — never the target of an in-flight move.

### `get_job`

```json
// → {"cmd": "get_job", "params": {"job_id": "j-7f21"}}
// ← result: <job object>
```

### `stop`

Aborts an in-flight move immediately. Because the rotor stops between detents, the position becomes **unknown**: the device enters `unhomed` and requires `home` before the next `set_position`.

```json
// → {"cmd": "stop"}
// ← result: { "state": "unhomed", "cancelled_job_id": "j-7f21" }
```

When idle, `stop` is a no-op (`{"state": "idle"}`).

Implementations on hardware that cannot physically abort a move MAY instead let the (short) move finish and return `{"state": "idle"}` with the position preserved — see the translation layer's documented deviation in `TRANSLATION.md`.

## 4. Positioning commands

### `home`

Declares the current physical position. The valve has no position sensor, so after power-up (`state = "unhomed"`, `position = null`) the operator must visually verify or manually set the rotor, then report it:

```json
// → {"cmd": "home", "params": {"position": 0}}
// ← result: { "homed": true, "position": 0 }
```

* `position` 0..N (`invalid_params` otherwise).
* Rejected with `busy` during a move.
* Any `set_position` before homing fails with `not_homed`.

### `set_position`

Rotates to the requested position.

```json
// → {"cmd": "set_position", "params": {"position": 4, "rotation": "shortest"}}
//    rotation optional: "shortest" | "direct" | "wrap"; default = config.default_rotation
// ← result:
{ "job": { "job_id": "j-7f21", "state": "running", "estimated_duration_s": 1.8 } }

// completed job result:
{ "position": 4, "from_position": 1, "direction": "increasing", "duration_s": 1.82 }
```

`direction` in the result is `"increasing"` or `"decreasing"`, relative to the position numbering.

Rotation modes (they matter because every port the rotor transits is momentarily opened):

| Mode | Path taken |
|---|---|
| `shortest` | Whichever arc is shorter (default) |
| `direct` | Through intermediate positions in numeric order — never across the 0↔6 boundary |
| `wrap` | Always the complementary arc — across the 0↔6 boundary |

* `position` 0..N; requesting the current position succeeds instantly with a completed job.
* Job `state = "succeeded"` is reported only after the motion has physically finished (and the motor is de-energized unless `hold_torque` is on).
* Errors: `not_homed`, `busy`, `invalid_params`, `hardware_error`.

## 5. Configuration

### `configure`

```json
// → {"cmd": "configure", "params": {"default_rotation": "shortest", "hold_torque": false}}
// ← result: { "default_rotation": "shortest", "hold_torque": false }
```

| Field | Values | Meaning |
|---|---|---|
| `default_rotation` | `"shortest"` \| `"direct"` \| `"wrap"` | Path strategy used when `set_position` omits `rotation` (see the mode table in §4). Default `"shortest"` |
| `hold_torque` | bool | `true` = keep the stepper coil energized after a move (resists back-pressure, consumes power/heats); `false` = release after each move. Default `false` |

Fields are optional; omitted ones are unchanged. Settings persist across power cycles.

## 6. Typical session

```
identify                          → valve, 6 positions
status                            → state "unhomed"  (fresh power-up)
home {position: 0}                → homed at 0
set_position {position: 4}        → job j-01 … poll get_job → succeeded, position 4
(run pump through output 4)
set_position {position: 0}        → all closed
```

## 7. Design notes

* **Homing is explicit** because the hardware has no encoder or end-switch: the protocol refuses to guess. The legacy firmware silently assumed position 0 at boot; here that assumption is surfaced as the `unhomed` state, and any interrupted move (`stop`, power loss mid-move, driver fault) also drops back to `unhomed` rather than reporting a possibly-wrong position.
* If a future hardware revision adds a home sensor, `home` gains a parameterless form (`{"cmd": "home"}`) that performs a physical homing move; the rest of the protocol is unchanged.
