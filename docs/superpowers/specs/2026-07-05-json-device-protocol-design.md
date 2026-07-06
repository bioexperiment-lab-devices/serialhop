# JSON Device Protocol (v2) — Design

**Date:** 2026-07-05
**Status:** approved for planning

## 1. Summary

SerialHop v2 replaces the raw-byte device API with a stable, per-device, high-level JSON
protocol. Devices keep their legacy 5-byte serial protocols; all translation and device
state management moves inside SerialHop. The JSON surface is what API consumers rely on;
the byte layer is expected to change as firmware improves and stays isolated per device.

The per-device contracts are already specified and are **canonical**:

- `docs/protocol_translation_docs/<device>/JSON_PROTOCOL.md` — the JSON command surface
  (envelope, error codes, job model, commands) for pump, densitometer, distribution valve.
- `docs/protocol_translation_docs/<device>/TRANSLATION.md` — the exact translation and
  state-management algorithm each driver implements, including quirk workarounds and the
  documented gaps.

This document does not restate those; it specifies the hub architecture around them: the
core runtime, driver contract, concurrency model, HTTP surface, discovery/lifecycle,
persistence, testing, and release plan.

Decisions fixed during brainstorming:

- JSON protocol docs are the contract; small deviations allowed but must be flagged
  (see §8 for the complete list).
- No consumers of the raw API exist — it is deleted outright, no compatibility shim.
- Fleet firmware upgrades happen together: exactly one byte-translator per device type at
  a time. No firmware-version selection mechanism; the byte layer stays isolated per
  device package so swapping a generation later is a local change.
- Persistent state is keyed by hardware serial (pump, densitometer) with COM-port
  fallback (valve, which has no serial command). API device IDs stay ordinal
  (`pump_1`), assigned per discovery in `(type, port)` order, as today.

## 2. Architecture

Approach: **shared device-runtime core + per-device driver packages.** Everything the
three JSON_PROTOCOL docs share verbatim (envelope, error taxonomy, job model, serial
transaction discipline, persistence, reboot-detection duties) lives once in a core
package. Everything the TRANSLATION docs describe as device-specific (pause-belief
tracking, reboot canaries, virtual-homing offset math, opcode selection, absorbance math)
is imperative code in the device's own package.

```
internal/device/              core runtime (new)
├── envelope.go               request/response envelope, error codes, error constructors
├── jobs.go                   job engine: lifecycle, progress, pause-freeze, history ring (8)
├── transact.go               serial transaction primitive over serial.Port
├── store.go                  persistent per-device state (atomic JSON files)
├── session.go                device session actor: owns port + driver + background work
├── clock.go                  injectable clock (real / fake for tests)
├── registry.go               driver factory registration by probe type code
├── pump/                     pump driver — implements pump/TRANSLATION.md
├── densitometer/             densitometer driver — implements densitometer/TRANSLATION.md
└── valve/                    valve driver — implements distribution_valve/TRANSLATION.md
```

A developer editing pump byte-protocol details or translation logic touches only
`internal/device/pump/`.

### 2.1 Envelope and errors

```go
type Request struct {
    ID     string          `json:"id"`               // client-supplied, echoed back
    Cmd    string          `json:"cmd"`
    Params json.RawMessage `json:"params,omitempty"`
}

type Response struct {
    ID     string    `json:"id"`
    Status string    `json:"status"`                 // "ok" | "error"
    Result any       `json:"result,omitempty"`
    Error  *CmdError `json:"error,omitempty"`
}

type CmdError struct {
    Code    string `json:"code"`
    Message string `json:"message"`
    Details any    `json:"details,omitempty"`
}
```

Error codes: the spec's shared set (`invalid_request`, `unknown_command`,
`invalid_params`, `busy`, `not_calibrated`, `not_homed`, `hardware_error`,
`internal_error`) plus two hub-level codes (`unknown_device`, `device_unreachable`,
see §4). Constructor helpers (`device.ErrInvalidParams(param, value, msg)`, …) keep
`details` shapes consistent across drivers.

