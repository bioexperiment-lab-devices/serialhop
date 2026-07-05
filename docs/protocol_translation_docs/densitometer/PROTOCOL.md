# Densitometer — Communication Protocol

Firmware: `sketch_70CellColor_ev_cell_TDS909A_wide9serNum_TOLst_sh.ino`
Device family code: **70** (cell-density / optical-absorbance detector, "UV cord")

## 1. Physical layer

| Parameter | Value |
|---|---|
| Interface | Hardware UART (`Serial`), USB‑COM port |
| Baud rate | **9600** |
| Frame | 8 data bits, no parity, 1 stop bit (Arduino default, 8N1) |
| Bluetooth | **Not present** on this board (pins reserved but unused) |

The firmware reads the port once per main-loop pass. After every received command the watchdog (8 s timeout) is reset; if the host floods or the MCU hangs, the board self-resets.

## 2. Framing

There is no start byte, checksum, or terminator. Every command is exactly **5 raw binary bytes**:

```
[N1] [N2] [N3] [N4] [N5]
 cmd  par1 par2 par3 par4
```

* Bytes are read back-to-back with a 20 ms `delay()` between reads — send all 5 bytes in one write; do not pause > ~100 ms mid-command.
* **Input validation:** the command is discarded unless `N1 == 1` (identification) or `70 ≤ N1 ≤ 79`. This filters garbage bytes produced when the port opens/closes.
* For almost all commands **N5 must be 0** (it acts as a guard byte); the exceptions are noted below.

All replies are sequences of **4 raw bytes** (some commands send five 4-byte records or twenty 4-byte records). There is no reply framing either — the host must know how many bytes to expect for each command.

### Fixed-point float encoding (used in most replies)

A float `V` is sent as two bytes `a, b`:

```
a = (int)V                 // integer part
b = (int)(V*100 - a*100)   // hundredths
V ≈ a + b/100
```

Negative values are **not** supported by this encoding (both bytes are truncated ints; a negative temperature or ABS will produce garbage).

### 16-bit detector value encoding (little-endian)

Detector intensities are `uint16` sent as `a = low byte`, `b = high byte`:
`value = a + 256*b`.

## 3. Identification handshake

| Host sends | Device replies (4 bytes) |
|---|---|
| `1 2 3 4 x` (N5 ignored) | `Name1 Name2 Name3 Name4` = **`70 0 0 2`** |

* `Name1 = 70` — device type (absorbance detector)
* `Name4 = 2` — number of light sources/detector channels reported

This is the discovery command: the host scans COM ports, sends `1 2 3 4 0`, and identifies the device by the first reply byte.

## 4. Command reference

Commands are grouped by the first byte `N1` (70–79). Unless stated otherwise **N5 = 0** is required.

### `70` — Stop everything

| Bytes | Action | Reply |
|---|---|---|
| `70 x x x 0` | Stop continuous measurement, LED off (DAC = 0) | none |

### `71` — Ping / device parameters

| Bytes | Action | Reply (4 bytes) |
|---|---|---|
| `71 2 3 4 0` | **Ping** — proves the device is alive | `70, 5, T_int, T_frac` — current temperature °C (type code 5 = TMP) |
| `71 0 0 0 0` | Device info | `71, 4, 0, 2` (`SerNum1=4`, `SerNum2=0`, 2 channels) |
| `71 0 0 1 0` | Channel 1 descriptor | `1, 2, 6, 0` → channel 1, type ABS(2), wavelength 600 nm |
| `71 0 0 2 0` | Channel 2 descriptor | `2, 2, 1, 0` → channel 2, type ABS(2), 100 nm (placeholder) |
| `71 0 0 3 0` | Channel 3 descriptor | `3, 5, 0, 50` → type TMP(5); 50 = max thermostat temperature |
| `71 0 0 4 0` | Channel 4 descriptor | `4, 5, 1, 0` → type TMP(5) |
| `71 0 0 5 0` | **Serial number** | `5, 7, 25, 6` → type SNN(7), year=25, unit=6 |
| `71 n 0 10 0` | Show COM-port number `n` (= N2) on the 7-segment display; blocks the device for ~4 s | none |
| `71 0 0 11 0` | Show thermostat set-point on display | none |

Wavelength in channel descriptors is encoded as two bytes: `a = nm / 100`, `b = nm % 100`.

Data-type codes used in replies: `2` = ABS (absorbance), `5` = TMP (temperature), `7` = SNN (serial number).

### `75` — Settings / actuators

| Bytes | Action | Reply |
|---|---|---|
| `75 0 B x 0` | Set LED brightness to index `B` (0–20; 0 = off). Index maps to DAC codes `XBri5[]` (1750–2415) on the MCP4725 | none |
| `75 1 B x 0` | Measure at **one** brightness `B`: fills the whole 20-slot result array with repeated readings at that brightness | none |
| `75 2 T x 0` | Set thermostat target to `T` °C (0–45); heater and cooler are switched off, set-point shown on display. `T < 20` disables the thermostat | none |
| `75 3 I F 0` | Set **tube-correction** factor: multiply current correction by `(I + F/100)` (N3 = integer part, N4 = hundredths). `75 3 0 0 0` resets correction to 1.0. Value is persisted to EEPROM | none |
| `75 4 x x 0` | Cooler ON | none |
| `75 5 x x 0` | Cooler OFF | none |

