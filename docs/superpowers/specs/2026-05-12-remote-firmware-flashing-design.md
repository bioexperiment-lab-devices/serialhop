# Remote Firmware Flashing — Design

**Date:** 2026-05-12
**Status:** Approved (brainstorming complete; pending spec review before plan)

## 1. Purpose & scope

A new HTTP endpoint that lets the lab-bridge operator flash compiled firmware onto a serial-connected device on the lab machine, over the existing chisel tunnel. Models the workflow that `bogdan-firmware` automates locally (`task flash:pump`), but with the operator running it remotely and an automatic pre-flash backup + automatic rollback on verify-or-test failure.

Scope:

- AVR / optiboot devices only (Arduino Uno R3 and pin-compatible clones — the four boards `bogdan-firmware` targets).
- Native Go STK500v1 client. No bundled `avrdude.exe`, no shelling out.
- Synchronous flash: one HTTP POST in, one HTTP POST out, ~15–35 s wall time.
- Pre-flash flash-memory backup (Intel HEX, saved on the lab machine *and* returned inline in the response).
- Automatic rollback on post-program byte-verify failure or on operator-supplied test-pair failure.
- Operator opt-out of the test phase by omitting `test_command` / `expected_response`.

Out of scope (deliberately YAGNI):

- Non-AVR families (ESP32, STM32, RP2040). The `internal/flasher/avr` subpackage leaves room for additions.
- EEPROM read/write. Optiboot cannot read EEPROM while the user sketch is running; flash-only backups, response advertises `scope: "flash_only"`.
- Fuse / lock-bit changes.
- Bundling `avrdude.exe` and `avrdude.conf`.
- Persistent / multi-step flash sessions; chunked uploads.
- Job-based polling API (single sync POST is sufficient for the target wall-time).
- Hardware-in-the-loop CI. Manual smoke tests against a real Uno suffice for v1.
- Local-panel UI for flashing. Operator-only feature.

## 2. Outcome taxonomy

Every successful HTTP call (200) returns one of six `outcome` values. Non-200 responses signal preflight rejections; the state machine never started.

| `outcome` | Reached stages | Device state on return | Operator action |
|---|---|---|---|
| `success` | backup → erase → program → verify → (test) → done | New firmware, byte-verified, test passed (or skipped) | None |
| `rolled_back_verify_failed` | backup → erase → program → verify (mismatch) → rollback (ok) | Original firmware restored | Investigate `.hex` integrity, USB cable, EMI |
| `rolled_back_test_failed` | backup → erase → program → verify (ok) → test (mismatch or read error) → rollback (ok) | Original firmware restored | Two possibilities: (a) new firmware broken at runtime, or (b) the `test_command` / `expected_response` pair was wrong. Response carries `test_result.sent`, `test_result.received`, `test_result.expected` to disambiguate. |
| `failed_preflight` | preflight (failed) | Untouched | Check port, cable, optiboot presence, port not held by another process |
| `failed_backup` | backup (failed) | Untouched (no erase yet) | Retry; backup-read is usually transient |
| `failed_no_recovery` | … → rollback (failed) OR rollback verify (mismatch) | Unknown / likely bricked from SerialHop's perspective | ISP-level recovery required. Saved backup file is locked from pruning. |

Notes:

- `failed_no_recovery` is the only outcome in which the device is potentially unusable on return. The response includes a `recovery_hint` string.
- Per-stage status is reported in `stages` (see §4.3) so the operator can see exactly where the run stopped.
- "Test" stage is skipped entirely when the operator does not provide a `test_command` / `expected_response` pair. In that case `stages.test = {"status": "skipped"}`, `test_result` is omitted, and `rolled_back_test_failed` is unreachable for that call.

## 3. Configuration

New top-level section in `SerialHop_config.yaml`:

```yaml
flashing:
  enabled: false       # allow POST /flash/{port}. higher risk than raw_serial —
                       # a bad .hex bricks the board (ISP recovery needed).
                       # independent of raw_serial.enabled.
  backup_dir: ""       # absolute path for pre-flash backups.
                       # empty → %ProgramData%\SerialHop\backups\
  keep_n: 10           # retain this many backups per COM port; oldest pruned
                       # after each completed flash. 0 = keep all.
```