### 2.2 Job engine

One engine instance per session, implementing the spec's job model: at most one active
job; `running | paused | succeeded | failed | cancelled`; clock-driven progress
(`elapsed / estimated`, frozen while paused — the pump's pause accounting); history ring
of the last 8 completed jobs; job IDs unique per session lifetime. Drivers call
`Start(kind, estimate)`, `Complete(result)`, `Fail(err)`, `Cancel()`,
`Freeze()`/`Unfreeze()`; `get_job` and the `status` job block are served from the engine
without serial traffic.

### 2.3 Serial transaction primitive

`s.Transact(frame []byte, replyLen int, timeout time.Duration) ([]byte, error)`
implements the discipline all three TRANSLATION docs share: flush RX → write the whole
frame in one write → read exactly `replyLen` bytes (per-byte timeout 500 ms, total
timeout ≥ `replyLen × 30 ms`) → on failure retry the whole transaction once → second
failure surfaces as a hardware error and trips the session's unreachable handling.
`replyLen == 0` returns after the write. Built directly on `serial.Port`.

### 2.4 Driver contract

```go
// Factory registered per probe type code; discovery uses this registry
// instead of a hardcoded switch.
func Register(code byte, name string, f Factory)
type Factory func(s *Session) Driver

type Driver interface {
    // Attach: post-probe setup per TRANSLATION §3 — read serial number, push
    // config mirrors, recover persistent state. probeReply is the 4-byte
    // identify reply discovery already consumed (pump: calibration bytes;
    // valve: position count; densitometer: channel count). Returns the
    // identify info served from cache by GET /devices and the `identify` cmd.
    Attach(ctx context.Context, probeReply []byte) (Info, error)
    // Execute: handle one JSON command on the session goroutine.
    Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *CmdError)
    // Tick: periodic housekeeping — reboot canaries, monitoring scheduler,
    // pause bookkeeping. Called ~1/s on the session goroutine while attached.
    Tick(now time.Time)
    // Detach: graceful shutdown — persist state, drop watchers. The session
    // closes the port afterwards.
    Detach()
}

type Info struct {
    DeviceType      string `json:"device_type"`
    Model           string `json:"model"`
    Serial          string `json:"serial,omitempty"`
    FirmwareVersion string `json:"firmware_version"`
    ProtocolVersion string `json:"protocol_version"`
    Capabilities    any    `json:"capabilities"`
}
```

Session services available to drivers: `Transact`, `Jobs()`, `Store(key)` (§5),
`After(d, fn)` (timer whose callback runs on the session goroutine), `Go(fn)`
(watcher goroutine for blocking port reads; its completion event is delivered back on
the session goroutine), `Now()` (injectable clock). `Model` and `FirmwareVersion` are
driver constants (the legacy firmware cannot report them); the valve's `Serial` is
omitted (no serial command).

## 3. Concurrency: the session actor

Each attached device gets one session goroutine; all driver state lives there and is
mutated only there. Drivers contain no mutexes.

- The API handler posts the envelope into the session mailbox; commands execute strictly
  one at a time (the spec's "device is single-client" rule). Fast commands (`status`,
  `get_job`, memory-served `ping` cases) queue behind at most one in-flight command.
- **Rule: nothing on the loop may block longer than a few serial transactions.** Long
  waits are never on the loop:
  - Clock-driven completion (valve moves, pump reverse/gradient/suckback runs,
    densitometer sweeps): `s.After(estimate)`; the callback runs end-of-job verification
    transactions on the loop.
  - Hardware completion signals (the pump's opcode-18 elapsed-µs reply, potentially
    minutes later): `s.Go` watcher blocked on the port read, result posted as a loop
    event. Write-only frames (`stop`/`pause`/`resume`) may be sent while a watcher read
    is pending — the pump doc's exact traffic discipline. A reply-expecting transaction
    while a watcher is pending is a driver bug → fail fast `internal_error`.
  - Canaries and schedulers (densitometer 30 s thermostat canary, valve idle
    `CHECK_BELIEF`, monitoring ticks): driven from `Tick` on a ~1 s heartbeat.
- Known worst case, accepted: valve `stop` blocks its loop up to ~6 s (documented spec
  deviation — the firmware cannot abort; `stop` waits out the move). Queued commands
  stall behind it; within single-client semantics.
- **Unreachable devices:** a transaction failing twice fails the active job and flips
  the session to unreachable. Subsequent commands return `device_unreachable`
  immediately; the session re-probes with backoff (5 s doubling to 60 s, indefinitely)
  and re-runs `Attach` on success — which is where each TRANSLATION doc's reboot
  recovery already lives. The device list shows `connected: false` meanwhile.
  Exception — memory-served commands (amended 2026-07-06): `identify` is served
  from the cached `Info` whenever a successful `Attach` has ever populated it
  (HTTP 200, envelope `ok`); if `Attach` has never succeeded it returns
  `device_unreachable`. `get_job` is always served from the jobs engine —
  including the job the unreachable transition just failed with
  `hardware_error` "device became unreachable mid-job"; unknown `job_id`
  remains `invalid_params`. Every other command, including `status`
  (driver-served), fails fast with `device_unreachable`.

## 4. HTTP API surface

New surface under `/api/v1`:

| Route | Purpose |
|---|---|
| `GET /api/v1/devices` | Cached device list from last discovery |
| `POST /api/v1/discover` | Re-probe ports, rebuild sessions; returns the new list |
| `POST /api/v1/devices/{id}/command` | Single command endpoint; body and response are the envelope |

Device list entry:

```json
{
  "id": "pump_1",
  "type": "pump",
  "port": "COM7",
  "connected": true,
  "identify": {
    "device_type": "pump", "model": "peristaltic-1ch", "serial": "26-025",
    "firmware_version": "legacy", "protocol_version": "1.0", "capabilities": { }
  }
}
```

plus a top-level `discovered_at`. `identify` is `null` until `Attach` succeeds.

HTTP status mapping — the envelope is authoritative; HTTP status is a convenience
mirror:

- Device-decided outcomes (`ok`, `busy`, `invalid_params`, `not_calibrated`,
  `not_homed`, `hardware_error`, `unknown_command`, `internal_error`): **HTTP 200**
  with the envelope.
- Hub-level failures, still envelope-shaped: unknown device id → **404**
  `unknown_device`; session unreachable → **503** `device_unreachable` — except
  `identify` (with cached info) and `get_job`, which stay memory-served at
  **200** per §3; malformed JSON / missing `cmd` or `id` → **400**
  `invalid_request`.
- `POST /api/v1/discover` while any session has an active job → **409** (uniform
  `{"error","detail"}` body as other infra routes); consumers `stop` jobs first.

Old routes deleted: `GET /devices`, `POST /discover`, `POST /devices/{id}/command`
(raw bytes), `GET /serial/ports`, `POST /serial/ports/{port}/command`, along with
`CommandRequest`/`CommandResponse`, `parseCmdParams`, and the `rawSerialEnabled`
config option. The panel's device-list/discover bindings move to `/api/v1`.

Infra routes stay untouched at current paths: `/agent/info` (external contract with the
labbridge server), `/flash/{port}`, `/devices/disconnect`, `/serial/ports/detailed`,
`/power/*`. The raw-byte diagnostic role is covered by the protocols' own diagnostics
commands (`rotate_raw`, `read_raw`, `set_led`).

### Discovery and lifecycle

- Probing is unchanged (`{1,2,3,4,0}`, classify by first reply byte; `PostOpenSettle`
  preserved). On match, discovery looks up the driver factory by type code, creates the
  session, and runs `Attach` with the probe reply bytes.
- `Attach` failure does not hide the device: it is listed with `connected: false` and
  the session retries in the background (same backoff as unreachable).
- Discovery remains on-demand (no auto-probe at startup), as today.
- Re-discovery and app shutdown detach sessions gracefully: `Detach` (persists state),
  cancel watchers/timers, close ports. The registry becomes a session registry
  (`map[id]*device.Session`) with today's `Replace`/`CloseAll` semantics.
- Flash keeps its "no attached devices" precondition via the existing disconnect flow.

## 5. Persistence

One JSON file per device under the app data dir (via `internal/paths`):
`devicestate/pump-26-025.json` (serial-keyed), `devicestate/valve-COM7.json`
(port-keyed fallback). The driver derives its state key during `Attach` (after reading
the serial number) and obtains its store handle via `s.Store(key)`; the core prefixes
the type name and sanitizes the key for the filesystem.

- Atomic writes: temp file + rename. Written on every persistent-field mutation — rare,
  human-paced events (calibration set, thermostat change, valve move).
- Each driver owns its schema and includes a `schema_version` field; unknown versions
  are treated as absent state (deliberate migration or discard on driver rewrite).
- Contents per the TRANSLATION docs: pump `{ml_per_step, set_at, serial}`; densitometer
  `{blank{slope, temperature_c, measured_at}, tube_correction, thermostat{enabled,
  target_c}}`; valve `{physical_position, device_belief_at_shutdown,
  config{default_rotation, hold_torque}}`.
- Volatile state (jobs, readings ring, monitoring schedule, pause beliefs) is
  in-memory only, per the docs.

## 6. Testing

Three layers, all pure Go, cross-platform (macOS + Windows), race-clean:

1. **Driver tests** (the workhorse): existing `serial.FakePort` + the fake clock. Feed
   reply bytes, assert exact frames written, advance the clock to fire job completions,
   canaries, monitoring ticks. The TRANSLATION docs convert nearly line-by-line into
   table tests — pump opcode selection (18 vs 15/16/17), suckback step inflation, speed
   quantization and echo-actual-values; valve wrap-mode Δ=0 guard, offset translation,
   reboot recovery paths; densitometer thermostat canary, slope/absorbance math, sweep
   validation.
2. **Core tests**: job engine state machine, store atomicity, session command ordering,
   timer/watcher event delivery, unreachable/backoff transitions.
3. **API tests**: `httptest` with a fake driver registered under a test type code —
   envelope handling, HTTP status mapping, 409 discovery-conflict — no device logic.

No new Windows-only code is introduced; the existing `_windows.go` fake-coverage rule
is unaffected.

## 7. Release plan

Incremental PRs to `main`, one logical change each, all passing the pre-flight checks:

1. `feat:` device core runtime (`internal/device`) + tests.
2. `feat:` pump driver.
3. `feat:` densitometer driver.
4. `feat:` valve driver.
5. `feat!:` v2 API cutover — new `/api/v1` surface wired into app/discovery/registry,
   raw endpoints and `rawSerialEnabled` deleted, panel bindings updated, README updated.
   This PR alone carries the `BREAKING CHANGE:` footer → release-please produces
   v2.0.0.

Intermediate merges release as harmless 1.x minors; nothing consumes the new packages
until the cutover PR.

## 8. Deviations from the JSON_PROTOCOL docs (flagged per agreement)

1. **Transport path**: `POST /api/v1/devices/{id}/command` (multi-device hub), not the
   docs' single-device `POST /api/v1`.
2. **Hub-level error codes added**: `unknown_device` (404), `device_unreachable` (503).
3. **HTTP status mirroring** as described in §4 (the docs leave transport out of scope).
4. Valve `stop` cannot abort motion — already documented as a deviation in
   `distribution_valve/TRANSLATION.md` §4; the JSON doc's `MAY` clause covers it.

## 9. Out of scope

- Panel UI redesign (only the device-list/discover binding paths change).
- Firmware changes; the new byte protocols ("huge improvements") come later and slot in
  as driver-package rewrites.
- Auth (delegated upstream, unchanged), chisel/labbridge/bootstrap, flasher, updater,
  camera streamer.
- Valve serial-number configuration (omitted from `identify` until a config need
  arises).
