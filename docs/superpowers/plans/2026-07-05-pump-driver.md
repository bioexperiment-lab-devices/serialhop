# Pump Driver Implementation Plan (v2 PR 2 of 5)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `internal/device/pump` — the pump Driver implementing `docs/protocol_translation_docs/pump/TRANSLATION.md` exactly on top of the `internal/device` core runtime (merged PR 1), plus three small core additions the pump needs (`Session.WriteFrame`, `ErrNotCalibrated`, shutdown-order swap).

**Architecture:** The driver is pure loop-state (no mutexes): every method runs on the session goroutine. Long waits never block the loop — clock-simulated completions use `s.After`, and the opcode-18 hardware completion reply is read by a watcher goroutine (`s.Go` + `HoldReader`) that reports back via `s.Post`. Math (speed/volume/gradient/suckback conversion) is pure functions in `convert.go`; command handlers in `commands.go`; job orchestration + watcher in `job.go` / `watch.go`.

**Tech Stack:** Go stdlib only (no new dependencies). Tests: stdlib `testing`, `serial.FakePort`/`FakeOpener`, `device.FakeClock`, real `device.Session` hosting the pump driver.

## Global Constraints

- Module path `github.com/bioexperiment-lab-devices/serialhop`; branch `pump-driver` off fresh `origin/main`.
- PR title: `feat: add pump device driver` — plain `feat:`; the string "BREAKING" must appear **nowhere** in the PR title or body (reserved for PR 5).
- Pre-flight before the PR (CLAUDE.md): `gofmt -l .` prints nothing; `go vet ./...`; `golangci-lint run`; `go test -race -count=1 ./...`; `govulncheck ./...`.
- Tests: stdlib `testing` only, no testify. Must pass on macOS and Windows; no Windows-only code in this PR.
- Canonical behavior source: `docs/protocol_translation_docs/pump/TRANSLATION.md` (algorithm), `pump/JSON_PROTOCOL.md` (wire shapes), `pump/PROTOCOL.md` (byte frames). Implement TRANSLATION exactly; deviations listed under "Behavior decisions" below are already settled — do not re-litigate.
- Every clock-driven behavior goes through the session's injectable clock (`s.Now()`, `s.After`); real time is allowed only for serial I/O deadlines (watcher poll, transact timeouts).
- Tunables are package `var`s so tests can shrink/see them: `pump.MinDelTimeUs`, `pump.CalSteps`, `pump.WatchPoll`, `pump.TimerGrace`.
- EEPROM-wear rules (TRANSLATION §5): liveness/verification polling uses the `[1,2,3,0,0]` identify frame only; the `[11,2,3,4,5]` serial-number ping is sent **only** at attach and as the end-of-job panel-disarm; motion frames never used for polling. `Tick` is a no-op (no periodic serial traffic).
- Commit messages end with `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.

### Carried-in decisions from PR 1's final review (binding)

1. **Watchers capture the port on the loop**: call `s.Conn()` on the session goroutine and close over the returned value; never call `s.Conn()` from the watcher goroutine. Reattach closes the old port, unblocking stale watchers with `serial.ErrClosed`.
2. **Tolerate orphaned completion events**: a completion event (watcher post or timer fire) may arrive for a job already failed by an unreachable transition — every event handler checks `jobGen` + `Jobs().Active() != nil` and no-ops otherwise.
3. **Reader discipline**: watcher exit must `Post(s.ReleaseReader)` (bundled with the completion event in one Post). While the reader is held, only write-only Transacts are sent (`Session.Transact` already bypasses drain/read-timeout for write-only frames while held).
4. **Cmd 19 (pause toggle) send path — settled**: sent via a new `Session.WriteFrame` (single write, **no drain, no retry**). Rationale: `transact()` retries the whole transaction on any failure; cmd 19 is a blind toggle, so a retry after a partially-delivered first write could double-send and silently invert pause state. A genuine write failure still flips the session unreachable via `markUnreachable`, and recovery (re-attach / next cmd-10 frame) resets pause belief anyway — a detected failure is strictly better than an undetected inversion. All other write-only frames (10/11/12/13/15/16/17/18) keep the standard retrying `Transact`: cmd 10 is idempotent, and motion/calibration frames follow the shared TRANSLATION discipline.
5. **Persistent state** serial-keyed via `s.Store(serial)`: `{schema_version, ml_per_step, set_at, serial}`, schema_version 1; unknown versions treated as absent.
6. **Shutdown order**: pump `Detach` performs serial I/O (write-only safety stop when motion is active), so this PR swaps the session shutdown order to store `connected=false` **before** `driver.Detach()` — a failing Detach write then hits `markUnreachable`'s `!connected` guard and cannot fail jobs / schedule reattach mid-shutdown.
7. **No unbounded Posts from loop context** (posts buffer = 64): the driver never Posts from the loop; at most one timer (`After`) and one watcher post are outstanding per job.

### Behavior decisions (flagged deviations / interpretations — already settled)

- **Suckback + reverse rejected** (`invalid_params`): firmware opcode 17 runs the forward leg only; TRANSLATION's opcode table has no reverse+suckback row.
- **Gradient `start == end` rejected** (`invalid_params`): the flag needs a direction of change.
- **`ping` is served from memory whenever `state != idle`** — including plain `rotating` (TRANSLATION §5 requires memory-serving mid-job; mid-rotate we extend it to avoid the ~100 ms motor stall from command reception).
- **`status.job`** embeds the active job, else the most recent job (JSON §2: "the active/last job is also embedded in `status`"); `dispensed_ml = progress × volume` of that job when its kind is dispense.
- **`get_calibration` when never calibrated** → `not_calibrated` error.
- **Unverified mirror calibration** (TRANSLATION §3 step 3): metered commands (`rotate`, `dispense`) return `not_calibrated` with `details{reason:"unverified_mirror", proposed_ml_per_step}`; capabilities carry `calibration_unverified: true` and omit `speed_ml_min` limits until verified. `rotate_raw` and `start_calibration` remain available.
- **`set_calibration` while motion is active** → `busy` (TRANSLATION §2: no serial traffic mid-job except stop/pause/resume/verification).
- **`set_at_uptime_ms` clamped ≥ 0** (persisted calibration predating this connection).
- **`Detach`** closes any watcher and sends one write-only `[10,0,0,0,0]` stop frame when motion is active (motor safety), tolerating a dead port; it does **not** send the disarm ping (documented gap: app death mid-job leaves the EEPROM "last command" armed).
- **Rotate polarity fixed**: forward → opcode 11, reverse → 12 (TRANSLATION's "configurable per installation" deferred until a need arises — YAGNI).
- **Gradient duration estimate** uses the closed-form integral of the firmware's fixed ramp (endpoints 300 µs / 30000 µs) instead of a 2×steps-term summation — O(1), accurate to well under the 0.5 s grace.
- **Gradient config frame sends N3=N4=0**: the firmware forces `DelTime = 30000` when a gradient is armed and overrides speed with its fixed ramp, so the speed bytes are inert; zeros make that explicit (cmd 10 only applies N4 "if both M>0 and S>0").

---

### Task 1: Core prep — `Session.WriteFrame`, `ErrNotCalibrated`, shutdown-order swap

**Files:**
- Modify: `internal/device/session.go` (add `WriteFrame`; swap two lines in `loop`'s shutdown case)
- Modify: `internal/device/envelope.go` (add `ErrNotCalibrated`)
- Modify: `internal/device/session_test.go` (extend `stubDriver` with a detach-time connected probe)
- Test: `internal/device/session_writeframe_test.go` (new), `internal/device/envelope_test.go` (append)

**Interfaces:**
- Consumes: existing `Session` internals (`s.conn`, `s.markUnreachable`), `CmdError`.
- Produces: `func (s *Session) WriteFrame(frame []byte) error` (session-goroutine only; single write, no drain/retry; flips unreachable on error) and `func ErrNotCalibrated(msg string) *CmdError`. Shutdown behavior: `connected=false` published **before** `driver.Detach()` runs.

- [ ] **Step 1: Branch off fresh main**

```bash
git fetch origin && git checkout -b pump-driver origin/main
```

- [ ] **Step 2: Write the failing tests**

Append to `internal/device/envelope_test.go`:

```go
func TestErrNotCalibrated(t *testing.T) {
	e := ErrNotCalibrated("no volume calibration stored")
	if e.Code != CodeNotCalibrated || e.Message != "no volume calibration stored" {
		t.Fatalf("bad error: %+v", e)
	}
}
```

In `internal/device/session_test.go`, add one field to `stubDriver` and record connectedness at detach time (replace the existing `Detach` method):

```go
// added field in stubDriver struct:
	connAtDetach atomic.Bool

// replaced method:
func (d *stubDriver) Detach() {
	d.connAtDetach.Store(d.s.Connected())
	d.detached.Store(true)
}
```

Create `internal/device/session_writeframe_test.go`:

```go
package device_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// TestWriteFrameSingleWriteNoDrain: WriteFrame must write the frame exactly
// once and must not touch RX — pre-fed bytes (a stand-in for an in-flight
// opcode-18 completion reply) must survive it. DrainWindow is non-zero so a
// Drain call would observably wipe them.
func TestWriteFrameSingleWriteNoDrain(t *testing.T) {
	oldPB, oldDW := device.PerByteTimeout, device.DrainWindow
	device.PerByteTimeout, device.DrainWindow = 10*time.Millisecond, 30*time.Millisecond
	t.Cleanup(func() { device.PerByteTimeout, device.DrainWindow = oldPB, oldDW })

	preFed := []byte{9, 9, 9, 9}
	frame := []byte{19, 0, 0, 0, 0}
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			if err := drv.s.WriteFrame(frame); err != nil {
				return nil, device.ErrInternal("write: " + err.Error())
			}
			device.DrainWindow = 0 // read back RX without re-draining
			reply, err := drv.s.Transact([]byte{1, 2, 3, 0, 0}, 4, time.Second)
			if err != nil {
				return nil, device.ErrHardware(err.Error())
			}
			return reply, nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	f.port.Feed(preFed)
	resp := f.s.Execute(context.Background(), device.Request{ID: "w", Cmd: "tx"})
	if resp.Status != "ok" {
		t.Fatalf("resp: %+v", resp)
	}
	got, ok := resp.Result.([]byte)
	if !ok || string(got) != string(preFed) {
		t.Fatalf("pre-fed RX must survive WriteFrame: %#v", resp.Result)
	}
	written := f.port.Written()
	count := 0
	for i := 0; i+5 <= len(written); i++ {
		if written[i] == 19 && string(written[i:i+5]) == string(frame) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("frame must be written exactly once, found %d in %v", count, written)
	}
}

// TestWriteFrameFailureFlipsUnreachable: a failed write marks the session
// unreachable (so recovery resets pause belief) and does NOT retry.
func TestWriteFrameFailureFlipsUnreachable(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			_ = drv.s.Conn().Close() // kill the port under the session
			if err := drv.s.WriteFrame([]byte{19, 0, 0, 0, 0}); err == nil {
				return nil, device.ErrInternal("expected write failure")
			}
			return "failed as expected", nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	resp := f.s.Execute(context.Background(), device.Request{ID: "w", Cmd: "tx"})
	if resp.Status != "ok" {
		t.Fatalf("resp: %+v", resp)
	}
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })
}