- `Default()` returns `Flashing.Enabled = false`, `BackupDir = ""`, `KeepN = 10`.
- Validation at config load:
  - `enabled: true` + relative `backup_dir` → reject with the standard panel validation-warning surface.
  - `keep_n < 0` → reject.
- Resolved `backup_dir` is created at startup (`os.MkdirAll(dir, 0o755)`), same pattern as `logs/` in `internal/paths`.
- `/devices/disconnect` and `/serial/ports/detailed` are always available (see §4.1, §4.2); only `/flash/{port}` is gated by `flashing.enabled`.

## 4. Endpoints

Three new endpoints. All registered on the existing `api.Server` mux; they share the existing chisel-tunnel trust boundary and bearer-auth model enforced by lab-bridge.

### 4.1 `POST /devices/disconnect`

Closes every serial handle in the registry, empties the registry, sleeps `portSettleDelay` (existing 100 ms). Always available; not gated by `flashing.enabled`.

No body. No query params.

**Response (200):**

```json
{ "released": 3 }
```

`released` is the number of devices that were in the registry before the call. Always succeeds (calling on an empty registry returns `{"released": 0}`).

Implementation reuses `Registry.CloseAll()` + `Registry.Replace(nil)`, exposed via a new `Registry.DisconnectAll() int` method that returns the count.

### 4.2 `GET /serial/ports/detailed`

Lists every port the OS enumerates, with USB descriptors. The `arduino-cli board list` analog. Always available; not gated by `flashing.enabled`.

