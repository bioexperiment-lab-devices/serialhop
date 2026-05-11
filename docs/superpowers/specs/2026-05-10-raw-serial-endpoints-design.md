# Raw Serial Port Endpoints — Design

**Date:** 2026-05-10
**Status:** Approved (brainstorming complete; pending spec review before plan)

## 1. Purpose & scope

Two new HTTP endpoints that let an operator enumerate serial ports on the lab machine and send raw bytes to ports that don't have a classified device. Use case: diagnosing unknown hardware on the lab machine — the operator suspects a device is on a port but discovery doesn't classify it, and wants to manually send bytes at 9600/8N1 and see what comes back.

Both endpoints are gated behind a config flag that defaults to **off** because they bypass the device-classification model. No baud-rate / parity tunability (fixed at 9600/8N1, same as discovery). No persistent open — each request opens, talks, closes.

Out of scope (deliberately YAGNI):

- Caller-controlled baud rate, parity, stop bits, flow control.
- Persistent port handles with separate `/open` and `/close` endpoints.
- Reconnect / re-probe on I/O error (no identity to verify; just bubble the error up).
- Talking raw to a port that already belongs to a discovered device.
- Filtering the listing by `discovery.include` / `discovery.exclude` (those affect `/discover` only).

## 2. Configuration

New top-level section in `SerialHop_config.yaml`:

```yaml
raw_serial:
  enabled: false   # allow GET /serial/ports and POST /serial/ports/{port}/command.
                   # bypasses device classification — leave off unless diagnosing.
```

- `Default()` returns `RawSerial.Enabled = false`.
- Validation: nothing extra — bool field; any value is structurally fine.
- Scaffold (`config.WriteScaffold`) gets the new section with the comment above.
- Panel renders one line: `Raw serial:       enabled` / `disabled`, between the existing `Discovery:` and `Log level:` rows.

## 3. Endpoints

### 3.1 `GET /serial/ports`

Lists every port the OS currently enumerates, annotated with discovery state.

**Response (200):**

```json
{
  "ports": [
    {"name": "COM3", "discovered": true,  "device_id": "pump_1"},
    {"name": "COM5", "discovered": false},
    {"name": "COM7", "discovered": true,  "device_id": "valve_1"}
  ]
}
```

- Sorted by `name` for determinism.
- `discovery.include` / `discovery.exclude` are NOT applied — listing reflects actual system state, not the discovery filter.
- A port is `discovered: true` iff a device in the registry has `Port == name`. The accompanying `device_id` is the registry key (e.g., `pump_1`). Unmatched ports omit `device_id`.

**Errors:**

| Status | When |
|---|---|
| 403 | `raw_serial.enabled = false` → `{"error":"raw serial disabled","detail":"set raw_serial.enabled: true in config"}` |
| 500 | `Opener.List()` itself returned an error → `{"error":"list ports failed","detail":"..."}` |

### 3.2 `POST /serial/ports/{port}/command`

Open `{port}` at 9600/8N1, drain (200 ms), write the bytes, optionally read, close.

**Path param:** `{port}` is the OS port name as returned by `GET /serial/ports` (e.g. `COM3`). The production target is Windows where `COM<n>` names are URL-safe; clients on other platforms must URL-encode (`%2Fdev%2FttyUSB0`).

**Query params** (verbatim copy of `POST /devices/{id}/command`, plus a raw-only post-open settle):

| Param | Default (`expected=-1`) | Default (`expected>0`) | Range |
|---|---|---|---|
| `timeout_ms` | 100 | 1000 | 1..60000 |
| `inter_byte_ms` | 25 | 50 | 1..1000 |
| `wait_for_response` | true | true | true / false |
| `expected_response_bytes` | -1 | — | -1 or 1..1024 |
| `post_open_settle_ms` | `discovery.post_open_settle_ms` from config | same | 0..60000 |

`post_open_settle_ms` waits after `Opener.Open` and before `Drain`, mirroring the discovery runner. Useful for diagnosing Arduino-class boards that auto-reset on DTR — the default inherits the value the operator already tuned for their hardware in config. Set to `0` for boards that don't need it.

**Request body** (≤ 32 KB; `command` ≤ 1024 bytes; each byte 0..255):

```json
{ "command": [1, 2, 3, 4, 0] }
```

**Response (200):**

```json
{ "response": [10, 1, 2, 3] }
```

