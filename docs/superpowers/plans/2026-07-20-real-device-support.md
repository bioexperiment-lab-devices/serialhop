# Real-Device Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make SerialHop discover and operate the real pumps and densitometers on the `protres_ksenios` client, whose firmware rejects the current probe frame and does not implement the identity opcodes the drivers demand.

**Architecture:** Three changes, each independently testable. (1) The universal discovery probe becomes `01 02 03 04 B5`, the only frame strict pump firmware accepts — verified safe against every real device type. (2) The pump driver stops requiring opcode 11: identity comes from nowhere (serial reports empty, like the valve), calibration comes from the device's own EEPROM on every attach, and the two non-attach opcode-11 call sites move to a zero-step opcode 18. (3) The densitometer's serial read becomes best-effort and its store keys by port.

**Tech Stack:** Go 1.x, standard library plus `log/slog`. Tests are table-free Go unit tests using the existing `device.Session` + `serial.FakePort` harness. No new dependencies.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-20-real-device-support-design.md`. Read it before starting.
- Tests must pass on **macOS and Windows**. Do not touch `_windows.go` files; nothing in this plan requires Windows-only code.
- Pre-flight before any PR push: `gofmt -l .` (must print nothing), `go vet ./...`, `golangci-lint run`, `go test -race -count=1 ./...`, `govulncheck ./...`.
- Conventional Commits on every commit. This work is a `fix:` overall — it repairs devices that never worked.
- **Never** write `BREAKING CHANGE:` in a commit body.
- The pump's EEPROM-wear rule (TRANSLATION §5) forbids *periodic* device writes. Calibration writes are operator-paced and fine; do not add any write to `Tick`.

---

### Task 1: Universal probe frame

**Files:**
- Modify: `internal/discovery/probe.go:29`
- Test: `internal/discovery/probe_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `discovery.ProbeBytes() []byte` returns `{1, 2, 3, 4, 181}`. Unchanged signature.

- [ ] **Step 1: Write the failing test**

Add to `internal/discovery/probe_test.go`:

```go
// TestProbeBytesUsesStrictIdentifyFrame pins the frame that strict pump
// firmware requires. COM6/COM7 answer only this exact sequence; valve and
// densitometer were verified to answer it identically to the old frame.
func TestProbeBytesUsesStrictIdentifyFrame(t *testing.T) {
	got := discovery.ProbeBytes()
	want := []byte{1, 2, 3, 4, 181}
	if !bytes.Equal(got, want) {
		t.Errorf("ProbeBytes() = %v, want %v", got, want)
	}
}
```

Add `"bytes"` to that file's imports if absent.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/discovery/ -run TestProbeBytesUsesStrictIdentifyFrame -v`
Expected: FAIL, showing `[1 2 3 4 0]` against `[1 2 3 4 181]`.

- [ ] **Step 3: Change the frame**

In `internal/discovery/probe.go` replace the `probeBytes` declaration:

```go
// probeBytes is the universal identification frame. The trailing 181 is
// required: one pump firmware generation in the field (Arduino 2341:0043 and
// CH340 1A86:7523 boards) validates all four parameter bytes and answers only
// this exact sequence, while the older generation accepts any 01 02 03 xx yy.
// Verified on real hardware against pump (0A), valve (1E) and densitometer
// (46) — see docs/superpowers/specs/2026-07-20-real-device-support-design.md.
var probeBytes = []byte{1, 2, 3, 4, 181}
```

- [ ] **Step 4: Run the discovery suite**

Run: `go test ./internal/discovery/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/discovery/probe.go internal/discovery/probe_test.go
git commit -m "fix(discovery): probe with the strict identify frame 01 02 03 04 B5"
```

---

### Task 2: Pump identify + disarm frames

**Files:**
- Modify: `internal/device/pump/pump.go:29-32`, `internal/device/pump/job.go:245`, `internal/device/pump/watch.go:85`
- Test: `internal/device/pump/pump_test.go`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: package-level `identifyFrame = []byte{1, 2, 3, 4, 181}` and `disarmFrame = []byte{18, 0, 0, 0, 0}`. `serialFrame` is deleted — later tasks must not reference it.