### `76` — Temperature queries

All replies use the fixed-point float encoding, header bytes `5, 5`:

| Bytes | Reply (4 bytes) | Meaning |
|---|---|---|
| `76 0 x x 0` | `5, 5, T_int, T_frac` | Current temperature (DS18B20) |
| `76 2 x x 0` | `5, 5, T_int, T_frac` | Thermostat set-point |
| `76 5 x x 0` | `5, 5, T_int, T_frac` | Temperature stored at baseline ("base") measurement (also shown on display) |

### `78` — Run measurements

| Bytes | Action | Reply |
|---|---|---|
| `78 3 x x 0` | **Measure baseline (blank)**: sweeps all 20 brightnesses, computes least-squares slope `MNK_A_base`, stores it + current temperature to EEPROM. Display shows `ba` during sweep | none |
| `78 4 x x 0` | Single ABS measurement (sweep + compute), result on display | none (fetch with `79 4 …`) |
| `78 5 x x 0` | Enable **continuous** measurement mode (one ABS measurement per loop pass, ~ every 200 ms + sweep time). Stop with command `70` | none |

### `79` — Read results

| Bytes | Reply | Meaning |
|---|---|---|
| `79 1 0 x 0` | **20 records × 4 bytes**: `105, i, lo, hi` for `i = 1..20` | Full intensity array `XBriRes[1..20]` (little-endian uint16 per record). First byte is fixed `100+5` |
| `79 1 B x 0` (B=1..20) | one record: `100+B, i, lo, hi` | Intensity at brightness index `B`. (Second byte is a stale loop counter — ignore it) |
| `79 2 x x 0` | `2, 2, a, b` | Slope of current sweep `MNK_A` (fixed-point float) |
| `79 3 x x 0` | `3, 3, a, b` | Baseline slope `MNK_A_base` |
| `79 4 x x 0` | `4, 4, a, b` | **Absorbance**: `ABS = |log10(MNK_A_base / MNK_A)| × TubeCorrection` (also shown on display) |

Note: `79 4` recomputes ABS from the last sweep **without** the temperature compensation applied by `78 4`/button measurements (`MeasureABS()` adds `(T − T_base) × 0.0022`).

## 5. Measurement model (what the numbers mean)

1. The LED is driven through an MCP4725 DAC at 20 increasing brightness levels (`XBri5[1..20]`).
2. At each level, the photodetector on `A0` is sampled 1000× and averaged (stored value ≈ 10 × mean 10-bit ADC counts, range 0–10230).
3. A least-squares line is fitted through (brightness index, intensity) pairs for points with `0 < value ≤ 3000`; the **slope** is the measure of transmitted light.
4. Absorbance = `|log10(slope_blank / slope_sample)|`, temperature-corrected and multiplied by the tube-calibration factor.

## 6. Typical host session

```
→ 1 2 3 4 0          # discover                     ← 70 0 0 2
→ 71 2 3 4 0         # ping                         ← 70 5 27 45   (27.45 °C)
→ 75 2 37 0 0        # thermostat to 37 °C
→ 78 3 0 0 0         # blank (cuvette with medium)  (takes ~20×(50ms+sampling))
→ 78 4 0 0 0         # measure sample
→ 79 4 0 0 0         # read ABS                     ← 4 4 0 52     (ABS = 0.52)
```

## 7. Persistent storage (EEPROM map)

| Address | Content | Encoding |
|---|---|---|
| 21, 22 | Baseline temperature | int part, hundredths |
| 31, 32 | `MNK_A_base` (baseline slope) | int part, hundredths |
| 41, 42 | Thermostat set-point | int part, hundredths |
| 51, 52 | Tube correction factor | int part, hundredths |

Loaded at boot (`GetBase()`), written by `78 3` (baseline) and `75 3` (tube correction).

**Exception:** the thermostat set-point is loaded from EEPROM but immediately overridden to 10 °C (= below `TermosMIN`, i.e. off) by `setup()`. The device therefore **always boots with the thermostat disabled**, regardless of what was stored.

## 8. Quirks and gotchas

* **Two different "serial numbers"**: `71 0 0 0 0` returns variables `SerNum1/SerNum2 = 4/0`, while `71 0 0 5 0` returns the compile-time constants `Sn1/Sn2 = 25/6`. The latter is the real per-device serial.
* No checksum or length field anywhere — a lost byte desynchronizes the stream until the N1-range filter happens to resynchronize it.
* Replies interleave `delay(10–20)` between bytes, so a 4-byte reply takes ~40–60 ms.
* During a 20-point sweep the port is **not** read (only the watchdog is serviced); commands sent mid-sweep sit in the 64-byte RX buffer.
* The thermostat set-point never survives a reboot: `setup()` forces it to 10 °C (off) right after loading EEPROM. Conveniently, this makes an unexpected `76 2` reply of `10.00` a reliable sign that the device has rebooted.
* The physical button (D3) triggers a measurement and shows ABS on the display without any serial traffic.
* Board also runs a thermostat loop (heater on D9 PWM, cooler on D10) independent of host commands.
