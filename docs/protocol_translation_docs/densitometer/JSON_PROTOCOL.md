# Densitometer — JSON Protocol v1.0

High-level JSON-over-HTTP protocol for the cell-density detector (replaces the legacy 5-byte binary protocol, see `PROTOCOL.md`). Transport details (framing, transport-level retries) are out of scope — every exchange is one JSON request and one JSON response.

## 1. Transport

* All commands: `POST /api/v1` with `Content-Type: application/json`; the body is the request envelope, the response body is the response envelope.
* One command is processed at a time; the device is single-client.

## 2. Envelope (shared across all devices)

**Request:**

```json
{
  "id": "c9f3a2e0-...",        // client-generated UUID, echoed back
  "cmd": "measure",
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
    "message": "target_c must be between 20 and 45",
    "details": { "param": "target_c", "value": 60 }
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
| `not_calibrated` | Operation requires a calibration that hasn't been done |
| `hardware_error` | Sensor/actuator fault (`details.component`) |
| `internal_error` | Firmware bug / unexpected state |

### Job model (long-running operations)

Commands that take noticeable time (measurements, sweeps) return a **job** immediately and complete asynchronously:

```json
{
  "job_id": "j-7f21",
  "state": "running",           // running | succeeded | failed | cancelled
  "progress": 0.35,             // 0.0–1.0
  "estimated_duration_s": 8.0,
  "result": null,               // populated when state == "succeeded"
  "error": null                 // populated when state == "failed"
}
```

* Poll with `get_job`; the active/last job is also embedded in `status`.
* Only **one job at a time**; starting another returns `busy`. `stop` cancels the active job.
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
  "device_type": "densitometer",
  "model": "TDS909A-wide",
  "serial": "",
  "firmware_version": "2.0.0",
  "protocol_version": "1.0",
  "capabilities": {
    "wavelength_nm": 600,
    "brightness_levels": 20,
    "thermostat": { "min_c": 20.0, "max_c": 45.0 },
    "temperature_sensor": "DS18B20"
  }
}
```

`serial` is a best-effort read of the serial-number frame at attach: no
tested densitometer answers it, so in practice it is always empty and
devices are identified by their port instead. A unit whose firmware does
implement the frame would still report a real serial here.

### `status`

```json
// → {"cmd": "status"}
// ← result:
{
  "state": "idle",                        // idle | measuring | monitoring
  "job": null,                            // active job object, if any
  "temperature_c": 36.98,
  "thermostat": {
    "enabled": true,
    "target_c": 37.0,
    "heating": true,
    "cooling": false
  },
  "calibration": {
    "blank": {                            // null if no blank measured
      "slope": 123.45,
      "temperature_c": 36.90,
      "age_s": 754
    },
    "tube_correction": 1.03
  },
  "last_measurement": {                   // null until first measurement
    "seq": 42,
    "absorbance": 0.523,
    "temperature_c": 36.98,
    "age_s": 12
  }
}
```

### `get_job`

```json
// → {"cmd": "get_job", "params": {"job_id": "j-7f21"}}
// ← result: <job object>
```

### `stop`

Cancels the active job and/or monitoring mode, turns the LED off. Always succeeds.

```json
// → {"cmd": "stop"}
// ← result: { "state": "idle", "cancelled_job_id": "j-7f21" }
```

## 4. Measurement commands

### `measure_blank`

Measures the reference (blank/medium-only) tube. Runs the full 20-level brightness sweep and stores the resulting slope + temperature as the baseline for absorbance. Persisted across power cycles.

```json
// → {"cmd": "measure_blank"}
// ← result: { "job": { "job_id": "j-01", "state": "running", "estimated_duration_s": 8.0 } }

// get_job after completion → result.result:
{
  "slope": 123.45,
  "temperature_c": 36.90,
  "sweep": [812, 1630, 2447, ...]        // 20 raw intensities, always included for blanks
}
```

Errors: `hardware_error` if the sweep yields no usable points (e.g. no tube inserted / detector saturated).

### `measure`