**Context:** `serialFrame` (opcode 11) draws no reply from any real pump. It had two jobs at these call sites: prove the device is alive after a run, and overwrite the EEPROM "last command" so a physical START press cannot replay a dispense. A zero-step opcode 18 does both — it replies with elapsed µs, and PROTOCOL §7 stores every command except types 10/13/19 as the last command.

- [ ] **Step 1: Write the failing test**

Add to `internal/device/pump/pump_test.go`:

```go
// TestFinishJobDisarmsWithZeroStepRun proves the end-of-job frame is the
// zero-step opcode-18 run, not the absent opcode-11 ping. Real firmware never
// answers opcode 11, so using it failed every dispense at completion.
func TestFinishJobDisarmsWithZeroStepRun(t *testing.T) {
	f := newCalibratedFixture(t)
	f.port.Reset()

	// 1 ml at 0.0005 ml/step = 2000 steps; opcode 15 draws no reply.
	f.exec(t, "dispense", `{"volume_ml":1.0}`)
	// Completion reply for the run, then the disarm frame's elapsed-us reply.
	f.port.Feed([]byte{0, 0, 0, 10})
	f.port.Feed([]byte{0, 0, 0, 10})
	waitFor(t, "job done", func() bool { return f.s.Jobs().Active() == nil })

	sent := f.port.Written()
	if !bytes.Contains(sent, []byte{18, 0, 0, 0, 0}) {
		t.Errorf("no zero-step disarm frame in %v", sent)
	}
	if bytes.Contains(sent, []byte{11, 2, 3, 4, 5}) {
		t.Error("opcode-11 ping still sent; real firmware never answers it")
	}
}
```

If `fixture` lacks `exec` or `port.Written()`/`port.Reset()` helpers, use whatever the neighbouring tests in this file already use to submit a command and read written bytes — mirror the closest existing dispense test rather than inventing helpers.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/device/pump/ -run TestFinishJobDisarmsWithZeroStepRun -v`
Expected: FAIL — either "no zero-step disarm frame" or a timeout, because opcode 11 is still sent and its reply is never fed.

- [ ] **Step 3: Replace the frames**

In `internal/device/pump/pump.go`, replace the frame block:

```go
// Command frames (PROTOCOL.md §4). identifyFrame carries the strict 181
// parameter byte required by newer pump firmware; it writes nothing to EEPROM
// and is the only frame safe for polling. disarmFrame is a zero-step timed run
// (opcode 18): it replies with elapsed microseconds, proving liveness, and is
// stored as the device's "last command" (PROTOCOL §7 stores everything except
// types 10/13/19), so a physical START press replays a harmless no-op instead
// of re-running the last dispense. Opcode 11 is NOT implemented by any pump
// firmware in the field and must not be used.
var (
	identifyFrame = []byte{1, 2, 3, 4, 181}
	disarmFrame   = []byte{18, 0, 0, 0, 0}
	pauseFrame    = []byte{19, 0, 0, 0, 0}
	stopFrame     = []byte{10, 0, 0, 0, 0}
)
```

In `internal/device/pump/job.go`, inside `finishJob`, replace the transaction and drop the type-code check — opcode 18 returns elapsed µs, so byte 0 is not a type code and a successful 4-byte read is itself the liveness proof:

```go
	if _, err := d.s.Transact(disarmFrame, 4, replyTimeout); err != nil {
		// Transact's double failure flipped the session unreachable and
		// failed the job (decision 2) — nothing left to do here.
		return
	}
```

Delete the immediately following block:

```go
	if reply[0] != TypeCode {
		d.s.Jobs().Fail(device.ErrHardware("post-job verification: unexpected reply"))
		d.clearJob()
		return
	}
```

Update `finishJob`'s doc comment: replace "The serial-number ping" with "The zero-step disarm run".

In `internal/device/pump/watch.go:85` replace `serialFrame` with `disarmFrame`:

```go
		_, _ = d.s.Transact(disarmFrame, 4, replyTimeout)