**Response (200):**

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
    }
  ]
}
```

- `vid` / `pid`: uppercase-hex strings without `0x`. Empty string when the OS does not provide the descriptor (e.g., a non-USB serial port).
- `discovered` / `device_id`: mirror the existing `/serial/ports` enrichment.
- Sorted by `name` for determinism.

Backed by `go.bug.st/serial.GetDetailedPortsList()` (already a transitive dependency).

### 4.3 `POST /flash/{port}`

Synchronous flash with pre-flash backup, post-program byte-verify, optional post-flash test, and auto-rollback on failure.

**Path param:** `{port}` is the OS port name as returned by `GET /serial/ports/detailed`. Non-Windows callers must URL-encode (`%2Fdev%2Fcu.usbmodemXYZ`).

**Request body** (≤ 256 KB; enforced via `http.MaxBytesReader`):

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
| `firmware` | yes | — | non-empty Intel HEX text, total program ≤ 32 256 B (Uno user space) |
| `test_command` | no | — | even-length hex string `[0-9a-fA-F]+`. Must be set iff `expected_response` is set. |
| `expected_response` | no | — | even-length hex string `[0-9a-fA-F]+`. Must be set iff `test_command` is set. |
| `timeout_ms` | no | 100 | 1..60000 — read timeout during the test phase |
| `inter_byte_ms` | no | 25 | 1..1000 — inter-byte gap during the test read |
| `post_open_settle_ms` | no | `discovery.post_open_settle_ms` from config (default 2000) | 0..60000 — settle between reopening the port at 9600 8N1 and sending `test_command` |

Notes:

- `expected_response_bytes` is implied by `len(expected_response)/2`; it is not a request field. Exact-match semantics: read exactly that many bytes, compare with `bytes.Equal`.
- `wait_for_response` is not a request field. With a test pair set, the test phase always reads. Without a test pair, there is no test phase.
- Test-pair asymmetry (`test_command` set, `expected_response` empty, or vice versa) → 400 with message "test_command and expected_response must both be set or both omitted".

**Preflight (4xx, no bootloader interaction):**

| Status | When |
|---|---|
| 400 | Body parse fails / firmware not Intel HEX / firmware too large / test pair asymmetric / test hex unparseable / numeric param out of range |
| 403 | `flashing.enabled = false` |
| 404 | `port` not present in `Opener.List()` |
| 409 | Registry non-empty (operator must `POST /devices/disconnect` first) |
| 409 | Discovery in progress |
| 409 | Another `POST /flash/{port}` is in flight (single-flight mutex) |

**Response (200 — any complete run of the state machine, including `failed_no_recovery`):**

```json
{
  "outcome": "rolled_back_verify_failed",
  "port":    "COM3",
  "stages": {
    "preflight": { "status": "ok",      "duration_ms":    12 },
    "backup":    { "status": "ok",      "duration_ms":  8214 },
    "erase":     { "status": "ok",      "duration_ms":    88 },
    "program":   { "status": "ok",      "duration_ms":  7902 },
    "verify":    { "status": "failed",  "duration_ms":  3105, "first_mismatch_offset": "0x1A40" },
    "test":      { "status": "skipped" },
    "rollback":  { "status": "ok",      "duration_ms": 11013, "verify_status": "ok" }
  },
  "backup": {
    "hex":         "<Intel HEX text>",
    "saved_path":  "C:\\ProgramData\\SerialHop\\backups\\COM3-2026-05-12T14-22-08Z.hex",
    "sha256":      "9f8e7d...",
    "size_bytes":  32768,
    "scope":       "flash_only"
  }
}
```

- `stages` keys: `preflight`, `backup`, `erase`, `program`, `verify`, `test`, `rollback`. Each carries `status` ∈ {`ok`, `failed`, `skipped`, `n/a`} and `duration_ms`. Stages downstream of a failure are `skipped`; `rollback` is `n/a` on `success`, `failed_preflight`, and `failed_backup`.
- Stage-specific extras:
  - `verify.first_mismatch_offset`: hex string offset, present only when verify failed.
  - `rollback.verify_status`: `ok` / `failed`, present whenever rollback ran.
- `backup.hex` is the full Intel HEX text, always included in the response per Section 2 sign-off (request was for both inline and disk).
- `backup.scope` is `"flash_only"` to call out the EEPROM caveat.
- `test_result` is **omitted** from the JSON when the test phase was skipped (`omitempty`); populated otherwise:

```json
"test_result": {
  "sent":     "010203",
  "expected": "AABBCC",
  "received": "AABBCD",
  "match":    false
}
```

`received` is always populated when the test phase ran, even on read timeout / I/O error (carries whatever bytes were read).

- `recovery_hint` is **omitted** from the JSON unless the outcome is `failed_no_recovery` (`omitempty`). When present:
  > "Rollback byte-verify failed at offset 0x1A40. The device may need ISP-level recovery (e.g. AVRISP mkII). The saved backup at <saved_path> is the last known good image."

**No reconnect-and-reprobe after flash.** The registry remains empty after `POST /flash/{port}` completes; the operator chooses when to run `POST /discover` to repopulate.

**Concurrency:** A single in-process `sync.Mutex` (`flashMu`) guards `Flasher.Flash()`. The lock is acquired in preflight after body parse so that 409s for "flash in flight" come back fast (~ms) without consuming I/O.

## 5. State machine

The full stage-by-stage flow inside `Flasher.Flash()`:

```
preflight ──► backup ──► erase ──► program ──► verify ──► [test] ──► success
   │            │          │          │           │           │
   fail         fail       fail       fail        fail        fail
   │            │          │          │           │           │
   ▼            ▼          ▼          ▼           ▼           ▼
failed_       failed_   rollback   rollback    rollback    rollback
preflight     backup    (erase→reprogram(backup)→readback)
                          │
                          ▼
                    readback == backup ?
                     │            │
                    yes          no
                     │            │
                     ▼            ▼
                 rolled_back_*  failed_no_recovery
