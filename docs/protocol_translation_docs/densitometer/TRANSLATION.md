# Densitometer — JSON ↔ Serial Translation Algorithm

How the translator layer implements each command of `JSON_PROTOCOL.md` on top of the unmodified legacy firmware (`PROTOCOL.md`). Pure logic, no code.

**Design principle:** the device is used only as a *sensor and actuator*: it runs sweeps, returns raw 16-bit intensities, reads temperature, and drives the LED/thermostat. Everything else — slope fitting, absorbance math, temperature compensation, tube correction, job tracking, monitoring, buffering — lives in the translator. This avoids the firmware's lossy 2-byte fixed-point encoding (no negatives, integer part wraps at 255) and its inconsistent temperature compensation (`79 4` omits it, `78 4` applies it).

## 1. Translator state

Persistent (keyed by device serial, survives translator restart):

| Field | Meaning |
|---|---|
| `blank` | `{slope, temperature_c, measured_at}` or null — computed by the translator from raw sweep data |
| `tube_correction` | float, default 1.0 — applied only in the translator; device-side factor is forced to 1.0 |
| `thermostat` | `{enabled, target_c}` mirror of what was last commanded |

Volatile:

| Field | Meaning |
|---|---|
| `busy_until` | timestamp; device is unresponsive during sweeps (it does not service serial mid-sweep) — all serial traffic is blocked until this time |
| `active_job` | job object (id, kind, state, started_at, estimated_duration) |
| `job_history` | last 8 completed jobs |
| `monitoring` | `{enabled, interval_s, next_tick_at}` |
| `readings` | ring buffer of 64 `{seq, uptime_ms, absorbance, temperature_c}` |
| `seq_counter` | monotonically increasing measurement counter |
| `connected_since` | timestamp of successful probe — basis for reported `uptime_ms` |

## 2. Serial primitives

All device I/O goes through one mutex-guarded transaction primitive:

```
TRANSACTION(command_5_bytes, expected_reply_len, timeout):
  1. acquire port mutex
  2. if now < busy_until → wait until busy_until (or fail fast with busy, see per-command rules)
  3. flush RX buffer (discard stale bytes)
  4. write all 5 command bytes in one write
  5. if expected_reply_len == 0 → release mutex, return ok
  6. read exactly expected_reply_len bytes; per-byte timeout 500 ms,
     total timeout = max(timeout, expected_reply_len × 30 ms)
     (device inserts 5–20 ms gaps between reply bytes)
  7. on timeout → retry whole transaction once (flush first);
     second failure → surface as hardware_error("device not responding")
  8. release mutex, return reply bytes
```

Decoding helpers (logic, applied to reply bytes):
* fixed-point float: `value = byte_a + byte_b / 100`
* raw intensity record `[hdr, idx, lo, hi]`: `value = lo + 256 × hi`

Timing constants (tune per board):

| Constant | Value | Basis |
|---|---|---|
| `SWEEP_WAIT` | 6 s | full sweep (`78 3`/`78 4`): 20 levels × (50 ms settle + ~0.11 s for 1000 ADC reads) ≈ 3.5 s + main-loop slack, with margin |
| `SINGLE_LEVEL_WAIT` | 15 s | single-level read (`75 1`): 20 slots × 5000 ADC reads ≈ 12 s |
| `ARRAY_READ_TIMEOUT` | 3 s | 80 reply bytes × ~15 ms inter-byte delay |

## 3. Device probe / connection setup

On first contact (and after any suspected reboot):

```
1. send [1,2,3,4,0], expect 4 bytes → must be [70, _, _, channels]
2. send [71,0,0,5,0], expect 4 bytes [5,7,sn1,sn2] → serial = "<sn1>-<sn2>"
3. send [71,0,0,1,0], expect [1,2,wl_hi,wl_lo] → wavelength_nm = wl_hi×100 + wl_lo
4. force device tube correction to 1.0: send [75,3,0,0,0] (no reply)
   — from now on ALL tube correction happens in the translator; this write is EEPROM-persistent,
     so it survives device reboots and never needs re-sending
5. thermostat sync — also arms the REBOOT CANARY (see §5):
     read [76,2,0,0,0] → device set-point
     if a persisted thermostat mirror exists:
        readback == mirror        → in sync, nothing to do
        readback == 10.00         → device rebooted while unattended (boot default is 10):
                                     re-push the mirror via the set_thermostat procedure
        any other mismatch        → log alert, re-push the mirror
     else (first-ever contact): send [75,2,0,0,0] (disable) and persist mirror {enabled: false}
     // invariant established: the mirror value is NEVER 10 (valid values: 0, or 20..45),
     // while a freshly-booted device always reports 10 — so 10 ⇔ reboot, detectably
6. set connected_since = now
```

Step 4 is essential: the legacy factor is *multiplicative and persistent*; leaving a stale device-side factor would double-apply correction.

## 4. Command translations

### `ping`

```
1. TRANSACTION([71,2,3,4,0], reply 4 bytes [70,5,t_int,t_frac])
2. return { uptime_ms: now − connected_since }
```

