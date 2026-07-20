# Peristaltic Pump — JSON ↔ Serial Translation Algorithm

How the translator layer implements each command of `JSON_PROTOCOL.md` on top of the unmodified legacy firmware (`PROTOCOL.md`). Pure logic, no code.

**Design principles:**

1. The firmware knows only *steps* and *step periods*. All unit conversion (ml, ml/min), job/progress tracking, and pause-state accounting live in the translator, for the current connection only — nothing here is persisted to disk. Volume calibration is the exception: the device's 3 EEPROM calibration bytes are the single source of truth, re-read and trusted on every attach; the translator holds no separate calibration store.
2. The firmware sends **no acknowledgment and no completion signal** for motion commands — with one exception: the "calibration run" command `18` replies with elapsed microseconds when the run finishes. The translator exploits this: **every plain forward dispense is issued as command `18` instead of `15`**, turning the reply into a genuine hardware completion event with a measured duration. Reverse runs, suckback runs, and gradient runs cannot use this trick and fall back to clock-based simulation.
3. Only the hardware serial port is used; the Bluetooth path is ignored.

## 1. Translator state

Sourced from the device on every attach, not persisted by the translator (§3 step 3):

| Field | Meaning |
|---|---|
| `ml_per_step` | float or 0 (uncalibrated); read from `cal_mirror` in the identify reply and trusted immediately — no separate store, no serial number to key one by |

Volatile:

| Field | Meaning |
|---|---|
| `state` | `idle` \| `rotating` \| `dispensing` \| `calibrating` \| `paused` |
| `active_job` | `{job_id, kind, direction, target_steps, del_time_us, started_at, active_elapsed, paused_at}` |
| `job_history` | last 8 completed jobs |
| `pause_assumed` | translator's belief about the firmware's pause toggle (see §4 `pause`) |
| `last_config_sent` | the last `[10,…]` parameter frame, to avoid redundant sends |
| `connected_since` | basis for reported `uptime_ms` |
| `cal_set_at` | wall-clock time of the last `set_calibration` this connection; used to compute `set_at_uptime_ms` relative to `connected_since` — absent (and the field omitted) across a reconnect |

## 2. Serial primitives and conversions

Same mutex-guarded `TRANSACTION` primitive as the other devices: flush RX → write 5 bytes in one write → read exactly N expected reply bytes (per-byte timeout 500 ms) → one retry on timeout → `hardware_error` on second failure.

**Command reception stalls the motor ~100 ms** (firmware inserts 20 ms delays between the 5 received bytes). Rule: while a motion job is active, send nothing except `stop`, `pause`/`resume`, and end-of-job verification.

### Speed → byte pair

The firmware's step half-period is `DelTime = N3 × N4 × 100 µs` (each byte 1–255; `N3 ≤ 1` is treated as 1). One full step = `2 × DelTime`.

```
SPEED_TO_BYTES(speed_ml_min):
  1. require ml_per_step ≠ null → else not_calibrated
  2. steps_per_s  = speed_ml_min / 60 / ml_per_step
  3. del_time_us  = 500000 / steps_per_s
  4. reject del_time_us < MIN_DELTIME_US (translator config, default 400 — protects
     against stalling the motor) or > 6_502_500 → invalid_params("speed out of range")
  5. P = round(del_time_us / 100)
  6. N3 = ceil(P / 255); N4 = round(P / N3)          // both now in 1..255
  7. actual_del_time_us = N3 × N4 × 100
  8. return (N3, N4, actual_del_time_us)             // echo the ACTUAL speed in responses:
                                                     // actual_ml_min = 30_000_000 × ml_per_step / actual_del_time_us
```

Speed is quantized; every response reports the actually-applied speed, not the requested one.

### Volume → step count

```
steps = round(volume_ml / ml_per_step)     // sent as 32-bit big-endian in bytes N2..N5
reject steps < 1 or steps > 2_000_000_000 → invalid_params
```

### Duration estimate

`estimated_duration_s = steps × 2 × del_time_us / 1e6` (+ suckback and gradient adjustments, see below).

## 3. Device probe / connection setup