Single absorbance measurement of the current sample.

```json
// → {"cmd": "measure", "params": {"include_raw": false}}
// ← result: { "job": { "job_id": "j-02", "state": "running", "estimated_duration_s": 8.0 } }

// completed job result:
{
  "absorbance": 0.523,                   // temperature-compensated, tube-corrected
  "absorbance_raw": 0.508,               // before compensation/correction
  "slope": 74.2,
  "blank_slope": 123.45,
  "temperature_c": 36.98,
  "tube_correction": 1.03,
  "seq": 43,
  "raw": null                            // 20-point sweep if include_raw = true
}
```

Errors: `not_calibrated` if no blank has been measured.

### `start_monitoring` / readings buffer

Continuous measurement: the device measures repeatedly and buffers results; the host polls.

```json
// → {"cmd": "start_monitoring", "params": {"interval_s": 30}}
// ← result: { "state": "monitoring", "interval_s": 30 }
```

```json
// → {"cmd": "get_readings", "params": {"since_seq": 40, "limit": 100}}
// ← result:
{
  "readings": [
    { "seq": 41, "uptime_ms": 7205000, "absorbance": 0.498, "temperature_c": 36.95 },
    { "seq": 42, "uptime_ms": 7235000, "absorbance": 0.510, "temperature_c": 36.97 }
  ],
  "dropped": 0                           // readings lost to buffer overflow since since_seq
}
```

* `interval_s` minimum is the sweep duration (~10 s); default 60.
* Ring buffer holds the most recent 64 readings; `dropped > 0` tells the host it polled too slowly.
* `stop` (or `{"cmd": "stop_monitoring"}`) ends the mode; a plain `measure` is rejected with `busy` while monitoring.

## 5. Thermostat commands

### `set_thermostat`

```json
// → {"cmd": "set_thermostat", "params": {"enabled": true, "target_c": 37.0}}
// ← result: { "enabled": true, "target_c": 37.0 }
```

* `target_c` range 20.0–45.0 (`invalid_params` otherwise).
* `{"enabled": false}` turns heater and cooler off; `target_c` may be omitted.
* Setting persists across power cycles.
* Regulation runs autonomously; current heater/cooler activity is visible in `status.thermostat`.

## 6. Calibration commands

### `set_tube_correction`

Sets the absolute tube-calibration factor (multiplier applied to absorbance). Replaces the legacy multiplicative accumulation.

```json
// → {"cmd": "set_tube_correction", "params": {"factor": 1.03}}
// ← result: { "tube_correction": 1.03 }
```

`factor` range 0.5–2.0; `{"factor": 1.0}` resets. Persisted.

### `calibrate_tube`

Computes the correction from a reference sample: measure a tube of known absorbance first, then call:

```json
// → {"cmd": "calibrate_tube", "params": {"reference_absorbance": 0.500}}
// ← result: { "tube_correction": 1.042, "based_on_seq": 43 }
```

Uses the last completed measurement (`invalid_state` via `not_calibrated` if none). Persisted.

## 7. Diagnostics commands

For service use; not needed in normal operation.

### `set_led`

```json
// → {"cmd": "set_led", "params": {"level": 12}}     // 0–20, 0 = off
// ← result: { "level": 12 }
```

### `read_raw`

Raw detector sweep without any computation.

```json
// → {"cmd": "read_raw", "params": {"level": null}}   // null = full 20-level sweep
// ← job; completed result:
{ "intensities": [812, 1630, ...], "levels": [1, 2, ...], "temperature_c": 36.98 }
```

With `"level": n` (1–20), samples only at that brightness and returns a single-element array.

## 8. Typical session

```
identify                                  → densitometer, serial "" (port-identified)
set_thermostat {enabled, target_c: 37}    → ok
status  (poll until temperature settles)
measure_blank                             → job j-01 … poll get_job → slope stored
measure                                   → job j-02 … poll get_job → absorbance 0.523
start_monitoring {interval_s: 60}
get_readings {since_seq: N}  (repeat)
stop
```