```

- [ ] **Step 4: Run the pump suite**

Run: `go test ./internal/device/pump/ -count=1`
Expected: `TestFinishJobDisarmsWithZeroStepRun` PASSes. Other tests may still fail — Task 3 removes `Attach`'s opcode-11 read, and fixtures still pre-feed its reply. Failures naming `serial number read` or attach timeouts are expected here and are fixed in Task 3. Do not paper over them.

- [ ] **Step 5: Commit**

```bash
git add internal/device/pump/pump.go internal/device/pump/job.go internal/device/pump/watch.go internal/device/pump/pump_test.go
git commit -m "fix(pump): disarm with a zero-step opcode-18 run instead of absent opcode 11"
```

---

### Task 3: Pump attach without an identity read

**Files:**
- Modify: `internal/device/pump/pump.go:130-170` (`Attach`), `internal/device/pump/pump.go:53-58` (delete `persistState`), `internal/device/pump/pump.go:98-102` (struct fields)
- Test: `internal/device/pump/pump_test.go`

**Interfaces:**
- Consumes: `identifyFrame` from Task 2.
- Produces: `Attach` no longer transacts anything. `Driver.serial` and `Driver.store` fields are deleted. `Driver.mlPerStep` is set from the probe reply's EEPROM mirror on every attach.

- [ ] **Step 1: Write the failing test**

Add to `internal/device/pump/pump_test.go`:

```go
// TestAttachNeedsNoIdentityRead proves attach completes against firmware that
// answers ONLY the identify probe. No real pump implements opcode 11, so a
// mandatory identity read left every pump at connected=false.
func TestAttachNeedsNoIdentityRead(t *testing.T) {
	shrinkTimeouts(t)
	clock := device.NewFakeClock(time.Unix(1000, 0))
	port := serial.NewFakePort("COM7")
	opener := serial.NewFakeOpener()
	opener.Add(port)
	conn, err := opener.Open("COM7")
	if err != nil {
		t.Fatal(err)
	}
	// Calibration mirror 92000 = 0x016760, exactly what COM7 reports.
	s := device.NewSession(device.SessionConfig{
		ID: "pump_1", Type: "pump", TypeCode: pump.TypeCode, PortName: "COM7",
		Conn: conn, Opener: opener, Clock: clock, StateDir: t.TempDir(),
		Factory:    pump.New,
		ProbeReply: []byte{10, 0x01, 0x67, 0x60},
		Reprobe: func(p serial.Port) ([]byte, error) {
			return []byte{10, 0x01, 0x67, 0x60}, nil
		},
	})
	s.Start(context.Background())
	t.Cleanup(s.Close)

	// Nothing is fed to the port: any transaction during attach would hang.
	waitFor(t, "attach", s.Connected)

	info := s.Info()
	if info.Serial != "" {
		t.Errorf("Serial = %q, want empty (no firmware serial exists)", info.Serial)
	}
}

// TestAttachTrustsEepromCalibration proves ml_per_step is taken from the
// device mirror on every attach and is immediately usable.
func TestAttachTrustsEepromCalibration(t *testing.T) {
	f := newFixture(t, withProbeReply([]byte{10, 0x01, 0x67, 0x60}))
	res := f.exec(t, "get_calibration", `{}`)
	// 92000 / 1e8 = 0.00092 ml/step
	if got := res["ml_per_step"].(float64); math.Abs(got-0.00092) > 1e-12 {
		t.Errorf("ml_per_step = %v, want 0.00092", got)
	}
}
```

Add `"math"` to imports. Use the file's existing helper for reading a command result if `exec` returns something other than `map[string]any` — mirror the nearest existing `get_calibration` test.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/pump/ -run 'TestAttachNeedsNoIdentityRead|TestAttachTrustsEepromCalibration' -v`
Expected: FAIL — attach never completes (`waitFor` times out) because `Attach` still transacts the absent identity frame; the calibration test fails as "not calibrated" or unverified.

- [ ] **Step 3: Rewrite Attach**

In `internal/device/pump/pump.go`, replace the whole `Attach` body:

```go
// Attach derives everything it needs from the identify probe reply. No real
// pump firmware implements the opcode-11 serial read, so there is no device
// serial number: Serial reports empty, exactly as the valve does. The
// calibration mirror in the probe reply is the single source of truth for
// ml_per_step and is re-read on every attach (spec: device-as-source-of-truth).
func (d *Driver) Attach(ctx context.Context, probeReply []byte) (device.Info, error) {
	if len(probeReply) != 4 || probeReply[0] != TypeCode {
		return device.Info{}, fmt.Errorf("pump: unexpected probe reply %v", probeReply)
	}
	calMirror := uint32(probeReply[1])<<16 | uint32(probeReply[2])<<8 | uint32(probeReply[3])

	d.mlPerStep, d.calSetAt = 0, time.Time{}
	if calMirror > 0 {
		d.mlPerStep = float64(calMirror) / 1e8
	}

	// Volatile reset — also the reboot-recovery path (TRANSLATION §5):
	// after a re-probe, state = idle and the pause toggle boots "running".
	d.connectedSince = d.s.Now()
	d.state, d.pausedFrom, d.pauseAssumed = stateIdle, "", false
	d.rotDirection, d.rotSpeedML, d.rotSpeedPct = "", 0, 0
	d.job, d.watch = nil, nil
	d.jobGen++
	return d.info(), nil
}
```