or `{ "response": [] }` when the port stayed silent or `wait_for_response=false`.

**Read termination rules** (when `wait_for_response=true`): identical to `/devices/{id}/command` (see `2026-04-26-lab-devices-client-design.md` §4.4) — `serial.ReadFrame` is used unchanged.

**Handler sequence:**

1. `raw_serial.enabled = false` → **403** (above).
2. Enumerate ports via `Opener.List()`. If `{port}` not present → **404** `{"error":"port not found","detail":"<name>"}`.
3. Registry contains a device with `Port == {port}` → **409** `{"error":"port has discovered device","detail":"use /devices/<id>/command instead"}`.
4. Discovery in progress → **409** `{"error":"discovery in progress"}`. Non-acquiring check via `Registry.IsDiscovering()`; the raw call does NOT hold the discovery gate.
5. Parse + validate query params → **400** on bad input.
6. Parse + validate body bytes → **400** on bad input.
7. `Opener.Open({port})` → on error **503** `{"error":"port open failed","detail":"..."}`.
8. Sleep `post_open_settle_ms` (no-op if 0) — covers the Arduino bootloader window on boards that auto-reset on DTR.
9. `port.Drain(200ms)` → on error close + **503**.
10. `port.Write(cmd)` → on error close + **503** `{"error":"port write failed","detail":"..."}`.
11. If `wait_for_response = false` → close, return `{"response":[]}`.
12. `serial.ReadFrame(...)` per query params → on error close + **503** `{"error":"port read failed","detail":"..."}`.
13. Close. Return `{"response":[...]}`.

**No reconnect-and-reprobe.** No identity to preserve; the port is closed at the end of every call regardless of outcome.

**Concurrency between raw-sends:** No mutex held across calls — each call opens fresh. If two raw-sends to the same port arrive concurrently, the OS rejects the second `Open()` and the second caller gets **503**. Documented behavior; consistent with the OS-level model.

## 4. Internal package layout

| File | Change |
|---|---|
| `internal/config/config.go` | Add `RawSerialConfig` struct, field on `Config`, `Default()` value (`false`), scaffold section. |
| `internal/config/load_test.go` | Coverage: parses `raw_serial.enabled: true`; `Default()` stays `false`; scaffold round-trip preserves the section. |
| `internal/registry/registry.go` | Add `IsDiscovering() bool` (non-acquiring read of `discoverGate`). Add `HasPort(name string) (deviceID string, ok bool)` helper that walks the device map under the read-lock. |
| `internal/registry/registry_test.go` | Coverage for the two new helpers. |
| `internal/api/server.go` | `New()` gains `opener serial.Opener` and `rawSerialEnabled bool` parameters. Routes `GET /serial/ports` and `POST /serial/ports/{port}/command` always registered; gating is per-handler so disabled hits get 403 with a helpful detail (rather than 404). |
| `internal/api/raw_serial.go` (new) | `handleGetSerialPorts`, `handlePostSerialCommand`. Reuses `parseCmdParams`, `parseCommandBody`, `bytesToInts`. The write+optional-read core is small; factored out of `executeCommand` into a tiny helper used by both handlers, or inlined if cleaner. |
| `internal/api/raw_serial_test.go` (new) | See §7. |
| `internal/api/types.go` | Add `PortDTO` and `PortsResponse`. `CommandRequest` / `CommandResponse` are reused unchanged. |
| `internal/api/handlers.go` | `Server` struct gains an `opener serial.Opener` field threaded from `New()`. The existing handlers don't need it; the new ones do. |
| `internal/app/app.go` | Pass `opener` and `cfg.RawSerial.Enabled` into `api.New(...)`. |
| `internal/panel/panel.go` | Add label rendering `raw_serial.enabled`. |
| `README.md` | Add the two endpoints to the REST API table. |

No changes to `internal/discovery/`, `internal/serial/`, `internal/chisel/`, `internal/winsvc/`, or release / build tooling.

## 5. Concurrency model

| Op | Discovery gate | Registry mutex | Port handle |
|---|---|---|---|
| `GET /serial/ports` | none | brief read-lock to walk registry for annotation | none |
| `POST /serial/ports/{p}/command` | non-acquiring read (`IsDiscovering`) | brief read-lock for `HasPort` | per-call open/close |
| `POST /discover` | acquires write (existing) | replaces map | closes registry-held ports |

