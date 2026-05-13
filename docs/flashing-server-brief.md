# SerialHop HTTP API — flashing endpoints

`SerialHop` is a Go service running on a lab machine. It exposes a REST API that lets a remote caller flash compiled firmware onto serial-attached microcontrollers and inspect the host's serial-port topology. The lab machine sits behind NAT and reaches the rest of the docker-compose network through a chisel reverse tunnel.

This document describes the wire-level behavior of the **port-discovery** and **firmware-flashing** endpoints. For the protocol used to command already-flashed devices, see `python-client-brief.md`.

## Connection

- **Base URL:** `http://chisel:<port>/` — `chisel` is the docker service name; `<port>` is unique per lab machine and is set by the lab machine's local config.
- **Transport:** plain HTTP. No TLS at this layer.
- **Authentication:** none at this layer. Authn/authz is enforced by an upstream service that fronts this URL inside the docker network.
- **Content type:** `application/json` on every request that has a body and on every response.

## Hardware scope

These endpoints target **AVR microcontrollers running optiboot** (Arduino Uno R3 / ATmega328P and pin-compatible clones). The wire protocol used internally is STK500v1.

| Property | Value |
|---|---|
| Total program flash | 32 768 bytes |
| Bootloader region (read-only) | 512 bytes |
| User-space ceiling | 32 256 bytes |
| Flash page size | 128 bytes |
| Bootloader baud | 115 200 |
| Sketch (post-flash) baud | 9 600 |
| Frame format | 8N1, no flow control |

Bootloader entry is triggered automatically by the service via a DTR pulse on the USB-serial bridge. The caller never sees this.

**EEPROM is not in scope.** Optiboot cannot read or write EEPROM while the user sketch is running, so pre-flash backups capture program flash only. The backup response advertises this with `backup.scope = "flash_only"`.

## Endpoints

### `POST /devices/disconnect`

Close every serial handle the service currently holds and empty its in-memory device registry. The service's prior knowledge of which port hosts which device is lost; the caller will need to re-run `POST /discover` (see `python-client-brief.md`) to repopulate it.

This is the precondition for a flash: a flash refuses (`409`) while any device is registered.

- **Request body:** none.
- **Query parameters:** none.
- **Gating:** always available; not gated by `flashing.enabled`.
- **Response (200):**
  ```json
  { "released": 3 }
  ```
  `released` is the number of devices that were in the registry before the call. `0` on an empty registry.
- **Errors:** none under normal operation. Idempotent; safe to call repeatedly.

### `GET /serial/ports/detailed`

Return every serial port the OS enumerates, annotated with USB descriptors (vendor / product / serial number) and whether the port currently hosts a discovered device.

Use this to verify which physical board is on which COM port before issuing a flash — there is no way for SerialHop to know "this is the board I want" without you choosing the port name. The `vid` / `pid` fields are the canonical identifiers.

- **Request body:** none.
- **Query parameters:** none.
- **Gating:** always available; not gated by `flashing.enabled`.
- **Response (200):**
  ```json
  {
    "ports": [
      {
        "name":          "COM3",
        "is_usb":        true,
        "vid":           "2341",
        "pid":           "0043",
        "serial_number": "8543931323535121F0A0",
        "product":       "Arduino Uno",
        "discovered":    false,
        "device_id":     ""
      },
      {
        "name":          "COM4",
        "is_usb":        true,
        "vid":           "1A86",
        "pid":           "7523",
        "serial_number": "",
        "product":       "USB-SERIAL CH340",
        "discovered":    true,
        "device_id":     "pump_1"
      }
    ]
  }
  ```
  - `vid` / `pid`: uppercase hex strings without `0x`. Empty string when the OS does not provide the descriptor (e.g., a non-USB serial port).
  - `serial_number` / `product`: passed through verbatim from the OS. May be empty.
  - `is_usb`: `false` for legacy / virtual serial ports.
  - `discovered`: `true` iff a device in the service's registry currently owns this port name. `device_id` is the registry ID for that device (e.g. `pump_1`); omitted when `discovered=false`.
  - Output is sorted by `name` for stable order across calls.
