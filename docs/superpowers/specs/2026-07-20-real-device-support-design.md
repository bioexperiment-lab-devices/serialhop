# Real-device support: strict identify frame + optional identity reads

Date: 2026-07-20
Status: approved

## Problem

Four of the five devices discovered on the `protres_ksenios` client are unusable, and two undiscovered ports are live pumps SerialHop never sees. Observed state before this change:

| Device | Port | State |
|---|---|---|
| `pump_1` | COM4 | discovered, `connected:false`, `identify:null` |
| `valve_1` | COM3 | **working** |
| `densitometer_1..3` | COM10, COM5, COM8 | discovered, `connected:false`, `identify:null` |
| (not discovered) | COM6 | live pump, calibration 83999 |
| (not discovered) | COM7 | live pump, calibration 92000 |

Two independent defects cause this. Both were established empirically against the real hardware via the raw serial attach endpoint (PR #196); neither is reachable by host-side timing changes.

## Evidence

All findings below are measurements on real devices, not readings of `PROTOCOL.md`. Where the doc and the hardware disagree, the hardware wins.

### Defect 1 — the probe frame is rejected by strict pump firmware

`discovery.probeBytes` is `01 02 03 04 00`. Two firmware dialects exist in the field:

- **Permissive** (COM4, FTDI 0403:6001): answers any `01 02 03 xx yy`.
- **Strict** (COM6 Arduino 2341:0043, COM7 CH340 1A86:7523): answers **only** the exact bytes `01 02 03 04 B5`. Every parameter byte is validated — a full sweep showed N5=180 and N5=182 produce total silence, as does any change to N2, N3 or N4. This is `PROTOCOL.md` §3's *Bluetooth* identification form, which this firmware also enforces on the serial link.

`01 02 03 04 B5` is a **strict superset**: it was verified to return correct identify replies from every real device — pump (`0A`), valve (`1E`), and all three densitometers (`46`). A single frame change therefore fixes discovery with no dual-probe logic.

The strictness applies **only to opcode 1**. Opcodes 10, 13, 18 and 19 were all confirmed working on the strict pumps with ordinary parameters, so no other frame needs magic bytes.

### Defect 2 — mandatory identity reads hard-fail on real firmware

`Attach` demands an identity read that real firmware does not implement:

- **Pump**: `serialFrame = {11,2,3,4,5}` gets **no reply from any of the three pumps**, including the permissive one, verified with 8-second waits while identify kept answering immediately before and after. `pump.Driver.Attach` treats this as fatal, so `session.attach` records `connected=false`, leaves `info` nil, and schedules endless re-attach. This is the whole reason `pump_1` never connects.
- **Densitometer**: `serialNumFrame = {71,0,0,5,0}` likewise gets no reply, failing `Attach` the same way.
- **Valve**: demands nothing, keys its store by port name (`valve.go:98,161`) — and is the only working device. That comment is the template for this fix.

A safe opcode sweep (all-zero parameters, so any motion opcode runs zero steps; opcodes 11/12 excluded as unbounded-run) found **opcode 18 is the only opcode that replies**. There is no alternative identity opcode on the pump.

For densitometers the ping frame `47 02 03 04 00` looked like a candidate identity source, but it is not: consecutive reads on COM5 returned `46 05 1C 5D` then `46 05 1D 00`. Bytes 2–3 are a live 16-bit sensor reading. Using it as a persistence key would produce a key that changes on every read.

**Conclusion: no firmware serial number is recoverable for either device type.**

### Calibration is readable, writable and verifiable

- Identify carries the EEPROM calibration mirror on every attach: COM4 `0A 01 86 A0` (100000), COM6 `0A 01 48 1F` (83999), COM7 `0A 01 67 60` (92000).
- Opcode 13 (`13 00 c1 c2 c3`) **writes calibration successfully on all three pumps with a single write** — the documented Bluetooth "three times in a row" rule does not apply on the serial link. Verified by perturbing one LSB, reading back, and restoring the original value exactly on each pump.
- Opcode 18 confirms the documented speed and step model within ~1%: at DelTime 6000 µs, 50 steps reported 606164 µs against a predicted 600000 µs; 100 steps reported 1209184 µs. Step counts are plain 32-bit big-endian full steps.

This makes the device itself a viable source of truth for calibration, which removes the need for any host-side identity key on pumps.

## Design

### Probe frame

`internal/discovery/probe.go`: `probeBytes` becomes `{1, 2, 3, 4, 181}`, commented with the fact that all three device families were verified against it on real hardware and that the trailing `181` is required by strict pump firmware.

### Pump driver — device is the source of truth

- `identifyFrame` becomes `{1, 2, 3, 4, 181}`.
- `Attach` drops the opcode-11 read entirely. `ml_per_step` is derived from the probe reply's calibration mirror (`calMirror / 1e8`) and **trusted**.
- Deleted: `persistState`, `store`, persisted `calSetAt`, the `unverified` flag and the dispense gate it fed.
- `set_calibration` writes `13 00 c1 c2 c3` where the value is `round(ml_per_step × 1e8)` clamped to 24 bits, then **verifies by re-reading identify**. A mismatch returns an error; it must never report success on an unconfirmed write.
- `get_calibration` returns the in-memory `ml_per_step`; `set_at_uptime_ms` becomes session-scoped.
- `serial` reports empty, following the valve.

Rationale: calibration then follows the physical pump across ports, hosts and reinstalls, and there is exactly one place it can live. EEPROM wear is not a concern — calibration is a rare operator action, and TRANSLATION §5's wear rule targets *periodic* traffic.

### Densitometer driver — optional identity, port-keyed store

- The `serialNumFrame` read becomes best-effort. On no reply, `serial` stays empty and the store keys by port name, mirroring `valve.go:161`.
- The store is retained: `blank`, `tube_correction` and `thermostat` are genuine host-side state with no device-side equivalent.
- `forceTubeFrame` also draws no reply, but it is transacted with 0 expected bytes, so that path is already correct and needs no change.

### Documentation

- `docs/protocol_translation_docs/pump/JSON_PROTOCOL.md`: `serial` now empty; `calibration_unverified` removed; `set_at_uptime_ms` session-scoped; calibration persisted in device EEPROM.
- `docs/protocol_translation_docs/pump/PROTOCOL.md`: record the strict identify dialect and that opcode 11 is absent on all real hardware.
- `docs/protocol_translation_docs/densitometer/`: record that the serial-number frame is absent and state is port-keyed.

## Error handling

- A silent port remains "no known device" — unchanged behaviour.
- A failed calibration write-back is surfaced to the caller, never swallowed.
- A missing densitometer serial logs once at Info and proceeds. Absence of an optional feature is not an error.

## Testing

Unit tests (all run on macOS/Linux and Windows via existing fakes — no Windows-only code is touched):

- The probe constant is `{1,2,3,4,181}`.
- Pump `Attach` succeeds against a fake that answers **only** identify and never opcode 11.
- Calibration encode/decode round-trip, including 24-bit clamping and the write-back verification failure path.
- Densitometer `Attach` succeeds when the serial frame stays silent, and keys its store by port.
- Existing tests asserting `serialFrame` or `unverified` are updated — an expected consequence, not incidental breakage.

Real-hardware verification on preprod after implementation: all three pumps and three densitometers must reach `connected:true`.

## Risks

- **The probe frame is shared by every device type.** Mitigated by having verified the replacement against all seven real ports before designing.
- **Deleting pump `persistState` orphans existing on-disk pump state.** Pump attach has never succeeded on this hardware, so there is almost certainly none; any stale file is inert.
- **Trusting the EEPROM mirror** removes a safety gate. Accepted deliberately: the operator owns calibration, and the previous gate made every pump unusable regardless.

## Out of scope / follow-ups

- **Stable pump identity.** With three pumps discoverable, IDs are assigned in port order (`pump_1`=COM4, `pump_2`=COM6, `pump_3`=COM7), so identity depends on COM numbering staying put. A config-driven port→ID pin is a reasonable follow-up.
- **The `46 05 1E 25` densitometer reading.** Now known to be a live sensor value; decoding it fully is separate work.
