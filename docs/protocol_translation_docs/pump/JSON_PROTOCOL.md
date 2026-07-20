# Peristaltic Pump — JSON Protocol v1.0

High-level JSON-over-HTTP protocol for the peristaltic pump (replaces the legacy 5-byte binary protocol, see `PROTOCOL.md`). Transport details are out of scope — every exchange is one JSON request and one JSON response. All quantities are in physical units (ml, ml/min, seconds); the device converts to motor steps internally using its stored calibration.

## 1. Transport

* All commands: `POST /api/v1` with `Content-Type: application/json`; the body is the request envelope, the response body is the response envelope.
* One command is processed at a time; the device is single-client.

## 2. Envelope (shared across all devices)

**Request:**

```json
{
  "id": "c9f3a2e0-...",        // client-generated UUID, echoed back
  "cmd": "dispense",
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
    "message": "volume_ml must be positive",
    "details": { "param": "volume_ml", "value": -1 }
  }
}
```

### Shared error codes

| Code | Meaning |
|---|---|
| `invalid_request` | Body is not valid JSON or missing `cmd` |
| `unknown_command` | `cmd` not recognized |
| `invalid_params` | Missing/out-of-range parameter (`details` says which) |
| `busy` | A job is running and the command conflicts with it |
| `not_calibrated` | Volume/speed requested but no volume calibration stored |
| `hardware_error` | Motor/driver fault |
| `internal_error` | Firmware bug / unexpected state |

### Job model (long-running operations)

Finite operations (dispense, calibration run) return a **job** immediately and complete asynchronously:

```json
{
  "job_id": "j-7f21",
  "state": "running",           // running | paused | succeeded | failed | cancelled
  "progress": 0.35,             // 0.0–1.0
  "estimated_duration_s": 200.0,
  "elapsed_s": 70.2,
  "result": null,               // populated when state == "succeeded"
  "error": null                 // populated when state == "failed"
}
```

* Poll with `get_job`; the active/last job is also embedded in `status`.
* Only **one job at a time**; starting another returns `busy`. `stop` cancels the active job.
* The device retains the last 8 completed jobs for polling.

Unbounded operation (continuous rotation) is modeled as a **state**, not a job.

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
  "device_type": "pump",
  "model": "peristaltic-1ch",
  "serial": "",
  "firmware_version": "2.0.0",
  "protocol_version": "1.0",
  "capabilities": {
    "channels": 1,
    "speed_ml_min": { "min": 0.05, "max": 40.0 },
    "supports_gradient": true,
    "supports_drop_suckback": true
  }
}
```

`serial` is always empty for pumps: the firmware has no serial-number command.
Devices are identified by their port. Calibration lives in the pump's own
EEPROM, is re-read on every connection, and is trusted — there is no
`calibration_unverified` flag and no host-side calibration store.
`set_at_uptime_ms` is session-scoped and always present (no `omitempty`): it
reports the connection-relative time when calibration was set during the
current connection, and reads `0` when the calibration predates this
connection (e.g. it was set on a previous connection and just re-read from
EEPROM at attach).

### `status`

```json
// → {"cmd": "status"}
// ← result:
{
  "state": "dispensing",          // idle | rotating | dispensing | calibrating | paused
  "job": {                        // null when idle/rotating
    "job_id": "j-7f21",
    "state": "running",
    "progress": 0.42,
    "estimated_duration_s": 200.0,
    "elapsed_s": 84.1
  },
  "direction": "forward",         // null when idle
  "speed_ml_min": 3.0,            // current instantaneous speed, null when idle
  "dispensed_ml": 4.2,            // volume moved in the current/last job
  "calibration": {                // null if never calibrated
    "ml_per_step": 0.000424,
    "set_at_uptime_ms": 120000
  }
}
```

### `get_job`

```json
// → {"cmd": "get_job", "params": {"job_id": "j-7f21"}}
// ← result: <job object>
```

### `stop`

Stops the motor immediately: cancels any job, ends continuous rotation. Always succeeds.

```json
// → {"cmd": "stop"}
// ← result: { "state": "idle", "cancelled_job_id": "j-7f21", "dispensed_ml": 4.2 }
```

## 4. Motion commands

Direction values everywhere: `"forward"` | `"reverse"` (relative to the labeled flow direction of the pump head).

### `rotate` — run continuously

```json
// → {"cmd": "rotate", "params": {"direction": "forward", "speed_ml_min": 3.0}}
// ← result: { "state": "rotating", "direction": "forward", "speed_ml_min": 3.0 }
```

* Runs until `stop` (or a new `rotate`, which retargets direction/speed on the fly).
* Requires volume calibration (`not_calibrated` otherwise) since speed is in ml/min. For uncalibrated bring-up use `rotate_raw` (§6).

### `dispense` — pump a metered volume

```json
// → {
//   "cmd": "dispense",
//   "params": {
//     "direction": "forward",
//     "volume_ml": 10.0,
//     "speed_ml_min": 3.0,
//     "drop_suckback_ml": 0.05,          // optional, default 0 (off)
//     "speed_profile": {                 // optional; overrides speed_ml_min sweep
//       "start_ml_min": 0.5,
//       "end_ml_min": 5.0,
//       "shape": "linear"                // linear | exponential
//     }
//   }
// }
// ← result:
{ "job": { "job_id": "j-7f21", "state": "running", "estimated_duration_s": 200.0 } }