```

### 5.1 Stage timing and constants

| Stage | Wall time (32 KB Uno @ optiboot/115200) |
|---|---|
| preflight | ~10–50 ms |
| bootloader sync + boot delay | ~50 ms (best case) — ~1.5 s on a sluggish board |
| backup | ~8 s |
| erase | 0 ms (no-op on optiboot — see §5.5) |
| program | ~8 s |
| verify | ~3 s |
| test | 0 (skipped) or ~0.05–5 s |
| rollback (if triggered) | ~11 s (reprogram + readback; +~150 ms for DTR-pulse re-entry when triggered after `test`) |
| **worst-case total** | ~36 s |

Comfortably within the existing `http.Server.WriteTimeout = 90 s`; no server-timeout tuning required.

New constants in `internal/flasher` (package-level):

- `bootloaderSyncTimeout = 1500 * time.Millisecond`
- `bootloaderSyncRetries = 5`
- `bootloaderBootDelay = 50 * time.Millisecond` (between DTR pulse and first sync attempt)

Per-chip constants in `internal/flasher/avr/uno.go`:

- `FlashSize = 32 * 1024`
- `BootloaderSize = 512` (user-space ceiling = `FlashSize - BootloaderSize = 32 256` B)
- `PageSize = 128`
- `BootloaderBaud = 115200`
- `TargetBaud = 9600`

### 5.2 Bootloader entry

Optiboot enters the bootloader for ~1 s after a reset. The reset is triggered by toggling DTR low → high on the USB-serial bridge (the same way `arduino-cli upload` does it):

1. `Opener.OpenWithBaud(port, 115200)`.
2. `port.SetDTR(false)` then `port.SetDTR(true)` (or analogously per `bugst.Port` semantics).
3. Sleep `bootloaderBootDelay`.
4. STK500v1 sync (`STK_GET_SYNC` until `STK_INSYNC + STK_OK`, up to `bootloaderSyncRetries`, 200 ms between attempts, total budget `bootloaderSyncTimeout`).

The port handle remains open from this point through stages 2–5 (backup, erase, program, verify) and through rollback if it fires. We close and reopen at 9 600 only for the test phase (stage 6).

### 5.3 Rollback details

Triggered by failure in `program`, `verify`, or `test`. (`erase` is a no-op stage on optiboot — see §5.5 — so it cannot trigger rollback.) Uses the in-memory backup image read during stage 2. Mechanics:

1. **If triggered after `test`:** the bootloader has exited and the user firmware is running at `TargetBaud`. Switch baud back to `BootloaderBaud`, pulse DTR low→high, sleep 50 ms, re-sync, re-`STK_ENTER_PROGMODE`. For `program` / `verify` triggers the bootloader is still alive in the existing session — skip this re-entry.
2. Page-write the backup image. On per-page failure → `failed_no_recovery`. (No `STK_CHIP_ERASE` — optiboot's per-page erase happens implicitly during `STK_PROG_PAGE`; see §5.5.)
3. Page-read flash, `bytes.Equal` against backup image. On mismatch → `failed_no_recovery`.
4. Otherwise → `rolled_back_verify_failed` or `rolled_back_test_failed` based on which stage triggered rollback.

We do not re-run `test_command` against the rolled-back device. The test was the contract for the new firmware; the backup's behaviour is taken as previously-known.

### 5.5 Why `erase` is a no-op stage

Optiboot 512 B (the variant shipped with `arduino:avr` 1.8.7) does **not** implement chip erase — `avrdude` requires its `-D` flag (skip chip erase) for this bootloader. Per-page erase happens implicitly inside `STK_PROG_PAGE` as each page is written. Sending `STK_CHIP_ERASE` would either be ignored (lenient optiboot variants reply INSYNC+OK) or fail outright (stricter variants), so the production flow never sends it.

The `erase` stage is retained in the response shape for stability; it always reports `{"status": "ok", "duration_ms": 0}` once preflight + bootloader entry succeed.

Reference: `bogdan-firmware/docs/firmware-backup-and-flash.md` §3 ("Critical: Optiboot can't access EEPROM" — same per-page-erase mechanic applies to flash) and §8 (the `-D` requirement).

### 5.4 Backup file lifecycle

After stage 2 (backup) completes successfully, the byte image is rendered to Intel HEX and written to:

```
<backup_dir>/<port>-<ISO8601-with-hyphen-time-separators-Z>.hex
```

Example: `C:\ProgramData\SerialHop\backups\COM3-2026-05-12T14-22-08Z.hex`. Hyphens (not colons) separate time components because Windows file names disallow colons; the format remains lexicographically sortable.

After the flash completes (regardless of outcome), the pruner runs:

- List `<backup_dir>/<port>-*.hex`.
- Skip any file whose name contains `-LOCKED-` (see below).
- Sort remaining by name (== sort by time given the ISO8601 prefix).
- Delete all but the newest `keep_n`. `keep_n = 0` disables pruning.

When `outcome == failed_no_recovery`, the just-written backup is **renamed** to insert the `-LOCKED-` marker between the port and the timestamp:

```
COM3-LOCKED-2026-05-12T14-22-08Z.hex
```

The pruner skips locked files indefinitely. The operator removes the marker (or deletes the file) manually after recovering the board out-of-band.

## 6. Internal package layout

| File | Change |
|---|---|
| `internal/flasher/flasher.go` (new) | `Flasher` struct with single-flight mutex, `New(opener, backupDir, keepN, settleAfterOpen)` constructor, public `Flash(ctx, port, Request) (*Result, error)` method, `ErrBusy`. |
| `internal/flasher/stages.go` (new) | Stage functions: `runPreflight`, `runBackup`, `runErase`, `runProgram`, `runVerify`, `runTest`, `runRollback`. Each takes the in-flight `runState` and returns updated state + early-exit signal. |
| `internal/flasher/intelhex.go` (new) | `ParseIntelHex([]byte) ([]byte, error)` and `RenderIntelHex([]byte) string`. Supports record types 00 (data) and 01 (EOF); rejects 02–05 cleanly. |
| `internal/flasher/stk500v1.go` (new) | STK500v1 client: `Sync`, `GetSignOn`, `LoadAddress`, `ProgPage`, `ReadPage`, `LeaveProgMode`, `ChipErase`. Operates on a `serial.Port` handle plus per-call timeouts. |
| `internal/flasher/backupstore.go` (new) | `SaveBackup(port, hex)`, `LockBackup(path)` (rename to `-LOCKED-` form), `PruneBackups(port, keepN)`, sha256 computation. |
| `internal/flasher/avr/uno.go` (new) | Per-chip constants listed in §5.1. |
| `internal/flasher/testing/fake_optiboot.go` (new) | `FakeOptiboot`: in-memory STK500v1 responder implementing `serial.Port`, with error-injection knobs for the test layer. |
| `internal/flasher/*_test.go` (new) | Tests per §7. |
| `internal/serial/port.go` | Add `SetBaudRate(int) error` and `SetDTR(bool) error` to `Port`. Add `OpenWithBaud(name string, baud int) (Port, error)` and `ListDetailed() ([]DetailedPort, error)` to `Opener`. New `DetailedPort` struct. `realPort` / `realOpener` implementations passthrough to `bugst.Port` / `bugst.GetDetailedPortsList()`. |
| `internal/serial/fake.go` | Mirror additions on the fake: programmable `OpenWithBaud`, `ListDetailed`, `SetDTR`, `SetBaudRate`. |
| `internal/registry/registry.go` | Add `DisconnectAll() int` — wraps `CloseAll()` + `Replace(nil)`, returns prior count. |
| `internal/api/server.go` | `Server` struct gains `flasher Flasher` and `flashingEnabled bool` fields. `New(...)` signature grows two params. Routes `POST /devices/disconnect`, `GET /serial/ports/detailed`, `POST /flash/{port}` registered always; gating is per-handler. |
| `internal/api/flash.go` (new) | `handlePostDevicesDisconnect`, `handleGetSerialPortsDetailed`, `handlePostFlashPort`. Body parsing, hex / Intel HEX validation, mapping `flasher.Result` → `FlashResponse`. |
| `internal/api/types.go` | Add `FlashRequest`, `FlashResponse`, `StageDTO`, `BackupDTO`, `TestResultDTO`, `DetailedPortDTO`, `DisconnectResponse`. |
| `internal/api/flash_test.go` (new) | HTTP-layer tests per §7. Uses a stub `Flasher` interface (`Server` accepts a `flasher.Interface`, not the concrete `*flasher.Flasher`). |
| `internal/api/handlers.go` | No behavioural change. Existing `executeCommand` / `tryReconnect` untouched. |
| `internal/config/config.go` | Add `FlashingConfig` (`Enabled`, `BackupDir`, `KeepN`), field on `Config`, `Default()` value (`false`, `""`, `10`), scaffold section. |
| `internal/config/load.go` / `load_test.go` | Validation: reject `enabled: true` + relative `backup_dir`; reject `keep_n < 0`. Coverage: parses block; `Default()` matches; scaffold round-trip preserves. |
| `internal/paths/paths.go` | Add `BackupsDir() string` returning `<ProgramData>/SerialHop/backups` on Windows, `~/.serialhop/backups` elsewhere. Mirror existing `LogsDir()` etc. |
| `internal/app/app.go` (or `cmd/serialhop/main.go`, wherever the wiring lives) | Resolve `cfg.Flashing.BackupDir` (empty → `paths.BackupsDir()`), `os.MkdirAll`, construct `flasher.New(...)`, pass to `api.New(...)`. |
| `README.md` | Add the three endpoints to the REST API table; mention `flashing` config block. |

No changes to `internal/discovery/`, `internal/chisel/`, `internal/winsvc/`, `internal/logship/`, `internal/panel/`, `internal/updater/`, or release/build tooling.

### 6.1 Public surface of `internal/flasher`

```go
type Flasher interface {
    Flash(ctx context.Context, port string, req Request) (*Result, error)
}

type Request struct {
    Firmware         []byte        // parsed flash image
    TestCommand      []byte        // nil/empty → skip test
    ExpectedResponse []byte
    Timeout          time.Duration
    InterByte        time.Duration
    PostOpenSettle   time.Duration
}

type Result struct {
    Outcome      Outcome
    Port         string
    Stages       map[string]StageResult
    Backup       BackupInfo
    TestResult   *TestResult // nil when test was skipped
    RecoveryHint string
}

type Outcome int
const (
    OutcomeSuccess Outcome = iota
    OutcomeRolledBackVerifyFailed
    OutcomeRolledBackTestFailed
    OutcomeFailedPreflight
    OutcomeFailedBackup
    OutcomeFailedNoRecovery
)
func (o Outcome) String() string // wire form

var ErrBusy = errors.New("flasher: another flash is in flight")
```

`Flasher` is an interface; the concrete `*flasherImpl` returned by `New(...)` satisfies it. This indirection is solely to make `internal/api` tests trivial — they pass a stub.

### 6.2 Dependency direction

```
cmd/serialhop  ──►  internal/api  ──►  internal/flasher  ──►  internal/serial
                          │                  │
                          ├─►  internal/registry
                          ├─►  internal/config
                          └─►  internal/paths

internal/flasher  ──►  internal/serial   (and internal/paths for default backup dir, only if injected nil)
```

`internal/flasher` does not import `internal/registry`, `internal/discovery`, or `internal/api`. The "registry must be empty before flash" rule is enforced one layer up, in the API handler.

## 7. Testing

Five layers; all run cross-platform (macOS / Linux dev, Windows CI) per the CLAUDE.md cross-platform rule. No hardware-in-loop tests.

### 7.1 `internal/flasher/intelhex_test.go`

- Round-trip: `parse(render(parse(x))) == parse(x)` for the four `bogdan-firmware` artifacts checked into `internal/flasher/testdata/` as golden fixtures.
- Parser error cases: truncated record, bad checksum, unsupported record type, trailing whitespace tolerance, BOM tolerance.
- `FuzzParseIntelHex`: feed random bytes, assert no panic. Short budget for CI; longer budget for ad-hoc runs.

### 7.2 `internal/flasher/stk500v1_test.go`

Tests the STK500v1 client against `FakeOptiboot` (in `internal/flasher/testing/`).

- Happy path: sync, sign-on, write a 32 KB image, read back, `bytes.Equal`.
- Sync retry: bootloader silent for K attempts (K < `bootloaderSyncRetries`), succeeds on K+1.
- Sync exhaustion: bootloader never replies → returns a recognisable error.
- Partial page-write: mid-stream I/O error surfaces to caller.
- Corrupted readback: returned page differs from written → caller detects via `bytes.Equal`.
- Sign-on tolerance: optiboot variants with different vendor strings still succeed, with the variant logged at debug.

### 7.3 `internal/flasher/stages_test.go`

End-to-end runs of `Flasher.Flash()` against `FakeOptiboot`, asserting on the `Result`.

| Test | Fake config | Expected `Outcome` |
|---|---|---|
| `Flash_Success` | clean | `OutcomeSuccess` |
| `Flash_Success_NoTest` | clean, empty `TestCommand` | `OutcomeSuccess`, `stages.test == skipped`, `TestResult == nil` |
| `Flash_FailedPreflight_BadHex` | n/a | `OutcomeFailedPreflight` |
| `Flash_FailedPreflight_TooLarge` | firmware 40 KB | `OutcomeFailedPreflight` |
| `Flash_FailedBackup_PortOpenFailed` | fake `OpenWithBaud` errors | `OutcomeFailedBackup`, `stages.backup.status == "failed"` |
| `Flash_FailedBackup_SyncTimeout` | fake never syncs | `OutcomeFailedBackup` |
| `Flash_FailedBackup_ReadFails` | fake errors mid-readback | `OutcomeFailedBackup` |
| `Flash_RolledBackVerifyFailed` | fake "ack-but-don't-persist" on next prog_page | `OutcomeRolledBackVerifyFailed`, `stages.rollback.verify_status == ok` |
| `Flash_RolledBackTestFailed` | fake clean, wrong `expected_response` | `OutcomeRolledBackTestFailed`, `TestResult.Match == false`, `TestResult.Received` populated |
| `Flash_FailedNoRecovery_RollbackVerifyFails` | fake corrupts readback during rollback | `OutcomeFailedNoRecovery`, `RecoveryHint` populated |
| `Flash_FailedNoRecovery_RollbackProgramFails` | fake errors during rollback page-write | `OutcomeFailedNoRecovery` |
| `Flash_BackupSavedToDisk` | clean | backup file exists at expected path, sha256 matches |
| `Flash_BackupPruning_KeepN` | run 12 flashes, `keep_n=10` | 10 backups remain, 2 oldest pruned |
| `Flash_BackupPruning_LockedSkipped` | seed `-LOCKED-` file, run 12 flashes, `keep_n=10` | locked file survives even when otherwise eligible for pruning |
| `Flash_SingleFlight` | two concurrent `Flash()` calls | second returns `ErrBusy` |

### 7.4 `internal/api/flash_test.go`

HTTP-layer tests with `httptest.Server` + stub `Flasher`. Asserts on status codes and JSON shape.

| Test | Setup | Expected |
|---|---|---|
| `Flash_403_Disabled` | `flashingEnabled: false` | 403; detail references config flag |
| `Flash_404_UnknownPort` | port absent from `opener.List()` | 404 |
| `Flash_409_RegistryNotEmpty` | registry has 1 device | 409; detail mentions `/devices/disconnect` |
| `Flash_409_DiscoveryInProgress` | `reg.LockDiscovery()` held | 409 |
| `Flash_409_FlashInFlight` | stub returns `ErrBusy` | 409 |
| `Flash_400_BadBody_NoFirmware` | empty body | 400 |
| `Flash_400_BadBody_TestPairAsymmetric` | only `test_command` set | 400 with explicit "both or neither" wording |
| `Flash_400_BadBody_TestCommandNotHex` | `test_command: "GG"` | 400 |
| `Flash_400_BadBody_OverSizeLimit` | body > 256 KB | 400 from `MaxBytesReader` |
| `Flash_200_SuccessShape` | stub returns clean Result | golden JSON; `outcome: "success"`, `stages.test.status: "ok"` |
| `Flash_200_RolledBackShape` | stub returns rolled-back Result | golden JSON; `outcome: "rolled_back_test_failed"`, `test_result.received` populated |
| `Flash_200_FailedNoRecoveryShape` | stub returns no-recovery Result | golden JSON; `recovery_hint` populated |
| `Disconnect_200_EmptyRegistry` | registry empty | 200, `{"released": 0}` |
| `Disconnect_200_PopulatedRegistry` | registry has 3 devices | 200, `{"released": 3}`, registry empty after |
| `SerialPortsDetailed_200` | fake opener returns 2 detailed ports | 200, JSON shape matches |

### 7.5 Coverage targets

- `internal/flasher` ≥ 85 % statement coverage.
- `internal/api/flash.go` ≥ 90 %.
- All tests pass on macOS, Linux, and Windows.
- `golangci-lint` (errcheck, staticcheck, unused, ineffassign, gosec) clean.
- `govulncheck ./...` clean.

## 8. Logging

slog → logship → Loki, same pipeline as today. Per-stage `slog.Info` lines plus a final summary.

```
slog.Info("flash_stage",   "port", "COM3", "stage", "backup",  "status", "ok",     "duration_ms", 8214)
slog.Info("flash_stage",   "port", "COM3", "stage", "verify",  "status", "failed", "duration_ms", 3105, "first_mismatch_offset", "0x1A40")
slog.Info("flash_stage",   "port", "COM3", "stage", "rollback","status", "ok",     "duration_ms", 11013, "verify_status", "ok")
slog.Info("flash_summary", "port", "COM3", "outcome", "rolled_back_verify_failed",
                            "firmware_sha256", "...", "backup_sha256", "...",
                            "total_duration_ms", 30448)
slog.Info("disconnect",    "released", 3)
slog.Info("flash_disabled","path", r.URL.Path) // at debug, mirroring raw_serial pattern
```

`slog.Debug` lines for STK500v1 internals (sync attempts, per-page addresses) are emitted only when `log.level: debug`; wire-byte traces are deliberately not logged at any level (32 KB per direction × 2 phases would drown logship). A `flashing.trace_wire_bytes` opt-in flag is a clean follow-up if real diagnosis demands it.

## 9. Concurrency model

| Op | Discovery gate | Registry mutex | Port handle | Flash mutex |
|---|---|---|---|---|
| `POST /devices/disconnect` | none | brief write (clear) | closes all | none |
| `GET /serial/ports/detailed` | none | brief read | none | none |
| `POST /flash/{port}` | non-acquiring read (`IsDiscovering`) | brief read (`len(reg.List())==0` check) | held across stages 2–5 + rollback, reopened for stage 6 | acquired in preflight, released on return |
| `POST /discover` (existing) | acquires write | replaces map | closes registry-held ports | n/a |
| `POST /serial/ports/{port}/command` (existing) | non-acquiring read | brief read | per-call open/close | n/a |

The flash mutex is package-level in `internal/flasher`. The `IsDiscovering` and registry-empty checks are re-evaluated *after* the flash mutex is acquired so that a `/discover` slipping in between body-parse and lock-acquire is caught.

If discovery starts while a flash is in flight, the discovery probe's `Opener.Open(port)` for that one port will be locked out by the flasher's handle and discovery skips it silently (existing OS-level behaviour for any busy port). No new logic.

## 10. Error response shape

Reuses `api.ErrorBody` (`{"error":"<code>","detail":"<readable>"}`).

| Status | When |
|---|---|
| 400 | Body parse / hex parse / Intel HEX parse / size / range / test-pair-asymmetric |
| 403 | `flashing.enabled = false` |
| 404 | Port not in `Opener.List()` |
| 409 | Registry non-empty, discovery in progress, or flash in flight |
| 500 | `Opener.List()` failed during preflight |

503 is not a flash response: I/O failures during the flash itself surface as 200 with an outcome of `failed_backup` / `failed_no_recovery` / `rolled_back_*`. The state machine ran to completion; HTTP-layer 503 would lose the per-stage detail and the backup payload.

## 11. Compatibility

- No breaking changes to existing endpoints, request bodies, response shapes, config fields, or persistent state.
- Configs written before this change parse cleanly: `Flashing.Enabled` defaults to `false`, `BackupDir = ""`, `KeepN = 10`. Operators opt in by editing YAML.
- A binary built with this change against a config that lacks the `flashing:` block behaves as `enabled: false` — no scaffold rewrite needed for upgrade.
- The two new `serial.Port` / `serial.Opener` methods (`SetBaudRate`, `SetDTR`, `OpenWithBaud`, `ListDetailed`) are additive; all existing call sites continue to use the original methods unchanged.

## 12. Build / release

No new dependencies. `go.bug.st/serial` already provides `GetDetailedPortsList`, `SetDTR`, and `SetBaudRate`. No changes to `Taskfile.yaml`, release-please config, or the Windows service worker.

Conventional Commits: lands as a single `feat(api): remote firmware flashing` PR. release-please bumps the minor on the next release. The four `bogdan-firmware` `.hex` golden fixtures (~80 KB each) add ~320 KB to the repo; well under any reasonable threshold.