**Race window** — if `POST /discover` starts while a raw-send is mid-flight, discovery's `Opener.Open(p)` for that one port will be locked out by the raw-send's handle and discovery silently skips it (existing behavior for any OS-locked port). No new logic; the discovery summary log line will show one fewer port matched, which is the same observable as a port that's busy with another OS process.

The reverse race — `POST /discover` is in progress, then a raw-send arrives — is rejected with 409 (step 4 above) before any port is touched.

`GET /serial/ports` is always allowed (read-only enumeration plus a registry walk). It is not gated on discovery state.

## 6. Error response shape

Reuses `api.ErrorBody` (`{"error":"<short_code>","detail":"<human readable>"}`). Status-code summary across both endpoints:

| Status | When |
|---|---|
| 400 | Bad query param, bad body, byte out of 0..255, command too long, body too large |
| 403 | `raw_serial.enabled = false` |
| 404 | Port not in `Opener.List()` at request time |
| 409 | Discovery in progress, or port belongs to a discovered device |
| 500 | `Opener.List()` itself failed |
| 503 | Open / drain / write / read failed |

## 7. Testing

`internal/api/raw_serial_test.go` parallels `handlers_test.go`, FakeOpener-backed:

- Both endpoints → 403 when `raw_serial.enabled = false` (and helpful `detail`).
- `GET /serial/ports`:
  - Empty registry, FakeOpener with several ports → all `discovered: false`, sorted by name, no `device_id` field.
  - Registry populated with two devices on a subset of ports → annotated correctly (`discovered: true`, `device_id` matches).
- `POST /serial/ports/{p}/command`:
  - Happy path with reply (FakePort fed bytes) — verifies written bytes and returned bytes.
  - No reply within `timeout_ms` → empty `response`.
  - `wait_for_response=false` → empty `response`; verifies bytes were written.
  - `expected_response_bytes` early-stop.
  - Bad query param / oversize body / bad byte / unknown JSON field → 400.
  - Port name not in `Opener.List()` → 404.
  - Port has discovered device → 409 (and detail mentions `/devices/<id>/command`).
  - Discovery in progress (`reg.LockDiscovery()` held by the test) → 409.
  - `Opener.Open()` fails after `List()` succeeded → 503. (The current `FakeOpener` shares one map between `List` and `Open`, so this case requires either extending `FakeOpener` with a configurable open-error per port, or using a small test-local stub Opener. Implementation plan picks one — the spec only requires the 503 path is exercised.)
  - Write fails (FakePort.Close() before the call) → 503.
- Config-level (`internal/config/load_test.go`): parses `raw_serial.enabled: true` correctly; `Default()` stays `false`; scaffold round-trip preserves the section.
- Registry-level (`internal/registry/registry_test.go`): `IsDiscovering()` reflects `LockDiscovery`/`UnlockDiscovery`; `HasPort` returns the correct ID and `ok` flag.

Coverage targets unchanged (≥80% on `internal/api`, `internal/config`). No new live-hardware test.

## 8. Logging

Mirror the existing device-command pattern.

- On every raw-send call:
  ```
  slog.Info("raw_serial_command",
      "port",         <port>,
      "cmd_bytes",    len(cmd),
      "resp_bytes",   len(resp),
      "duration_ms",  ...,
      "outcome",      ok | open_failed | drain_failed | write_failed | read_failed)
  slog.Debug("raw_serial_command bytes", "port", <port>, "cmd", []int{...}, "resp", []int{...})
  ```
  Bytes are rendered as int arrays (same convention as `command bytes` — see fix in commit `5333bd8`).
- On every listing call:
  ```
  slog.Info("raw_serial_list", "count", N)
  ```
- Disabled-endpoint hits go to `slog.Debug` so frequent rejections don't spam info-level logs.

## 9. Compatibility

- No breaking changes to existing endpoints, request bodies, response shapes, config fields, or persistent state.
- A config file written before this change parses cleanly: `RawSerial.Enabled` defaults to `false`. Operators must opt in by editing the YAML.
- A binary built with this change but run against a config that lacks the section behaves the same as `enabled: false` — no scaffold rewrite needed for upgrade.

## 10. Build / release

No new dependencies. No changes to `Taskfile.yaml`, release-please config, or the Windows service worker. Conventional Commits: this lands as a single `feat(api): raw serial port endpoints` PR; release-please will bump the minor on the next release.