// completed job result:
{
  "dispensed_ml": 10.0,
  "duration_s": 199.4,
  "mean_speed_ml_min": 3.01,
  "suckback_ml": 0.05
}
```

* `drop_suckback_ml`: after delivering the full volume the pump reverses by this amount to retract the hanging drop at the outlet. The delivered volume is still `volume_ml`.
* `speed_profile` produces a flow-rate gradient across the volume (replaces the legacy Grad 12/21 modes: an increasing gradient is `start < end`, decreasing is `start > end`).
* Errors: `not_calibrated`, `invalid_params` (speed/volume out of range), `busy`.

### `pause` / `resume`

```json
// → {"cmd": "pause"}
// ← result: { "state": "paused", "job_id": "j-7f21", "dispensed_ml": 4.2 }

// → {"cmd": "resume"}
// ← result: { "state": "dispensing", "job_id": "j-7f21" }
```

Pausing keeps the job and its remaining volume; `stop` while paused cancels it. `pause` with no active motion → `busy` error with `details.state = "idle"`.

## 5. Calibration

Volume calibration maps motor steps to ml. Two-phase flow: run a fixed cycle, weigh the output, report it back.

### `start_calibration`

```json
// → {"cmd": "start_calibration", "params": {"speed_pct": 50}}   // speed_pct optional, default 50
// ← result: { "job": { "job_id": "j-c1", "state": "running", "estimated_duration_s": 120.0 } }

// completed job result:
{ "steps": 48000, "duration_s": 118.7 }
```

Runs a fixed internal number of steps forward. Collect and weigh/measure the pumped liquid.

### `set_calibration`

```json
// → {"cmd": "set_calibration", "params": {"job_id": "j-c1", "measured_volume_ml": 20.35}}
// ← result: { "ml_per_step": 0.000424 }
```

The device computes `ml_per_step = measured_volume_ml / steps` from the referenced calibration job and persists it.

Direct restore (e.g. from a host database) and readback:

```json
// → {"cmd": "set_calibration", "params": {"ml_per_step": 0.000424}}
// → {"cmd": "get_calibration"}   ← result: { "ml_per_step": 0.000424, "set_at_uptime_ms": 120000 }
```

## 6. Diagnostics commands

### `rotate_raw`

Calibration-independent rotation for bring-up and priming: speed as a percentage of the motor's maximum step rate.

```json
// → {"cmd": "rotate_raw", "params": {"direction": "forward", "speed_pct": 25}}
// ← result: { "state": "rotating", "speed_pct": 25 }
```

## 7. Typical sessions

**Calibration:**

```
identify                          → pump, serial "" (port-identified), no calibration
start_calibration                 → job j-c1 … poll get_job → 48000 steps
(weigh output: 20.35 ml)
set_calibration {job_id, 20.35}   → ml_per_step stored
```

**Dispensing:**

```
identify
dispense {forward, 10 ml, 3 ml/min, suckback 0.05}   → job j-7f21
status  (poll: progress, dispensed_ml)                → … succeeded, 10.0 ml in 199 s
```

**Gradient feed:**

```
dispense {forward, 50 ml, speed_profile {0.5 → 5.0 ml/min, linear}}
```