```
1. Discovery already ran the universal probe (PROTOCOL.md §3: [1,2,3,4,181],
   reply [10, c1, c2, c3]) before the translator's attach step even starts;
   attach reuses that reply directly — cal_mirror = (c1<<16)+(c2<<8)+c3.
2. No device serial number: no TRANSACTION is sent for one. No pump firmware
   in the field answers opcode 11 (PROTOCOL.md §4), so serial stays empty,
   same as the valve — attach performs zero serial transactions of its own.
3. Read `ml_per_step = cal_mirror / 1e8` from the identify reply and trust it.
   The device's EEPROM is the single source of truth: it is re-read on every
   attach, and `set_calibration` writes it back via cmd 13 and verifies the
   write by reading identify again.
4. connected_since = now; state = idle; pause_assumed = "running-allowed"
```

## 4. Command translations

### `ping`

```
TRANSACTION([1,2,3,4,181], reply 4 bytes)   // identify frame — chosen because it writes
                                             // NOTHING to EEPROM, so it is safe for
                                             // frequent liveness polling
return { uptime_ms: now − connected_since }  // GAP: true device uptime unknowable
```

Note: it briefly flashes `CO` on the pump's display — cosmetic only.

### `identify`

Served from probe cache. `firmware_version` and `model`: static configured strings (not queryable). `capabilities.speed_ml_min` limits are computed from `ml_per_step` and `MIN_DELTIME_US`; null when uncalibrated.

### `status`

Served **entirely from translator state** — the firmware has no state-query command at all.

```
state, job (with progress = active_elapsed / estimated_duration, clock-driven,
frozen while paused), direction, speed, dispensed_ml = progress × target volume,
calibration from translator DB.
```

**Gap (major):** the front-panel buttons (start/stop/reverse/speed±) act directly on the firmware and are invisible to the translator, so `status` reflects only remotely-commanded activity. Operating rule: panel use during remote control voids state tracking; the translator can partially detect trouble only when end-of-job verification fails.

### `rotate`

```
1. if active_job → busy   (a bare `rotating` state may be retargeted freely)
2. (N3, N4, actual) = SPEED_TO_BYTES(speed_ml_min)
3. cmd = 11 if direction == "forward" else 12       // polarity configurable per installation
4. TRANSACTION([10, 0, N3, N4, 0], no reply)
   // arming frame — REQUIRED: commands 11/12 do NOT touch the firmware's pause toggle,
   // so a rotate sent while the device happens to be paused would silently not move.
   // Command 10 is the only serial command that forces the toggle to "running"; it also
   // clears any leftover gradient mode. Reset pause_assumed = running-allowed here.
5. TRANSACTION([cmd, 0, N3, N4, 0], no reply)       // this frame both sets speed and starts
6. state = rotating; store direction/actual speed; return them
```

Retargeting (new `rotate` while rotating): send both frames again — the firmware accepts them mid-run (serial is polled between steps; the arming frame briefly de-energizes the motor, which is acceptable for continuous rotation).

### `rotate_raw`

Same two-frame send as `rotate` but bypasses calibration:

```
del_time_us = clamp(round(10000 / speed_pct), MIN_DELTIME_US, 6_502_500)
              // 100% → 100 µs, 1% → 10 ms half-period (the MIN_DELTIME_US guard applies)
then P = max(1, round(del_time_us / 100)) and factorize into N3, N4
     as in SPEED_TO_BYTES steps 5–7
```

### `dispense`

