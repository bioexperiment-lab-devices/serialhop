# Peristaltic Pump — Communication Protocol

Firmware: `sketch_10Peristalt_ArMini_1119Grad4.ino`
Device family code: **10** (peristaltic pump, one stepper motor)

## 1. Physical layer

| Parameter | Value |
|---|---|
| Primary interface | Hardware UART (`Serial`), USB‑COM port, **9600** baud, 8N1 |
| Secondary interface | Bluetooth on SoftwareSerial (RX = D11, TX = D12), **9600** baud |
| Watchdog | 4 s — reset on every accepted command |

Both interfaces speak the same protocol; the firmware replies on whichever interface the command arrived from. Serial has priority: Bluetooth is only polled when no serial bytes are pending.

## 2. Framing

Every command is exactly **5 raw binary bytes**, no header/checksum/terminator:

```
[N1] [N2] [N3] [N4] [N5]
 cmd  par1 par2 par3 par4
```

* Bytes are read with 20 ms `delay()` between reads — send all 5 bytes in one write.
* Valid `N1`: `1` (identification) or `10–19`. On **Bluetooth** additional sanity checks apply: `N1 > 19` aborts the read; `N1 == 13` or `N1 == 19` requires `N2 == 0`.
* Replies are raw byte sequences (4 bytes) with 10–20 ms pauses between bytes.

## 3. Identification handshake

| Interface | Host sends | Device replies (4 bytes) |
|---|---|---|
| Serial | `1 2 3 x x` | `10, cal_1, cal_2, cal_3` |
| Bluetooth | `1 2 3 4 181` (N4 = DevNumber2, N5 = DevNumber3) | `10, cal_1, cal_2, cal_3` |