- **Errors:**
  - `500 Internal Server Error` — the OS-level port enumeration itself failed. Body: `{"error":"list ports failed","detail":"<message>"}`.

### `POST /flash/{port}`

Flash an Intel-HEX firmware image onto the AVR connected to `{port}`. Each call runs the full state machine — preflight, backup, erase, program, verify, optional functional test, optional rollback — and returns one of six terminal outcomes with stage-by-stage detail.

- **Path parameter:** `{port}` is the OS port name as returned by `GET /serial/ports/detailed` (e.g. `COM3`). Non-Windows hosts that use `/`-containing names must URL-encode (`%2Fdev%2Fcu.usbmodemXYZ`).
- **Gating:** `403` unless the service is configured with `flashing.enabled: true`. Independent of any other feature flag.
- **Body size limit:** 256 KiB.
- **Request body:**
  ```json
  {
    "firmware":            "<Intel HEX text>",
    "test_command":        "010203",
    "expected_response":   "AABBCC",
    "timeout_ms":          100,
    "inter_byte_ms":       25,
    "post_open_settle_ms": 2000
  }
  ```

  | Field | Required | Default | Range / format |
  |---|---|---|---|
  | `firmware` | yes | — | non-empty Intel HEX text, total program ≤ 32 256 bytes |
  | `test_command` | no | — | even-length hex string `[0-9a-fA-F]+`. Must be set iff `expected_response` is set. |
  | `expected_response` | no | — | even-length hex string `[0-9a-fA-F]+`. Must be set iff `test_command` is set. |
  | `timeout_ms` | no | `100` | `1..60000` — read timeout during the post-flash test |
  | `inter_byte_ms` | no | `25` | `1..1000` — inter-byte gap during the test read |
  | `post_open_settle_ms` | no | `2000` | `0..60000` — sleep between switching to sketch baud and sending `test_command`, covers Arduino boot |

  Notes:
  - The `firmware` field contains the *full Intel HEX document* (the contents of an `arduino-cli compile` `.hex` output, including `\n` line endings and the `:00000001FF` EOF record). The service tolerates a leading UTF-8 BOM and CRLF line endings.
  - `test_command` / `expected_response` are byte strings encoded as hex. The service expects to read **exactly** `len(expected_response)/2` bytes back; comparison is byte-equal. Both fields together form the contract "the new firmware works"; failing it triggers a rollback (see Outcomes).
  - Omit *both* `test_command` and `expected_response` to skip the post-flash test entirely. In that case `outcome=success` only requires byte-verify to pass.
  - The numeric parameters are upper bounds, not minimums. The wall time of a successful flash is ~15–30 s independent of these.

- **Preflight rejections (4xx, no bootloader interaction):**

  | Status | `error` code | When |
  |---|---|---|
  | 400 | `invalid request body` | Body unparseable, firmware empty, firmware > 32 256 B, firmware not valid Intel HEX, `test_command` / `expected_response` asymmetric, hex parse fails, numeric param out of range |
  | 403 | `flashing disabled` | Server configured with `flashing.enabled: false` |
  | 404 | `port not found` | `{port}` is not in the current OS-level port list |
  | 409 | `registry not empty` | The service still holds at least one discovered device. Caller must `POST /devices/disconnect` first. |
  | 409 | `discovery in progress` | A `POST /discover` is in flight. |
  | 409 | `flash in flight` | Another `POST /flash/{port}` is already running (single-flight mutex). |
  | 500 | `list ports failed` | OS-level port enumeration failed during preflight. |

  These 4xx responses use the standard error shape (see Error response shape below). The bootloader is **not** touched on these paths.