```
1.  if active_job or state == rotating → busy
2.  steps  = VOLUME_TO_STEPS(volume_ml)
3.  (N3, N4, del_time) = SPEED_TO_BYTES(speed_ml_min)
4.  suckback: if drop_suckback_ml > 0:
      drop_unit_ml = 100 × ml_per_step               // firmware drop quantum = 100 steps
      drop_mult    = clamp(round(drop_suckback_ml / drop_unit_ml), 2, 255)
      actual_suckback_ml = drop_mult × drop_unit_ml  // echo actual value; GAP: quantized,
                                                     // minimum ≈ 200 steps worth of volume
      steps += 100 × drop_mult
      // CRITICAL: the firmware's forward leg equals the COMMANDED count and it then
      // retracts the drop, netting (commanded − drop). Inflating the commanded count by
      // the drop makes the net delivered volume equal volume_ml, as the JSON spec promises.
    else drop_mult = 0
5.  gradient: if speed_profile present:
      // GAP: the firmware supports exactly TWO hardwired gradient shapes. The profile's
      // endpoints and shape are NOT programmable; only the direction of change is honored.
      grad_flag = 12 if start_ml_min < end_ml_min else 21
      constraints: gradient only works with direction == "forward" and drop_mult == 0
                   (firmware computes gradient coefficients only for command 15)
                   → otherwise invalid_params("gradient unsupported with reverse/suckback")
      respond with "speed_profile": {"applied": "hardware-fixed quadratic ramp",
                                     "start_ml_min": null, "end_ml_min": null}
    else grad_flag = 0
6.  configuration frame: TRANSACTION([10, drop_mult, N3, N4, grad_flag], no reply)
      // sets DelTime, DropMult, gradient mode; forces the pause toggle to "running"
      // (reset pause_assumed) and resets the driver — safe, the pump is idle.
      // In gradient mode the firmware overrides this speed with its fixed ramp;
      // N3/N4 are then inert.
7.  motion frame — pick the opcode by capability:
      forward, no suckback, no gradient → opcode 18   // completion reply available!
      forward + gradient                → opcode 15   // timer only
      forward + suckback                → opcode 17   // timer only; firmware runs the full
                                                      // commanded count forward (which now
                                                      // includes the drop), then retracts the drop
      reverse                           → opcode 16   // timer only
    TRANSACTION([opcode, s3, s2, s1, s0], no reply now)   // steps as 32-bit big-endian
8.  create job; estimated_duration:
      plain:    steps × 2 × del_time / 1e6
      suckback: (2 × steps + 400 × drop_mult) × del_time / 1e6 + 0.1
                // steps already includes the drop; the 200·drop_mult reverse toggles run at
                // doubled period, plus the firmware's 100 ms turnaround pause
      gradient: numerically integrate the firmware ramp: half-period(k) = sqrt(1/(A + B·k))
                for k = (2 × steps)..1 (the firmware counts pin toggles), where A, B are
                derived from the fixed endpoints T0 = 300 µs, TE = 30000 µs (mirror the
                firmware formula; the requested speed_ml_min plays no role here)
9.  completion:
      opcode 18: wait for the 4-byte big-endian reply (elapsed µs);
                 timeout = estimate × 1.5 + 5 s, extended by any paused time;
                 on reply → job succeeded, duration_s = reply / 1e6 (measured, not estimated)
      others:    clock-based; when active_elapsed ≥ estimate → grace wait 0.5 s → job succeeded
10. end-of-job verification & panel disarm (always):
      TRANSACTION([18,0,0,0,0], reply 4 bytes elapsed µs)
      — (a) confirms the device is alive and responsive after the run;
        (b) overwrites the EEPROM "last command" (which now holds this dispense) with a
            zero-step opcode-18 run, so a later press of the physical START button replays
            a harmless no-op instead of RE-RUNNING THE LAST DISPENSE. This defuses the
            firmware's most dangerous standalone behavior. (Opcode 11 is not used here —
            no pump firmware in the field implements it; see PROTOCOL.md §4.)
11. job result: {dispensed_ml: volume_ml, duration_s, mean_speed_ml_min, suckback_ml: actual}
```