* First byte `10` identifies the device as a peristaltic pump.
* `cal_1..cal_3` are the **calibration bytes** from EEPROM; the calibrated volume is
  `V = (cal_1 << 16) + (cal_2 << 8) + cal_3` (units defined by the host's calibration procedure; the firmware uses `V/10` for its speed display).
* The display briefly shows `CO` (command came via COM) or `Sn` (via Bluetooth).

## 4. Command reference

`N1` = 10–19. Except for `19` (pause) and `13` (calibration write), every accepted command is also **saved to EEPROM** and re-executed when the operator presses the physical START button (see §7).

### `10` — Configure / stop

`10 D M S G`

| Byte | Meaning |
|---|---|
| N2 = `D` | Drop multiplier `DropMult` (used by command 17). `0/1` → 1, `>1` → value |
| N3 = `M` | Speed multiplier `Mult` (`≤1` → 1) |
| N4 = `S` | Speed base: step half-period `DelTime = S × Mult × 100` µs (only if both `M>0` and `S>0`) |
| N5 = `G` | Gradient mode: `12` = increasing-speed gradient, `21` = decreasing, anything else = off |

Side effects: pauses the pump (`iPause=1`), de-energizes the motor, resets step counter. Selecting a gradient forces `DelTime = 30000` µs as the starting period; the actual profile is computed when the next `15`/`16` run starts.

### `11` / `12` — Run continuously

| Bytes | Action |
|---|---|
| `11 x M S 0` | Run **forward** until stopped (motor steps indefinitely) |
| `12 x M S 0` | Run **reverse** until stopped |

Speed is taken from N3/N4 exactly as in command 10: `DelTime = S × M × 100` µs per half-step (if `S ≤ 1` it defaults to `1`, i.e. very fast — always send a sane N4).

Stop with `19` (pause toggle), `10`, or the physical STOP button.

### `11` — Ping / serial number (special form, N5 = 5)

| Bytes | Reply (4 bytes) |
|---|---|
| `11 2 3 4 5` | `10, SerNum1, SerNum2, 1` → e.g. `10, 26, 25, 1` (year 26, unit 25) |
| `11 2 3 10 5` | none — shows the serial number on the 7-segment display; blocks the device for ~4 s |

The ping form does **not** start the motor (N1 is cleared internally). Note it is still written to EEPROM as the "last command" (firmware quirk — see §8).

### `15` / `16` — Pump a metered volume (counted steps)

`15 A B C D` (forward) / `16 A B C D` (reverse)

Steps to execute (32-bit big-endian from the 4 parameter bytes):

```
StepM1 = ((A<<24) + (B<<16) + (C<<8) + D) × 2
```

The ×2 is internal (the step pin is toggled, two toggles = one pulse) — the host sends the **number of full steps** as a plain 32-bit big-endian value in N2..N5.

Speed: uses the current `DelTime` (set previously by command `10` — the parameter bytes here are all consumed by the step count).

If a gradient was armed by command `10` (N5 = 12 or 21), the step period is swept between ~300 µs and ~30000 µs over the whole run (quadratic profile), giving a flow-rate gradient.

When the count finishes the motor is de-energized (`ENABLE` high).

### `17` — Dispense with drop-suckback

`17 A B C D` — same 32-bit step count as `15`, but with automatic drop-suckback: the pump runs **forward for exactly the commanded step count**, then reverses by

```
drop = 100 × DropMult full steps    (DropMult set by command 10 parameter N2;
                                     DropDrop = 50, doubled to 100 at boot)
```

The reverse leg runs at half speed (the firmware doubles the step period at the turnaround, after a 100 ms pause). **Net displaced volume = commanded steps − drop** — a host that wants to net-deliver a target volume must add the drop to the commanded count. Used to prevent a hanging drop at the outlet.

### `18` — Calibration run (timed)

`18 A B C D` — runs forward the given 32-bit number of steps (like `15`) and, when the run completes, replies with the **elapsed time in microseconds** as 4 raw bytes, **big-endian**:

```
reply: [µs >> 24] [µs >> 16 & FF] [µs >> 8 & FF] [µs & FF]
```

The host uses (steps, time, weighed volume) to compute the calibration constant it then stores with command `13`.

### `13` — Store calibration bytes

`13 0 c1 c2 c3` — writes `c1 c2 c3` to EEPROM (addresses 21–23, mirrored at 31–33). The calibrated volume reported at identification becomes `(c1<<16)+(c2<<8)+c3`.

Over **Bluetooth** the command must be received **three times in a row** before it executes (protection against radio noise). Over serial it executes immediately. Not stored as a "last command".

### `19` — Pause / resume toggle

`19 0 x x x` — toggles run/pause. Not stored in EEPROM.

## 5. Speed model

* The motor is stepped by toggling D2 every `DelTime` microseconds ⇒ full-step period = `2 × DelTime`.
* `DelTime = N4 × N3 × 100` µs, range enforced by buttons ~100 … 100000 µs.
* Flow display: `flow = VcalibrV / (0.000333 × DelTime + 0.01232) / 10000` (units per the host calibration).

## 6. Typical host session

```
→ 1 2 3 0 0            # discover                ← 10 c1 c2 c3
→ 11 2 3 4 5           # ping / serial number    ← 10 26 25 1
→ 10 1 2 30 0          # set speed: DelTime = 30×2×100 = 6000 µs, no gradient
→ 15 0 0 19 136        # pump 5000 steps forward (0x00001388)
→ 18 0 0 39 16         # calibration: 10000 steps  ← 4 bytes elapsed µs
→ 13 0 c1 c2 c3        # store computed calibration
```

## 7. EEPROM map & standalone operation

| Address (mirror) | Content |
|---|---|
| 1–5 (11–15) | Last command `N1..N5` (except types 10, 13, 19) |
| 6 (16) | Speed multiplier `Mult` (from cmd 10 N3) |
| 7 (17) | Speed base (from cmd 10 N4) |
| 8 (18) | Drop multiplier (from cmd 10 N2) |
| 21–23 (31–33) | Calibration bytes `cal_1..cal_3` |

Every value is stored twice; on read the copies must match or defaults are used. Pressing the **START** button (D8) reloads and re-executes the stored command (the **REVERS**-side start, D6, swaps 11↔12 and 15↔16 first). Other panel buttons: STOP (D7), speed up (D10), speed down (D9). This lets the pump repeat its last job with no host attached.

## 8. Quirks and gotchas

* No checksum/terminator; a dropped byte desynchronizes parsing until the `N1` range filter recovers.
* The serial port **is** polled between motor steps (once per half-step-period loop pass), so commands — including a stop via `10` or pause via `19` — take effect mid-run. However, receiving a command stalls the motor for ~100 ms (5 × 20 ms inter-byte delays), so avoid chatter during precision dispensing.
* The ping (`11 2 3 4 5`) is written to EEPROM as the last command before it is recognized as a ping; a subsequent START-button press may therefore replay it (harmless — it's a no-op for the motor, but it overwrites the previously stored job).
* `11`/`12` with `N4 ≤ 1` gives `DelTime = 100` µs — near the maximum step rate; always set an explicit speed.
* Replies have 10–20 ms gaps between bytes; a 4-byte reply takes ~50 ms.