Delete the `persistState` type declaration entirely. Delete the `serial`, `store` and `unverified` fields from `Driver`. Remove now-unused imports (`slog` may become unused in this file — let the compiler tell you).

In `info()`, replace `Serial: d.serial` with `Serial: ""`.

- [ ] **Step 4: Fix the fixtures**

In `internal/device/pump/pump_test.go`, delete the line that pre-feeds attach's identity reply:

```go
	port.Feed([]byte{10, 26, 25, 1}) // Attach's serial-number reply
```

and update `newFixture`'s doc comment to say attach consumes no transactions. `newCalibratedFixture` must stop pre-writing a store file; instead give it a probe reply carrying the calibration mirror. `0.0005 ml/step` = `50000` = `0x00C350`, so its `ProbeReply` and `Reprobe` become `[]byte{10, 0x00, 0xC3, 0x50}`.

- [ ] **Step 5: Run the pump suite**

Run: `go test ./internal/device/pump/ -count=1`
Expected: PASS. If a test asserts a non-empty `serial` or a persisted-calibration behaviour, update it to the new contract — those assertions encoded the broken design.

- [ ] **Step 6: Commit**

```bash
git add internal/device/pump/
git commit -m "fix(pump): attach without opcode 11, trusting the EEPROM calibration mirror"
```

---

### Task 4: Remove the unverified flag and its gate

**Files:**
- Modify: `internal/device/pump/commands.go:36,49,238,401-431`, `internal/device/pump/pump.go` (`requireCalibration`, `capabilities`)
- Test: `internal/device/pump/calibration_test.go`, `internal/device/pump/commands_test.go`

**Interfaces:**
- Consumes: `Attach` from Task 3.
- Produces: `calibrationInfo` loses its `Unverified` field; `capabilities` loses `CalibrationUnverified`; `persistCalibration(mlPerStep float64) *device.CmdError` keeps its signature but no longer touches a store.

- [ ] **Step 1: Write the failing test**

Add to `internal/device/pump/calibration_test.go`:

```go
// TestEepromCalibrationDispensesWithoutConfirmation proves the mirror is
// trusted: a pump reporting calibration at attach can dispense immediately.
func TestEepromCalibrationDispensesWithoutConfirmation(t *testing.T) {
	f := newFixture(t, withProbeReply([]byte{10, 0x00, 0xC3, 0x50})) // 0.0005 ml/step
	if _, cerr := f.execErr(t, "dispense", `{"volume_ml":0.1}`); cerr != nil {
		t.Fatalf("dispense rejected on a mirror-calibrated pump: %v", cerr)
	}
}
```

