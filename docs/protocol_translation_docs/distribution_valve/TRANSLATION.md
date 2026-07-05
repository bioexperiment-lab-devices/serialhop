# Distribution Valve — JSON ↔ Serial Translation Algorithm

How the translator layer implements each command of `JSON_PROTOCOL.md` on top of the unmodified legacy firmware (`PROTOCOL.md`). Pure logic, no code.

**Design principles:**

1. The firmware blindly assumes position 0 at boot and offers no way to be told otherwise. The translator implements homing **virtually**: it tracks the *device's belief* separately from the *physical position* and translates every high-level target through the offset between them. The device never needs to know where it really is — only relative moves matter, and those are congruent under the offset.
2. The firmware updates its reported position the instant a move command is parsed (before the motor finishes), and provides no completion signal. The translator owns all motion timing.
3. There are 7 rotor slots: position 0 (all closed) plus outputs 1..N (N from `identify`, usually 6). All position arithmetic below is modulo `S = N + 1`.

## 1. Translator state

Persistent (keyed by port/configured device id — the firmware has **no serial number command**):

| Field | Meaning |
|---|---|
| `physical_position` | last known true rotor position (or null) |
| `device_belief_at_shutdown` | device's position counter when translator last saw it — used for restart recovery |
| `config` | `{default_rotation, hold_torque}` mirror |

Volatile:

| Field | Meaning |
|---|---|
| `homed` | bool |
| `device_belief` | what the firmware currently thinks its position is (tracked from boot=0 plus every commanded move) |
| `active_job` | `{job_id, target_physical, target_device, direction, started_at, estimated_duration}` |
| `job_history` | last 8 jobs |
| `connected_since` | basis for `uptime_ms` |

Timing constant: `SECONDS_PER_SLOT = 0.92` (460 step-pin toggles × 2000 µs each).

## 2. Serial primitives

Same mutex-guarded `TRANSACTION` primitive as the other devices: flush RX → write 5 bytes in one write → read exact expected reply length (per-byte timeout 500 ms) → one retry → `hardware_error` on second failure.

Unlike the other devices, the valve firmware **services serial during motion**, so queries never block — but replies received mid-move reflect the *target*, not reality; the translator must not interpret them as "arrived".

### Consistency check (the core safety mechanism)

Run **before every move and at every status refresh**:

```
CHECK_BELIEF():
  1. TRANSACTION([33,1,0,0,0], reply 4 bytes [30,1,1,pos])
  2. if pos == device_belief → consistent, done
  3. if pos == 0 and device_belief ≠ 0:
       // watchdog/power reboot: firmware reset its counter to 0
       if no move was in flight when the reboot could have happened:
          // rotor did not physically move — recover automatically:
          device_belief = 0
          re-push config (§4 configure frames)          // firmware config is RAM-only
          homed stays true; physical_position unchanged  // offset math absorbs the reset
       else:
          homed = false; physical_position = null        // reboot mid-move: rotor position unknown
  4. any other mismatch (pos ≠ 0, ≠ belief):
       // a command was lost, or someone else talked to the device
       homed = false; physical_position = null; raise alarm
```

This turns the firmware's most dangerous silent failure (reboot → wrong absolute positions forever) into either an automatic recovery or an explicit `unhomed` state.

## 3. Device probe / connection setup

```
1. TRANSACTION([1,2,3,4,0], reply [30,1,1,N]) → positions = N, S = N + 1
2. TRANSACTION([33,1,0,0,0], reply [30,1,1,pos]) → device_belief = pos
3. restart recovery:
     if persisted physical_position ≠ null
        and pos == persisted device_belief_at_shutdown:
          homed = true                       // device kept running while translator was away
     else homed = false                      // require explicit home
4. push config mirror: [35,1,rotation_code,0,0] and [35,2, hold?0:1, 0,0]
   // GAP: valve has no EEPROM — configuration must be re-sent after every device reboot
5. connected_since = now
```

## 4. Command translations

### `ping`

```
TRANSACTION([31,2,3,4,5], reply [30,1,1,pos])    // side-effect-free liveness probe
opportunistically feed pos into CHECK_BELIEF logic
return { uptime_ms: now − connected_since }       // GAP: true device uptime unknowable
```

### `identify`

From probe cache + static configuration. **Gaps:** no serial number command exists → `serial` comes from translator configuration (or null); `firmware_version` is a static configured string.

### `status`

```
1. if idle: CHECK_BELIEF()          // catches reboots even when nothing is happening
2. state = !homed ? "unhomed" : (active_job ? "moving" : "idle")
3. position        = (homed and idle) ? physical_position : null
   target_position = active_job ? active_job.target_physical : null
   job              = active_job (progress = elapsed / estimated, clock-driven)
   config           = translator mirror   // GAP: firmware config is write-only, not queryable —
                                          // the mirror is authoritative by construction since
                                          // the translator re-pushes it on every reboot detection
```

### `home`

```
1. active_job → busy
2. validate 0 ≤ position ≤ N → invalid_params
3. TRANSACTION([33,1,0,0,0]) → device_belief = reported pos   // resync belief first
4. physical_position = position; homed = true
5. persist physical_position and device_belief
6. return {homed: true, position}
```

No serial write is needed — homing is purely a translator-side declaration; all future moves are computed relative to it.

### `set_position`

