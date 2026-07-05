# Distribution Valve (Radial Switch) — Communication Protocol

Firmware: `sketch_30SwitchRadial_D1_Ping.ino`
Device family code: **30** (radial flow switch: 6 outputs driven by one stepper; at most one output open, position 0 = all closed)

## 1. Physical layer

| Parameter | Value |
|---|---|
| Interface | Hardware UART (`Serial`), USB‑COM port |
| Baud rate | **9600**, 8N1 |
| Watchdog | 4 s, serviced continuously |

Serial only — no Bluetooth, no display.

## 2. Framing

Every command is exactly **5 raw binary bytes**, no header/checksum/terminator:

```
[N1] [N2] [N3] [N4] [N5]
 cmd  par1 par2 par3 par4
```

* Bytes are read with 20 ms `delay()` between reads — send all 5 bytes in one write.
* **Input validation:** the command is discarded unless `N1 == 1` (identification) or `30 ≤ N1 ≤ 39`.
* Replies are 4 raw bytes with 10–20 ms gaps between them.
* Unlike the pump, this firmware **keeps polling the serial port while the motor is moving** (`my_delay()` calls `ComPortRead()`), so a new position command can arrive mid-rotation. Avoid sending a new `36` before the previous move completes — the position bookkeeping assumes the previous move finished.

## 3. Identification handshake

| Host sends | Device replies (4 bytes) |
|---|---|
| `1 2 3 4 x` | `Name1 Name2 Name3 Name4` = **`30 1 1 6`** |

* `Name1 = 30` — device type (flow switch)
* `Name4 = 6` — number of switch positions (a 2-position build reports `2`)

## 4. Command reference

### `30` — Parameter query

| Bytes | Reply (4 bytes) |
|---|---|
| `30 0 0 0 0` | `30 1 1 6` — same payload as the identification reply |

### `31` — Ping ("Are you OK?")

| Bytes | Reply (4 bytes) |
|---|---|
| `31 2 3 4 5` | `30, 1, 1, P` where `P` = current switch position (0–6) |

### `33` — Query current position

| Bytes | Reply (4 bytes) |
|---|---|
| `33 1 0 0 0` | `30, 1, 1, P` — current position of motor 1 |

(Identical payload to the ping; `31` verifies liveness, `33` is the semantic "where are you" query.)

### `35` — Configuration

| Bytes | Action |
|---|---|
| `35 1 R x x` (R = 1–3) | Set rotation method: `1` = **direct** (move by the signed position difference; never crosses the 6↔0 boundary), `2` = **complementary/wrap** (always travels the other arc, crossing the 6↔0 boundary), `3` = **shortest path** (default). Note: the firmware's source comment calls 1/2 "clockwise/counter-clockwise", but the code implements direct/complementary moves — direction depends on the sign of the position difference in both modes |
| `35 2 0 x x` | Hold mode ON: keep the stepper energized after a move (holding torque, coil stays powered) |
| `35 2 1 x x` | Hold mode OFF (default): de-energize the stepper when the move completes |

### `36` — Go to position

| Bytes | Action |
|---|---|
| `36 1 P 0 0` (P = 0–6) | Rotate motor 1 to position `P`. `0` = all channels closed; `1–6` = open channel P |

No reply. Motion parameters:

* **460 step-pin toggles per position** (`DeltaStep = 460`), toggle period `DelTime = 2000` µs ⇒ one adjacent-position move takes ~0.9 s; worst case ~2.8 s (3 positions) in shortest-path mode, up to ~6.4 s (7 positions) in modes 1/2.
* Direction and step count are computed from the difference between current and requested position according to the configured rotation method.
* On completion the motor is de-energized unless hold mode is on.

Poll `33 1 0 0 0` after issuing a move; when the reply's last byte equals the requested position **and** enough time has passed for the motion, the valve is in place (the firmware updates its position variable immediately on receipt, so combine with a worst-case delay of ~3 s rather than relying on the reply alone).

## 5. Typical host session

```
→ 1 2 3 4 0        # discover                    ← 30 1 1 6
→ 31 2 3 4 5       # ping                        ← 30 1 1 0   (position 0)
→ 35 1 3 0 0       # shortest-path rotation
→ 36 1 4 0 0       # open channel 4              (no reply, ~1.8 s motion)
→ 33 1 0 0 0       # confirm                     ← 30 1 1 4
→ 36 1 0 0 0       # close all
```

## 6. Quirks and gotchas

* **Position is volatile.** There is no EEPROM and no homing sensor: at power-up the firmware assumes position 0. If the valve was left elsewhere (or steps were lost), the physical and reported positions diverge. Hosts should drive the valve to a known state at startup, ideally after manual verification.
* `SwitchPosition` is updated the moment a `36` command is parsed, *before* the motion completes — `33` will report the target position while the motor is still turning.
* The rotor has **7 detents** (position 0 plus outputs 1–6), so a full circle is 7 × 460 steps and the wrap formulas `(6 − Δ + 1) = 7 − Δ` are exact — not an off-by-one. The real trap is in method 2 (wrap): commanding the **current** position (Δ = 0) performs a full 360° revolution instead of standing still. Prefer the default shortest-path mode.
* No checksum/terminator; a dropped byte desynchronizes parsing until the `N1` range filter recovers.
* Commands `32`, `34`, `37–39` are accepted by the range filter but do nothing.