Use whichever helper the file already has for a command that may return a `*device.CmdError`; mirror the nearest existing rejection test rather than inventing `execErr`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/device/pump/ -run TestEepromCalibrationDispensesWithoutConfirmation -v`
Expected: FAIL with the `unverified_mirror` not-calibrated error.

- [ ] **Step 3: Strip the flag**

In `internal/device/pump/pump.go`, simplify `requireCalibration` to just the zero check:

```go
// requireCalibration gates metered (ml-denominated) commands.
func (d *Driver) requireCalibration() *device.CmdError {
	if d.mlPerStep <= 0 {
		return device.ErrNotCalibrated("no volume calibration stored")
	}
	return nil
}
```

Delete the `CalibrationUnverified` field from `capabilities` and its assignment in `info()`. The speed-range guard becomes `if d.mlPerStep > 0 {`.

In `internal/device/pump/commands.go`: delete the `Unverified` field from `calibrationInfo` and drop it from the `calibrationBlock()` return. In `persistCalibration`, delete the `d.store.Save(...)` call and its error branch, and set `d.mlPerStep, d.calSetAt = mlPerStep, now`. Keep the opcode-13 write and the identify read-back verification exactly as they are — they already implement the spec. Remove the now-stale "unverified flag" mention from the `SetInfo` comment.

- [ ] **Step 4: Run the pump suite**

Run: `go test ./internal/device/pump/ -count=1`
Expected: PASS. Update any test still asserting `unverified` in a JSON body.

- [ ] **Step 5: Commit**

```bash
git add internal/device/pump/
git commit -m "fix(pump): trust the EEPROM calibration mirror, dropping the unverified gate"
```

---

### Task 5: Densitometer optional identity

**Files:**
- Modify: `internal/device/densitometer/densitometer.go:146-166`
- Test: `internal/device/densitometer/densitometer_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `Attach` tolerates a silent `serialNumFrame`; `d.serial` may be `""`; the store key falls back to `d.s.PortName()`.

**Context:** `47 00 00 05 00` draws no reply from any real densitometer, so `Attach` fails and all three sit at `connected:false`. The valve already models the fix (`valve.go:161`). The densitometer keeps its store — `blank`, `tube_correction` and `thermostat` have no device-side equivalent.

- [ ] **Step 1: Write the failing test**

Add to `internal/device/densitometer/densitometer_test.go`:

```go
// TestAttachToleratesSilentSerialFrame proves attach completes against real
// firmware, which never answers the serial-number frame. State then keys by
// port, exactly as the valve does.
func TestAttachToleratesSilentSerialFrame(t *testing.T) {
	f := newFixtureNoSerialReply(t)
	if !f.s.Connected() {
		t.Fatal("device did not attach when the serial frame stayed silent")
	}
	if got := f.s.Info().Serial; got != "" {
		t.Errorf("Serial = %q, want empty", got)
	}
}
```

Build `newFixtureNoSerialReply` by copying this package's existing fixture constructor and omitting only the serial-number reply from the fed bytes. Keep feeding the wavelength reply — that frame *is* implemented (`47 00 00 01 00` returns `01 02 04 00` on real hardware).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/device/densitometer/ -run TestAttachToleratesSilentSerialFrame -v`
Expected: FAIL — attach returns `densitometer: serial read: ...`.

- [ ] **Step 3: Make the read best-effort**

In `internal/device/densitometer/densitometer.go`, replace the serial read and the store-key line:

```go
	// Real firmware does not implement the serial-number frame (verified on
	// all three lab densitometers). Absence of an optional feature is not an
	// error: fall back to port-keyed state, exactly as the valve does.
	storeKey := d.s.PortName()
	if snReply, err := d.s.Transact(serialNumFrame, 4, replyTimeout); err == nil {
		d.serial = formatSerial(snReply[2], snReply[3])
		storeKey = d.serial
	} else {
		d.serial = ""
		slog.Info("densitometer: no serial-number support, keying state by port",
			"port", d.s.PortName())
	}
```

and further down:

```go
	d.store = d.s.Store(storeKey)
```

Leave the wavelength read and `forceTubeFrame` untouched — the wavelength frame is implemented, and `forceTubeFrame` is transacted with 0 expected bytes so a silent device already succeeds.

- [ ] **Step 4: Run the densitometer suite**

Run: `go test ./internal/device/densitometer/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/device/densitometer/
git commit -m "fix(densitometer): tolerate firmware without a serial-number frame"
```

---

### Task 6: Documentation

**Files:**
- Modify: `docs/protocol_translation_docs/pump/PROTOCOL.md`, `docs/protocol_translation_docs/pump/JSON_PROTOCOL.md`, `docs/protocol_translation_docs/pump/TRANSLATION.md`, `docs/protocol_translation_docs/densitometer/PROTOCOL.md`

**Interfaces:**
- Consumes: the behaviour established in Tasks 1–5.
- Produces: docs matching shipped behaviour.

- [ ] **Step 1: Record the hardware reality in the pump PROTOCOL**

In `docs/protocol_translation_docs/pump/PROTOCOL.md` §3, add after the handshake table:

```markdown
> **Field reality (verified 2026-07-20 on three pumps).** Two firmware
> generations exist. The older one accepts any `1 2 3 x x`; the newer one
> (Arduino 2341:0043, CH340 1A86:7523 boards) validates **all four** parameter
> bytes and answers only `1 2 3 4 181` — the "Bluetooth" form above, enforced
> on the serial link too. SerialHop therefore always probes with
> `01 02 03 04 B5`, which both generations accept.
```

In §4 under `11`, add:

```markdown
> **Not implemented in the field.** No pump tested answers opcode `11` in any
> parameter combination, including the older permissive firmware. SerialHop
> does not use it: there is no device serial number, and the end-of-job panel
> disarm uses a zero-step `18` run instead (which §7 also stores as the "last
> command", so a START press replays a harmless no-op).
```

- [ ] **Step 2: Update the pump JSON protocol**

In `docs/protocol_translation_docs/pump/JSON_PROTOCOL.md`: change the identify example's `"serial": "26-025"` to `"serial": ""`, and add below it:

```markdown
`serial` is always empty for pumps: the firmware has no serial-number command.
Devices are identified by their port. Calibration lives in the pump's own
EEPROM, is re-read on every connection, and is trusted — there is no
`calibration_unverified` flag and no host-side calibration store.
`set_at_uptime_ms` is session-scoped: it reports when calibration was set
during the current connection, and is absent otherwise.
```

Remove every other `calibration_unverified` mention in the file.

- [ ] **Step 3: Update TRANSLATION.md**

In `docs/protocol_translation_docs/pump/TRANSLATION.md`, replace the §3 step-3 sentence that proposes an "unverified" mirror with:

```markdown
3. Read `ml_per_step = cal_mirror / 1e8` from the identify reply and trust it.
   The device's EEPROM is the single source of truth: it is re-read on every
   attach, and `set_calibration` writes it back via cmd 13 and verifies the
   write by reading identify again.
```

- [ ] **Step 4: Update the densitometer PROTOCOL**

In `docs/protocol_translation_docs/densitometer/PROTOCOL.md`, add to the identification section:

```markdown
> **Field reality (verified 2026-07-20).** The serial-number frame
> `47 00 00 05 00` draws no reply from any tested densitometer. SerialHop
> treats it as optional and keys persistent state by COM port. The ping frame
> `47 02 03 04 00` is **not** an identity source — bytes 2–3 are a live sensor
> reading that changes between consecutive calls.
```

- [ ] **Step 5: Commit**

```bash
git add docs/protocol_translation_docs/
git commit -m "docs: record real pump and densitometer firmware behaviour"
```

---

### Task 7: Full verification and PR

**Files:** none modified.

- [ ] **Step 1: Run the complete pre-flight**

```bash
gofmt -l .
go vet ./...
golangci-lint run
go test -race -count=1 ./...
govulncheck ./...
```

Expected: `gofmt -l .` prints nothing; everything else passes. Fix anything that fails before continuing — do not open a PR on red.

- [ ] **Step 2: Push and open the PR**

```bash
git push -u origin feat/real-device-support
gh pr create --title "fix: support real pump and densitometer firmware" --body "..."
```

The title must stay a `fix:` — this repairs devices that never worked. Keep the body free of wrapped parentheses in a way that trips release-please's parser (see `docs/superpowers/specs/2026-05-01-ci-design.md`).

- [ ] **Step 3: Verify against real hardware**

With the tunnel open (`ssh -L 8081:172.18.0.5:8081 khamit@111.88.145.138`), the built agent deployed to the `ksenios` client should discover three pumps and three densitometers, all reaching `connected:true`. Record the result in the PR body. Note this step needs the agent binary installed on the Windows client, which is outside this repo's CI.

---

## Self-Review

**Spec coverage:** Defect 1 → Task 1. Defect 2 (pump) → Task 3; (densitometer) → Task 5. Defect 3 → Task 2. EEPROM-as-source-of-truth → Tasks 3 and 4. Docs → Task 6. Verification → Task 7. No spec section is unimplemented.

**Placeholders:** none. Every code step carries the actual code. Where a test helper's exact name is uncertain (`exec`, `execErr`, `port.Written`), the step says to mirror the nearest existing test rather than guess — that is a deliberate instruction, not a TODO.

**Type consistency:** `identifyFrame` and `disarmFrame` are introduced in Task 2 and used under those names in Tasks 3 and 4. `serialFrame` is deleted in Task 2 and referenced nowhere afterwards. `persistCalibration` keeps its signature throughout. `calibrationInfo` loses `Unverified` in Task 4 only, after Task 3 stops setting it.