**Gap:** the device has no uptime counter and no boot notification. `uptime_ms` is translator-tracked connection age, not true device uptime; a silent watchdog reboot is invisible.

### `identify`

Answered from data cached at probe time (§3). `firmware_version` is not queryable → report a fixed string configured per fleet (e.g. `"legacy"`). `model` likewise comes from translator configuration, not the device.

### `status`

```
1. state = monitoring.enabled ? "monitoring" : (active_job ? "measuring" : "idle")
2. if device not busy (now ≥ busy_until):
     temperature_c   ← TRANSACTION([76,0,0,0,0]) → fixed-point decode
     thermostat_target ← TRANSACTION([76,2,0,0,0]) → fixed-point decode
   else: reuse last cached values, mark them with their age
3. reboot canary check on the step-2 thermostat readback:
     if readback == 10.00 and mirror ≠ 10:
        → the device has REBOOTED since last seen:
          fail any active job (its data is gone), reset connected_since,
          re-push the thermostat mirror ([75,2,…] sequence), log an alert
          (device-side tube correction reloads as 1.0 from EEPROM — no action needed)
     any other mismatch ⇒ log warning, re-push the mirror
4. assemble:
     thermostat.enabled  = translator mirror (target ≥ 20 means enabled)
     thermostat.target_c = mirror value (confirmed by step 2/3)
     thermostat.heating  = null        // GAP: not queryable
     thermostat.cooling  = null        // GAP: not queryable
     calibration.blank   = translator-stored blank (or null)
     calibration.tube_correction = translator-stored factor
     last_measurement    = newest ring-buffer entry (or null)
```

**Gaps:** heater/cooler activity is never reported by the firmware → always `null`. Temperatures below 0 °C or the (impossible here) case ≥ 256 °C decode as garbage — clamp-and-flag if `t_int > 100`.

### Internal procedure: `RUN_SWEEP(trigger_command, wait_time)` — shared by measure/blank/read_raw

`wait_time` = `SWEEP_WAIT` for full 20-level sweeps (`78 …` triggers), `SINGLE_LEVEL_WAIT` for the much slower single-level read (`75 1 …` — the firmware takes ~12 s there).

```
1. if active_job exists → error busy
2. create job (state running, estimated_duration_s = wait_time + 2)
3. TRANSACTION(trigger_command, no reply)          // fire-and-forget: firmware never acks
4. busy_until = now + wait_time                    // device will NOT answer serial during the sweep
5. progress = elapsed / estimated (clock-driven; the device offers no progress signal)
6. after busy_until: liveness check — TRANSACTION([71,2,3,4,0]);
   retry up to 3× with 1 s spacing (the device may still be finishing)
   all fail → job state = failed (hardware_error)
7. TRANSACTION([79,1,0,0,0], reply 80 bytes, ARRAY_READ_TIMEOUT)
   parse 20 records → intensities[1..20]
   (validate: every record header must equal 105 and idx must be sequential 1..20;
    otherwise flush, retry the read once; second failure → job failed)
8. TRANSACTION([76,0,0,0,0]) → temperature_c at sweep time
9. return {intensities, temperature_c}
```