**Gap:** delivered volume is never measured by hardware; `dispensed_ml` on success is the commanded volume (trustworthy only as far as the stepper didn't stall), and on cancellation it is a clock-based estimate.

### `pause` / `resume`

The firmware's pause (`19`) is a **blind toggle** — there is no way to read the current pause state.

```
pause:
  1. no active motion → error busy(details.state="idle")
  2. TRANSACTION([19,0,0,0,0], no reply)
  3. pause_assumed = paused; freeze job clock (record paused_at, accumulate active_elapsed)
  4. return {state: paused, dispensed_ml estimate}
resume:
  1. TRANSACTION([19,0,0,0,0], no reply)
  2. pause_assumed = running; unfreeze job clock
```

**Gap:** if the operator presses the panel STOP (also a toggle) between the translator's `pause` and `resume`, translator belief inverts relative to reality and cannot be re-synchronized by query. Mitigations: (a) the end-of-job verification in dispense step 10 catches "job never finished" via timeout; (b) every `dispense`, `rotate` and `stop` sends a command-10 frame, which unconditionally forces the firmware toggle to "running" — so a desync can never survive past the current job; (c) document that panel and remote control are mutually exclusive.

### `stop`

```
1. TRANSACTION([10,0,0,0,0], no reply)
   // firmware: clears the remaining step count, disables the driver — takes effect
   // within one step period because serial is polled between steps.
   // Side effects: resets DropMult and Mult to 1 (irrelevant — every dispense re-sends the
   // full configuration frame) and forces the pause toggle to "running", so also reset
   // pause_assumed = running-allowed: stop doubles as the pause-state resync point.
2. if a command-18 job was awaiting its reply: abandon the read (the reply will never come —
   the firmware only replies if the run finishes on its own)
3. cancel active_job / leave rotating state;
   dispensed_ml = (active_elapsed / estimated_duration) × volume_ml   // estimate
4. verification: TRANSACTION([1,2,3,4,181]) must answer → else hardware_error
5. return {state: idle, cancelled_job_id, dispensed_ml}
```

### `start_calibration`

```
1. busy check
2. steps = CAL_STEPS (translator config, default 20000 — big enough to weigh accurately)
3. del_time from speed_pct via the rotate_raw formula (default 50%)
4. TRANSACTION([10, 0, N3, N4, 0]) then TRANSACTION([18, s3, s2, s1, s0])
5. job (kind=calibrating); wait for the 4-byte reply as in dispense step 9
6. job result: {steps, duration_s: measured from reply}
7. panel-disarm run as in dispense step 10
```

### `set_calibration`

```
variant A (from a calibration job):
  1. look up job → not found / not succeeded → invalid_params
  2. ml_per_step = measured_volume_ml / job.steps
variant B (direct): ml_per_step given
then:
  3. sanity: 1e-6 ≤ ml_per_step ≤ 0.1 → else invalid_params
  4. update in-memory ml_per_step and cal_set_at = now; no on-disk store — the
     device's EEPROM (step 5) is the only persistence layer
  5. mirror to device: v = round(ml_per_step × 1e8), clamp to 24 bits
     TRANSACTION([13, 0, v>>16, (v>>8)&255, v&255], no reply)
     // the write step 6 verifies
  6. verify mirror: TRANSACTION([1,2,3,4,181]) → returned cal bytes must equal v
     (identify conveniently returns exactly these bytes); mismatch → hardware_error
  7. refresh published capabilities (speed_ml_min limits now derivable)
```

### `get_calibration` / `get_job`

Pure translator-state reads, no serial traffic.

## 5. Concurrency & recovery rules

* One serial transaction at a time; one job at a time.
* While a motion job runs, permitted traffic: `stop`, `pause`, `resume` frames (all reply-less) and the awaited command-18 reply only. `status`/`get_job` are answered from memory, and `ping` too — sending the identify frame mid-run could interleave its 4-byte reply with a command-18 completion reply arriving at the same moment.
* Transaction failing twice: mark unreachable, fail active job, re-probe with backoff. After a successful re-probe assume a possible device reboot: state = idle, `pause_assumed` = running-allowed (the firmware boots that way), warn if a job was in flight (its outcome is unknown — GAP: a watchdog reset mid-dispense silently loses the remaining volume).
* EEPROM wear: every motion command writes ~10 EEPROM bytes on the device (~100k-cycle endurance). The translator must not use motion frames for polling; liveness polling uses the `[1,2,3,4,181]` identify frame exclusively (opcode `11` is unimplemented in the field regardless — PROTOCOL.md §4).

## 6. Gap summary (JSON promises the legacy firmware cannot keep)

| High-level feature | Resolution |
|---|---|
| Query device run/pause state | Impossible — fully simulated in translator; panel use breaks it |
| `speed_profile` endpoints & shape | Only ramp *direction* honored; endpoints hardware-fixed, echoed as `null` |
| `speed_ml_min` in gradient mode | Ignored by the hardware (the fixed ramp overrides it) |
| Gradient with reverse or suckback | Rejected `invalid_params` (firmware computes gradient only for opcode 15) |
| Exact delivered volume on cancel/pause | Clock-based estimate only |
| Completion signal for reverse/suckback/gradient runs | Timer simulation (forward plain runs get a real signal via opcode 18) |
| `drop_suckback_ml` resolution | Quantized to 100-step units, minimum 2 units; actual value echoed |
| Speed resolution | Quantized to `N3×N4×100 µs` grid; actual speed echoed |
| True device uptime / reboot detection | Connection age; mid-job watchdog reset detected only as a timeout |
| Firmware version | Static configured string |
