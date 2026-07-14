# Discovery probe truncation fix — design

Date: 2026-07-14
Status: approved

## 1. Problem

`discovery.Probe` reads the 4-byte identify reply with
`ReadFrame(p, ProbeReadTimeout, ProbeInterByteSlack, 4)` where
`ProbeInterByteSlack = 25ms`, and `internal/discovery/probe.go` discards any
reply shorter than 4 bytes as "no known device".

USB-serial adapters batch RX bytes behind latency timers (FTDI default 16 ms)
with OS scheduling jitter that regularly exceeds 25 ms. A reply like
`[10, 1, 134]` — byte 0 already proves a pump is present — is thrown away,
and the device silently vanishes from that discovery round.

Field evidence (from the investigation this design responds to):

- Raw probes at 25 ms slack truncated: densitometer 4/10, valve 2/10,
  pump 1/6 — **all device types affected**; this is a transport-timing bug,
  not a device-firmware quirk.
- 5 back-to-back discoveries dropped a working `pump_1` once.
- Historical matched counts fluctuate 3/4/5 across identical port sets.

The same `discovery.Probe` is reused by the background reattach path
(`internal/app/app.go` `reprobe`), so reattach inherits the same flakiness.

## 2. Fix — `internal/discovery/probe.go` only

Two changes; the function signature and classification rules are unchanged.
Classification still requires the full 4-byte frame because every driver's
`Attach` consumes the payload bytes (pump: calibration mirror; valve:
position count; densitometer: channels). The fix makes complete frames
arrive; it does not tolerate partial ones.

### 2.1 Widen the inter-byte slack

`ProbeInterByteSlack`: 25 ms → **250 ms** (~10× the observed jitter
threshold, still well under `ProbeReadTimeout = 1s`).

Cost analysis: a complete frame returns the moment byte 4 lands (`max=4`),
and a silent port still bails at the 1 s initial timeout — the widened slack
spends extra time only when a device sent 1–3 bytes and stalled, which is
exactly the failure being fixed.

### 2.2 Retry once on a partial reply

A 1–3 byte reply is proof a device is present; discarding it is the worst
response. New flow inside `Probe`:

```
reply := ReadFrame(p, ProbeReadTimeout, ProbeInterByteSlack, 4)
if 1 <= len(reply) <= 3:
    Drain(DrainDuration)      // flush any straggler byte of attempt 1
    resend probeBytes         // second and final attempt
    reply = ReadFrame(...)
if len(reply) < 4: return reply, nil, nil   // caller logs it
classify reply[0]                            // unchanged
```

- Two attempts total. Retry triggers only on partial replies — never on
  empty ones (a silent port is genuinely deviceless; the investigation
  showed 10 s settles and 5× resync bursts never wake a mute board, so
  empty-reply retries only slow every discovery down).
- Write/drain errors during the retry return an error, as today.
- The returned `reply` is the final attempt's bytes, preserved for logging.

Considered and rejected: a cross-attempt consistency check on `reply[0]`
(attempt-1 byte must equal attempt-2 byte). It guards a near-zero-probability
misalignment (a straggler byte surviving both the 250 ms slack expiry and the
200 ms drain, then prepending to attempt 2's frame and colliding with a valid
type code) but would break the far-more-likely legitimate recovery of "boot
noise partial, then clean reply". The drain is the proportionate defense.

## 3. Observability — `internal/discovery/runner.go`

Today a truncated reply and an empty port produce identical Debug lines,
which is why this bug hid in 30 days of logs. Split the no-match log:

| Outcome                          | Level | Message                                              |
| -------------------------------- | ----- | ---------------------------------------------------- |
| empty reply                      | Debug | no device on port (unchanged)                        |
| 1–3 bytes after retry            | Warn  | partial probe reply — device present, frame incomplete |
| ≥4 bytes, unknown type byte      | Warn  | unknown device type                                  |

## 4. Out of scope

- **No pump-driver changes.** The truncation happens before any driver is
  selected; there is no driver-level fix.
- **No config knob** for the slack (a constant suffices; add a knob only if
  field data ever shows 250 ms insufficient).
- **No multi-baud probing.** `Opener.OpenWithBaud` already exists (the
  flasher uses it) but no known device needs a non-9600 probe.
- **No code for the COM6 pump (pump #2).** Recorded diagnosis so nobody
  re-investigates: the board (genuine Arduino Uno, VID:PID 2341:0043) opens
  fine at 9600 8N1 and accepts writes, but has never transmitted a byte —
  across 30 days of logs, settle times 0–10 s, read timeouts to 5 s, and 5×
  resync bursts. The vendor software also opens it at 9600. Conclusion:
  dead/incompatible firmware or a dead TX path on the board itself. No
  host-side change can discover a device that never transmits.

## 5. Testing

All fake-based (`serial.FakePort`), cross-platform, no Windows-only code.

- Update `TestProbe_FewerThan4Bytes`: a partial reply now triggers a retry —
  assert the probe sequence was written twice, the result is still nil after
  two partial replies, and the partial bytes are returned for logging.
- New: partial reply on attempt 1, full frame on attempt 2 → classified.
- New (regression for the original bug): 4 reply bytes fed with ~100 ms
  inter-byte gaps → classified. Fails against the old 25 ms slack.
- Existing `reader_test.go` untouched — `ReadFrame` itself is correct; the
  bug was the caller's timing parameter.

## 6. Delivery

One PR: `fix: retry truncated probe replies and widen discovery inter-byte
slack` → patch release (v2.0.1) via release-please. Spec, plan, and
implementation land on the same branch.