Slope computation (translator-side, replicating the firmware's point filter):

```
SLOPE(intensities):
  points = { (i, v) : i in 1..20, 0 < v ≤ 3000 }
  if |points| < 3 → error hardware_error("sweep unusable: detector dark or saturated")
  return least-squares slope of v over i
```

### `measure_blank`

```
1. RUN_SWEEP([78,3,0,0,0], SWEEP_WAIT)   // also makes the firmware store ITS OWN base — harmless,
                                  // keeps the physical button/display roughly consistent
2. slope = SLOPE(intensities)
3. persist blank = {slope, temperature_c, measured_at: now}
4. job succeeds with {slope, temperature_c, sweep: intensities}
```

### `measure`

```
1. if blank is null → error not_calibrated
2. RUN_SWEEP([78,4,0,0,0], SWEEP_WAIT)
3. slope = SLOPE(intensities)
4. absorbance_raw = |log10(blank.slope / slope)|
5. absorbance = absorbance_raw
                + (temperature_c − blank.temperature_c) × 0.0022    // firmware's own coefficient
   absorbance = absorbance × tube_correction
6. seq_counter += 1; append reading to ring buffer
7. job succeeds with the full measurement object (raw sweep included iff include_raw)
```

Note this is *more* correct than the legacy `79 4` readback, which drops temperature compensation and truncates to hundredths.

### `start_monitoring` / `stop_monitoring` / `get_readings`

The firmware's continuous mode (`78 5`) is **never used** — it saturates the device loop and starves serial handling. Monitoring is a translator scheduler:

```
start_monitoring:
  1. reject interval_s < 10 (sweep duration bound) → invalid_params
  2. monitoring = {enabled, interval_s, next_tick_at = now}
scheduler tick (translator timer):
  when now ≥ next_tick_at and no job is active:
    run the `measure` procedure as an internal job; on success the reading
    lands in the ring buffer; next_tick_at += interval_s
get_readings:
  return buffer entries with seq > since_seq (up to limit);
  dropped = max(0, oldest_available_seq − since_seq − 1)
stop_monitoring: disable the scheduler; also triggered by `stop`
```

### `set_thermostat`

```
1. if enabled:
     t = round(target_c)                       // GAP: firmware accepts whole °C only —
                                                // fractional set-points are rounded; echo the
                                                // actually-applied integer in the result
     reject t < 20 or t > 45 → invalid_params
   else: t = 0                                  // any value < 20 disables the thermostat
2. TRANSACTION([75,2,t,0,0], no reply)
3. wait ≥ 1.5 s — the firmware blocks ~1 s redrawing the display before it reads serial again
4. verify: TRANSACTION([76,2,0,0,0]) → decoded value must equal t (±0.01); mismatch → hardware_error
5. update and PERSIST the translator mirror; return {enabled, target_c: t}
```

**GAP — no device-side persistence:** the firmware force-resets the set-point to 10 °C (off)
at every boot, *ignoring its own EEPROM copy*, so the set-point cannot be made to survive a
device reboot. The translator provides persistence instead: the mirror is re-pushed whenever
the reboot canary fires (§3 step 5, `status` step 3, §5 idle poll). Between the reboot and its
detection — at most one canary-poll interval — the thermostat is silently off. The invariant
"mirror is never 10" (only 0 or 20–45) is what makes the canary sound.

### `set_tube_correction` / `calibrate_tube`

Pure translator operations — **no serial traffic**:

```
set_tube_correction: validate 0.5 ≤ factor ≤ 2.0; persist; return it
calibrate_tube:      last = newest completed measurement; none → not_calibrated
                     tube_correction = reference_absorbance / (last.absorbance / old_correction)
                     persist; return {tube_correction, based_on_seq: last.seq}
```

**Gap:** because correction is translator-side, the device's *own display* (button-triggered measurements) shows uncorrected absorbance. Accepted trade-off.

### `set_led`

```
1. validate 0 ≤ level ≤ 20
2. TRANSACTION([75,0,level,0,0], no reply)
3. no readback exists → optimistically return {level}   // GAP: unverifiable
```

### `read_raw`

```
level == null:  RUN_SWEEP([78,4,0,0,0], SWEEP_WAIT) but skip absorbance math; return raw intensities
level == n:     RUN_SWEEP([75,1,n,0,0], SINGLE_LEVEL_WAIT); the firmware fills all 20 slots with
                readings taken at that one brightness (this variant takes ~12 s — it samples 5×
                more per slot) → return the array (or its mean) tagged with the level
```

### `stop`

```
1. TRANSACTION([70,0,0,0,0], no reply)     // stops device-side continuous mode (defensive), LED off
2. cancel active_job (state = cancelled), disable monitoring
3. return {state: "idle", cancelled_job_id}
```

If the device is mid-sweep (`now < busy_until`), the `70` command sits in the device's 64-byte RX buffer and executes when the sweep ends. **Gap:** a sweep in progress cannot be physically aborted; `stop` cancels the *job bookkeeping* immediately but the hardware finishes its sweep (≤ ~6 s).

### `get_job`

Served entirely from translator job registry.

## 5. Concurrency & recovery rules

* One serial transaction at a time (mutex); one job at a time (`busy` otherwise).
* Never send anything while `now < busy_until` except queued-by-design `stop`.
* If any transaction times out twice: mark device `unreachable`, fail the active job, re-run the probe (§3) with backoff. A successful re-probe resets `connected_since`; its step 5 re-pushes the thermostat mirror if a reboot happened (the tube-correction force-to-1 persists in device EEPROM and needs no re-send).
* **Idle canary poll:** when idle, read `[76,2,0,0,0]` every ~30 s and apply the `status` step-3 canary logic, so device reboots are detected promptly (a reboot silently disables the thermostat — see `set_thermostat`).
* The physical button on the device can trigger sweeps the translator doesn't know about; if a reply to `79 1 0` looks inconsistent (headers wrong), flush and retry — the button session may have interleaved. Advise operators not to use the button under remote control.

## 6. Gap summary (JSON promises the legacy firmware cannot keep)

| High-level feature | Resolution |
|---|---|
| `thermostat.heating` / `.cooling` flags | Always `null` — firmware never reports actuator state |
| Thermostat persistence across device reboots | Firmware boots with the set-point forced to 10 °C (off), ignoring its own EEPROM; the translator re-pushes its mirror when the canary detects the reboot — thermostat is off for up to one poll interval |
| True device uptime | `uptime_ms` = translator connection age; reboots are detected only via the thermostat canary (readback of exactly 10.00), not in real time |
| Fractional thermostat set-point | Rounded to whole °C; applied value echoed back |
| Abort a running sweep | Impossible; `stop` cancels bookkeeping, hardware finishes (≤ 6 s) |
| `set_led` verification | None; optimistic success |
| Progress within a sweep | Simulated from wall clock, not real |
| Firmware version in `identify` | Static configured string |
| Sub-zero temperatures | Legacy encoding cannot express them; flagged as invalid if suspected |