// TestSessionShutdownPublishesDisconnectBeforeDetach: PR-1 review decision 6 —
// pump Detach writes a safety stop frame, so connected must already be false
// when Detach runs (a failing write then no-ops in markUnreachable).
func TestSessionShutdownPublishesDisconnectBeforeDetach(t *testing.T) {
	f := newFixture(t, nil)
	waitFor(t, "attach", f.s.Connected)
	f.s.Close()
	if !f.drv.detached.Load() {
		t.Fatal("Detach not called")
	}
	if f.drv.connAtDetach.Load() {
		t.Fatal("connected must be false before driver.Detach() runs")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/device/ -run 'TestWriteFrame|TestSessionShutdownPublishes|TestErrNotCalibrated' -v`
Expected: FAIL — `ErrNotCalibrated` and `WriteFrame` undefined (compile error).

- [ ] **Step 4: Implement**

Append to `internal/device/envelope.go`:

```go
func ErrNotCalibrated(msg string) *CmdError {
	return &CmdError{Code: CodeNotCalibrated, Message: msg}
}
```

In `internal/device/session.go`, add after the `Transact` method:

```go
// WriteFrame writes one frame with no drain, no read, and no retry.
// For blind-toggle commands (pump cmd 19 pause/resume) where a duplicate
// send inverts device state: transact()'s whole-transaction retry could
// double-send a frame whose first attempt partially reached the device.
// A write failure still flips the session unreachable — recovery resets
// the toggle belief. Session-goroutine only.
func (s *Session) WriteFrame(frame []byte) error {
	if _, err := s.conn.Write(frame); err != nil {
		s.markUnreachable(err)
		return err
	}
	return nil
}
```

In `internal/device/session.go`'s `loop`, swap the shutdown order (the `<-ctx.Done()` case). Replace:

```go
		case <-ctx.Done():
			// Detach unconditionally: even a session that never reached
			// "connected" (or that went unreachable before shutdown) still
			// needs its persistence hook run. Detach doing serial I/O on a
			// dead port just fails harmlessly.
			s.driver.Detach()
			s.connected.Store(false)
```

with:

```go
		case <-ctx.Done():
			// Detach unconditionally: even a session that never reached
			// "connected" (or that went unreachable before shutdown) still
			// needs its persistence hook run. connected=false is published
			// FIRST so a Detach that does serial I/O on a dead port (pump's
			// safety stop) no-ops in markUnreachable instead of failing jobs
			// and scheduling a reattach mid-shutdown.
			s.connected.Store(false)
			s.driver.Detach()
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -race ./internal/device/ -count=1`
Expected: PASS (all existing + new).

- [ ] **Step 6: Commit**

```bash
git add internal/device/session.go internal/device/envelope.go internal/device/session_test.go internal/device/session_writeframe_test.go internal/device/envelope_test.go
git commit -m "feat(device): add WriteFrame, ErrNotCalibrated; publish disconnect before Detach

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---
### Task 2: Conversion math (`convert.go`)

**Files:**
- Create: `internal/device/pump/convert.go`
- Test: `internal/device/pump/convert_test.go` (internal test package `pump` — the math is unexported)

**Interfaces:**
- Consumes: `device.CmdError`, `device.ErrInvalidParams`.
- Produces (all used by later tasks):
  - `var MinDelTimeUs = 400.0`, `var CalSteps = int64(20000)`, `var WatchPoll = 500 * time.Millisecond`, `var TimerGrace = 500 * time.Millisecond`, `const maxDelTimeUs = 6_502_500`
  - `func speedToBytes(speedMlMin, mlPerStep float64) (n3, n4 byte, actualDelUs float64, cerr *device.CmdError)`
  - `func actualSpeedMlMin(mlPerStep, delTimeUs float64) float64`
  - `func volumeToSteps(volumeMl, mlPerStep float64) (int64, *device.CmdError)`
  - `func rawDelTimeUs(speedPct int) float64`
  - `func factorDelTime(delUs float64) (n3, n4 byte, actualDelUs float64)`
  - `func be32(steps int64) []byte` (4 bytes, big-endian)
  - `func quantizeSuckback(suckbackMl, mlPerStep float64) (dropMult int, actualMl float64)`
  - `func plainEstimate(steps int64, delUs float64) time.Duration`
  - `func suckbackEstimate(steps int64, dropMult int, delUs float64) time.Duration` (steps already includes the drop)
  - `func gradientEstimate(steps int64) time.Duration`

- [ ] **Step 1: Write the failing tests**

Create `internal/device/pump/convert_test.go`:

```go
package pump

import (
	"math"
	"testing"
	"time"
)

// mlPerStep = 0.0005 gives round numbers: 3 ml/min → 100 steps/s → 5000 µs.
const testCal = 0.0005

func TestSpeedToBytes(t *testing.T) {
	cases := []struct {
		name    string
		speed   float64
		n3, n4  byte
		actual  float64
		wantErr bool
	}{
		{name: "exact", speed: 3.0, n3: 1, n4: 50, actual: 5000},
		// 2.9 ml/min → delUs 5172.4 → P=52 → 5200 µs (quantized)
		{name: "quantized", speed: 2.9, n3: 1, n4: 52, actual: 5200},
		// > 30e6×0.0005/400 = 37.5 ml/min busts MinDelTimeUs
		{name: "too fast", speed: 40, wantErr: true},
		// < 30e6×0.0005/6502500 ≈ 0.0023 ml/min busts maxDelTimeUs
		{name: "too slow", speed: 0.001, wantErr: true},
		{name: "zero", speed: 0, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n3, n4, actual, cerr := speedToBytes(c.speed, testCal)
			if c.wantErr {
				if cerr == nil {
					t.Fatalf("want invalid_params, got n3=%d n4=%d", n3, n4)
				}
				if cerr.Code != "invalid_params" {
					t.Fatalf("code = %s", cerr.Code)
				}
				return
			}
			if cerr != nil {
				t.Fatal(cerr)
			}
			if n3 != c.n3 || n4 != c.n4 || actual != c.actual {
				t.Fatalf("got n3=%d n4=%d actual=%v, want n3=%d n4=%d actual=%v",
					n3, n4, actual, c.n3, c.n4, c.actual)
			}
		})
	}
}

func TestSpeedToBytesLargePFactorizes(t *testing.T) {
	// 0.005 ml/min → steps/s = 1/6 → delUs = 3e6 → P = 30000 → n3 must
	// exceed 1 and both bytes stay in 1..255.
	n3, n4, actual, cerr := speedToBytes(0.005, testCal)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if n3 < 1 || n4 < 1 {
		t.Fatalf("bytes out of range: n3=%d n4=%d", n3, n4)
	}
	p := int(math.Round(3e6 / 100))
	if got := int(n3) * int(n4); got < p-int(n3) || got > p+int(n3) {
		t.Fatalf("n3×n4 = %d too far from P = %d", got, p)
	}
	if actual != float64(int(n3)*int(n4)*100) {
		t.Fatalf("actual %v != n3×n4×100", actual)
	}
}

func TestActualSpeedRoundTrip(t *testing.T) {
	if got := actualSpeedMlMin(testCal, 5000); got != 3.0 {
		t.Fatalf("actualSpeedMlMin = %v, want 3.0", got)
	}
}

func TestVolumeToSteps(t *testing.T) {
	steps, cerr := volumeToSteps(1.0, testCal)
	if cerr != nil || steps != 2000 {
		t.Fatalf("steps=%d cerr=%v", steps, cerr)
	}
	if _, cerr := volumeToSteps(0.0001, testCal); cerr == nil {
		t.Fatal("sub-step volume must be invalid_params") // rounds to 0 < 1
	}
	if _, cerr := volumeToSteps(2e6, testCal); cerr == nil {
		t.Fatal("steps > 2e9 must be invalid_params") // 4e9 steps
	}
}

func TestRawDelTime(t *testing.T) {
	// 50% → 200 µs, clamped up to MinDelTimeUs (400)
	if got := rawDelTimeUs(50); got != 400 {
		t.Fatalf("50%% = %v, want 400", got)
	}
	// 1% → 10000 µs
	if got := rawDelTimeUs(1); got != 10000 {
		t.Fatalf("1%% = %v, want 10000", got)
	}
}

func TestBe32(t *testing.T) {
	b := be32(2000)
	if b[0] != 0 || b[1] != 0 || b[2] != 7 || b[3] != 208 {
		t.Fatalf("be32(2000) = %v", b)
	}
}

func TestQuantizeSuckback(t *testing.T) {
	// drop unit = 100 × 0.0005 = 0.05 ml. 0.12 ml → round(2.4) = 2 units.
	mult, actual := quantizeSuckback(0.12, testCal)
	if mult != 2 || actual != 0.1 {
		t.Fatalf("got mult=%d actual=%v", mult, actual)
	}
	// below the 2-unit floor: 0.05 ml → round(1) = 1 → clamped to 2.
	mult, actual = quantizeSuckback(0.05, testCal)
	if mult != 2 || actual != 0.1 {
		t.Fatalf("floor: mult=%d actual=%v", mult, actual)
	}
	// ceiling 255
	mult, _ = quantizeSuckback(100, testCal)
	if mult != 255 {
		t.Fatalf("ceiling: mult=%d", mult)
	}
}

func TestEstimates(t *testing.T) {
	// plain: 2000 steps × 2 × 5000 µs = 20 s
	if got := plainEstimate(2000, 5000); got != 20*time.Second {
		t.Fatalf("plain = %v", got)
	}
	// suckback: (2×2200 + 400×2) × 5000 µs + 0.1 s = 26.1 s
	if got := suckbackEstimate(2200, 2, 5000); got != 26100*time.Millisecond {
		t.Fatalf("suckback = %v", got)
	}
}

// TestGradientEstimate cross-checks the closed-form integral against the
// firmware ramp summed toggle-by-toggle (an independent computation).
func TestGradientEstimate(t *testing.T) {
	steps := int64(1000)
	kmax := 2 * steps
	// firmware ramp: half-period(k) = 1/sqrt(A + B·k), T(1)=30000, T(kmax)=300
	b := (1/(300.0*300.0) - 1/(30000.0*30000.0)) / float64(kmax-1)
	a := 1/(30000.0*30000.0) - b
	var sumUs float64
	for k := int64(1); k <= kmax; k++ {
		sumUs += 1 / math.Sqrt(a+b*float64(k))
	}
	want := time.Duration(sumUs) * time.Microsecond
	got := gradientEstimate(steps)
	diff := math.Abs(float64(got-want)) / float64(want)
	if diff > 0.02 {
		t.Fatalf("gradientEstimate = %v, brute-force = %v (%.1f%% off)", got, want, diff*100)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/pump/ -v`
Expected: FAIL — package does not exist / functions undefined.

- [ ] **Step 3: Implement**

Create `internal/device/pump/convert.go`:

```go
// Package pump implements the peristaltic-pump Driver for the v2 JSON device
// protocol. It translates docs/protocol_translation_docs/pump/JSON_PROTOCOL.md
// onto the legacy 5-byte firmware protocol exactly as specified by
// docs/protocol_translation_docs/pump/TRANSLATION.md.
package pump

import (
	"encoding/binary"
	"math"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// Translator config (TRANSLATION §2). Vars so tests and installations can tune.
var (
	// MinDelTimeUs is the fastest allowed step half-period — protects
	// against stalling the motor (TRANSLATION SPEED_TO_BYTES step 4).
	MinDelTimeUs = 400.0
	// CalSteps is the fixed calibration-run step count (TRANSLATION §4
	// start_calibration step 2) — big enough to weigh accurately.
	CalSteps = int64(20000)
	// WatchPoll is the opcode-18 watcher's per-read timeout (real time;
	// bounds how fast an abandoned watcher notices its stop signal).
	WatchPoll = 500 * time.Millisecond
	// TimerGrace pads clock-simulated completions (TRANSLATION §4
	// dispense step 9: "grace wait 0.5 s").
	TimerGrace = 500 * time.Millisecond
)

// maxDelTimeUs = 255 × 255 × 100: the slowest half-period the byte pair encodes.
const maxDelTimeUs = 6_502_500

// gradient ramp endpoints, hardware-fixed (TRANSLATION §4 dispense step 8).
const (
	gradT0Us = 300.0
	gradTEUs = 30000.0
)

// factorDelTime quantizes a half-period onto the firmware's N3×N4×100 µs grid
// (SPEED_TO_BYTES steps 5–7). delUs must already be range-checked.
func factorDelTime(delUs float64) (n3, n4 byte, actualDelUs float64) {
	p := math.Max(1, math.Round(delUs/100))
	f3 := math.Ceil(p / 255)
	f4 := math.Round(p / f3)
	return byte(f3), byte(f4), f3 * f4 * 100
}

// speedToBytes implements SPEED_TO_BYTES (TRANSLATION §2). The caller is
// responsible for the not_calibrated check; mlPerStep must be > 0 here.
func speedToBytes(speedMlMin, mlPerStep float64) (n3, n4 byte, actualDelUs float64, cerr *device.CmdError) {
	if speedMlMin <= 0 {
		return 0, 0, 0, device.ErrInvalidParams("speed_ml_min", speedMlMin, "speed_ml_min must be positive")
	}
	stepsPerS := speedMlMin / 60 / mlPerStep
	delUs := 500000 / stepsPerS
	if delUs < MinDelTimeUs || delUs > maxDelTimeUs {
		return 0, 0, 0, device.ErrInvalidParams("speed_ml_min", speedMlMin, "speed out of range")
	}
	n3, n4, actual := factorDelTime(delUs)
	return n3, n4, actual, nil
}

// actualSpeedMlMin reports the speed the quantized half-period really gives:
// 30_000_000 × ml_per_step / del_time_us (TRANSLATION §2 step 8).
func actualSpeedMlMin(mlPerStep, delTimeUs float64) float64 {
	return 30_000_000 * mlPerStep / delTimeUs
}

// volumeToSteps converts ml to full steps (TRANSLATION §2).
func volumeToSteps(volumeMl, mlPerStep float64) (int64, *device.CmdError) {
	steps := int64(math.Round(volumeMl / mlPerStep))
	if steps < 1 || steps > 2_000_000_000 {
		return 0, device.ErrInvalidParams("volume_ml", volumeMl, "volume out of range")
	}
	return steps, nil
}

// rawDelTimeUs maps a speed percentage to a half-period, bypassing
// calibration (TRANSLATION §4 rotate_raw): 100% → 100 µs, 1% → 10 ms,
// clamped to [MinDelTimeUs, maxDelTimeUs].
func rawDelTimeUs(speedPct int) float64 {
	delUs := math.Round(10000 / float64(speedPct))
	return math.Min(math.Max(delUs, MinDelTimeUs), maxDelTimeUs)
}

// be32 renders a step count as the 4 big-endian parameter bytes N2..N5.
func be32(steps int64) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(steps)) // #nosec G115 -- capped at 2e9 by volumeToSteps
	return b
}

// quantizeSuckback converts a suckback volume to the firmware's DropMult
// (drop quantum = 100 steps), clamped to [2, 255]; returns the actual
// quantized volume to echo (TRANSLATION §4 dispense step 4).
func quantizeSuckback(suckbackMl, mlPerStep float64) (dropMult int, actualMl float64) {
	dropUnitMl := 100 * mlPerStep
	m := math.Round(suckbackMl / dropUnitMl)
	m = math.Min(math.Max(m, 2), 255)
	return int(m), m * dropUnitMl
}

// plainEstimate: steps × 2 toggles × delUs (TRANSLATION §2 duration estimate).
func plainEstimate(steps int64, delUs float64) time.Duration {
	return time.Duration(float64(steps) * 2 * delUs * float64(time.Microsecond))
}

// suckbackEstimate: steps already includes the drop inflation; the reverse
// leg's 200×dropMult toggles run at doubled period, plus the firmware's
// 100 ms turnaround pause (TRANSLATION §4 dispense step 8).
func suckbackEstimate(steps int64, dropMult int, delUs float64) time.Duration {
	toggles := float64(2*steps) + 400*float64(dropMult)
	return time.Duration(toggles*delUs*float64(time.Microsecond)) + 100*time.Millisecond
}

// gradientEstimate integrates the firmware's fixed quadratic ramp
// half-period(k) = 1/sqrt(A + B·k), k = 1..2×steps, with endpoints
// T(1) = 30000 µs and T(2×steps) = 300 µs. Closed form of the integral:
// ∫ dk/sqrt(A+Bk) = 2·sqrt(A+Bk)/B, evaluated with a half-step midpoint
// correction — O(1) and within a fraction of a percent of the exact sum.
func gradientEstimate(steps int64) time.Duration {
	kmax := float64(2 * steps)
	if kmax < 2 {
		return time.Duration(gradTEUs) * time.Microsecond
	}
	b := (1/(gradT0Us*gradT0Us) - 1/(gradTEUs*gradTEUs)) / (kmax - 1)
	a := 1/(gradTEUs*gradTEUs) - b
	integral := func(k float64) float64 { return 2 * math.Sqrt(a+b*k) / b }
	sumUs := integral(kmax+0.5) - integral(0.5)
	return time.Duration(sumUs) * time.Microsecond
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/pump/ -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/device/pump/convert.go internal/device/pump/convert_test.go
git commit -m "feat(pump): speed/volume/suckback/gradient conversion math

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Driver skeleton — `Attach`, `Detach`, `Tick`, dispatch, registration

**Files:**
- Create: `internal/device/pump/pump.go`
- Test: `internal/device/pump/pump_test.go` (external package `pump_test` — exercises the driver through a real `device.Session`)

**Interfaces:**
- Consumes: `device.Session` services (`Transact`, `Store`, `Now`, `Jobs`), `device.Register`, conversion vars from Task 2.
- Produces:
  - `const TypeCode = 10`, `func Register()`, `func New(s *device.Session) device.Driver`
  - `type Driver struct` with loop-owned fields used by every later task: `s *device.Session; serial string; store *device.Store; mlPerStep float64; calSetAt time.Time; unverified bool; connectedSince time.Time; state pumpState; pausedFrom pumpState; pauseAssumed bool; rotDirection string; rotSpeedML float64; rotSpeedPct int; jobGen int; job *motionJob; lastJobID string; lastJobKind string; lastVolumeML float64; watch *watchHandle`
  - `type pumpState string` with `stateIdle/stateRotating/stateDispensing/stateCalibrating/statePaused` (= the JSON `status.state` strings)
  - `type motionJob struct { id, kind, direction string; volumeML float64; steps int64; delTimeUs float64; speedML float64; suckbackML float64; gradient bool; estimate time.Duration }`
  - `type watchHandle struct { stop, done chan struct{}; timedOut bool }` (declared here so `Detach` compiles; used by Task 8)
  - frame vars: `identifyFrame = []byte{1,2,3,0,0}`, `serialFrame = []byte{11,2,3,4,5}`, `pauseFrame = []byte{19,0,0,0,0}`, `stopFrame = []byte{10,0,0,0,0}`
  - `func (d *Driver) info() device.Info`, `func (d *Driver) requireCalibration() *device.CmdError`
  - persistent schema: `type persistState struct { SchemaVersion int "json:\"schema_version\""; MlPerStep float64 "json:\"ml_per_step\""; SetAt time.Time "json:\"set_at\""; Serial string "json:\"serial\"" }`
  - Test fixture reused by all later tasks: `newFixture(t, opts...)`, `newCalibratedFixture(t)`, `(f *fixture) exec(cmd, params string) device.Response`, `(f *fixture) frames() [][]byte`, `(f *fixture) resultMap(resp) map[string]any`, `waitFor`, `shrinkTimeouts`.

- [ ] **Step 1: Write the failing tests**

Create `internal/device/pump/pump_test.go`:

```go
package pump_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/pump"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

type fixture struct {
	t     *testing.T
	s     *device.Session
	clock *device.FakeClock
	port  *serial.FakePort
	dir   string
}

type fixtureOpt func(*device.SessionConfig)

func withProbeReply(r []byte) fixtureOpt {
	return func(cfg *device.SessionConfig) { cfg.ProbeReply = r }
}

func withStateDir(dir string) fixtureOpt {
	return func(cfg *device.SessionConfig) { cfg.StateDir = dir }
}

func shrinkTimeouts(t *testing.T) {
	t.Helper()
	oldPB, oldDW, oldWP := device.PerByteTimeout, device.DrainWindow, pump.WatchPoll
	device.PerByteTimeout, device.DrainWindow, pump.WatchPoll =
		10*time.Millisecond, 0, 5*time.Millisecond
	t.Cleanup(func() {
		device.PerByteTimeout, device.DrainWindow, pump.WatchPoll = oldPB, oldDW, oldWP
	})
}

// newFixture boots a real Session hosting the pump driver. Attach consumes
// one serial-number transaction, so its reply is pre-fed (DrainWindow is 0,
// so pre-fed RX survives the transaction's drain step).
func newFixture(t *testing.T, opts ...fixtureOpt) *fixture {
	t.Helper()
	shrinkTimeouts(t)
	clock := device.NewFakeClock(time.Unix(1000, 0))
	port := serial.NewFakePort("COM7")
	opener := serial.NewFakeOpener()
	opener.Add(port)
	conn, err := opener.Open("COM7")
	if err != nil {
		t.Fatal(err)
	}
	cfg := device.SessionConfig{
		ID: "pump_1", Type: "pump", TypeCode: pump.TypeCode, PortName: "COM7",
		Conn: conn, Opener: opener, Clock: clock, StateDir: t.TempDir(),
		Factory:    pump.New,
		ProbeReply: []byte{10, 0, 0, 0}, // no calibration mirror by default
		Reprobe:    func(p serial.Port) ([]byte, error) { return []byte{10, 0, 0, 0}, nil },
	}
	for _, o := range opts {
		o(&cfg)
	}
	port.Feed([]byte{10, 26, 25, 1}) // Attach's serial-number reply
	s := device.NewSession(cfg)
	s.Start(context.Background())
	t.Cleanup(s.Close)
	f := &fixture{t: t, s: s, clock: clock, port: port, dir: cfg.StateDir}
	waitFor(t, "attach", s.Connected)
	return f
}

// newCalibratedFixture pre-writes a verified calibration (0.0005 ml/step:
// 3 ml/min → [n3 n4] = [1 50], 1 ml → 2000 steps) into the state dir.
func newCalibratedFixture(t *testing.T, opts ...fixtureOpt) *fixture {
	t.Helper()
	dir := t.TempDir()
	st := device.NewStore(dir, "pump-26-025")
	err := st.Save(map[string]any{
		"schema_version": 1, "ml_per_step": 0.0005,
		"set_at": time.Unix(900, 0).UTC(), "serial": "26-025",
	})
	if err != nil {
		t.Fatal(err)
	}
	return newFixture(t, append([]fixtureOpt{withStateDir(dir)}, opts...)...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (f *fixture) exec(cmd, params string) device.Response {
	f.t.Helper()
	req := device.Request{ID: "t-" + cmd, Cmd: cmd}
	if params != "" {
		req.Params = json.RawMessage(params)
	}
	return f.s.Execute(context.Background(), req)
}

// frames splits everything written to the port into 5-byte command frames.
func (f *fixture) frames() [][]byte {
	tx := f.port.Written()
	var out [][]byte
	for i := 0; i+5 <= len(tx); i += 5 {
		out = append(out, tx[i:i+5])
	}
	return out
}

func frameEq(a []byte, b ...byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// resultMap round-trips a Result through JSON for shape assertions.
func (f *fixture) resultMap(resp device.Response) map[string]any {
	f.t.Helper()
	b, err := json.Marshal(resp.Result)
	if err != nil {
		f.t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		f.t.Fatal(err)
	}
	return m
}

func TestAttachReadsSerialAndServesIdentify(t *testing.T) {
	f := newFixture(t)
	if !frameEq(f.frames()[0], 11, 2, 3, 4, 5) {
		t.Fatalf("first frame must be the serial-number read: %v", f.frames())
	}
	resp := f.exec("identify", "")
	if resp.Status != "ok" {
		t.Fatalf("identify: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["serial"] != "26-025" || m["device_type"] != "pump" ||
		m["model"] != "peristaltic-1ch" || m["firmware_version"] != "legacy" ||
		m["protocol_version"] != "1.0" {
		t.Fatalf("identify result: %v", m)
	}
	caps := m["capabilities"].(map[string]any)
	if caps["channels"] != float64(1) || caps["supports_gradient"] != true ||
		caps["supports_drop_suckback"] != true {
		t.Fatalf("capabilities: %v", caps)
	}
	if caps["speed_ml_min"] != nil {
		t.Fatalf("uncalibrated pump must not report speed limits: %v", caps)
	}
}

func TestAttachRecoversVerifiedCalibration(t *testing.T) {
	f := newCalibratedFixture(t)
	caps := f.resultMap(f.exec("identify", ""))["capabilities"].(map[string]any)
	sr, ok := caps["speed_ml_min"].(map[string]any)
	if !ok {
		t.Fatalf("calibrated pump must report speed limits: %v", caps)
	}
	// max = 30e6 × 0.0005 / 400 = 37.5; min = 30e6 × 0.0005 / 6502500
	if sr["max"] != 37.5 {
		t.Fatalf("max speed: %v", sr)
	}
	if caps["calibration_unverified"] != nil {
		t.Fatalf("verified calibration must not be flagged: %v", caps)
	}
}

func TestAttachProposesUnverifiedMirrorCalibration(t *testing.T) {
	// mirror bytes encode 50000 → proposed ml_per_step = 50000/1e8 = 0.0005,
	// but unverified: no speed limits, capabilities flagged.
	f := newFixture(t, withProbeReply([]byte{10, 0, 195, 80}))
	caps := f.resultMap(f.exec("identify", ""))["capabilities"].(map[string]any)
	if caps["calibration_unverified"] != true {
		t.Fatalf("mirror recovery must be flagged unverified: %v", caps)
	}
	if caps["speed_ml_min"] != nil {
		t.Fatalf("unverified calibration must not report speed limits: %v", caps)
	}
}

func TestUnknownCommand(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("frobnicate", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeUnknownCommand {
		t.Fatalf("resp: %+v", resp)
	}
}

func TestRegister(t *testing.T) {
	pump.Register()
	name, factory, ok := device.LookupDriver(pump.TypeCode)
	if !ok || name != "pump" || factory == nil {
		t.Fatalf("LookupDriver(10) = %q %v %v", name, factory, ok)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/pump/ -v`
Expected: FAIL — `pump.New`, `pump.Register`, `pump.TypeCode` undefined.

- [ ] **Step 3: Implement**

Create `internal/device/pump/pump.go`:

```go
package pump

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// TypeCode is the pump's probe identify code (PROTOCOL.md §3).
const TypeCode = 10

const (
	deviceType   = "pump"
	model        = "peristaltic-1ch"
	firmwareVer  = "legacy"
	protocolVer  = "1.0"
	schemaV      = 1
	replyTimeout = 2 * time.Second // 4-byte replies arrive within ~50 ms
)

// Command frames (PROTOCOL.md §4). identifyFrame is the only frame safe for
// polling — it writes nothing to EEPROM. serialFrame IS stored as the
// device's "last command", which the end-of-job panel disarm exploits.
var (
	identifyFrame = []byte{1, 2, 3, 0, 0}
	serialFrame   = []byte{11, 2, 3, 4, 5}
	pauseFrame    = []byte{19, 0, 0, 0, 0}
	stopFrame     = []byte{10, 0, 0, 0, 0}
)

// Register binds the pump driver into the device registry. Called at app
// wiring time (PR 5); nothing calls it in this PR.
func Register() { device.Register(TypeCode, deviceType, New) }

// New is the device.Factory for pumps.
func New(s *device.Session) device.Driver { return &Driver{s: s} }

type pumpState string

// JSON status.state values (JSON_PROTOCOL.md §3 status).
const (
	stateIdle        pumpState = "idle"
	stateRotating    pumpState = "rotating"
	stateDispensing  pumpState = "dispensing"
	stateCalibrating pumpState = "calibrating"
	statePaused      pumpState = "paused"
)

// persistState is the serial-keyed on-disk schema (spec §5).
type persistState struct {
	SchemaVersion int       `json:"schema_version"`
	MlPerStep     float64   `json:"ml_per_step"`
	SetAt         time.Time `json:"set_at"`
	Serial        string    `json:"serial"`
}

// motionJob carries the driver-side details of the active job (the Jobs
// engine owns lifecycle/progress; this holds what the pump needs to build
// results and completion handling).
type motionJob struct {
	id         string
	kind       string // "dispense" | "calibration"
	direction  string
	volumeML   float64
	steps      int64 // commanded count, includes suckback inflation
	delTimeUs  float64
	speedML    float64 // actual quantized ml/min; 0 in gradient/raw mode
	suckbackML float64 // actual quantized echo value
	gradient   bool
	estimate   time.Duration
}

// watchHandle wires the loop to one opcode-18 watcher goroutine.
// stop: loop → watcher abandon signal. done: watcher → loop exit signal
// (closed before the watcher's final Post so the loop may safely block on
// it). timedOut is loop-owned bookkeeping set by the watchdog.
type watchHandle struct {
	stop     chan struct{}
	done     chan struct{}
	timedOut bool
}

// Driver implements device.Driver for the peristaltic pump. All fields are
// loop-owned: every method runs on the session goroutine (spec §3).
type Driver struct {
	s *device.Session

	serial     string
	store      *device.Store
	mlPerStep  float64 // 0 = not calibrated
	calSetAt   time.Time
	unverified bool // recovered from the device's EEPROM mirror, unconfirmed

	connectedSince time.Time
	state          pumpState
	pausedFrom     pumpState // state resume returns to
	pauseAssumed   bool      // belief about the firmware's blind cmd-19 toggle

	rotDirection string
	rotSpeedML   float64 // actual quantized; 0 when unknown (raw/uncalibrated)
	rotSpeedPct  int     // last rotate_raw percentage, 0 otherwise

	jobGen       int // bumps on job start/stop/attach; guards stale timers+watchers
	job          *motionJob
	lastJobID    string  // most recent job (for status embedding)
	lastJobKind  string
	lastVolumeML float64 // volume of the most recent dispense job
	watch        *watchHandle
}

// Attach implements TRANSLATION §3: read the serial number, recover
// persistent calibration (store first, EEPROM mirror as unverified
// fallback), reset volatile state.
func (d *Driver) Attach(ctx context.Context, probeReply []byte) (device.Info, error) {
	if len(probeReply) != 4 || probeReply[0] != TypeCode {
		return device.Info{}, fmt.Errorf("pump: unexpected probe reply %v", probeReply)
	}
	calMirror := uint32(probeReply[1])<<16 | uint32(probeReply[2])<<8 | uint32(probeReply[3])

	reply, err := d.s.Transact(serialFrame, 4, replyTimeout)
	if err != nil {
		return device.Info{}, fmt.Errorf("pump: serial number read: %w", err)
	}
	if reply[0] != TypeCode {
		return device.Info{}, fmt.Errorf("pump: unexpected serial reply %v", reply)
	}
	d.serial = fmt.Sprintf("%d-%03d", reply[1], reply[2])

	d.store = d.s.Store(d.serial)
	d.mlPerStep, d.calSetAt, d.unverified = 0, time.Time{}, false
	var ps persistState
	found, err := d.store.Load(&ps)
	if err != nil {
		slog.Warn("pump: state file unreadable, treating as absent", "device", d.serial, "err", err)
		found = false
	}
	switch {
	case found && ps.SchemaVersion == schemaV && ps.MlPerStep > 0:
		d.mlPerStep, d.calSetAt = ps.MlPerStep, ps.SetAt
	case calMirror > 0:
		// TRANSLATION §3 step 3: propose the EEPROM mirror, but devices
		// calibrated under the legacy host may hold bytes with different
		// semantics — require confirmation before metered dispensing.
		d.mlPerStep, d.unverified = float64(calMirror)/1e8, true
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

type speedRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

type capabilities struct {
	Channels             int         `json:"channels"`
	SpeedMlMin           *speedRange `json:"speed_ml_min"`
	SupportsGradient     bool        `json:"supports_gradient"`
	SupportsDropSuckback bool        `json:"supports_drop_suckback"`
	// CalibrationUnverified flags an ml_per_step recovered from the device
	// mirror that has not been confirmed (TRANSLATION §3 step 3).
	CalibrationUnverified bool `json:"calibration_unverified,omitempty"`
}

func (d *Driver) info() device.Info {
	caps := capabilities{
		Channels: 1, SupportsGradient: true, SupportsDropSuckback: true,
		CalibrationUnverified: d.unverified,
	}
	if d.mlPerStep > 0 && !d.unverified {
		caps.SpeedMlMin = &speedRange{
			Min: actualSpeedMlMin(d.mlPerStep, maxDelTimeUs),
			Max: actualSpeedMlMin(d.mlPerStep, MinDelTimeUs),
		}
	}
	return device.Info{
		DeviceType: deviceType, Model: model, Serial: d.serial,
		FirmwareVersion: firmwareVer, ProtocolVersion: protocolVer,
		Capabilities: caps,
	}
}

// requireCalibration gates metered (ml-denominated) commands.
func (d *Driver) requireCalibration() *device.CmdError {
	if d.mlPerStep <= 0 {
		return device.ErrNotCalibrated("no volume calibration stored")
	}
	if d.unverified {
		e := device.ErrNotCalibrated(
			"device calibration mirror is unverified — confirm with set_calibration or run start_calibration")
		e.Details = map[string]any{
			"reason": "unverified_mirror", "proposed_ml_per_step": d.mlPerStep,
		}
		return e
	}
	return nil
}

// Execute dispatches one JSON command (identify/get_job are session-served).
func (d *Driver) Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *device.CmdError) {
	switch cmd {
	default:
		return nil, device.ErrUnknownCommand(cmd)
	}
}

// Tick is a no-op: the pump has no canaries or monitoring schedule, and the
// EEPROM-wear rules forbid periodic traffic (TRANSLATION §5).
func (d *Driver) Tick(now time.Time) {}

// Detach drops the watcher and leaves the motor stopped if motion was
// active. Write-only; tolerates a dead port (the session publishes
// connected=false before calling Detach, so a failed write cannot trigger
// the unreachable machinery mid-shutdown).
func (d *Driver) Detach() {
	if d.watch != nil {
		close(d.watch.stop)
		d.watch = nil
	}
	switch d.state {
	case stateRotating, stateDispensing, stateCalibrating, statePaused:
		_, _ = d.s.Transact(stopFrame, 0, time.Second)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/pump/ -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/device/pump/pump.go internal/device/pump/pump_test.go
git commit -m "feat(pump): driver skeleton — attach, calibration recovery, registration

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---
### Task 4: Memory-served commands — `ping`, `status`, `get_calibration`

**Files:**
- Create: `internal/device/pump/commands.go`
- Modify: `internal/device/pump/pump.go` (add dispatch cases)
- Test: `internal/device/pump/commands_test.go`

**Interfaces:**
- Consumes: Driver fields from Task 3; `device.Job`, `d.s.Jobs()`.
- Produces:
  - `func (d *Driver) ping() (any, *device.CmdError)` — idle: identify-frame liveness transact; any other state: pure memory (TRANSLATION §4/§5)
  - `func (d *Driver) status() (any, *device.CmdError)` — pure memory
  - `func (d *Driver) getCalibration() (any, *device.CmdError)` — pure memory
  - `type calibrationInfo struct { MlPerStep float64 "json:\"ml_per_step\""; SetAtUptimeMs int64 "json:\"set_at_uptime_ms\""; Unverified bool "json:\"unverified,omitempty\"" }`
  - `func (d *Driver) calibrationBlock() *calibrationInfo` (nil when uncalibrated — reused by status)
  - `func (d *Driver) statusJob() *device.Job` (active job, else last job, else nil — reused by Task 6+ tests)

- [ ] **Step 1: Write the failing tests**

Create `internal/device/pump/commands_test.go`:

```go
package pump_test

import (
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

func TestPingIdleUsesIdentifyFrameOnly(t *testing.T) {
	f := newFixture(t)
	before := len(f.frames())
	f.port.Feed([]byte{10, 0, 0, 0}) // identify reply
	resp := f.exec("ping", "")
	if resp.Status != "ok" {
		t.Fatalf("ping: %+v", resp)
	}
	fr := f.frames()
	if len(fr) != before+1 || !frameEq(fr[len(fr)-1], 1, 2, 3, 0, 0) {
		t.Fatalf("idle ping must send exactly one identify frame (EEPROM-safe): %v", fr[before:])
	}
	if _, ok := f.resultMap(resp)["uptime_ms"]; !ok {
		t.Fatalf("ping result: %v", f.resultMap(resp))
	}
}

func TestPingUptimeTracksClock(t *testing.T) {
	f := newFixture(t)
	f.clock.Advance(8 * time.Second)
	f.port.Feed([]byte{10, 0, 0, 0})
	m := f.resultMap(f.exec("ping", ""))
	if m["uptime_ms"] != float64(8000) {
		t.Fatalf("uptime_ms = %v, want 8000", m["uptime_ms"])
	}
}

func TestPingFailureGoesUnreachable(t *testing.T) {
	f := newFixture(t)
	// no reply fed → both transaction attempts time out
	resp := f.exec("ping", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("ping: %+v", resp)
	}
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })
}

func TestStatusIdleShape(t *testing.T) {
	f := newCalibratedFixture(t)
	m := f.resultMap(f.exec("status", ""))
	if m["state"] != "idle" || m["job"] != nil || m["direction"] != nil ||
		m["speed_ml_min"] != nil || m["dispensed_ml"] != nil {
		t.Fatalf("idle status: %v", m)
	}
	cal, ok := m["calibration"].(map[string]any)
	if !ok || cal["ml_per_step"] != 0.0005 {
		t.Fatalf("calibration block: %v", m["calibration"])
	}
	// calibration was persisted before this connection → clamped to 0
	if cal["set_at_uptime_ms"] != float64(0) {
		t.Fatalf("set_at_uptime_ms: %v", cal)
	}
}

func TestStatusUncalibrated(t *testing.T) {
	f := newFixture(t)
	m := f.resultMap(f.exec("status", ""))
	if m["calibration"] != nil {
		t.Fatalf("uncalibrated status must have null calibration: %v", m)
	}
}

func TestGetCalibration(t *testing.T) {
	f := newCalibratedFixture(t)
	m := f.resultMap(f.exec("get_calibration", ""))
	if m["ml_per_step"] != 0.0005 {
		t.Fatalf("get_calibration: %v", m)
	}
	f2 := newFixture(t)
	resp := f2.exec("get_calibration", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeNotCalibrated {
		t.Fatalf("uncalibrated get_calibration: %+v", resp)
	}
}
```

Add `"time"` to the test file imports (used by `TestPingUptimeTracksClock`).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/pump/ -run 'TestPing|TestStatus|TestGetCalibration' -v`
Expected: FAIL — commands return `unknown_command`.

- [ ] **Step 3: Implement**

Create `internal/device/pump/commands.go`:

```go
package pump

import (
	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

type pingResult struct {
	UptimeMs int64 `json:"uptime_ms"`
}

// ping (TRANSLATION §4): when idle, prove liveness with the identify frame —
// the only frame that writes nothing to EEPROM. In any other state it is
// answered from memory: mid-job serial traffic could interleave with an
// opcode-18 completion reply, and mid-rotate it would stall the motor
// ~100 ms. uptime_ms is connection age (true device uptime is unknowable).
func (d *Driver) ping() (any, *device.CmdError) {
	if d.state == stateIdle {
		reply, err := d.s.Transact(identifyFrame, 4, replyTimeout)
		if err != nil {
			return nil, device.ErrHardware("ping: " + err.Error())
		}
		if reply[0] != TypeCode {
			return nil, device.ErrHardware("ping: unexpected identify reply")
		}
	}
	return pingResult{UptimeMs: d.s.Now().Sub(d.connectedSince).Milliseconds()}, nil
}

type calibrationInfo struct {
	MlPerStep     float64 `json:"ml_per_step"`
	SetAtUptimeMs int64   `json:"set_at_uptime_ms"`
	Unverified    bool    `json:"unverified,omitempty"`
}

func (d *Driver) calibrationBlock() *calibrationInfo {
	if d.mlPerStep <= 0 {
		return nil
	}
	var upMs int64
	if !d.calSetAt.IsZero() {
		if ms := d.calSetAt.Sub(d.connectedSince).Milliseconds(); ms > 0 {
			upMs = ms // clamped ≥ 0: persisted calibration may predate this connection
		}
	}
	return &calibrationInfo{MlPerStep: d.mlPerStep, SetAtUptimeMs: upMs, Unverified: d.unverified}
}

func (d *Driver) getCalibration() (any, *device.CmdError) {
	cal := d.calibrationBlock()
	if cal == nil {
		return nil, device.ErrNotCalibrated("no volume calibration stored")
	}
	return cal, nil
}

type statusResult struct {
	State       string           `json:"state"`
	Job         *device.Job      `json:"job"`
	Direction   *string          `json:"direction"`
	SpeedMlMin  *float64         `json:"speed_ml_min"`
	DispensedMl *float64         `json:"dispensed_ml"`
	Calibration *calibrationInfo `json:"calibration"`
}

// statusJob returns the active job, else the most recent one (JSON §2:
// "the active/last job is also embedded in status").
func (d *Driver) statusJob() *device.Job {
	if j := d.s.Jobs().Active(); j != nil {
		return j
	}
	if d.lastJobID != "" {
		return d.s.Jobs().Get(d.lastJobID)
	}
	return nil
}

// status (TRANSLATION §4) is served entirely from translator state — the
// firmware has no state-query command. Panel-button activity is invisible
// (documented gap).
func (d *Driver) status() (any, *device.CmdError) {
	res := statusResult{State: string(d.state), Calibration: d.calibrationBlock()}
	res.Job = d.statusJob()

	switch d.state {
	case stateRotating:
		res.Direction = &d.rotDirection
		if d.rotSpeedML > 0 {
			v := d.rotSpeedML
			res.SpeedMlMin = &v
		}
	case stateDispensing, stateCalibrating, statePaused:
		if d.job != nil {
			res.Direction = &d.job.direction
			if d.job.speedML > 0 {
				v := d.job.speedML
				res.SpeedMlMin = &v
			}
		} else if d.pausedFrom == stateRotating {
			res.Direction = &d.rotDirection
			if d.rotSpeedML > 0 {
				v := d.rotSpeedML
				res.SpeedMlMin = &v
			}
		}
	}

	// dispensed_ml: progress × volume of the current/last dispense job
	// (clock-driven estimate; exact only on verified completion).
	if res.Job != nil {
		if d.job != nil && d.job.kind == "dispense" {
			v := res.Job.Progress * d.job.volumeML
			res.DispensedMl = &v
		} else if d.job == nil && d.lastJobKind == "dispense" && d.lastVolumeML > 0 {
			v := res.Job.Progress * d.lastVolumeML
			res.DispensedMl = &v
		}
	}
	return res, nil
}
```

In `internal/device/pump/pump.go`, extend the `Execute` switch:

```go
	switch cmd {
	case "ping":
		return d.ping()
	case "status":
		return d.status()
	case "get_calibration":
		return d.getCalibration()
	default:
		return nil, device.ErrUnknownCommand(cmd)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/pump/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/device/pump/commands.go internal/device/pump/commands_test.go internal/device/pump/pump.go internal/device/pump/pump_test.go
git commit -m "feat(pump): ping, status, get_calibration

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: `rotate` and `rotate_raw`

**Files:**
- Modify: `internal/device/pump/commands.go` (add handlers), `internal/device/pump/pump.go` (dispatch cases)
- Test: `internal/device/pump/commands_test.go` (append)

**Interfaces:**
- Consumes: `speedToBytes`, `rawDelTimeUs`, `factorDelTime`, `actualSpeedMlMin`, Driver fields.
- Produces:
  - `func (d *Driver) rotate(params json.RawMessage) (any, *device.CmdError)`
  - `func (d *Driver) rotateRaw(params json.RawMessage) (any, *device.CmdError)`
  - `func (d *Driver) startRotation(direction string, n3, n4 byte) *device.CmdError` (sends arming + motion frames; reused by both)
  - `func parseDirection(dir string) (opcode byte, cerr *device.CmdError)` — forward → 11, reverse → 12
  - `func (d *Driver) busyGuard() *device.CmdError` — `busy` when a job is active or state is paused (reused by dispense/calibration in later tasks)

- [ ] **Step 1: Write the failing tests**

Append to `internal/device/pump/commands_test.go`:

```go
func TestRotateSendsArmingThenMotionFrame(t *testing.T) {
	f := newCalibratedFixture(t)
	resp := f.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	if resp.Status != "ok" {
		t.Fatalf("rotate: %+v", resp)
	}
	fr := f.frames()
	n := len(fr)
	// TRANSLATION §4 rotate steps 4–5: cmd-10 arming frame (forces the pause
	// toggle to "running"), then the 11/12 motion frame. 3 ml/min → [1 50].
	if !frameEq(fr[n-2], 10, 0, 1, 50, 0) || !frameEq(fr[n-1], 11, 0, 1, 50, 0) {
		t.Fatalf("frames: %v", fr[n-2:])
	}
	m := f.resultMap(resp)
	if m["state"] != "rotating" || m["direction"] != "forward" || m["speed_ml_min"] != 3.0 {
		t.Fatalf("result: %v", m)
	}
	if st := f.resultMap(f.exec("status", "")); st["state"] != "rotating" {
		t.Fatalf("status: %v", st)
	}
}

func TestRotateEchoesQuantizedSpeed(t *testing.T) {
	f := newCalibratedFixture(t)
	m := f.resultMap(f.exec("rotate", `{"direction":"reverse","speed_ml_min":2.9}`))
	// 2.9 → 5200 µs → actual = 15000/5200 ≈ 2.8846: echo ACTUAL, not requested
	want := 30_000_000 * 0.0005 / 5200
	if m["speed_ml_min"] != want {
		t.Fatalf("speed_ml_min = %v, want %v", m["speed_ml_min"], want)
	}
	fr := f.frames()
	if fr[len(fr)-1][0] != 12 {
		t.Fatalf("reverse must use opcode 12: %v", fr[len(fr)-1])
	}
}

func TestRotateRetargetsWhileRotating(t *testing.T) {
	f := newCalibratedFixture(t)
	f.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	resp := f.exec("rotate", `{"direction":"reverse","speed_ml_min":3.0}`)
	if resp.Status != "ok" {
		t.Fatalf("retarget must be allowed while rotating: %+v", resp)
	}
}

func TestRotateRequiresVerifiedCalibration(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeNotCalibrated {
		t.Fatalf("uncalibrated rotate: %+v", resp)
	}
	fu := newFixture(t, withProbeReply([]byte{10, 0, 195, 80})) // unverified mirror
	resp = fu.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeNotCalibrated {
		t.Fatalf("unverified rotate: %+v", resp)
	}
	m, _ := resp.Error.Details.(map[string]any)
	if m["reason"] != "unverified_mirror" {
		t.Fatalf("details: %#v", resp.Error.Details)
	}
}

func TestRotateInvalidParams(t *testing.T) {
	f := newCalibratedFixture(t)
	for _, params := range []string{
		`{"direction":"sideways","speed_ml_min":3.0}`,
		`{"direction":"forward","speed_ml_min":0}`,
		`{"direction":"forward","speed_ml_min":40}`, // > 37.5 max at this calibration
		`not json`,
	} {
		resp := f.exec("rotate", params)
		if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
			t.Fatalf("params %s: %+v", params, resp)
		}
	}
}

func TestRotateRawBypassesCalibration(t *testing.T) {
	f := newFixture(t) // uncalibrated
	resp := f.exec("rotate_raw", `{"direction":"forward","speed_pct":50}`)
	if resp.Status != "ok" {
		t.Fatalf("rotate_raw: %+v", resp)
	}
	fr := f.frames()
	n := len(fr)
	// 50% → 200 µs clamped to 400 → P=4 → [1 4]
	if !frameEq(fr[n-2], 10, 0, 1, 4, 0) || !frameEq(fr[n-1], 11, 0, 1, 4, 0) {
		t.Fatalf("frames: %v", fr[n-2:])
	}
	m := f.resultMap(resp)
	if m["state"] != "rotating" || m["speed_pct"] != float64(50) {
		t.Fatalf("result: %v", m)
	}
}

func TestRotateRawValidatesPct(t *testing.T) {
	f := newFixture(t)
	for _, params := range []string{`{"direction":"forward","speed_pct":0}`,
		`{"direction":"forward","speed_pct":101}`} {
		resp := f.exec("rotate_raw", params)
		if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
			t.Fatalf("params %s: %+v", params, resp)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/pump/ -run TestRotate -v`
Expected: FAIL — `unknown_command`.

- [ ] **Step 3: Implement**

Append to `internal/device/pump/commands.go`:

```go
// parseDirection maps the JSON direction to the run opcode. Polarity is
// fixed forward=11 / reverse=12 (per-installation configurability deferred).
func parseDirection(dir string) (opcode byte, cerr *device.CmdError) {
	switch dir {
	case "forward":
		return 11, nil
	case "reverse":
		return 12, nil
	default:
		return 0, device.ErrInvalidParams("direction", dir, `direction must be "forward" or "reverse"`)
	}
}

// busyGuard rejects motion-starting commands while a job is active or the
// device is paused (a bare rotating state may be retargeted freely).
func (d *Driver) busyGuard() *device.CmdError {
	if j := d.s.Jobs().Active(); j != nil {
		return device.ErrBusy("a job is running", map[string]any{"job_id": j.ID})
	}
	if d.state == statePaused {
		return device.ErrBusy("device is paused — resume or stop first",
			map[string]any{"state": string(statePaused)})
	}
	return nil
}

// startRotation sends the two-frame sequence (TRANSLATION §4 rotate steps
// 4–5): the cmd-10 arming frame is REQUIRED — 11/12 do not touch the
// firmware's pause toggle, and cmd 10 is the only command that forces it to
// "running" (it also clears leftover gradient mode).
func (d *Driver) startRotation(direction string, n3, n4 byte) *device.CmdError {
	if _, err := d.s.Transact([]byte{10, 0, n3, n4, 0}, 0, time.Second); err != nil {
		return device.ErrHardware("rotate arming frame: " + err.Error())
	}
	d.pauseAssumed = false
	opcode, cerr := parseDirection(direction)
	if cerr != nil {
		return cerr
	}
	if _, err := d.s.Transact([]byte{opcode, 0, n3, n4, 0}, 0, time.Second); err != nil {
		return device.ErrHardware("rotate motion frame: " + err.Error())
	}
	d.state = stateRotating
	d.rotDirection = direction
	return nil
}

type rotateResult struct {
	State      string  `json:"state"`
	Direction  string  `json:"direction"`
	SpeedMlMin float64 `json:"speed_ml_min"`
}

func (d *Driver) rotate(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Direction  string  `json:"direction"`
		SpeedMlMin float64 `json:"speed_ml_min"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if cerr := d.busyGuard(); cerr != nil {
		return nil, cerr
	}
	if cerr := d.requireCalibration(); cerr != nil {
		return nil, cerr
	}
	if _, cerr := parseDirection(p.Direction); cerr != nil {
		return nil, cerr
	}
	n3, n4, actualUs, cerr := speedToBytes(p.SpeedMlMin, d.mlPerStep)
	if cerr != nil {
		return nil, cerr
	}
	if cerr := d.startRotation(p.Direction, n3, n4); cerr != nil {
		return nil, cerr
	}
	d.rotSpeedML = actualSpeedMlMin(d.mlPerStep, actualUs)
	d.rotSpeedPct = 0
	return rotateResult{State: "rotating", Direction: p.Direction, SpeedMlMin: d.rotSpeedML}, nil
}

type rotateRawResult struct {
	State    string `json:"state"`
	SpeedPct int    `json:"speed_pct"`
}

func (d *Driver) rotateRaw(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Direction string `json:"direction"`
		SpeedPct  int    `json:"speed_pct"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if cerr := d.busyGuard(); cerr != nil {
		return nil, cerr
	}
	if p.SpeedPct < 1 || p.SpeedPct > 100 {
		return nil, device.ErrInvalidParams("speed_pct", p.SpeedPct, "speed_pct must be 1..100")
	}
	if _, cerr := parseDirection(p.Direction); cerr != nil {
		return nil, cerr
	}
	n3, n4, actualUs := factorDelTime(rawDelTimeUs(p.SpeedPct))
	if cerr := d.startRotation(p.Direction, n3, n4); cerr != nil {
		return nil, cerr
	}
	d.rotSpeedPct = p.SpeedPct
	d.rotSpeedML = 0
	if d.mlPerStep > 0 && !d.unverified {
		d.rotSpeedML = actualSpeedMlMin(d.mlPerStep, actualUs)
	}
	return rotateRawResult{State: "rotating", SpeedPct: p.SpeedPct}, nil
}
```

Add `"encoding/json"` and `"time"` to `commands.go` imports. Extend the `Execute` switch in `pump.go`:

```go
	case "rotate":
		return d.rotate(params)
	case "rotate_raw":
		return d.rotateRaw(params)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/pump/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/device/pump/commands.go internal/device/pump/commands_test.go internal/device/pump/pump.go
git commit -m "feat(pump): rotate and rotate_raw

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---
### Task 6: `dispense` — validation, frames, job, timer-based completion (opcodes 15/16/17)

The opcode-18 hardware-completion path is Task 8; until then, plain forward dispense is also started with the timer path behind a temporary `useWatcher = false` constant that Task 8 removes.

**Files:**
- Create: `internal/device/pump/job.go`
- Modify: `internal/device/pump/pump.go` (dispatch case)
- Test: `internal/device/pump/job_test.go`

**Interfaces:**
- Consumes: all of `convert.go`; `busyGuard`, `requireCalibration`, `parseDirection`; `d.s.Jobs()` (`Start`, `Active`, `Complete`, `Fail`), `d.s.After`, `d.s.Transact`.
- Produces:
  - `func (d *Driver) dispense(params json.RawMessage) (any, *device.CmdError)`
  - `type dispensePlan struct { opcode byte; dropMult int; gradFlag byte; n3, n4 byte; job motionJob }` and `func (d *Driver) planDispense(p dispenseParams) (*dispensePlan, *device.CmdError)` (pure; no I/O — separately testable)
  - `type dispenseParams struct { Direction string "json:\"direction\""; VolumeMl float64 "json:\"volume_ml\""; SpeedMlMin float64 "json:\"speed_ml_min\""; DropSuckbackMl float64 "json:\"drop_suckback_ml\""; SpeedProfile *speedProfile "json:\"speed_profile\"" }`, `type speedProfile struct { StartMlMin float64 "json:\"start_ml_min\""; EndMlMin float64 "json:\"end_ml_min\""; Shape string "json:\"shape\"" }`
  - `func (d *Driver) launchMotion(plan *dispensePlan) (device.Job, *device.CmdError)` (config frame → motion frame → Jobs.Start → bookkeeping; shared with start_calibration)
  - `func (d *Driver) armTimer(gen int)`, `func (d *Driver) timerFire(gen int)` — pause-aware clock completion
  - `func (d *Driver) finishJob(gen int, dur time.Duration)` — panel-disarm ping + `Jobs.Complete` (both completion paths funnel here)
  - `func (d *Driver) clearJob()` — `job=nil`, `state=idle`, `jobGen++`
  - `type dispenseJobResult struct { DispensedMl float64 "json:\"dispensed_ml\""; DurationS float64 "json:\"duration_s\""; MeanSpeedMlMin float64 "json:\"mean_speed_ml_min\""; SuckbackMl float64 "json:\"suckback_ml\"" }`
  - `type calibrationRunResult struct { Steps int64 "json:\"steps\""; DurationS float64 "json:\"duration_s\"" }` (declared here; produced by Task 10)
  - `type gradientEcho struct { Applied string "json:\"applied\""; StartMlMin *float64 "json:\"start_ml_min\""; EndMlMin *float64 "json:\"end_ml_min\"" }`

- [ ] **Step 1: Write the failing tests**

Create `internal/device/pump/job_test.go`:

```go
package pump_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/pump"
)

// startDispense issues a dispense and returns the job_id.
func startDispense(t *testing.T, f *fixture, params string) string {
	t.Helper()
	resp := f.exec("dispense", params)
	if resp.Status != "ok" {
		t.Fatalf("dispense: %+v", resp)
	}
	job := f.resultMap(resp)["job"].(map[string]any)
	if job["state"] != "running" {
		t.Fatalf("job: %v", job)
	}
	return job["job_id"].(string)
}

func jobState(t *testing.T, f *fixture, id string) map[string]any {
	t.Helper()
	resp := f.exec("get_job", `{"job_id":"`+id+`"}`)
	if resp.Status != "ok" {
		t.Fatalf("get_job: %+v", resp)
	}
	return f.resultMap(resp)
}

func TestDispenseReverseTimerCompletion(t *testing.T) {
	f := newCalibratedFixture(t)
	// 1 ml reverse @ 3 ml/min → opcode 16, 2000 steps, estimate 20 s
	id := startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 10, 0, 1, 50, 0) {
		t.Fatalf("config frame: %v", fr[n-2])
	}
	if !frameEq(fr[n-1], 16, 0, 0, 7, 208) { // be32(2000) = 0 0 7 208
		t.Fatalf("motion frame: %v", fr[n-1])
	}
	st := f.resultMap(f.exec("status", ""))
	if st["state"] != "dispensing" || *jsonStr(st["direction"]) != "reverse" {
		t.Fatalf("status: %v", st)
	}

	f.port.Feed([]byte{10, 26, 25, 1}) // panel-disarm ping reply
	f.clock.Advance(20*time.Second + pump.TimerGrace)
	waitFor(t, "job success", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
	js := jobState(t, f, id)
	res := js["result"].(map[string]any)
	if res["dispensed_ml"] != 1.0 || res["suckback_ml"] != 0.0 {
		t.Fatalf("result: %v", res)
	}
	if res["mean_speed_ml_min"].(float64) < 2.8 || res["mean_speed_ml_min"].(float64) > 3.2 {
		t.Fatalf("mean speed: %v", res)
	}
	// the disarm ping (serial-number frame) must have been sent
	fr = f.frames()
	if !frameEq(fr[len(fr)-1], 11, 2, 3, 4, 5) {
		t.Fatalf("panel-disarm ping missing: %v", fr[len(fr)-1])
	}
	if f.resultMap(f.exec("status", ""))["state"] != "idle" {
		t.Fatal("must return to idle")
	}
}

func jsonStr(v any) *string {
	if v == nil {
		return nil
	}
	s := v.(string)
	return &s
}

func TestDispenseSuckbackInflatesSteps(t *testing.T) {
	f := newCalibratedFixture(t)
	// 1 ml + 0.12 ml suckback → dropMult 2, steps 2000+200=2200, opcode 17
	startDispense(t, f,
		`{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0,"drop_suckback_ml":0.12}`)
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 10, 2, 1, 50, 0) { // dropMult rides N2 of the config frame
		t.Fatalf("config frame: %v", fr[n-2])
	}
	if !frameEq(fr[n-1], 17, 0, 0, 8, 152) { // be32(2200) = 0 0 8 152
		t.Fatalf("motion frame: %v", fr[n-1])
	}
	// estimate = (2×2200 + 400×2) × 5000 µs + 0.1 s = 26.1 s
	st := f.resultMap(f.exec("status", ""))
	job := st["job"].(map[string]any)
	if job["estimated_duration_s"] != 26.1 {
		t.Fatalf("estimate: %v", job)
	}
}

func TestDispenseSuckbackCompletionEchoesActual(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f,
		`{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0,"drop_suckback_ml":0.12}`)
	f.port.Feed([]byte{10, 26, 25, 1})
	f.clock.Advance(26100*time.Millisecond + pump.TimerGrace)
	waitFor(t, "job success", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
	res := jobState(t, f, id)["result"].(map[string]any)
	if res["suckback_ml"] != 0.1 { // quantized actual, not the requested 0.12
		t.Fatalf("suckback_ml: %v", res)
	}
	if res["dispensed_ml"] != 1.0 { // net delivered volume, drop excluded
		t.Fatalf("dispensed_ml: %v", res)
	}
}

func TestDispenseGradient(t *testing.T) {
	f := newCalibratedFixture(t)
	resp := f.exec("dispense",
		`{"direction":"forward","volume_ml":1.0,"speed_profile":{"start_ml_min":0.5,"end_ml_min":5.0,"shape":"linear"}}`)
	if resp.Status != "ok" {
		t.Fatalf("gradient dispense: %+v", resp)
	}
	fr := f.frames()
	n := len(fr)
	// increasing profile → grad flag 12; speed bytes inert (firmware ramp)
	if !frameEq(fr[n-2], 10, 0, 0, 0, 12) {
		t.Fatalf("config frame: %v", fr[n-2])
	}
	if fr[n-1][0] != 15 { // gradient runs must use opcode 15
		t.Fatalf("motion frame: %v", fr[n-1])
	}
	m := f.resultMap(resp)
	prof := m["speed_profile"].(map[string]any)
	if prof["applied"] != "hardware-fixed quadratic ramp" ||
		prof["start_ml_min"] != nil || prof["end_ml_min"] != nil {
		t.Fatalf("profile echo: %v", prof)
	}
}

func TestDispenseGradientDecreasingFlag(t *testing.T) {
	f := newCalibratedFixture(t)
	f.exec("dispense",
		`{"direction":"forward","volume_ml":1.0,"speed_profile":{"start_ml_min":5.0,"end_ml_min":0.5,"shape":"linear"}}`)
	fr := f.frames()
	if fr[len(fr)-2][4] != 21 {
		t.Fatalf("decreasing profile must arm grad flag 21: %v", fr[len(fr)-2])
	}
}

func TestDispenseRejections(t *testing.T) {
	f := newCalibratedFixture(t)
	cases := []struct {
		name, params, code string
	}{
		{"gradient+reverse", `{"direction":"reverse","volume_ml":1,"speed_profile":{"start_ml_min":1,"end_ml_min":2,"shape":"linear"}}`, "invalid_params"},
		{"gradient+suckback", `{"direction":"forward","volume_ml":1,"drop_suckback_ml":0.1,"speed_profile":{"start_ml_min":1,"end_ml_min":2,"shape":"linear"}}`, "invalid_params"},
		{"gradient flat", `{"direction":"forward","volume_ml":1,"speed_profile":{"start_ml_min":2,"end_ml_min":2,"shape":"linear"}}`, "invalid_params"},
		{"suckback+reverse", `{"direction":"reverse","volume_ml":1,"speed_ml_min":3,"drop_suckback_ml":0.1}`, "invalid_params"},
		{"zero volume", `{"direction":"forward","volume_ml":0,"speed_ml_min":3}`, "invalid_params"},
		{"no speed", `{"direction":"forward","volume_ml":1}`, "invalid_params"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := f.exec("dispense", c.params)
			if resp.Status != "error" || resp.Error.Code != c.code {
				t.Fatalf("%+v", resp)
			}
		})
	}
}

func TestDispenseBusyWhileJobActive(t *testing.T) {
	f := newCalibratedFixture(t)
	startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	resp := f.exec("dispense", `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("second dispense: %+v", resp)
	}
	resp = f.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("rotate during job: %+v", resp)
	}
}

func TestDispenseBusyWhileRotating(t *testing.T) {
	f := newCalibratedFixture(t)
	f.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	resp := f.exec("dispense", `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("dispense while rotating: %+v", resp)
	}
}

func TestDispenseUncalibrated(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("dispense", `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeNotCalibrated {
		t.Fatalf("%+v", resp)
	}
}

// TestDispenseVerificationFailureFailsJob: the end-of-job disarm ping gets no
// reply → transaction double-fails → session flips unreachable and fails the
// job; the completion handler must tolerate that (PR-1 decision 2).
func TestDispenseVerificationFailureFailsJob(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	// feed nothing: the disarm ping will time out twice
	f.clock.Advance(20*time.Second + pump.TimerGrace)
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })
	// job failed, not completed; reattach and inspect it
	f.port.Feed([]byte{10, 26, 25, 1}) // next attach's serial reply
	f.clock.Advance(device.ReattachBase)
	waitFor(t, "reattach", f.s.Connected)
	if st := jobState(t, f, id); st["state"] != "failed" {
		t.Fatalf("job after failed verification: %v", st)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/pump/ -run TestDispense -v`
Expected: FAIL — `unknown_command`.

- [ ] **Step 3: Implement**

Create `internal/device/pump/job.go`:

```go
package pump

import (
	"encoding/json"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// useWatcher gates the opcode-18 hardware-completion path; the watcher lands
// in a follow-up task, until then plain forward dispenses run on the timer.
const useWatcher = false

type speedProfile struct {
	StartMlMin float64 `json:"start_ml_min"`
	EndMlMin   float64 `json:"end_ml_min"`
	Shape      string  `json:"shape"`
}

type dispenseParams struct {
	Direction      string        `json:"direction"`
	VolumeMl       float64       `json:"volume_ml"`
	SpeedMlMin     float64       `json:"speed_ml_min"`
	DropSuckbackMl float64       `json:"drop_suckback_ml"`
	SpeedProfile   *speedProfile `json:"speed_profile"`
}

type dispenseJobResult struct {
	DispensedMl    float64 `json:"dispensed_ml"`
	DurationS      float64 `json:"duration_s"`
	MeanSpeedMlMin float64 `json:"mean_speed_ml_min"`
	SuckbackMl     float64 `json:"suckback_ml"`
}

type calibrationRunResult struct {
	Steps     int64   `json:"steps"`
	DurationS float64 `json:"duration_s"`
}

// gradientEcho is the response's speed_profile block: only the ramp
// direction is honored by hardware; endpoints are echoed as null
// (TRANSLATION §4 dispense step 5, gap table).
type gradientEcho struct {
	Applied    string   `json:"applied"`
	StartMlMin *float64 `json:"start_ml_min"`
	EndMlMin   *float64 `json:"end_ml_min"`
}

// dispensePlan is the fully-resolved byte-level plan for one motion job.
type dispensePlan struct {
	opcode   byte
	dropMult int
	gradFlag byte
	n3, n4   byte
	job      motionJob
}

// planDispense implements TRANSLATION §4 dispense steps 1–8 (the pure part:
// validation, conversion, opcode selection, estimate). No I/O.
func (d *Driver) planDispense(p dispenseParams) (*dispensePlan, *device.CmdError) {
	if _, cerr := parseDirection(p.Direction); cerr != nil {
		return nil, cerr
	}
	steps, cerr := volumeToSteps(p.VolumeMl, d.mlPerStep)
	if cerr != nil {
		return nil, cerr
	}
	plan := &dispensePlan{job: motionJob{
		kind: "dispense", direction: p.Direction, volumeML: p.VolumeMl,
	}}

	if p.SpeedProfile != nil {
		// Gradient: firmware computes its fixed ramp only for opcode 15 —
		// forward, no suckback (TRANSLATION §4 dispense step 5).
		if p.Direction != "forward" || p.DropSuckbackMl > 0 {
			return nil, device.ErrInvalidParams("speed_profile", nil,
				"gradient unsupported with reverse/suckback")
		}
		switch {
		case p.SpeedProfile.StartMlMin < p.SpeedProfile.EndMlMin:
			plan.gradFlag = 12
		case p.SpeedProfile.StartMlMin > p.SpeedProfile.EndMlMin:
			plan.gradFlag = 21
		default:
			return nil, device.ErrInvalidParams("speed_profile", nil,
				"start_ml_min and end_ml_min must differ")
		}
		plan.opcode = 15
		plan.job.gradient = true
		plan.job.steps = steps
		plan.job.estimate = gradientEstimate(steps)
		// n3/n4 stay 0: the firmware overrides speed with its fixed ramp.
		return plan, nil
	}

	n3, n4, actualUs, cerr := speedToBytes(p.SpeedMlMin, d.mlPerStep)
	if cerr != nil {
		return nil, cerr
	}
	plan.n3, plan.n4 = n3, n4
	plan.job.delTimeUs = actualUs
	plan.job.speedML = actualSpeedMlMin(d.mlPerStep, actualUs)

	if p.DropSuckbackMl > 0 {
		// The firmware's forward leg equals the COMMANDED count, then it
		// retracts the drop, netting (commanded − drop): inflating the
		// count by the drop makes net delivery equal volume_ml.
		if p.Direction != "forward" {
			return nil, device.ErrInvalidParams("drop_suckback_ml", p.DropSuckbackMl,
				"drop_suckback requires direction=forward")
		}
		dropMult, actualMl := quantizeSuckback(p.DropSuckbackMl, d.mlPerStep)
		steps += int64(100 * dropMult)
		if steps > 2_000_000_000 {
			return nil, device.ErrInvalidParams("volume_ml", p.VolumeMl, "volume out of range")
		}
		plan.dropMult = dropMult
		plan.job.suckbackML = actualMl
		plan.job.steps = steps
		plan.job.estimate = suckbackEstimate(steps, dropMult, actualUs)
		plan.opcode = 17
		return plan, nil
	}

	plan.job.steps = steps
	plan.job.estimate = plainEstimate(steps, actualUs)
	if p.Direction == "reverse" {
		plan.opcode = 16
	} else {
		plan.opcode = 18 // completion reply available — the opcode-18 trick
	}
	return plan, nil
}

// launchMotion performs TRANSLATION §4 dispense steps 6–8 (and the
// start_calibration analog): config frame, motion frame, job start.
func (d *Driver) launchMotion(plan *dispensePlan) (device.Job, *device.CmdError) {
	cfg := []byte{10, byte(plan.dropMult), plan.n3, plan.n4, plan.gradFlag}
	if _, err := d.s.Transact(cfg, 0, time.Second); err != nil {
		return device.Job{}, device.ErrHardware("configuration frame: " + err.Error())
	}
	d.pauseAssumed = false // cmd 10 forces the firmware toggle to "running"

	steps := be32(plan.job.steps)
	motion := []byte{plan.opcode, steps[0], steps[1], steps[2], steps[3]}
	if _, err := d.s.Transact(motion, 0, time.Second); err != nil {
		return device.Job{}, device.ErrHardware("motion frame: " + err.Error())
	}

	job, cerr := d.s.Jobs().Start(plan.job.kind, plan.job.estimate)
	if cerr != nil {
		return device.Job{}, cerr // unreachable: busyGuard ran first
	}
	mj := plan.job
	mj.id = job.ID
	d.job = &mj
	d.jobGen++
	d.lastJobID, d.lastJobKind = job.ID, mj.kind
	if mj.kind == "dispense" {
		d.lastVolumeML = mj.volumeML
	}
	return job, nil
}

func (d *Driver) dispense(params json.RawMessage) (any, *device.CmdError) {
	var p dispenseParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if cerr := d.busyGuard(); cerr != nil {
		return nil, cerr
	}
	if d.state == stateRotating {
		return nil, device.ErrBusy("device is rotating — stop first",
			map[string]any{"state": string(stateRotating)})
	}
	if cerr := d.requireCalibration(); cerr != nil {
		return nil, cerr
	}
	plan, cerr := d.planDispense(p)
	if cerr != nil {
		return nil, cerr
	}
	job, cerr := d.launchMotion(plan)
	if cerr != nil {
		return nil, cerr
	}
	d.state = stateDispensing
	gen := d.jobGen
	if plan.opcode == 18 && useWatcher {
		d.startWatch(gen, plan.job.estimate)
	} else {
		d.armTimer(gen)
	}

	result := map[string]any{"job": job}
	if plan.job.gradient {
		result["speed_profile"] = gradientEcho{Applied: "hardware-fixed quadratic ramp"}
	}
	if plan.dropMult > 0 {
		result["suckback_ml"] = d.job.suckbackML
	}
	return result, nil
}

// armTimer schedules the clock-simulated completion (TRANSLATION §4 dispense
// step 9, non-18 opcodes): fire at estimate + grace of ACTIVE time. Pauses
// freeze the job clock, so timerFire re-arms for the remainder when it wakes
// early. One timer is outstanding per arm — no unbounded Posts.
func (d *Driver) armTimer(gen int) {
	if d.job == nil {
		return
	}
	remaining := d.job.estimate + TimerGrace
	if a := d.s.Jobs().Active(); a != nil {
		remaining = d.job.estimate - elapsedOf(a) + TimerGrace
	}
	d.s.After(remaining, func() { d.timerFire(gen) })
}

func elapsedOf(j *device.Job) time.Duration {
	return time.Duration(j.ElapsedS * float64(time.Second))
}

func (d *Driver) timerFire(gen int) {
	if gen != d.jobGen || d.job == nil {
		return // stale timer: job already finished/cancelled/replaced
	}
	active := d.s.Jobs().Active()
	if active == nil {
		return // failed by an unreachable transition — tolerate (decision 2)
	}
	if elapsedOf(active) < d.job.estimate {
		d.armTimer(gen) // a pause froze the clock; wait out the remainder
		return
	}
	d.finishJob(gen, elapsedOf(active))
}

// finishJob runs TRANSLATION §4 dispense steps 10–11: end-of-job
// verification + panel disarm, then job completion. The serial-number ping
// (a) confirms the device is alive after the run and (b) overwrites the
// EEPROM "last command" so a physical START press replays a harmless ping
// instead of re-running the dispense.
func (d *Driver) finishJob(gen int, dur time.Duration) {
	if gen != d.jobGen || d.job == nil || d.s.Jobs().Active() == nil {
		return
	}
	reply, err := d.s.Transact(serialFrame, 4, replyTimeout)
	if err != nil {
		// Transact's double failure flipped the session unreachable and
		// failed the job (decision 2) — nothing left to do here.
		return
	}
	if reply[0] != TypeCode {
		d.s.Jobs().Fail(device.ErrHardware("post-job verification: unexpected reply"))
		d.clearJob()
		return
	}
	j := d.job
	var result any
	if j.kind == "calibration" {
		result = calibrationRunResult{Steps: j.steps, DurationS: dur.Seconds()}
	} else {
		durS := dur.Seconds()
		result = dispenseJobResult{
			DispensedMl:    j.volumeML,
			DurationS:      durS,
			MeanSpeedMlMin: j.volumeML / durS * 60,
			SuckbackMl:     j.suckbackML,
		}
	}
	d.s.Jobs().Complete(result)
	d.clearJob()
}

func (d *Driver) clearJob() {
	d.job = nil
	d.state = stateIdle
	d.jobGen++
}
```

Task 8 replaces the `useWatcher` gate with the real watcher; to keep this task compiling, add a stub in `job.go`:

```go
// startWatch is implemented with the opcode-18 watcher (see watch.go, added
// by a later task). The useWatcher gate keeps it unreachable until then.
func (d *Driver) startWatch(gen int, estimate time.Duration) { d.armTimer(gen) }
```

Extend the `Execute` switch in `pump.go`:

```go
	case "dispense":
		return d.dispense(params)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/pump/ -count=1`
Expected: PASS. Note `TestDispenseReverseTimerCompletion` exercises the full loop: motion frames → clock advance → timer completion → disarm ping → job result.

- [ ] **Step 5: Commit**

```bash
git add internal/device/pump/job.go internal/device/pump/job_test.go internal/device/pump/pump.go
git commit -m "feat(pump): dispense with clock-simulated completion

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: `pause` / `resume`

**Files:**
- Modify: `internal/device/pump/commands.go` (handlers), `internal/device/pump/pump.go` (dispatch)
- Test: `internal/device/pump/commands_test.go` (append)

**Interfaces:**
- Consumes: `d.s.WriteFrame` (Task 1 — single write, no retry: cmd 19 is a blind toggle), `d.s.Jobs().Freeze()/Unfreeze()`, Driver state fields.
- Produces: `func (d *Driver) pause() (any, *device.CmdError)`, `func (d *Driver) resume() (any, *device.CmdError)`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/device/pump/commands_test.go`:

```go
func countFrames(f *fixture, opcode byte) int {
	n := 0
	for _, fr := range f.frames() {
		if fr[0] == opcode {
			n++
		}
	}
	return n
}

func TestPauseFreezesJobClock(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.clock.Advance(10 * time.Second) // halfway through the 20 s estimate

	resp := f.exec("pause", "")
	if resp.Status != "ok" {
		t.Fatalf("pause: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["state"] != "paused" || m["job_id"] != id {
		t.Fatalf("pause result: %v", m)
	}
	if m["dispensed_ml"].(float64) < 0.45 || m["dispensed_ml"].(float64) > 0.55 {
		t.Fatalf("dispensed estimate: %v", m)
	}
	if countFrames(f, 19) != 1 {
		t.Fatalf("pause must send exactly one cmd-19 frame: %v", f.frames())
	}

	// while paused, elapsed and progress are frozen and the job survives
	// well past its estimate
	f.clock.Advance(time.Minute)
	js := jobState(t, f, id)
	if js["state"] != "paused" || js["elapsed_s"] != 10.0 {
		t.Fatalf("paused job: %v", js)
	}

	// resume unfreezes; the timer path completes after the REMAINING time
	resp = f.exec("resume", "")
	if resp.Status != "ok" || f.resultMap(resp)["state"] != "dispensing" {
		t.Fatalf("resume: %+v", resp)
	}
	if countFrames(f, 19) != 2 {
		t.Fatalf("resume must send exactly one more cmd-19 frame")
	}
	f.port.Feed([]byte{10, 26, 25, 1}) // disarm ping reply
	f.clock.Advance(10*time.Second + pump.TimerGrace)
	waitFor(t, "job success after resume", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
}

func TestPauseWhileRotating(t *testing.T) {
	f := newCalibratedFixture(t)
	f.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	resp := f.exec("pause", "")
	if resp.Status != "ok" || f.resultMap(resp)["state"] != "paused" {
		t.Fatalf("pause while rotating: %+v", resp)
	}
	resp = f.exec("resume", "")
	if resp.Status != "ok" || f.resultMap(resp)["state"] != "rotating" {
		t.Fatalf("resume back to rotating: %+v", resp)
	}
}

func TestPauseIdleIsBusy(t *testing.T) {
	f := newCalibratedFixture(t)
	resp := f.exec("pause", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("pause idle: %+v", resp)
	}
	m, _ := resp.Error.Details.(map[string]any)
	if m["state"] != "idle" {
		t.Fatalf("details: %#v", resp.Error.Details)
	}
}

func TestPauseTwiceRejected(t *testing.T) {
	f := newCalibratedFixture(t)
	startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.exec("pause", "")
	resp := f.exec("pause", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("double pause would double-toggle cmd 19: %+v", resp)
	}
	if countFrames(f, 19) != 1 {
		t.Fatal("second pause must not send a frame")
	}
	resp = f.exec("resume", "")
	if resp.Status != "ok" {
		t.Fatalf("resume: %+v", resp)
	}
	resp = f.exec("resume", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("double resume: %+v", resp)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/pump/ -run TestPause -v`
Expected: FAIL — `unknown_command`.

- [ ] **Step 3: Implement**

Append to `internal/device/pump/commands.go`:

```go
type pauseResult struct {
	State       string   `json:"state"`
	JobID       string   `json:"job_id,omitempty"`
	DispensedMl *float64 `json:"dispensed_ml,omitempty"`
}

// pause / resume (TRANSLATION §4): cmd 19 is a blind toggle with no state
// query, so the frame goes out via WriteFrame — single write, no retry — a
// duplicate send would invert the toggle undetectably (PR-1 decision 4).
// pauseAssumed tracks our belief; every cmd-10 frame (dispense/rotate/stop)
// forces the firmware toggle to "running", so a desync from panel use never
// survives past the current job.
func (d *Driver) pause() (any, *device.CmdError) {
	switch d.state {
	case stateIdle:
		return nil, device.ErrBusy("nothing to pause", map[string]any{"state": "idle"})
	case statePaused:
		return nil, device.ErrBusy("already paused", map[string]any{"state": "paused"})
	}
	if err := d.s.WriteFrame(pauseFrame); err != nil {
		return nil, device.ErrHardware("pause: " + err.Error())
	}
	d.pauseAssumed = true
	d.s.Jobs().Freeze()
	d.pausedFrom = d.state
	d.state = statePaused

	res := pauseResult{State: "paused"}
	if a := d.s.Jobs().Active(); a != nil {
		res.JobID = a.ID
		if d.job != nil && d.job.kind == "dispense" {
			v := a.Progress * d.job.volumeML
			res.DispensedMl = &v
		}
	}
	return res, nil
}

func (d *Driver) resume() (any, *device.CmdError) {
	if d.state != statePaused {
		return nil, device.ErrBusy("not paused", map[string]any{"state": string(d.state)})
	}
	if err := d.s.WriteFrame(pauseFrame); err != nil {
		return nil, device.ErrHardware("resume: " + err.Error())
	}
	d.pauseAssumed = false
	d.s.Jobs().Unfreeze()
	d.state = d.pausedFrom
	res := pauseResult{State: string(d.state)}
	if a := d.s.Jobs().Active(); a != nil {
		res.JobID = a.ID
	}
	return res, nil
}
```

Extend the `Execute` switch in `pump.go`:

```go
	case "pause":
		return d.pause()
	case "resume":
		return d.resume()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/pump/ -count=1`
Expected: PASS. `TestPauseFreezesJobClock` proves the pause-belief bookkeeping AND that the timer path waits out paused time (timerFire re-arms).

- [ ] **Step 5: Commit**

```bash
git add internal/device/pump/commands.go internal/device/pump/commands_test.go internal/device/pump/pump.go
git commit -m "feat(pump): pause and resume with pause-belief tracking

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---
### Task 8: Opcode-18 watcher — hardware completion for plain forward dispense

**Files:**
- Create: `internal/device/pump/watch.go`
- Modify: `internal/device/pump/job.go` (remove the `useWatcher` gate and the `startWatch` stub)
- Test: `internal/device/pump/watch_test.go`

**Interfaces:**
- Consumes: `d.s.Conn()` (captured ON the loop — decision 1), `d.s.Go`, `d.s.Post`, `d.s.HoldReader()/ReleaseReader()`, `d.s.After`, `watchHandle` (Task 3), `finishJob`/`clearJob` (Task 6), `WatchPoll`.
- Produces:
  - `func (d *Driver) startWatch(gen int, estimate time.Duration)` (replaces the Task 6 stub)
  - `func readCompletion(port serial.Port, stop <-chan struct{}) ([]byte, error)` — blocking 4-byte read on the watcher goroutine
  - `func (d *Driver) watchEvent(h *watchHandle, gen int, reply []byte, err error)` — loop-side completion handler
  - `func (d *Driver) armWatchdog(gen int, budget time.Duration)`, `func (d *Driver) watchdogFire(gen int, budget time.Duration)` — loop-side timeout (estimate × 1.5 + 5 s of active time, pause-extended)
  - `func (d *Driver) abandonWatch()` — synchronous abandon used by `stop` (Task 9) and reused nowhere else

- [ ] **Step 1: Write the failing tests**

Create `internal/device/pump/watch_test.go`:

```go
package pump_test

import (
	"testing"
	"time"
)

// TestDispenseForwardUsesOpcode18AndMeasuredDuration: plain forward dispense
// must be issued as opcode 18 and complete on the device's elapsed-µs reply
// (a real hardware completion), not the clock.
func TestDispenseForwardUsesOpcode18AndMeasuredDuration(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	fr := f.frames()
	if last := fr[len(fr)-1]; last[0] != 18 || !frameEq(last, 18, 0, 0, 7, 208) {
		t.Fatalf("plain forward dispense must use opcode 18: %v", last)
	}

	// Completion reply: 19,400,000 µs = 0x01280A40, then the disarm ping reply.
	f.port.Feed([]byte{0x01, 0x28, 0x0A, 0x40})
	f.port.Feed([]byte{10, 26, 25, 1})
	waitFor(t, "hardware completion", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
	res := jobState(t, f, id)["result"].(map[string]any)
	if res["duration_s"] != 19.4 { // measured, not the 20 s estimate
		t.Fatalf("duration_s = %v, want 19.4 (measured)", res["duration_s"])
	}
	if f.resultMap(f.exec("status", ""))["state"] != "idle" {
		t.Fatal("must return to idle")
	}
	// reader must be released: an idle ping (reply-expecting) works
	f.port.Feed([]byte{10, 0, 0, 0})
	if resp := f.exec("ping", ""); resp.Status != "ok" {
		t.Fatalf("ping after completion: %+v", resp)
	}
}

// TestWatcherBlocksReplyExpectingTraffic: while the opcode-18 reply is
// pending, a reply-expecting command sneaking onto the wire would interleave
// with the completion reply — memory-served ping must NOT touch the port.
func TestWatcherServesPingFromMemoryMidJob(t *testing.T) {
	f := newCalibratedFixture(t)
	startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	before := len(f.frames())
	resp := f.exec("ping", "")
	if resp.Status != "ok" {
		t.Fatalf("mid-job ping: %+v", resp)
	}
	if len(f.frames()) != before {
		t.Fatalf("mid-job ping must not touch the serial port: %v", f.frames()[before:])
	}
}

// TestWatchdogTimeoutFailsJob: no completion reply ever arrives (e.g. panel
// STOP silently halted the run) → after estimate×1.5 + 5 s of active time
// the watchdog abandons the wait and fails the job; the disarm ping still
// runs afterwards.
func TestWatchdogTimeoutFailsJob(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.port.Feed([]byte{10, 26, 25, 1}) // the post-timeout disarm ping reply
	f.clock.Advance(35 * time.Second)  // 20 × 1.5 + 5
	waitFor(t, "watchdog failure", func() bool {
		return jobState(t, f, id)["state"] == "failed"
	})
	js := jobState(t, f, id)
	errObj := js["error"].(map[string]any)
	if errObj["code"] != "hardware_error" {
		t.Fatalf("job error: %v", js)
	}
	if f.resultMap(f.exec("status", ""))["state"] != "idle" {
		t.Fatal("must return to idle after watchdog failure")
	}
	// session must still be reachable (device may be fine; only the run
	// outcome is unknown) and the reader released
	if !f.s.Connected() {
		t.Fatal("watchdog timeout must not flip the session unreachable")
	}
	f.port.Feed([]byte{10, 0, 0, 0})
	if resp := f.exec("ping", ""); resp.Status != "ok" {
		t.Fatalf("ping after watchdog: %+v", resp)
	}
}

// TestWatchdogExtendedByPause: paused time must not count against the
// watchdog budget (TRANSLATION §4 dispense step 9).
func TestWatchdogExtendedByPause(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.exec("pause", "")
	f.clock.Advance(2 * time.Minute) // paused: frozen clock, watchdog re-arms
	if js := jobState(t, f, id); js["state"] != "paused" {
		t.Fatalf("job must survive a long pause: %v", js)
	}
	f.exec("resume", "")
	// complete normally after resume
	f.port.Feed([]byte{0x01, 0x28, 0x0A, 0x40})
	f.port.Feed([]byte{10, 26, 25, 1})
	waitFor(t, "completion after pause", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
}

// TestPauseDuringWatcherJobUsesWriteOnlyPath: cmd 19 while the reader is
// held must go out as a bare write (no drain that would eat the pending
// completion reply). The completion reply fed BEFORE the pause frame is
// still consumed correctly afterwards.
func TestPauseDuringWatcherJobUsesWriteOnlyPath(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	if resp := f.exec("pause", ""); resp.Status != "ok" {
		t.Fatalf("pause during opcode-18 job: %+v", resp)
	}
	if resp := f.exec("resume", ""); resp.Status != "ok" {
		t.Fatalf("resume: %+v", resp)
	}
	f.port.Feed([]byte{0x01, 0x28, 0x0A, 0x40})
	f.port.Feed([]byte{10, 26, 25, 1})
	waitFor(t, "completion", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
}

// TestWatcherToleratesPortDeathMidJob (decisions 1+2): the port dies while
// the watcher blocks on the completion reply. The watcher — which captured
// the port value on the loop, never via Conn() from its own goroutine —
// unblocks with ErrClosed and its posted event fails the job cleanly (no
// panic, no double state change). The session itself stays connected: no
// Transact failed, and recovery belongs to the unreachable machinery
// whenever the next command touches the port.
func TestWatcherToleratesPortDeathMidJob(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	_ = f.port.Close() // port dies under the watcher
	waitFor(t, "job failed by watcher death", func() bool {
		return jobState(t, f, id)["state"] == "failed"
	})
	if st := f.resultMap(f.exec("status", ""))["state"]; st != "idle" {
		t.Fatalf("driver must return to idle after watcher death: %v", st)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/pump/ -run 'TestDispenseForward|TestWatch|TestPauseDuring|TestWatcherTolerates' -v`
Expected: FAIL — opcode 18 jobs complete via the timer (wrong duration), `TestDispenseForwardUsesOpcode18AndMeasuredDuration` gets `duration_s` = 20.0 (estimate) instead of 19.4 after advancing... the completion never fires without a clock advance, so the `waitFor` times out.

- [ ] **Step 3: Implement**

Create `internal/device/pump/watch.go`:

```go
package pump

import (
	"errors"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

var errWatchAbandoned = errors.New("pump: completion watch abandoned")

// startWatch begins the opcode-18 completion wait (TRANSLATION §4 dispense
// step 9): a watcher goroutine blocks on the 4-byte elapsed-µs reply while
// the loop holds the reader — only write-only frames (stop/pause/resume) may
// touch the port meanwhile. The port handle is captured HERE, on the session
// goroutine, and closed over — never fetched inside the watcher (decision 1:
// reattach swaps the port; the old handle unblocks with ErrClosed).
// Loop-side, a watchdog bounds the wait to estimate×1.5 + 5 s of active
// (unpaused) time.
func (d *Driver) startWatch(gen int, estimate time.Duration) {
	port := d.s.Conn()
	h := &watchHandle{stop: make(chan struct{}), done: make(chan struct{})}
	d.watch = h
	d.s.HoldReader()
	d.s.Go(func() {
		reply, err := readCompletion(port, h.stop)
		close(h.done) // before the Post: stop() may be blocking on done
		d.s.Post(func() { d.watchEvent(h, gen, reply, err) })
	})
	d.armWatchdog(gen, time.Duration(1.5*float64(estimate))+5*time.Second)
}

// readCompletion accumulates exactly 4 reply bytes on the watcher goroutine,
// polling in WatchPoll slices so an abandon signal is noticed promptly.
// Returns serial.ErrClosed when a reattach/shutdown closes the port.
func readCompletion(port serial.Port, stop <-chan struct{}) ([]byte, error) {
	if err := port.SetReadTimeout(WatchPoll); err != nil {
		return nil, err
	}
	buf := make([]byte, 0, 4)
	for {
		select {
		case <-stop:
			return nil, errWatchAbandoned
		default:
		}
		chunk := make([]byte, 4-len(buf))
		n, err := port.Read(chunk)
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk[:n]...)
		if len(buf) == 4 {
			return buf, nil
		}
	}
}

// watchEvent handles the watcher's report on the loop; its first act is
// releasing the reader (decision 3: release happens on the loop, via the
// watcher's Post). The release is guarded by watch identity: a stale event
// from a watcher already torn down by stop/Detach must not release a
// SUCCESSOR watcher's hold — abandonWatch released that stale watcher's
// hold itself. Stale events and jobs already failed by an unreachable
// transition (decision 2) are no-ops.
func (d *Driver) watchEvent(h *watchHandle, gen int, reply []byte, err error) {
	if d.watch != h {
		return // consumed by stop/Detach; abandonWatch/shutdown owned the release
	}
	d.s.ReleaseReader()
	d.watch = nil
	if gen != d.jobGen || d.job == nil || d.s.Jobs().Active() == nil {
		return // job already failed/cancelled elsewhere — tolerate
	}
	switch {
	case h.timedOut:
		// No completion within the budget: panel interference or a stall —
		// the run outcome is unknown (TRANSLATION §4 pause gap, mitigation a).
		d.s.Jobs().Fail(device.ErrHardware(
			"completion reply never arrived (panel interference or stall?)"))
		d.clearJob()
		// Panel disarm + liveness check still run; a failure here flips the
		// session unreachable via the standard Transact path.
		_, _ = d.s.Transact(serialFrame, 4, replyTimeout)
	case err != nil:
		// Port died mid-wait (reattach/shutdown closed it). The unreachable
		// machinery owns recovery; if the job somehow still shows active
		// (e.g. the write that failed was not job-fatal), fail it here.
		d.s.Jobs().Fail(device.ErrHardware("completion wait aborted: " + err.Error()))
		d.clearJob()
	default:
		us := uint32(reply[0])<<24 | uint32(reply[1])<<16 | uint32(reply[2])<<8 | uint32(reply[3])
		d.finishJob(gen, time.Duration(us)*time.Microsecond)
	}
}

// armWatchdog bounds the completion wait in ACTIVE time: fired early because
// a pause froze the job clock → re-arm for the remainder (one timer
// outstanding at a time — no unbounded Posts, decision 7).
func (d *Driver) armWatchdog(gen int, budget time.Duration) {
	d.s.After(budget, func() { d.watchdogFire(gen, budget) })
}

func (d *Driver) watchdogFire(gen int, budget time.Duration) {
	if gen != d.jobGen || d.job == nil || d.watch == nil {
		return // job finished or was replaced; nothing to time out
	}
	active := d.s.Jobs().Active()
	if active == nil {
		return
	}
	if elapsed := elapsedOf(active); elapsed < budget {
		d.s.After(budget-elapsed, func() { d.watchdogFire(gen, budget) })
		return
	}
	d.watch.timedOut = true
	close(d.watch.stop) // the watcher exits and posts the timeout event
}

// abandonWatch synchronously tears down a pending watch (used by stop: the
// firmware only replies to opcode 18 if the run finishes on its own, so
// after a stop frame the reply will never come). Blocks the loop up to
// ~WatchPoll — bounded, and done is always closed BEFORE the watcher's
// final Post, so this cannot deadlock. Clearing d.watch first makes the
// watcher's queued Post a full no-op in watchEvent, so the release below
// is the abandoned watcher's only release — a successor watcher's hold is
// never touched.
func (d *Driver) abandonWatch() {
	if d.watch == nil {
		return
	}
	h := d.watch
	d.watch = nil
	close(h.stop)
	<-h.done
	d.s.ReleaseReader()
}
```

In `internal/device/pump/job.go`:
1. Delete the `useWatcher` constant and the `startWatch` stub.
2. In `dispense`, change the completion dispatch to:

```go
	if plan.opcode == 18 {
		d.startWatch(gen, plan.job.estimate)
	} else {
		d.armTimer(gen)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/pump/ -count=1`
Expected: PASS — including all earlier tasks' tests (`TestDispenseReverseTimerCompletion` still uses the timer path; forward tests now take the watcher path).

- [ ] **Step 5: Commit**

```bash
git add internal/device/pump/watch.go internal/device/pump/watch_test.go internal/device/pump/job.go
git commit -m "feat(pump): opcode-18 completion watcher with pause-aware watchdog

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: `stop`

**Files:**
- Modify: `internal/device/pump/commands.go` (handler), `internal/device/pump/pump.go` (dispatch)
- Test: `internal/device/pump/commands_test.go` (append)

**Interfaces:**
- Consumes: `abandonWatch` (Task 8), `stopFrame`, `identifyFrame`, `d.s.Jobs().Cancel()`, `clearJob` fields.
- Produces: `func (d *Driver) stop() (any, *device.CmdError)` returning `stopResult{State, CancelledJobID, DispensedMl}`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/device/pump/commands_test.go`:

```go
func TestStopCancelsWatcherJob(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.clock.Advance(5 * time.Second) // a quarter through the 20 s estimate

	f.port.Feed([]byte{10, 0, 0, 0}) // post-stop verification reply
	resp := f.exec("stop", "")
	if resp.Status != "ok" {
		t.Fatalf("stop: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["state"] != "idle" || m["cancelled_job_id"] != id {
		t.Fatalf("stop result: %v", m)
	}
	if got := m["dispensed_ml"].(float64); got < 0.2 || got > 0.3 {
		t.Fatalf("dispensed estimate: %v", got)
	}
	// frame order: ... [10 0 0 0 0] halt, then [1 2 3 0 0] verification
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 10, 0, 0, 0, 0) || !frameEq(fr[n-1], 1, 2, 3, 0, 0) {
		t.Fatalf("stop frames: %v", fr[n-2:])
	}
	if js := jobState(t, f, id); js["state"] != "cancelled" {
		t.Fatalf("job: %v", js)
	}
	// watcher fully torn down: a fresh dispense works and completes
	id2 := startDispense(t, f, `{"direction":"forward","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.port.Feed([]byte{0x01, 0x28, 0x0A, 0x40})
	f.port.Feed([]byte{10, 26, 25, 1})
	waitFor(t, "second dispense completes", func() bool {
		return jobState(t, f, id2)["state"] == "succeeded"
	})
}

func TestStopEndsRotation(t *testing.T) {
	f := newCalibratedFixture(t)
	f.exec("rotate", `{"direction":"forward","speed_ml_min":3.0}`)
	f.port.Feed([]byte{10, 0, 0, 0})
	resp := f.exec("stop", "")
	m := f.resultMap(resp)
	if resp.Status != "ok" || m["state"] != "idle" {
		t.Fatalf("stop: %+v", resp)
	}
	if _, has := m["cancelled_job_id"]; has {
		t.Fatalf("no job to cancel when rotating: %v", m)
	}
	if f.resultMap(f.exec("status", ""))["state"] != "idle" {
		t.Fatal("status must be idle")
	}
}

func TestStopWhilePausedCancels(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.exec("pause", "")
	f.port.Feed([]byte{10, 0, 0, 0})
	resp := f.exec("stop", "")
	if resp.Status != "ok" || f.resultMap(resp)["cancelled_job_id"] != id {
		t.Fatalf("stop while paused: %+v", resp)
	}
	if js := jobState(t, f, id); js["state"] != "cancelled" {
		t.Fatalf("job: %v", js)
	}
}

func TestStopIdleSucceeds(t *testing.T) {
	f := newCalibratedFixture(t)
	f.port.Feed([]byte{10, 0, 0, 0})
	resp := f.exec("stop", "")
	if resp.Status != "ok" || f.resultMap(resp)["state"] != "idle" {
		t.Fatalf("idle stop must succeed: %+v", resp)
	}
}

func TestStopVerificationFailure(t *testing.T) {
	f := newCalibratedFixture(t)
	startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	// no verification reply → hardware_error and unreachable
	resp := f.exec("stop", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("stop without verification reply: %+v", resp)
	}
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/pump/ -run TestStop -v`
Expected: FAIL — `unknown_command`.

- [ ] **Step 3: Implement**

Append to `internal/device/pump/commands.go`:

```go
type stopResult struct {
	State          string   `json:"state"`
	CancelledJobID string   `json:"cancelled_job_id,omitempty"`
	DispensedMl    *float64 `json:"dispensed_ml,omitempty"`
}

// stop (TRANSLATION §4): the cmd-10 frame clears the remaining step count
// and takes effect within one step period. It also forces the firmware's
// pause toggle to "running" — stop doubles as the pause-belief resync point.
// An opcode-18 wait is abandoned (the firmware only replies if the run
// finishes on its own). Post-stop, the identify frame verifies the device
// is still responsive.
func (d *Driver) stop() (any, *device.CmdError) {
	if _, err := d.s.Transact(stopFrame, 0, time.Second); err != nil {
		return nil, device.ErrHardware("stop: " + err.Error())
	}
	d.pauseAssumed = false
	d.abandonWatch()

	res := stopResult{State: "idle"}
	if a := d.s.Jobs().Active(); a != nil {
		cancelled := d.s.Jobs().Cancel()
		res.CancelledJobID = cancelled.ID
		if d.job != nil && d.job.kind == "dispense" {
			v := cancelled.Progress * d.job.volumeML
			res.DispensedMl = &v
		}
	}
	d.job = nil
	d.jobGen++ // invalidate any in-flight timer/watchdog callbacks
	d.state = stateIdle
	d.pausedFrom = ""
	d.rotDirection, d.rotSpeedML, d.rotSpeedPct = "", 0, 0

	reply, err := d.s.Transact(identifyFrame, 4, replyTimeout)
	if err != nil {
		return nil, device.ErrHardware("post-stop verification: " + err.Error())
	}
	if reply[0] != TypeCode {
		return nil, device.ErrHardware("post-stop verification: unexpected reply")
	}
	return res, nil
}
```

Extend the `Execute` switch in `pump.go`:

```go
	case "stop":
		return d.stop()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/pump/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/device/pump/commands.go internal/device/pump/commands_test.go internal/device/pump/pump.go
git commit -m "feat(pump): stop with watcher abandon and post-stop verification

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---
### Task 10: `start_calibration` / `set_calibration` — calibration flow and persistence

**Files:**
- Modify: `internal/device/pump/job.go` (start_calibration — reuses `launchMotion`/`startWatch`), `internal/device/pump/commands.go` (set_calibration), `internal/device/pump/pump.go` (dispatch)
- Test: `internal/device/pump/calibration_test.go`

**Interfaces:**
- Consumes: `CalSteps`, `rawDelTimeUs`, `factorDelTime`, `launchMotion`, `startWatch`, `calibrationRunResult` (Task 6), `persistState`/`d.store` (Task 3), `d.s.SetInfo`, `d.s.Jobs().Get`.
- Produces:
  - `func (d *Driver) startCalibration(params json.RawMessage) (any, *device.CmdError)`
  - `func (d *Driver) setCalibration(params json.RawMessage) (any, *device.CmdError)`
  - `func (d *Driver) persistCalibration(mlPerStep float64) *device.CmdError` (store save + EEPROM mirror + mirror verify + `SetInfo` refresh)

- [ ] **Step 1: Write the failing tests**

Create `internal/device/pump/calibration_test.go`:

```go
package pump_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/pump"
)

// runCalibration drives a start_calibration job to success and returns its id.
// 20000 steps at the default 50 % (→ 400 µs half-period): estimate 16 s.
func runCalibration(t *testing.T, f *fixture) string {
	t.Helper()
	resp := f.exec("start_calibration", `{"speed_pct":50}`)
	if resp.Status != "ok" {
		t.Fatalf("start_calibration: %+v", resp)
	}
	id := f.resultMap(resp)["job"].(map[string]any)["job_id"].(string)
	if st := f.resultMap(f.exec("status", ""))["state"]; st != "calibrating" {
		t.Fatalf("state = %v", st)
	}
	// completion reply: 15,800,000 µs = 0x00F11870, then disarm ping reply
	f.port.Feed([]byte{0x00, 0xF1, 0x18, 0x70})
	f.port.Feed([]byte{10, 26, 25, 1})
	waitFor(t, "calibration completes", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
	return id
}

func TestStartCalibrationFramesAndResult(t *testing.T) {
	f := newFixture(t) // works UNCALIBRATED — that's the point
	id := runCalibration(t, f)
	fr := f.frames()
	// find the config + opcode-18 frames it sent (before the disarm ping):
	// 50 % → 400 µs → [1 4]; 20000 steps = 0x00004E20
	n := len(fr)
	if !frameEq(fr[n-3], 10, 0, 1, 4, 0) || !frameEq(fr[n-2], 18, 0, 0, 78, 32) {
		t.Fatalf("calibration frames: %v", fr)
	}
	res := jobState(t, f, id)["result"].(map[string]any)
	if res["steps"] != float64(20000) || res["duration_s"] != 15.8 {
		t.Fatalf("calibration result: %v", res)
	}
}

func TestSetCalibrationFromJob(t *testing.T) {
	f := newFixture(t)
	id := runCalibration(t, f)
	// measured 10.0 ml over 20000 steps → 0.0005 ml/step
	// mirror v = 0.0005 × 1e8 = 50000 = 0x00C350 → frame [13 0 0 195 80],
	// then identify returns the same bytes for the mirror verify.
	f.port.Feed([]byte{10, 0, 195, 80})
	resp := f.exec("set_calibration", `{"job_id":"`+id+`","measured_volume_ml":10.0}`)
	if resp.Status != "ok" {
		t.Fatalf("set_calibration: %+v", resp)
	}
	if f.resultMap(resp)["ml_per_step"] != 0.0005 {
		t.Fatalf("result: %v", f.resultMap(resp))
	}
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 13, 0, 0, 195, 80) || !frameEq(fr[n-1], 1, 2, 3, 0, 0) {
		t.Fatalf("mirror frames: %v", fr[n-2:])
	}
	// capabilities refreshed: identify now reports speed limits
	caps := f.resultMap(f.exec("identify", ""))["capabilities"].(map[string]any)
	if caps["speed_ml_min"] == nil {
		t.Fatalf("capabilities not refreshed: %v", caps)
	}
	// metered dispensing is now available
	if resp := f.exec("dispense", `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`); resp.Status != "ok" {
		t.Fatalf("dispense after calibration: %+v", resp)
	}
}

func TestSetCalibrationPersistsAcrossSessions(t *testing.T) {
	dir := t.TempDir()
	f := newFixture(t, withStateDir(dir))
	id := runCalibration(t, f)
	f.port.Feed([]byte{10, 0, 195, 80})
	if resp := f.exec("set_calibration", `{"job_id":"`+id+`","measured_volume_ml":10.0}`); resp.Status != "ok" {
		t.Fatalf("set_calibration: %+v", resp)
	}
	f.s.Close()

	// fresh session, same state dir: calibration must be recovered VERIFIED
	f2 := newFixture(t, withStateDir(dir))
	m := f2.resultMap(f2.exec("get_calibration", ""))
	if m["ml_per_step"] != 0.0005 || m["unverified"] != nil {
		t.Fatalf("recovered calibration: %v", m)
	}
}

func TestSetCalibrationDirect(t *testing.T) {
	f := newFixture(t)
	f.port.Feed([]byte{10, 0, 195, 80}) // mirror verify reply
	resp := f.exec("set_calibration", `{"ml_per_step":0.0005}`)
	if resp.Status != "ok" {
		t.Fatalf("direct set_calibration: %+v", resp)
	}
}

func TestSetCalibrationConfirmsUnverifiedMirror(t *testing.T) {
	f := newFixture(t, withProbeReply([]byte{10, 0, 195, 80})) // unverified 0.0005
	f.port.Feed([]byte{10, 0, 195, 80})
	if resp := f.exec("set_calibration", `{"ml_per_step":0.0005}`); resp.Status != "ok" {
		t.Fatalf("confirming set_calibration: %+v", resp)
	}
	caps := f.resultMap(f.exec("identify", ""))["capabilities"].(map[string]any)
	if caps["calibration_unverified"] != nil || caps["speed_ml_min"] == nil {
		t.Fatalf("must be verified now: %v", caps)
	}
}

func TestSetCalibrationMirrorMismatch(t *testing.T) {
	f := newFixture(t)
	f.port.Feed([]byte{10, 9, 9, 9}) // device echoes WRONG mirror bytes
	resp := f.exec("set_calibration", `{"ml_per_step":0.0005}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("mirror mismatch: %+v", resp)
	}
}

func TestSetCalibrationValidation(t *testing.T) {
	f := newFixture(t)
	for _, params := range []string{
		`{}`,                          // neither variant
		`{"ml_per_step":0.5}`,         // > 0.1 sanity bound
		`{"ml_per_step":1e-9}`,        // < 1e-6 sanity bound
		`{"job_id":"j-99","measured_volume_ml":10}`, // unknown job
		`{"job_id":"j-1","measured_volume_ml":10,"ml_per_step":0.0005}`, // both variants
	} {
		resp := f.exec("set_calibration", params)
		if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
			t.Fatalf("params %s: %+v", params, resp)
		}
	}
}

func TestSetCalibrationFromDispenseJobRejected(t *testing.T) {
	f := newCalibratedFixture(t)
	id := startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	f.port.Feed([]byte{10, 26, 25, 1})
	f.clock.Advance(20*time.Second + pump.TimerGrace)
	waitFor(t, "dispense done", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
	resp := f.exec("set_calibration", `{"job_id":"`+id+`","measured_volume_ml":10}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
		t.Fatalf("dispense job must not calibrate: %+v", resp)
	}
}

func TestCalibrationCommandsBusyMidJob(t *testing.T) {
	f := newCalibratedFixture(t)
	startDispense(t, f, `{"direction":"reverse","volume_ml":1.0,"speed_ml_min":3.0}`)
	for _, cmd := range []string{"start_calibration", "set_calibration"} {
		resp := f.exec(cmd, `{"ml_per_step":0.0005,"speed_pct":50}`)
		if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
			t.Fatalf("%s mid-job: %+v", cmd, resp)
		}
	}
}

func TestStartCalibrationValidatesPct(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("start_calibration", `{"speed_pct":101}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
		t.Fatalf("%+v", resp)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/pump/ -run 'TestStartCalibration|TestSetCalibration|TestCalibrationCommands' -v`
Expected: FAIL — `unknown_command`.

- [ ] **Step 3: Implement**

Append to `internal/device/pump/job.go`:

```go
// startCalibration (TRANSLATION §4): a fixed CalSteps forward run issued as
// opcode 18, so the completion reply gives a measured duration. speed_pct
// uses the calibration-independent rotate_raw mapping (default 50 %).
func (d *Driver) startCalibration(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		SpeedPct int `json:"speed_pct"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
		}
	}
	if p.SpeedPct == 0 {
		p.SpeedPct = 50
	}
	if p.SpeedPct < 1 || p.SpeedPct > 100 {
		return nil, device.ErrInvalidParams("speed_pct", p.SpeedPct, "speed_pct must be 1..100")
	}
	if cerr := d.busyGuard(); cerr != nil {
		return nil, cerr
	}
	if d.state == stateRotating {
		return nil, device.ErrBusy("device is rotating — stop first",
			map[string]any{"state": string(stateRotating)})
	}
	n3, n4, actualUs := factorDelTime(rawDelTimeUs(p.SpeedPct))
	plan := &dispensePlan{
		opcode: 18, n3: n3, n4: n4,
		job: motionJob{
			kind: "calibration", direction: "forward", steps: CalSteps,
			delTimeUs: actualUs, estimate: plainEstimate(CalSteps, actualUs),
		},
	}
	job, cerr := d.launchMotion(plan)
	if cerr != nil {
		return nil, cerr
	}
	d.state = stateCalibrating
	d.startWatch(d.jobGen, plan.job.estimate)
	return map[string]any{"job": job}, nil
}
```

Append to `internal/device/pump/commands.go`:

```go
// setCalibration (TRANSLATION §4): variant A computes ml_per_step from a
// succeeded calibration job; variant B restores a known value directly.
// Either way the value is persisted serial-keyed, mirrored to the device's
// 3 EEPROM calibration bytes (cmd 13 — survives translator-database loss),
// and the mirror is read back for verification via the identify frame.
func (d *Driver) setCalibration(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		JobID            string  `json:"job_id"`
		MeasuredVolumeMl float64 `json:"measured_volume_ml"`
		MlPerStep        float64 `json:"ml_per_step"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if cerr := d.busyGuard(); cerr != nil {
		return nil, cerr
	}
	if d.state != stateIdle {
		return nil, device.ErrBusy("device is moving — stop first",
			map[string]any{"state": string(d.state)})
	}

	var mlPerStep float64
	switch {
	case p.JobID != "" && p.MlPerStep != 0:
		return nil, device.ErrInvalidParams("ml_per_step", p.MlPerStep,
			"provide either job_id+measured_volume_ml or ml_per_step, not both")
	case p.JobID != "":
		if p.MeasuredVolumeMl <= 0 {
			return nil, device.ErrInvalidParams("measured_volume_ml", p.MeasuredVolumeMl,
				"measured_volume_ml must be positive")
		}
		job := d.s.Jobs().Get(p.JobID)
		if job == nil || job.State != device.JobSucceeded || job.Kind != "calibration" {
			return nil, device.ErrInvalidParams("job_id", p.JobID,
				"job_id must reference a succeeded calibration job")
		}
		res, ok := job.Result.(calibrationRunResult)
		if !ok || res.Steps <= 0 {
			return nil, device.ErrInternal("calibration job has no step count")
		}
		mlPerStep = p.MeasuredVolumeMl / float64(res.Steps)
	case p.MlPerStep != 0:
		mlPerStep = p.MlPerStep
	default:
		return nil, device.ErrInvalidParams("ml_per_step", nil,
			"provide job_id+measured_volume_ml or ml_per_step")
	}
	if mlPerStep < 1e-6 || mlPerStep > 0.1 {
		return nil, device.ErrInvalidParams("ml_per_step", mlPerStep,
			"ml_per_step out of sane range [1e-6, 0.1]")
	}
	if cerr := d.persistCalibration(mlPerStep); cerr != nil {
		return nil, cerr
	}
	return map[string]any{"ml_per_step": mlPerStep}, nil
}

func (d *Driver) persistCalibration(mlPerStep float64) *device.CmdError {
	now := d.s.Now()
	err := d.store.Save(persistState{
		SchemaVersion: schemaV, MlPerStep: mlPerStep, SetAt: now, Serial: d.serial,
	})
	if err != nil {
		return device.ErrInternal("persist calibration: " + err.Error())
	}
	d.mlPerStep, d.calSetAt, d.unverified = mlPerStep, now, false

	// EEPROM mirror (human-paced only — EEPROM wear rules). Round, don't
	// truncate: 0.0005 × 1e8 is 49999.999… in float64.
	v := uint32(math.Round(mlPerStep * 1e8)) // sanity bound keeps this under 24 bits
	if v > 0xFFFFFF {
		v = 0xFFFFFF
	}
	frame := []byte{13, 0, byte(v >> 16), byte(v >> 8), byte(v)}
	if _, err := d.s.Transact(frame, 0, time.Second); err != nil {
		return device.ErrHardware("calibration mirror write: " + err.Error())
	}
	reply, err := d.s.Transact(identifyFrame, 4, replyTimeout)
	if err != nil {
		return device.ErrHardware("calibration mirror verify: " + err.Error())
	}
	got := uint32(reply[1])<<16 | uint32(reply[2])<<8 | uint32(reply[3])
	if reply[0] != TypeCode || got != v {
		return device.ErrHardware("calibration mirror verify: device echoed different bytes")
	}
	d.s.SetInfo(d.info()) // capabilities changed (speed limits, unverified flag)
	return nil
}
```

Extend the `Execute` switch in `pump.go`:

```go
	case "start_calibration":
		return d.startCalibration(params)
	case "set_calibration":
		return d.setCalibration(params)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/pump/ -count=1`
Expected: PASS.

Notes: `persistCalibration` needs `"math"` added to `commands.go` imports. gosec/golangci may flag the float→int conversion in `uint32(math.Round(...))`; the sanity bound (≤ 0.1 → ≤ 1e7) makes it safe — add `// #nosec G115 -- bounded by the [1e-6, 0.1] sanity check` if the linter complains.

- [ ] **Step 5: Commit**

```bash
git add internal/device/pump/job.go internal/device/pump/commands.go internal/device/pump/calibration_test.go internal/device/pump/pump.go
git commit -m "feat(pump): calibration run, set_calibration with EEPROM mirror verify

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 11: Full-suite verification, pre-flight, PR

**Files:**
- No new code. Possibly `docs/superpowers/plans/2026-07-05-pump-driver.md` (this plan, committed with the PR as repo convention).

- [ ] **Step 1: Full test suite, race detector, both fast and default**

Run: `go test -race -count=1 ./...`
Expected: PASS across all packages (the core `internal/device` changes must not break existing session tests).

- [ ] **Step 2: Pre-flight (CLAUDE.md)**

```bash
gofmt -l .                     # must print nothing
go vet ./...
golangci-lint run
govulncheck ./...
```

Expected: all clean. Fix anything reported and re-run before continuing (typical candidates: unused parameters in stubs, gosec integer-conversion notes — see Task 10's `#nosec` note).

- [ ] **Step 3: Commit the plan document**

```bash
git add docs/superpowers/plans/2026-07-05-pump-driver.md
git commit -m "docs: pump driver implementation plan

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

- [ ] **Step 4: Push and open the PR**

```bash
git push -u origin pump-driver
gh pr create --title "feat: add pump device driver" --body "$(cat <<'EOF'
## Summary

PR 2 of 5 in the v2 JSON device protocol effort (spec: docs/superpowers/specs/2026-07-05-json-device-protocol-design.md §7).

- `internal/device/pump`: the pump Driver implementing docs/protocol_translation_docs/pump/TRANSLATION.md — opcode-18 hardware completion watcher, clock-simulated completions for reverse/suckback/gradient runs, pause-belief tracking, EEPROM-safe polling, panel-disarm ping, serial-keyed calibration persistence with device-mirror recovery.
- Core additions the pump needs: `Session.WriteFrame` (single-write, no-retry path for the blind cmd-19 pause toggle), `ErrNotCalibrated`, and the session shutdown-order swap (`connected=false` published before `driver.Detach()` so the pump's write-only safety stop cannot trigger the unreachable machinery mid-shutdown).

Nothing consumes the new package yet; wiring happens in the v2 API cutover PR.

## Test plan

- [x] `go test -race -count=1 ./...` (macOS locally; Windows via CI)
- [x] `gofmt -l .`, `go vet ./...`, `golangci-lint run`, `govulncheck ./...`

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 5: Watch CI**

Run: `gh pr checks --watch`
Expected: `verify` job green on both `macos-latest` and `windows-latest`.

---

## Execution notes for the implementer

- **Feeding replies in tests**: `DrainWindow` is shrunk to 0, so `FakePort.Feed` **before** the command keeps the bytes through the transaction's drain step. Reply-expecting transactions consume exactly 4 bytes, so multiple replies may be pre-fed concatenated in send order.
- **Real time vs fake time**: `WatchPoll` (5 ms in tests) and `PerByteTimeout` (10 ms) are real-time; everything else advances via `FakeClock`. `waitFor(...)` polls with a 2 s deadline — plenty for the watcher to notice fed bytes.
- **Never call `d.s.Conn()` inside a `d.s.Go` closure body** — capture it before, on the loop. This is the exact race PR 1's review flagged.
- **Do not add commands to `Execute` before their task** — earlier tasks' tests assert `unknown_command` for not-yet-implemented commands only implicitly; the dispatch grows one case per task.