- **Response (200) — every complete run of the state machine, including failures.**

  All terminal outcomes return HTTP 200 with this body shape:

  ```json
  {
    "outcome": "success",
    "port":    "COM3",
    "stages": {
      "preflight": { "status": "ok",      "duration_ms":    12 },
      "backup":    { "status": "ok",      "duration_ms":  8214 },
      "erase":     { "status": "ok",      "duration_ms":    88 },
      "program":   { "status": "ok",      "duration_ms":  7902 },
      "verify":    { "status": "ok",      "duration_ms":  3105 },
      "test":      { "status": "ok",      "duration_ms":   214 },
      "rollback":  { "status": "n/a" }
    },
    "backup": {
      "hex":         "<Intel HEX text of the pre-flash flash image>",
      "saved_path":  "C:\\ProgramData\\SerialHop\\backups\\COM3-2026-05-12T14-22-08Z.hex",
      "sha256":      "9f8e7d...",
      "size_bytes":  32768,
      "scope":       "flash_only"
    },
    "test_result": {
      "sent":     "010203",
      "expected": "aabbcc",
      "received": "aabbcc",
      "match":    true
    }
  }
  ```

  Field semantics:

  - `outcome` — one of six terminal values; see Outcomes below.
  - `port` — echoes the path parameter.
  - `stages` — always carries the keys `preflight`, `backup`, `erase`, `program`, `verify`, `test`, `rollback`. Each entry's `status` is one of:
    - `"ok"` — stage ran and succeeded.
    - `"failed"` — stage ran and failed; `error` field carries the message.
    - `"skipped"` — stage was skipped because an earlier stage failed, or (for `test`) the caller omitted the test pair.
    - `"n/a"` — stage was not applicable on this path (e.g. `rollback` on a successful flash).
  - `stages.verify.first_mismatch_offset` — present only when verify failed; hex string offset where the post-program readback first diverged from the source.
  - `stages.rollback.verify_status` — present whenever rollback ran; `"ok"` if the post-rollback readback matched the backup, `"failed"` otherwise.
  - `backup.hex` — the full Intel HEX text of the device's flash *before* programming. Always populated when the backup stage reached `ok`. The same content is written to disk on the lab machine at `backup.saved_path` and is sha256-summed in `backup.sha256`.
  - `backup.scope` — always `"flash_only"`; reminder that EEPROM is not captured.
  - `test_result` — present only when the test phase ran. `received` is *whatever bytes were read*, including on timeout, so a caller diagnosing a `rolled_back_test_failed` can diff `received` against `expected`.
  - `recovery_hint` — present only when `outcome=failed_no_recovery`; a human-readable string suggesting next steps (typically: use an ISP programmer; the backup file is preserved with a `-LOCKED-` marker in its filename).

  Output encoding details:
  - All hex strings in the response (`test_result.sent`, `test_result.expected`, `test_result.received`) are **lowercase**.
  - `backup.size_bytes` counts the bytes of `backup.hex` (the Intel HEX text), not the bytes of the underlying flash image. (A 32 KB image renders to ~95 KB of Intel HEX.)

## Outcomes

Every `POST /flash/{port}` 200 response carries exactly one `outcome` value.