```
1.  !homed → not_homed;  active_job → busy;  validate 0 ≤ target ≤ N
2.  CHECK_BELIEF() — abort with hardware_error/unhomed on mismatch
3.  target == physical_position → return an already-succeeded job immediately (no motion)
      // also essential for safety: in wrap mode the firmware interprets "move to the
      // current position" as a full 360° revolution — never let that frame through
4.  rotation mode:
      mode = params.rotation ?? config.default_rotation
      if mode differs from the last mode pushed to the device:
        TRANSACTION([35,1, code,0,0])        // 1=direct, 2=wrap, 3=shortest
5.  offset translation — the heart of virtual homing:
      delta         = (target − physical_position) mod S
      target_device = (device_belief + delta) mod S
      // Correctness: every firmware mode moves the rotor by a step count CONGRUENT to
      // (target_device − device_belief) mod S, so the FINAL position is always right.
      // Shortest mode also picks the same physical arc in any frame (it depends only on
      // the delta mod S). Direct/wrap modes, however, choose their arc from the SIGNED
      // device-frame difference — see the transit-path gap note below.
6.  slots to travel (mirror of the firmware's arithmetic, for the duration estimate):
      d = target_device − device_belief        // signed, in −N..N; never 0 (step 3)
      direct (1):   slots = |d|
      wrap (2):     slots = S − |d|
      shortest (3): slots = min(|d|, S − |d|)
      direction for the job result: "increasing" if the firmware will drive positions
      upward (direct: d > 0; wrap: d < 0; shortest: whichever arc won), else "decreasing"
7.  TRANSACTION([36,1,target_device,0,0], no reply)
8.  create job: estimated_duration = slots × SECONDS_PER_SLOT + 0.3 s margin
      // GAP: no completion signal — progress and completion are clock-simulated
9.  optimistically update device_belief = target_device (matches firmware, which updates
    its counter immediately on parse)
10. when the clock says the move is done:
      TRANSACTION([33,1,0,0,0]) → must report target_device
        match    → physical_position = target; persist; job succeeded
                   result {position, from_position, direction, duration_s: estimated}
        mismatch → homed = false; job failed (hardware_error)
      // note: this readback confirms the device is alive and processed the command;
      // it CANNOT confirm the rotor physically arrived (no encoder) — a stalled motor
      // is undetectable. GAP inherent to the hardware.
```

**Transit-path gap (direct/wrap modes only):** every port the rotor transits is momentarily
opened, so the *path* can matter to the plumbing, not just the destination. Direct and wrap
choose their arc from the signed device-frame difference; with a nonzero virtual-homing offset
that arc can differ from what the physical position numbers suggest (e.g. a physical 2→4 move
may travel the long way around through 0). The offset never changes on its own — it is fixed at
`home` time and only a device reboot disturbs it. Mitigation for path-sensitive installations:
establish a **zero offset** — bring the rotor physically to position 0, power-cycle the valve
(device belief resets to 0), then `home {position: 0}`. Shortest mode is unaffected.

### `stop`

The firmware has **no abort command**; motion always runs to completion (worst case ≈ `N × SECONDS_PER_SLOT` ≈ 5.5 s).

```
1. no active_job → return {state: idle} (no-op)
2. mark job cancel_requested; WAIT until its estimated end + margin
3. run step 10 of set_position (verification) — position knowledge is preserved
4. job state = cancelled (motion completed physically; the flag records intent)
5. return {state: homed ? "idle" : "unhomed", cancelled_job_id}
```

**Gap / spec deviation:** `JSON_PROTOCOL.md` specifies stop-aborts-motion with position becoming unknown. The legacy firmware cannot abort, so the translator implements the safer semantics available: `stop` *waits out* the short move and keeps the position valid. Callers must treat `stop` as "settle and report", latency ≤ ~6 s.

### `configure`

```
1. active_job → busy
2. for each provided field:
     default_rotation → TRANSACTION([35,1, code, 0,0])   // direct=1, wrap=2, shortest=3
     hold_torque      → TRANSACTION([35,2, value ? 0 : 1, 0,0])
        // firmware encoding is inverted: N3=0 means "hold energized", N3=1 means "release"
3. persist mirror; return the full effective config
   // persistence promise of the JSON spec is honored by the TRANSLATOR (mirror + re-push
   // on reboot), not by the device
```

### `get_job`

Pure translator-state read.

## 5. Concurrency & recovery rules

* One serial transaction at a time; one move at a time. `set_position` during a move → `busy` — **never** forwarded mid-move: the firmware would accept it and compute from its already-advanced counter while the rotor is between detents, desynchronizing physical tracking.
* Transaction failing twice → unreachable; fail active job with `homed = false` if it was mid-move (outcome unknown), then re-probe (§3) with backoff.
* Persist `physical_position` and `device_belief` on every successful move so a translator restart can recover `homed` without operator involvement (§3 step 3).
* Recommend a periodic idle-time `CHECK_BELIEF` (e.g. every 30 s) so reboots are detected promptly rather than at the next move.

## 6. Gap summary (JSON promises the legacy firmware cannot keep)

| High-level feature | Resolution |
|---|---|
| Abort in-flight motion | Impossible — `stop` waits for completion (≤ ~6 s), position stays valid |
| Transit path in `direct`/`wrap` modes | Arc is chosen in the device frame; with a nonzero homing offset the rotor may pass different intermediate ports (each transiently opened) than the physical numbering suggests. `shortest` is frame-invariant; zero the offset for path-sensitive plumbing |
| Physical arrival confirmation | Impossible (no encoder/home sensor) — readback only confirms the command was processed; stalls are invisible |
| Device-acknowledged homing | Virtual: translator-side offset between device belief and physical position |
| Config readback | Firmware config is write-only and RAM-only — translator mirror is authoritative, re-pushed on reboot |
| Serial number | Not queryable — configured per installation |
| True device uptime | Connection age reported; reboots detected via position-counter mismatch, not uptime |
| Real motion progress | Clock-simulated from 0.92 s/slot |
| Firmware version | Static configured string |