| `outcome` | Stages that ran | Device state on return | Caller action |
|---|---|---|---|
| `success` | preflight → backup → erase → program → verify → (test) → done | New firmware running, byte-verified; if a test pair was supplied, the firmware also passed it. | None. |
| `rolled_back_verify_failed` | preflight → backup → erase → program → verify (mismatch) → rollback (ok) | Original firmware restored, byte-verified against the backup. | Investigate firmware integrity (corrupt `.hex`, USB cable, EMI). |
| `rolled_back_test_failed` | preflight → backup → erase → program → verify (ok) → test (mismatch or read error) → rollback (ok) | Original firmware restored. | Two possibilities: (a) the new firmware is broken at runtime, or (b) `test_command` / `expected_response` were wrong. Use `test_result.received` to disambiguate. |
| `failed_preflight` | preflight failed | Untouched. | Fix the request and retry. |
| `failed_backup` | preflight → backup failed (couldn't sync with bootloader, couldn't read flash, or couldn't persist the backup to disk) | Untouched (no erase happened). | Check that the device is in fact AVR/optiboot, that no other process holds the port, and that the lab machine's backup directory is writable. Retry. |
| `failed_no_recovery` | preflight → backup ok → erase/program/verify/test failed → rollback failed | Unknown / likely unusable from SerialHop's perspective. | ISP-level recovery required (e.g., AVRISP mkII). The saved backup file has been renamed to insert `-LOCKED-` into its filename to prevent automatic pruning; treat it as the last known good image. |

**`failed_no_recovery` is the only outcome in which the device is potentially unusable on return.** Everything else either left the device untouched or restored it to its pre-flash state.

## Backup file lifecycle

After a successful backup stage, the service:

1. Renders the device's flash to Intel HEX.
2. Writes it to `<backup_dir>/<port>-<ISO8601-Z>.hex` on the lab machine (e.g. `COM3-2026-05-12T14-22-08Z.hex` — colons in the timestamp are replaced with hyphens for Windows filename compatibility; the format remains lexicographically sortable).
3. Returns the same bytes inline in `backup.hex`.

After every flash that completes (regardless of outcome), the service prunes old backups for that port, keeping the newest `flashing.keep_n` (configurable on the lab machine). When `outcome=failed_no_recovery`, the just-written backup is renamed to insert a `-LOCKED-` marker between the port and the timestamp (e.g. `COM3-LOCKED-2026-05-12T14-22-08Z.hex`); the pruner skips locked files indefinitely.

The disk-resident backups are only visible to processes on the lab machine. Inline `backup.hex` is the only handle a remote caller has on a backup.

## Concurrency

- One `POST /flash/{port}` at a time per service instance. A second concurrent call returns `409 flash in flight` immediately (before any port I/O).
- `POST /devices/disconnect` and `GET /serial/ports/detailed` can run any time and do not block each other.
- A `POST /discover` racing with `POST /flash/{port}` is detected at preflight: if discovery is in flight when flash starts, flash returns `409 discovery in progress`; if flash is in flight when discovery starts, discovery silently skips the locked port (its standard behavior for any busy port).

## Timing budget

A complete flash against a 32 KB AVR / optiboot target at 115 200 baud:

| Stage | Wall-clock |
|---|---|
| preflight | ~10–50 ms |
| bootloader sync + boot-delay | ~50 ms (best) — ~1.5 s (slow board) |
| backup (full 32 KB readback) | ~8 s |
| erase | ~50–100 ms |
| program (full 32 KB write) | ~8 s |
| verify (full 32 KB readback) | ~3 s |
| test (if supplied) | 0–5 s |
| rollback (if triggered: re-erase + re-program + re-verify) | ~11 s |
| **worst-case total** | **~36 s** |

A caller should set its HTTP read timeout to at least 60 s.

## Error response shape

The same shape as the rest of the SerialHop API:

```json
{ "error": "<short_code>", "detail": "<human readable, may be empty>" }
```

`error` codes returned by these endpoints:

| Status | `error` codes |
|---|---|
| 400 | `invalid request body` |
| 403 | `flashing disabled` |
| 404 | `port not found` |
| 409 | `registry not empty`, `discovery in progress`, `flash in flight` |
| 500 | `list ports failed`, `flash failed` |

Note: I/O failures **during** the flash itself surface as a `200` response with the corresponding `outcome` (`failed_backup` / `rolled_back_*` / `failed_no_recovery`), not as `5xx`. The state machine reaches a terminal outcome in every case where preflight passed.

## Byte encoding

| Surface | Encoding |
|---|---|
| `firmware` (request) | Intel HEX text |
| `test_command`, `expected_response` (request) | hex string, even length, any case |
| `backup.hex` (response) | Intel HEX text |
| `test_result.sent` / `expected` / `received` (response) | hex string, lowercase |
| `backup.sha256` (response) | hex string, lowercase |
| `vid` / `pid` (response) | hex string, uppercase, no `0x` |

This is the only place in the SerialHop API where bytes are represented as hex strings rather than JSON integer arrays — the choice keeps firmware images compact and human-inspectable.
