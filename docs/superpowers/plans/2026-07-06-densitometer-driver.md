# Densitometer Driver Implementation Plan (v2 PR 3 of 5)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `internal/device/densitometer` — the densitometer Driver that translates `docs/protocol_translation_docs/densitometer/JSON_PROTOCOL.md` onto the legacy 5-byte firmware exactly as `TRANSLATION.md` specifies, plugged into the merged `internal/device` core runtime.

**Architecture:** The device is used only as a sensor/actuator: it runs sweeps, returns raw 16-bit intensities, reads temperature, and drives the LED/thermostat. **All numeric work lives in the driver** — least-squares slope fitting, absorbance math, temperature compensation, tube correction, job tracking, monitoring, and the readings ring buffer. The driver runs entirely on the session goroutine (spec §3); it holds no mutexes. Long waits are never blocking on the loop: a sweep fires its trigger, sets a `busy_until` window during which serial commands fail fast with `busy`, and completes via an `s.After(SweepWait)` callback chain (liveness retry → 80-byte array read → slope/absorbance math → job completion). This PR is pure library + tests — nothing consumes the package until the PR 5 cutover, so it merges as a plain `feat:` with **no** `BREAKING CHANGE` footer.

**Tech Stack:** Go stdlib only (no new dependencies); the merged `internal/device` core; existing `internal/serial` `FakePort`/`FakeOpener` and `device.FakeClock` for tests.

## Global Constraints

- Module path: `github.com/bioexperiment-lab-devices/serialhop`. Work on branch `densitometer-driver`, already created off fresh `origin/main` (HEAD = pump PR #179 merged).
- PR title (squash commit on `main`): **`feat: add densitometer device driver`** — plain `feat:`. **NEVER** write `BREAKING CHANGE:` anywhere (body or footer); that footer is reserved for PR 5.
- Pre-flight before opening/pushing the PR (CLAUDE.md), all must pass on **macOS and Windows**: `gofmt -l .` prints nothing; `go vet ./...`; `golangci-lint run` (errcheck, staticcheck, unused, ineffassign, gosec); `go test -race -count=1 ./...`; `govulncheck ./...`. Or `task test` for the test portion.
- Tests: stdlib `testing` only, no testify. No Windows-only code is introduced (the `_windows.go` fake-coverage rule is unaffected).
- gosec is enabled: any file writes go through `device.Store` (already `0o600`/`0o700`); when narrowing an `int`/`int64` to `byte` for a frame, keep the value provably in `[0,255]` and add a `// #nosec G115 -- <reason>` comment on the conversion line only where a range check guarantees it (mirror `pump/commands.go:417`).
- Every value that clock-drives behavior (job progress, sweep completion, canary poll, monitoring schedule, reattach backoff) goes through the injectable `device.Clock` via `s.Now()` / `s.After()`. Real time is allowed **only** for serial I/O deadlines (`device.PerByteTimeout`, `device.DrainWindow`) and the two short device-settle waits below.
- Timing knobs are package `var`s so tests can shrink them (repo precedent: `device.PerByteTimeout`, `pump.WatchPoll`). Namely: `SweepWait` (6 s), `SingleLevelWait` (15 s), `ArrayReadTimeout` (3 s), `ThermoSettle` (1.5 s), `LivenessSpacing` (1 s), `LivenessRetries` (3), `CanaryInterval` (30 s).
- Canonical behavior source, in priority order: `docs/protocol_translation_docs/densitometer/TRANSLATION.md` (per-command algorithm), then `JSON_PROTOCOL.md` (wire shapes / error codes), then `PROTOCOL.md` (byte layer), then spec §2.4/§3/§5. Where TRANSLATION and JSON_PROTOCOL disagree, the JSON wire shape wins and the deviation is flagged (see "Flagged deviations" below).
- Commit messages end with the trailer `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.

## Decisions from PR-1/PR-2 review that this plan incorporates

1. **`busy_until` = fail-fast.** During a sweep the firmware does not read serial. Commands that would touch the port during the busy window return `busy` (`ping`, `set_thermostat`, `set_led`, `measure`, `measure_blank`, `read_raw`, `start_monitoring`, `calibrate_tube`'s… no — those are memory-only). `status` serves cached temperature/thermostat with an age instead. `stop` is the one exception (§stop): its `[70,…]` write buffers in the device RX and executes when the sweep ends. **No blocking wait inside `Execute`.** No `Clock` accessor is added to the core; sweep completion is an `s.After(SweepWait)` callback chain on the loop.
2. **No watcher goroutines, no `HoldReader`.** Every device reply is a transaction reply; all long waits are `After`-based. The one exception to "use `s.Transact`" is the liveness poll, which uses a raw `s.Conn()` read so it does **not** trip the session unreachable during its retry window (see Task 5) — this is a synchronous loop read, not a watcher.
3. **Reboot canary.** The firmware forces the thermostat set-point to 10 °C (off) at every boot, ignoring EEPROM; a driver mirror value is never 10 (only 0, or 20–45). So a `[76,2,…]` readback of exactly 10.00 ⇔ the device rebooted. The canary is checked in `Attach` §3 step 5, in `status` step 3, and on a ~30 s idle poll driven from `Tick`. Firing must: fail any active job (its sweep data is gone), reset `connected_since`, re-push the thermostat mirror, and log an alert. Device-side tube correction reloads as 1.0 from EEPROM on reboot, so it needs no re-send.
4. **Persistent state, serial-keyed** via `s.Store(serial)`: `{schema_version, blank{slope, temperature_c, measured_at}, tube_correction, thermostat{enabled, target_c}}`. `Attach` forces the device-side tube correction to 1.0 (`[75,3,0,0,0]`, EEPROM-persistent so it survives reboots and never needs re-sending); from then on **all** tube correction is applied in the driver.
5. **Monitoring is a driver-side scheduler on `Tick`** (the firmware `78 5` continuous mode is never used — it starves serial). Ring buffer of 64 readings, monotonic `seq` counter, `get_readings` with `since_seq`/`limit`/`dropped` semantics.
6. **Don't `Post` unboundedly from loop context** (posts buffer = 64). This driver's completion chain schedules at most one outstanding `After` at a time (mirror the pump's one-timer-outstanding rule). `Detach` performs **no** serial I/O (state is already persisted on every mutation), so the `connected=false`-before-`Detach` shutdown order — already swapped in PR 2 — is not load-bearing here, but do not regress it.

## Flagged deviations from the JSON_PROTOCOL doc (call these out in the PR body)

1. **Serial string format**: `identify.serial` is rendered `"%d-%03d"` → `"25-006"` (matching the JSON_PROTOCOL identify example), not TRANSLATION §3's literal `"<sn1>-<sn2>"` which would give `"25-6"`.
2. **`firmware_version`**: reported as the fixed fleet string `"legacy"` (consistent with the pump driver), not the JSON example's illustrative `"2.0.0"`. TRANSLATION §identify makes this a configured constant.
3. **`set_thermostat` settle**: after `[75,2,t,…]` the loop waits `ThermoSettle` (~1.5 s, a shrinkable package var) then verifies via `[76,2,…]`. This is a bounded loop-block for a rare, human-paced command, consistent with the accepted valve-`stop` loop-block in spec §3.
4. **`start_monitoring` requires a blank**: returns `not_calibrated` when no blank exists, because the scheduler's internal `measure` needs one. The JSON doc lists only the `interval_s` check.
5. **Liveness retry**: implemented as up to `LivenessRetries` polls spaced `LivenessSpacing`, the non-final polls via a raw `s.Conn()` read (no unreachable trip), the final poll a hard `s.Transact` that trips unreachable on failure — realizing TRANSLATION §4 step 6 "retry up to 3× with 1 s spacing … all fail → job failed" without adding a core primitive.

---

## File structure

All new files under `internal/device/densitometer/`. Mirrors the pump package's granularity (focused files, one responsibility each, a test file per source file plus a shared fixture).

| File | Responsibility | Task |
|---|---|---|
| `convert.go` | Pure decode/math helpers + timing/range package vars: `decodeFixedPoint`, `decodeIntensity`, `parseIntensityArray`, `leastSquaresSlope`, `absorbance`, `formatSerial`, and the tunable knobs. No I/O, no `Session`. | 1 |
| `densitometer.go` | Package doc, `TypeCode`, constants, frames, `Register`, `New`, `Driver` struct + state types, `Attach`, `Detach`, `info()`/capabilities, `Execute` dispatch, `serialGate`/`busyGuard`. | 2 |
| `thermostat.go` | `pushThermostat` (frame + settle + verify), `set_thermostat` handler, `applyThermostatReadback` canary, `resyncThermostat`/`handleReboot`, and `Attach`'s persisted-mirror branches. | 3 |
| `commands.go` | `ping`, `status` (cached-vs-live, canary), `stop`, `stop_monitoring`, `set_tube_correction`, `calibrate_tube`, `set_led`, `get_readings`. | 4, 6, 7, 8 (added incrementally) |
| `sweep.go` | `runSweep`, the `After`-based completion chain (`onSweepDone` → liveness → `readSweepAndFinish`), `softPing`, `measure_blank`, `measure`, `read_raw`. | 5, 6, 7 |
| `monitoring.go` | Ring buffer type, `start_monitoring`, `Tick` (canary poll + monitoring scheduler). | 8 |

Test files: `densitometer_test.go` (shared fixture + attach/identify/dispatch), `convert_test.go`, `thermostat_test.go`, `commands_test.go`, `sweep_test.go`, `monitoring_test.go`, plus a final `integration_test.go` (Task 9).

---

## Shared type & interface contract (defined in Task 2, referenced everywhere)

```go
// TypeCode is the densitometer's probe identify code (PROTOCOL.md §3: Name1 = 70).
const TypeCode = 70

// persistState is the serial-keyed on-disk schema (spec §5, decision 4).
type persistState struct {
    SchemaVersion  int               `json:"schema_version"`
    Blank          *blankState       `json:"blank"`           // nil until measure_blank
    TubeCorrection float64           `json:"tube_correction"` // default 1.0
    Thermostat     thermostatMirror  `json:"thermostat"`
}

type blankState struct {
    Slope        float64   `json:"slope"`
    TemperatureC float64   `json:"temperature_c"`
    MeasuredAt   time.Time `json:"measured_at"`
}

// thermostatMirror is the driver's belief of the device set-point. Its value is
// NEVER 10 (only 0 when disabled, or 20..45) — the reboot-canary invariant.
type thermostatMirror struct {
    Enabled bool    `json:"enabled"`
    TargetC float64 `json:"target_c"`
}

// mirrorValue is the °C the device set-point should read back as: 0 when
// disabled, else the target. (target < 20 disables on the firmware.)
func (m thermostatMirror) mirrorValue() float64 {
    if !m.Enabled {
        return 0
    }
    return m.TargetC
}

// reading is one buffered measurement. Wire-exposed fields feed get_readings;
// tubeCorrectionAt is internal, used by calibrate_tube to recover the
// uncorrected value.
type reading struct {
    seq             int64
    measuredAt      time.Time
    uptimeMs        int64
    absorbance      float64 // temperature-compensated, tube-corrected
    temperatureC    float64
    tubeCorrectionAt float64
}

// sweep carries the driver-side detail of the active sweep job (the Jobs engine
// owns lifecycle/progress; this holds what completion needs).
type sweep struct {
    gen        int
    kind       string // "blank" | "measure" | "read_raw" | "monitor"
    includeRaw bool   // measure: attach the 20-point sweep to the result
    level      int    // read_raw: 0 = full 20-level sweep, n = single-level read
}
```

The `Driver` struct (all fields loop-owned):

```go
type Driver struct {
    s      *device.Session
    serial string
    store  *device.Store

    wavelengthNm   int
    connectedSince time.Time

    // persistent (mirrored in memory; saved via persist())
    blank          *blankState
    tubeCorrection float64
    thermo         thermostatMirror

    // volatile
    busyUntil    time.Time
    sweep        *sweep
    sweepGen     int
    lastReading  *reading // newest completed measurement, for status.last_measurement
    ring         *ringBuffer
    seqCounter   int64
    monitoring   monitoringState
    nextCanaryAt time.Time

    // cached for the busy-window status path
    cachedTemp   float64
    cachedTempAt time.Time
    haveCachTemp bool
}

type monitoringState struct {
    enabled    bool
    intervalS  int
    nextTickAt time.Time
}
```

Key exported services from the core used throughout (already implemented, do not modify): `s.Transact(frame, replyLen, timeout) ([]byte, error)` (double-fail → session unreachable + job failed), `s.Conn() serial.Port`, `s.After(d, fn)`, `s.Now()`, `s.Jobs()` (`Start`/`Complete`/`Fail`/`Cancel`/`Active`/`Get`), `s.Store(key) *device.Store`, `s.SetInfo(info)`. Error constructors: `device.ErrInvalidParams`, `device.ErrBusy`, `device.ErrHardware`, `device.ErrInternal`, `device.ErrNotCalibrated`, `device.ErrUnknownCommand`.

---

### Task 1: Pure conversion & math helpers

**Files:**
- Create: `internal/device/densitometer/convert.go`
- Test: `internal/device/densitometer/convert_test.go`

**Interfaces:**
- Consumes: `device.ErrHardware`, `device.CmdError` (from the merged core).
- Produces:
  - Timing package vars: `SweepWait`, `SingleLevelWait`, `ArrayReadTimeout`, `ThermoSettle`, `LivenessSpacing time.Duration`; `LivenessRetries int`; `CanaryInterval time.Duration`; and const `replyTimeout = 2 * time.Second`.
  - `decodeFixedPoint(reply []byte) float64` — `reply[2] + reply[3]/100` (the shared 2-byte encoding; caller passes a ≥4-byte reply, value lives in bytes 2–3).
  - `decodeIntensity(rec []byte) int` — `int(rec[2]) + 256*int(rec[3])` (little-endian uint16).
  - `parseIntensityArray(buf []byte) ([20]int, *device.CmdError)` — validates `len(buf)==80`, each record `buf[4k]==105` and `buf[4k+1]==k+1`; returns the 20 decoded intensities or `hardware_error`.
  - `leastSquaresSlope(intensities [20]int) (float64, *device.CmdError)` — LS slope of `v` over index `i=1..20` for points with `0 < v <= 3000`; `<3` usable points → `hardware_error("sweep unusable: detector dark or saturated")`.
  - `absorbance(blankSlope, sampleSlope, tempC, blankTempC, tubeCorrection float64) (final, raw float64)` — `raw = |log10(blankSlope/sampleSlope)|`; `final = (raw + (tempC-blankTempC)*0.0022) * tubeCorrection`.
  - `formatSerial(sn1, sn2 byte) string` — `fmt.Sprintf("%d-%03d", sn1, sn2)`.

- [ ] **Step 1: Write the failing test** (`internal/device/densitometer/convert_test.go`)

```go
package densitometer

import (
	"math"
	"testing"
)

func TestDecodeFixedPoint(t *testing.T) {
	if got := decodeFixedPoint([]byte{5, 5, 27, 45}); math.Abs(got-27.45) > 1e-9 {
		t.Fatalf("decodeFixedPoint = %v, want 27.45", got)
	}
	if got := decodeFixedPoint([]byte{70, 5, 10, 0}); got != 10.0 {
		t.Fatalf("decodeFixedPoint = %v, want 10.0", got)
	}
}

func TestDecodeIntensity(t *testing.T) {
	// value 300 → lo=44 hi=1 (44 + 256)
	if got := decodeIntensity([]byte{105, 3, 44, 1}); got != 300 {
		t.Fatalf("decodeIntensity = %d, want 300", got)
	}
}

// buildArray renders a 20-record intensity array with value(i)=fn(i), i=1..20.
func buildArray(fn func(i int) int) []byte {
	buf := make([]byte, 0, 80)
	for i := 1; i <= 20; i++ {
		v := fn(i)
		buf = append(buf, 105, byte(i), byte(v%256), byte(v/256))
	}
	return buf
}

func TestParseIntensityArray(t *testing.T) {
	got, cerr := parseIntensityArray(buildArray(func(i int) int { return 100 * i }))
	if cerr != nil {
		t.Fatal(cerr)
	}
	for i := 0; i < 20; i++ {
		if got[i] != 100*(i+1) {
			t.Fatalf("intensities[%d] = %d, want %d", i, got[i], 100*(i+1))
		}
	}
}

func TestParseIntensityArrayRejectsBadHeader(t *testing.T) {
	bad := buildArray(func(i int) int { return 100 * i })
	bad[4*7] = 99 // corrupt the 8th record header (button-session interleave)
	if _, cerr := parseIntensityArray(bad); cerr == nil || cerr.Code != "hardware_error" {
		t.Fatalf("want hardware_error, got %v", cerr)
	}
	if _, cerr := parseIntensityArray([]byte{105, 1, 0, 0}); cerr == nil {
		t.Fatal("short buffer must error")
	}
}

func TestLeastSquaresSlope(t *testing.T) {
	// perfect line v=100*i → slope 100
	slope, cerr := leastSquaresSlope(arr(func(i int) int { return 100 * i }))
	if cerr != nil {
		t.Fatal(cerr)
	}
	if math.Abs(slope-100) > 1e-6 {
		t.Fatalf("slope = %v, want 100", slope)
	}
}

func TestLeastSquaresSlopeFiltersRange(t *testing.T) {
	// all zero → 0 usable points → hardware_error
	if _, cerr := leastSquaresSlope([20]int{}); cerr == nil || cerr.Code != "hardware_error" {
		t.Fatalf("dark detector must be hardware_error, got %v", cerr)
	}
	// all saturated (>3000) → filtered out → hardware_error
	var sat [20]int
	for i := range sat {
		sat[i] = 5000
	}
	if _, cerr := leastSquaresSlope(sat); cerr == nil {
		t.Fatal("saturated detector must be hardware_error")
	}
}

// arr adapts buildArray's generator into the [20]int the slope fn takes.
func arr(fn func(i int) int) [20]int {
	var out [20]int
	for i := 1; i <= 20; i++ {
		out[i-1] = fn(i)
	}
	return out
}

func TestAbsorbance(t *testing.T) {
	// blank 100, sample 50, no temp delta, tube 1.0 → |log10(2)| = 0.30103
	final, raw := absorbance(100, 50, 27.45, 27.45, 1.0)
	if math.Abs(raw-0.30103) > 1e-4 || math.Abs(final-0.30103) > 1e-4 {
		t.Fatalf("absorbance final=%v raw=%v, want ~0.30103", final, raw)
	}
	// +10 °C over blank → +0.022 compensation; tube 2.0 doubles it
	final, raw = absorbance(100, 50, 37.45, 27.45, 2.0)
	wantFinal := (0.30103 + 0.022) * 2.0
	if math.Abs(final-wantFinal) > 1e-3 {
		t.Fatalf("compensated final=%v, want ~%v (raw=%v)", final, wantFinal, raw)
	}
}

func TestFormatSerial(t *testing.T) {
	if got := formatSerial(25, 6); got != "25-006" {
		t.Fatalf("formatSerial = %q, want 25-006", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/densitometer/ -run 'Decode|Parse|LeastSquares|Absorbance|FormatSerial' -v`
Expected: build failure — `undefined: decodeFixedPoint`, etc.

- [ ] **Step 3: Write the implementation** (`internal/device/densitometer/convert.go`)

```go
// Package densitometer implements the cell-density / optical-absorbance
// detector Driver for the v2 JSON device protocol. It translates
// docs/protocol_translation_docs/densitometer/JSON_PROTOCOL.md onto the legacy
// 5-byte firmware protocol exactly as
// docs/protocol_translation_docs/densitometer/TRANSLATION.md specifies. Design
// principle: the device is a sensor/actuator only; all slope fitting,
// absorbance math, temperature compensation, and tube correction live here.
package densitometer

import (
	"fmt"
	"math"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// Translator timing knobs (TRANSLATION.md §2). Vars so tests can shrink them
// (repo precedent: device.PerByteTimeout, pump.WatchPoll).
var (
	// SweepWait bounds a full 20-level sweep (78 3 / 78 4): ~3.5 s of ADC work
	// plus main-loop slack, with margin.
	SweepWait = 6 * time.Second
	// SingleLevelWait bounds the single-level read (75 1): ~12 s (5× the ADC
	// reads per slot).
	SingleLevelWait = 15 * time.Second
	// ArrayReadTimeout bounds the 80-byte array read (79 1 0): 80 bytes ×
	// ~15 ms inter-byte delay.
	ArrayReadTimeout = 3 * time.Second
	// ThermoSettle is how long set_thermostat waits after 75 2 before verifying
	// — the firmware blocks ~1 s redrawing the display before reading serial.
	ThermoSettle = 1500 * time.Millisecond
	// LivenessSpacing separates post-sweep liveness retries.
	LivenessSpacing = time.Second
	// LivenessRetries is how many liveness polls a sweep completion attempts
	// before declaring the device unreachable.
	LivenessRetries = 3
	// CanaryInterval is the idle reboot-canary poll period (TRANSLATION §5).
	CanaryInterval = 30 * time.Second
)

// replyTimeout bounds the small 4-byte replies (arrive within ~60 ms).
const replyTimeout = 2 * time.Second

// decodeFixedPoint decodes the firmware's 2-byte fixed-point float carried in a
// reply's bytes 2–3: value = int + hundredths/100.
func decodeFixedPoint(reply []byte) float64 {
	return float64(reply[2]) + float64(reply[3])/100
}

// decodeIntensity decodes one [hdr, idx, lo, hi] record: value = lo + 256×hi.
func decodeIntensity(rec []byte) int {
	return int(rec[2]) + 256*int(rec[3])
}

// parseIntensityArray validates and decodes the 80-byte reply of 79 1 0 into 20
// intensities. Every record header must be 105 and the index must run 1..20;
// otherwise a button session interleaved and the read is unusable.
func parseIntensityArray(buf []byte) ([20]int, *device.CmdError) {
	var out [20]int
	if len(buf) != 80 {
		return out, device.ErrHardware(
			fmt.Sprintf("intensity array: got %d bytes, want 80", len(buf)))
	}
	for k := 0; k < 20; k++ {
		rec := buf[4*k : 4*k+4]
		if rec[0] != 105 || int(rec[1]) != k+1 {
			return out, device.ErrHardware(
				"intensity array: record header/index mismatch (button interference?)")
		}
		out[k] = decodeIntensity(rec)
	}
	return out, nil
}

// leastSquaresSlope fits a line through (index, intensity) for points with
// 0 < v ≤ 3000 (the firmware's own filter). Fewer than 3 usable points means
// the detector is dark or saturated.
func leastSquaresSlope(intensities [20]int) (float64, *device.CmdError) {
	var n, sx, sy, sxx, sxy float64
	for idx, v := range intensities {
		if v <= 0 || v > 3000 {
			continue
		}
		x := float64(idx + 1)
		y := float64(v)
		n++
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	if n < 3 {
		return 0, device.ErrHardware("sweep unusable: detector dark or saturated")
	}
	denom := n*sxx - sx*sx
	if denom == 0 {
		return 0, device.ErrHardware("sweep unusable: degenerate brightness points")
	}
	return (n*sxy - sx*sy) / denom, nil
}

// absorbance computes the temperature-compensated, tube-corrected absorbance
// and the raw (pre-compensation, pre-correction) value. 0.0022/°C is the
// firmware's own compensation coefficient (TRANSLATION §4 measure step 5).
func absorbance(blankSlope, sampleSlope, tempC, blankTempC, tubeCorrection float64) (final, raw float64) {
	raw = math.Abs(math.Log10(blankSlope / sampleSlope))
	final = (raw + (tempC-blankTempC)*0.0022) * tubeCorrection
	return final, raw
}

// formatSerial renders the compile-time serial (71 0 0 5 → sn1, sn2) as
// "<year>-<unit>" zero-padded to match the JSON identify example ("25-006").
func formatSerial(sn1, sn2 byte) string {
	return fmt.Sprintf("%d-%03d", sn1, sn2)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/device/densitometer/ -run 'Decode|Parse|LeastSquares|Absorbance|FormatSerial' -v`
Expected: PASS.

- [ ] **Step 5: Pre-flight the package and commit**

Run: `gofmt -l internal/device/densitometer/ && go vet ./internal/device/densitometer/ && go test ./internal/device/densitometer/`
Expected: gofmt prints nothing; vet/test clean.

```bash
git add internal/device/densitometer/convert.go internal/device/densitometer/convert_test.go
git commit -m "feat(densitometer): pure decode, slope and absorbance helpers

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 2: Package skeleton — `Attach` (first contact), `identify`, `Detach`, dispatch, fixture

**Files:**
- Create: `internal/device/densitometer/densitometer.go`
- Test: `internal/device/densitometer/densitometer_test.go` (the shared fixture lives here)

**Interfaces:**
- Consumes: everything from Task 1; `device.Session`, `device.Driver`, `device.Info`, `device.Factory`, `device.Register`, `device.Store`, error constructors.
- Produces:
  - `const TypeCode = 70`; `func Register()`; `func New(s *device.Session) device.Driver`.
  - The `Driver` struct, `persistState`, `blankState`, `thermostatMirror` (+ `mirrorValue()`), `reading`, `sweep`, `monitoringState`, `ringBuffer` (declared here as an opaque field; implemented in Task 8 — for now a nil-safe stub type with a no-op constructor is fine, but to keep Task 2 self-contained, declare `ringBuffer` fully in Task 8 and in Task 2 leave `ring` unused/nil). **To avoid a forward dependency, Task 2 declares only the fields it uses and adds `ring`, `monitoring`, `seqCounter`, `lastReading`, `nextCanaryAt` as struct fields without touching them.** `ringBuffer` type is introduced in Task 8.
  - `Attach(ctx, probeReply) (device.Info, error)` — first-contact path (persisted-mirror branches added in Task 3).
  - `Detach()` (persist only, no serial); `Tick(now)` (stub in Task 2, filled in Task 8); `Execute(ctx, cmd, params)` dispatch (only `unknown_command` wired; handlers added per later task).
  - `info() device.Info`, `capabilities` type.
  - Gate helpers: `serialGate() *device.CmdError` (busy if `now < busyUntil`), `busyGuard() *device.CmdError` (busy if a job is active).
  - Frame vars: `serialNumFrame = {71,0,0,5,0}`, `channel1Frame = {71,0,0,1,0}`, `forceTubeFrame = {75,3,0,0,0}`, `pingFrame = {71,2,3,4,0}`, `tempFrame = {76,0,0,0,0}`, `thermReadFrame = {76,2,0,0,0}`, `stopFrame = {70,0,0,0,0}`, `arrayReadFrame = {79,1,0,0,0}`.

- [ ] **Step 1: Write the failing test** (`internal/device/densitometer/densitometer_test.go`) — fixture + attach/identify/dispatch

```go
package densitometer_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/densitometer"
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

func withStateDir(dir string) fixtureOpt {
	return func(cfg *device.SessionConfig) { cfg.StateDir = dir }
}

func withProbeReply(r []byte) fixtureOpt {
	return func(cfg *device.SessionConfig) {
		cfg.ProbeReply = r
		cfg.Reprobe = func(p serial.Port) ([]byte, error) { return r, nil }
	}
}

// shrinkTimeouts collapses every real-time and clock knob so tests run fast.
func shrinkTimeouts(t *testing.T) {
	t.Helper()
	oldPB, oldDW := device.PerByteTimeout, device.DrainWindow
	oldTS, oldLS := densitometer.ThermoSettle, densitometer.LivenessSpacing
	device.PerByteTimeout, device.DrainWindow = 10*time.Millisecond, 0
	densitometer.ThermoSettle, densitometer.LivenessSpacing = 5*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() {
		device.PerByteTimeout, device.DrainWindow = oldPB, oldDW
		densitometer.ThermoSettle, densitometer.LivenessSpacing = oldTS, oldLS
	})
}

// attachReplies feeds the three reply-bearing frames of first-contact Attach:
// serial number, channel-1 descriptor, thermostat readback (defaults to 10.00,
// a fresh-boot value that first-contact ignores).
func feedAttach(port *serial.FakePort, thermReadback byte) {
	port.Feed([]byte{5, 7, 25, 6})              // 71 0 0 5 → serial 25-006
	port.Feed([]byte{1, 2, 6, 0})               // 71 0 0 1 → wavelength 600
	port.Feed([]byte{5, 5, thermReadback, 0})   // 76 2     → device set-point
}

func newFixture(t *testing.T, opts ...fixtureOpt) *fixture {
	t.Helper()
	shrinkTimeouts(t)
	clock := device.NewFakeClock(time.Unix(1000, 0))
	port := serial.NewFakePort("COM8")
	opener := serial.NewFakeOpener()
	opener.Add(port)
	conn, err := opener.Open("COM8")
	if err != nil {
		t.Fatal(err)
	}
	cfg := device.SessionConfig{
		ID: "densitometer_1", Type: "densitometer", TypeCode: densitometer.TypeCode,
		PortName: "COM8", Conn: conn, Opener: opener, Clock: clock, StateDir: t.TempDir(),
		Factory:    densitometer.New,
		ProbeReply: []byte{70, 0, 0, 2},
		Reprobe:    func(p serial.Port) ([]byte, error) { return []byte{70, 0, 0, 2}, nil },
	}
	for _, o := range opts {
		o(&cfg)
	}
	feedAttach(port, 10) // first-contact: readback ignored, so 10 is fine
	s := device.NewSession(cfg)
	s.Start(context.Background())
	t.Cleanup(s.Close)
	f := &fixture{t: t, s: s, clock: clock, port: port, dir: cfg.StateDir}
	waitFor(t, "attach", s.Connected)
	return f
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

func TestAttachReadsSerialWavelengthAndForcesTubeCorrection(t *testing.T) {
	f := newFixture(t)
	fr := f.frames()
	// Attach order: serial read, channel-1 read, force-tube (75 3 0 0 0),
	// thermostat read (76 2), first-contact disable (75 2 0 0 0).
	if !frameEq(fr[0], 71, 0, 0, 5, 0) {
		t.Fatalf("frame 0 must be serial read: %v", fr[0])
	}
	if !frameEq(fr[1], 71, 0, 0, 1, 0) {
		t.Fatalf("frame 1 must be channel-1 read: %v", fr[1])
	}
	if !frameEq(fr[2], 75, 3, 0, 0, 0) {
		t.Fatalf("frame 2 must force tube correction to 1.0: %v", fr[2])
	}
	if !frameEq(fr[3], 76, 2, 0, 0, 0) {
		t.Fatalf("frame 3 must read thermostat set-point: %v", fr[3])
	}
	if !frameEq(fr[4], 75, 2, 0, 0, 0) {
		t.Fatalf("frame 4 must disable thermostat on first contact: %v", fr[4])
	}
}

func TestAttachServesIdentify(t *testing.T) {
	f := newFixture(t)
	m := f.resultMap(f.exec("identify", ""))
	if m["device_type"] != "densitometer" || m["serial"] != "25-006" ||
		m["model"] != "TDS909A-wide" || m["firmware_version"] != "legacy" ||
		m["protocol_version"] != "1.0" {
		t.Fatalf("identify: %v", m)
	}
	caps := m["capabilities"].(map[string]any)
	if caps["wavelength_nm"] != float64(600) || caps["brightness_levels"] != float64(20) ||
		caps["temperature_sensor"] != "DS18B20" {
		t.Fatalf("capabilities: %v", caps)
	}
	th := caps["thermostat"].(map[string]any)
	if th["min_c"] != 20.0 || th["max_c"] != 45.0 {
		t.Fatalf("thermostat caps: %v", th)
	}
}

func TestAttachPersistsFirstContactMirror(t *testing.T) {
	dir := t.TempDir()
	f := newFixture(t, withStateDir(dir))
	st := device.NewStore(dir, "densitometer-25-006")
	var ps struct {
		SchemaVersion  int     `json:"schema_version"`
		TubeCorrection float64 `json:"tube_correction"`
		Thermostat     struct {
			Enabled bool    `json:"enabled"`
			TargetC float64 `json:"target_c"`
		} `json:"thermostat"`
	}
	found, err := st.Load(&ps)
	if err != nil || !found {
		t.Fatalf("state not persisted: found=%v err=%v", found, err)
	}
	if ps.SchemaVersion != 1 || ps.TubeCorrection != 1.0 || ps.Thermostat.Enabled {
		t.Fatalf("first-contact state: %+v", ps)
	}
	_ = f
}

func TestUnknownCommand(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("frobnicate", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeUnknownCommand {
		t.Fatalf("resp: %+v", resp)
	}
}

func TestRegister(t *testing.T) {
	densitometer.Register()
	name, factory, ok := device.LookupDriver(densitometer.TypeCode)
	if !ok || name != "densitometer" || factory == nil {
		t.Fatalf("LookupDriver(70) = %q %v %v", name, factory, ok)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/densitometer/ -run 'Attach|Unknown|Register' -v`
Expected: build failure — `undefined: densitometer.New`, `densitometer.TypeCode`, etc.

- [ ] **Step 3: Write the implementation** (`internal/device/densitometer/densitometer.go`)

```go
package densitometer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

const (
	deviceType  = "densitometer"
	model       = "TDS909A-wide"
	firmwareVer = "legacy"
	protocolVer = "1.0"
	schemaV     = 1

	thermoMinC = 20.0
	thermoMaxC = 45.0
)

// Command frames (PROTOCOL.md §4). Frames with N5=0 satisfy the firmware guard;
// pingFrame is the liveness/keepalive (71 2 3 4 0 → 70 5 T_int T_frac).
var (
	serialNumFrame = []byte{71, 0, 0, 5, 0}
	channel1Frame  = []byte{71, 0, 0, 1, 0}
	forceTubeFrame = []byte{75, 3, 0, 0, 0}
	pingFrame      = []byte{71, 2, 3, 4, 0}
	tempFrame      = []byte{76, 0, 0, 0, 0}
	thermReadFrame = []byte{76, 2, 0, 0, 0}
	stopFrame      = []byte{70, 0, 0, 0, 0}
	arrayReadFrame = []byte{79, 1, 0, 0, 0}
)

// Register binds the densitometer driver into the device registry. Called at
// app wiring time (PR 5); nothing calls it in this PR.
func Register() { device.Register(TypeCode, deviceType, New) }

// New is the device.Factory for densitometers.
func New(s *device.Session) device.Driver { return &Driver{s: s} }

// Attach implements TRANSLATION §3: read serial + wavelength, force the
// device-side tube correction to 1.0, recover persistent state, sync the
// thermostat mirror (which arms the reboot canary). probeReply is the 4-byte
// identify reply discovery consumed ([70, _, _, channels]).
func (d *Driver) Attach(ctx context.Context, probeReply []byte) (device.Info, error) {
	if len(probeReply) != 4 || probeReply[0] != TypeCode {
		return device.Info{}, fmt.Errorf("densitometer: unexpected probe reply %v", probeReply)
	}

	snReply, err := d.s.Transact(serialNumFrame, 4, replyTimeout)
	if err != nil {
		return device.Info{}, fmt.Errorf("densitometer: serial read: %w", err)
	}
	d.serial = formatSerial(snReply[2], snReply[3])

	wlReply, err := d.s.Transact(channel1Frame, 4, replyTimeout)
	if err != nil {
		return device.Info{}, fmt.Errorf("densitometer: wavelength read: %w", err)
	}
	d.wavelengthNm = int(wlReply[2])*100 + int(wlReply[3])

	// TRANSLATION §3 step 4: force the device factor to 1.0. EEPROM-persistent,
	// so it survives reboots — from here all tube correction is driver-side.
	if _, err := d.s.Transact(forceTubeFrame, 0, replyTimeout); err != nil {
		return device.Info{}, fmt.Errorf("densitometer: force tube correction: %w", err)
	}

	// Recover persistent state before the thermostat sync (which needs the mirror).
	d.store = d.s.Store(d.serial)
	d.blank, d.tubeCorrection, d.thermo = nil, 1.0, thermostatMirror{}
	var ps persistState
	found, lerr := d.store.Load(&ps)
	if lerr != nil {
		slog.Warn("densitometer: state file unreadable, treating as absent",
			"device", d.serial, "err", lerr)
		found = false
	}
	if found && ps.SchemaVersion == schemaV {
		d.blank = ps.Blank
		if ps.TubeCorrection > 0 {
			d.tubeCorrection = ps.TubeCorrection
		}
		d.thermo = ps.Thermostat
	}

	// Volatile reset (also the reboot-recovery path).
	d.connectedSince = d.s.Now()
	d.busyUntil = time.Time{}
	d.sweep, d.lastReading = nil, nil
	d.sweepGen++
	d.seqCounter = 0
	d.monitoring = monitoringState{}
	d.ring = newRingBuffer()
	d.nextCanaryAt = d.s.Now().Add(CanaryInterval)

	if err := d.syncThermostat(found && ps.SchemaVersion == schemaV); err != nil {
		return device.Info{}, err
	}
	return d.info(), nil
}

type thermostatCaps struct {
	MinC float64 `json:"min_c"`
	MaxC float64 `json:"max_c"`
}

type capabilities struct {
	WavelengthNm      int            `json:"wavelength_nm"`
	BrightnessLevels  int            `json:"brightness_levels"`
	Thermostat        thermostatCaps `json:"thermostat"`
	TemperatureSensor string         `json:"temperature_sensor"`
}

func (d *Driver) info() device.Info {
	return device.Info{
		DeviceType: deviceType, Model: model, Serial: d.serial,
		FirmwareVersion: firmwareVer, ProtocolVersion: protocolVer,
		Capabilities: capabilities{
			WavelengthNm:      d.wavelengthNm,
			BrightnessLevels:  20,
			Thermostat:        thermostatCaps{MinC: thermoMinC, MaxC: thermoMaxC},
			TemperatureSensor: "DS18B20",
		},
	}
}

// Execute dispatches one JSON command. identify/get_job are session-served.
func (d *Driver) Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *device.CmdError) {
	switch cmd {
	// handlers wired in later tasks:
	// ping, status, stop, stop_monitoring, set_thermostat, set_tube_correction,
	// calibrate_tube, set_led, measure, measure_blank, read_raw,
	// start_monitoring, get_readings
	default:
		return nil, device.ErrUnknownCommand(cmd)
	}
}

// Tick runs ~1/s while attached (filled in Task 8: idle canary + monitoring).
func (d *Driver) Tick(now time.Time) {}

// Detach persists current state; it performs no serial I/O (state is already
// saved on every mutation, so this is belt-and-suspenders and safe on a dead
// port).
func (d *Driver) Detach() {
	if d.store != nil {
		if err := d.persist(); err != nil {
			slog.Warn("densitometer: detach persist failed", "device", d.serial, "err", err)
		}
	}
}

// persist writes the serial-keyed state file (spec §5).
func (d *Driver) persist() error {
	return d.store.Save(persistState{
		SchemaVersion:  schemaV,
		Blank:          d.blank,
		TubeCorrection: d.tubeCorrection,
		Thermostat:     d.thermo,
	})
}

// serialGate rejects commands that would touch the port while a sweep is in
// flight — the WHOLE sweep, including the post-busy_until completion phase
// (liveness retries + array/temp read-out), during which d.sweep stays set.
// Gating on busy_until alone opens a window on a slow device where a
// concurrent status/ping does a live Transact against the still-finishing
// device, times out, and tears the sweep down via markUnreachable. The
// busy_until half preserves the post-stop case (d.sweep nilled but the
// hardware is still physically sweeping). status serves cached values instead.
func (d *Driver) serialGate() *device.CmdError {
	if d.sweep != nil || d.s.Now().Before(d.busyUntil) {
		busyMs := d.busyUntil.Sub(d.s.Now()).Milliseconds()
		if busyMs < 0 {
			busyMs = 0
		}
		return device.ErrBusy("device is mid-sweep", map[string]any{"busy_ms": busyMs})
	}
	return nil
}

// busyGuard rejects a job-starting command while one is already active.
func (d *Driver) busyGuard() *device.CmdError {
	if j := d.s.Jobs().Active(); j != nil {
		return device.ErrBusy("a job is running", map[string]any{"job_id": j.ID})
	}
	return nil
}
```

Note: `Attach` calls `syncThermostat(hasMirror bool)` and `newRingBuffer()`, defined in Tasks 3 and 8. To keep Task 2 compiling on its own, **add minimal placeholders in this task and replace them in the later tasks**:

```go
// TEMPORARY (replaced in Task 3): first-contact disable only.
func (d *Driver) syncThermostat(hasMirror bool) error {
	if _, err := d.s.Transact(thermReadFrame, 4, replyTimeout); err != nil {
		return fmt.Errorf("densitometer: thermostat read: %w", err)
	}
	if _, err := d.s.Transact([]byte{75, 2, 0, 0, 0}, 0, replyTimeout); err != nil {
		return fmt.Errorf("densitometer: thermostat disable: %w", err)
	}
	d.thermo = thermostatMirror{Enabled: false, TargetC: 0}
	return d.persist()
}

// TEMPORARY (replaced in Task 8): ring buffer stub.
func newRingBuffer() *ringBuffer { return &ringBuffer{} }

type ringBuffer struct{}
```

The `Driver`, `persistState`, `blankState`, `thermostatMirror`, `reading`, `sweep`, `monitoringState` types from the "Shared type & interface contract" section above go into this file too (place `ringBuffer`'s real definition in Task 8, replacing the stub).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/device/densitometer/ -run 'Attach|Unknown|Register' -v`
Expected: PASS (all 5 tests).

- [ ] **Step 5: Pre-flight and commit**

Run: `gofmt -l internal/device/densitometer/ && go vet ./internal/device/densitometer/ && go test -race ./internal/device/densitometer/`

```bash
git add internal/device/densitometer/densitometer.go internal/device/densitometer/densitometer_test.go
git commit -m "feat(densitometer): package skeleton, Attach and identify

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 3: Thermostat — `pushThermostat`, `set_thermostat`, canary, reboot recovery

**Files:**
- Create: `internal/device/densitometer/thermostat.go`
- Modify: `internal/device/densitometer/densitometer.go` (delete the temporary `syncThermostat` stub from Task 2; the real one lives in the new file — or move it; keep one definition)
- Test: `internal/device/densitometer/thermostat_test.go`

**Interfaces:**
- Consumes: `Driver`, `thermostatMirror`, frames, `decodeFixedPoint`, `ThermoSettle`, timing vars.
- Produces:
  - `pushThermostat(enabled bool, targetC float64) *device.CmdError` — sends `[75,2,t,0,0]`, waits `ThermoSettle` (real time), verifies `[76,2,…] == t ±0.01`, updates+persists the mirror. `t = 0` when disabling.
  - `setThermostat(params) (any, *device.CmdError)` — validates `target_c ∈ [20,45]` when enabling (rounds to whole °C, echoes the applied integer), gates on `serialGate`, calls `pushThermostat`.
  - `syncThermostat(hasMirror bool) error` — the real Attach §3 step-5 logic (replaces the Task-2 stub).
  - `applyThermostatReadback(readback float64, fromCanary bool)` — the canary: `10.00` ⇒ reboot recovery (fail active job, reset `connected_since`, re-push mirror, log alert) when `fromCanary`; on plain mismatch, log + re-push.

- [ ] **Step 1: Write the failing test** (`internal/device/densitometer/thermostat_test.go`)

```go
package densitometer_test

import (
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// feedThermostatSet feeds the verify readback of a set_thermostat: 76 2 → t.00.
func feedThermSet(port interface{ Feed([]byte) }, t byte) {
	port.Feed([]byte{5, 5, t, 0})
}

func TestSetThermostatEnable(t *testing.T) {
	f := newFixture(t)
	feedThermSet(f.port, 37)
	resp := f.exec("set_thermostat", `{"enabled":true,"target_c":37.0}`)
	if resp.Status != "ok" {
		t.Fatalf("set_thermostat: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["enabled"] != true || m["target_c"] != 37.0 {
		t.Fatalf("result: %v", m)
	}
	// The set frame 75 2 37 0 0 then the verify read 76 2 0 0 0 must both appear.
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 75, 2, 37, 0, 0) {
		t.Fatalf("set frame: %v", fr[n-2])
	}
	if !frameEq(fr[n-1], 76, 2, 0, 0, 0) {
		t.Fatalf("verify frame: %v", fr[n-1])
	}
}

func TestSetThermostatRoundsFractional(t *testing.T) {
	f := newFixture(t)
	feedThermSet(f.port, 37) // round(36.6) = 37
	m := f.resultMap(f.exec("set_thermostat", `{"enabled":true,"target_c":36.6}`))
	if m["target_c"] != 37.0 {
		t.Fatalf("fractional set-point must round to 37: %v", m)
	}
}

func TestSetThermostatDisable(t *testing.T) {
	f := newFixture(t)
	feedThermSet(f.port, 0) // disable verifies against 0
	m := f.resultMap(f.exec("set_thermostat", `{"enabled":false}`))
	if m["enabled"] != false || m["target_c"] != 0.0 {
		t.Fatalf("disable result: %v", m)
	}
	fr := f.frames()
	if !frameEq(fr[len(fr)-2], 75, 2, 0, 0, 0) {
		t.Fatalf("disable frame: %v", fr[len(fr)-2])
	}
}

func TestSetThermostatRangeRejected(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []string{`{"enabled":true,"target_c":19}`, `{"enabled":true,"target_c":46}`} {
		resp := f.exec("set_thermostat", tc)
		if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
			t.Fatalf("%s: %+v", tc, resp)
		}
	}
}

func TestSetThermostatVerifyMismatch(t *testing.T) {
	f := newFixture(t)
	feedThermSet(f.port, 30) // device echoes 30, we asked for 37 → hardware_error
	resp := f.exec("set_thermostat", `{"enabled":true,"target_c":37}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("verify mismatch must be hardware_error: %+v", resp)
	}
}

func TestSetThermostatPersistsMirror(t *testing.T) {
	dir := t.TempDir()
	f := newFixture(t, withStateDir(dir))
	feedThermSet(f.port, 37)
	if resp := f.exec("set_thermostat", `{"enabled":true,"target_c":37}`); resp.Status != "ok" {
		t.Fatalf("set: %+v", resp)
	}
	st := device.NewStore(dir, "densitometer-25-006")
	var ps struct {
		Thermostat struct {
			Enabled bool    `json:"enabled"`
			TargetC float64 `json:"target_c"`
		} `json:"thermostat"`
	}
	if _, err := st.Load(&ps); err != nil {
		t.Fatal(err)
	}
	if !ps.Thermostat.Enabled || ps.Thermostat.TargetC != 37 {
		t.Fatalf("mirror not persisted: %+v", ps.Thermostat)
	}
}

// TestAttachRebootCanaryRepushes: a persisted enabled mirror + a device that
// reads back 10.00 (fresh boot) must re-push the set-point during Attach.
func TestAttachRebootCanaryRepushes(t *testing.T) {
	dir := t.TempDir()
	// Seed a persisted enabled mirror at 37.
	st := device.NewStore(dir, "densitometer-25-006")
	if err := st.Save(map[string]any{
		"schema_version": 1, "tube_correction": 1.0,
		"thermostat": map[string]any{"enabled": true, "target_c": 37.0},
	}); err != nil {
		t.Fatal(err)
	}
	shrinkTimeouts(t)
	clock := device.NewFakeClock(timeUnix1000())
	port := newPort("COM8")
	opener := newOpener(port)
	conn := mustOpen(t, opener, "COM8")
	// Attach reads: serial, wavelength, (force tube), thermostat=10 → reboot →
	// re-push: 75 2 37, then verify 76 2 → 37.
	port.Feed([]byte{5, 7, 25, 6})
	port.Feed([]byte{1, 2, 6, 0})
	port.Feed([]byte{5, 5, 10, 0}) // thermostat readback = 10.00 → rebooted
	port.Feed([]byte{5, 5, 37, 0}) // re-push verify readback
	f := startFixture(t, clock, port, opener, dir)
	// The re-push set frame must have been sent.
	sawRepush := false
	for _, fr := range f.frames() {
		if frameEq(fr, 75, 2, 37, 0, 0) {
			sawRepush = true
		}
	}
	if !sawRepush {
		t.Fatalf("reboot canary must re-push 75 2 37; frames=%v", f.frames())
	}
}
```

This test needs small fixture helpers (`timeUnix1000`, `newPort`, `newOpener`, `mustOpen`, `startFixture`) so a test can drive a custom pre-seeded state dir and feed sequence. **Add these to `densitometer_test.go`** (extract the guts of `newFixture` so both share them):

```go
// (add to densitometer_test.go)
func timeUnix1000() time.Time { return time.Unix(1000, 0) }
func newPort(name string) *serial.FakePort { return serial.NewFakePort(name) }
func newOpener(p *serial.FakePort) *serial.FakeOpener {
	o := serial.NewFakeOpener()
	o.Add(p)
	return o
}
func mustOpen(t *testing.T, o *serial.FakeOpener, name string) serial.Port {
	t.Helper()
	c, err := o.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func startFixture(t *testing.T, clock *device.FakeClock, port *serial.FakePort, opener *serial.FakeOpener, dir string) *fixture {
	t.Helper()
	cfg := device.SessionConfig{
		ID: "densitometer_1", Type: "densitometer", TypeCode: densitometer.TypeCode,
		PortName: port.Name(), Conn: mustOpen(t, opener, port.Name()), Opener: opener,
		Clock: clock, StateDir: dir, Factory: densitometer.New,
		ProbeReply: []byte{70, 0, 0, 2},
		Reprobe:    func(p serial.Port) ([]byte, error) { return []byte{70, 0, 0, 2}, nil },
	}
	s := device.NewSession(cfg)
	s.Start(context.Background())
	t.Cleanup(s.Close)
	f := &fixture{t: t, s: s, clock: clock, port: port, dir: dir}
	waitFor(t, "attach", s.Connected)
	return f
}
```

Then refactor `newFixture` to build its config and delegate its tail to the same body (optional; simplest is to leave `newFixture` as-is and let `startFixture` duplicate a few lines).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/densitometer/ -run 'Thermostat|Reboot' -v`
Expected: build failure — `set_thermostat` unhandled (returns `unknown_command`), so the ok-path tests fail; `undefined` helpers.

- [ ] **Step 3: Write the implementation** (`internal/device/densitometer/thermostat.go`), and delete the temporary `syncThermostat` stub from `densitometer.go`

```go
package densitometer

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// pushThermostat commands the set-point (TRANSLATION §4 set_thermostat): write
// 75 2 t, wait ThermoSettle (the firmware blocks ~1 s redrawing the display),
// verify via 76 2, then update + persist the mirror. t = 0 disables. The
// settle is a bounded loop-block for a rare human-paced command (flagged
// deviation 3).
func (d *Driver) pushThermostat(enabled bool, targetC float64) *device.CmdError {
	t := 0
	if enabled {
		t = int(targetC)
	}
	if _, err := d.s.Transact([]byte{75, 2, byte(t), 0, 0}, 0, replyTimeout); err != nil { // #nosec G115 -- t is 0 or 20..45
		return device.ErrHardware("set_thermostat write: " + err.Error())
	}
	time.Sleep(ThermoSettle)
	reply, err := d.s.Transact(thermReadFrame, 4, replyTimeout)
	if err != nil {
		return device.ErrHardware("set_thermostat verify: " + err.Error())
	}
	if got := decodeFixedPoint(reply); math.Abs(got-float64(t)) > 0.01 {
		return device.ErrHardware(fmt.Sprintf(
			"set_thermostat verify: device echoed %.2f, want %d", got, t))
	}
	d.thermo = thermostatMirror{Enabled: enabled, TargetC: float64(t)}
	if err := d.persist(); err != nil {
		return device.ErrInternal("persist thermostat: " + err.Error())
	}
	return nil
}

type setThermostatResult struct {
	Enabled bool    `json:"enabled"`
	TargetC float64 `json:"target_c"`
}

func (d *Driver) setThermostat(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Enabled bool     `json:"enabled"`
		TargetC *float64 `json:"target_c"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if cerr := d.serialGate(); cerr != nil {
		return nil, cerr
	}
	var target float64
	if p.Enabled {
		if p.TargetC == nil {
			return nil, device.ErrInvalidParams("target_c", nil, "target_c is required when enabling")
		}
		target = math.Round(*p.TargetC) // firmware accepts whole °C only
		if target < thermoMinC || target > thermoMaxC {
			return nil, device.ErrInvalidParams("target_c", *p.TargetC,
				"target_c must be between 20 and 45")
		}
	}
	if cerr := d.pushThermostat(p.Enabled, target); cerr != nil {
		return nil, cerr
	}
	return setThermostatResult{Enabled: d.thermo.Enabled, TargetC: d.thermo.TargetC}, nil
}

// syncThermostat is Attach §3 step 5: read the device set-point and reconcile
// against the persisted mirror, arming the reboot canary. hasMirror is true
// when persistent state was recovered.
func (d *Driver) syncThermostat(hasMirror bool) error {
	reply, err := d.s.Transact(thermReadFrame, 4, replyTimeout)
	if err != nil {
		return fmt.Errorf("densitometer: thermostat read: %w", err)
	}
	readback := decodeFixedPoint(reply)
	if !hasMirror {
		// First-ever contact: disable and persist mirror {false}.
		if cerr := d.pushThermostat(false, 0); cerr != nil {
			return fmt.Errorf("densitometer: thermostat first-contact disable: %w", cerr)
		}
		return nil
	}
	if math.Abs(readback-d.thermo.mirrorValue()) <= 0.01 {
		return nil // in sync
	}
	// Mismatch (reboot ⇒ readback 10, or drift) — re-push the mirror. During
	// Attach there is no active job to fail and connected_since was just set.
	if math.Abs(readback-10.0) <= 0.01 {
		slog.Warn("densitometer: device rebooted (thermostat readback 10.00), re-pushing mirror",
			"device", d.serial, "mirror", d.thermo)
	} else {
		slog.Warn("densitometer: thermostat drift, re-pushing mirror",
			"device", d.serial, "readback", readback, "mirror", d.thermo)
	}
	if cerr := d.pushThermostat(d.thermo.Enabled, d.thermo.TargetC); cerr != nil {
		return fmt.Errorf("densitometer: thermostat re-push: %w", cerr)
	}
	return nil
}

// applyThermostatReadback is the canary shared by status step 3 and the idle
// Tick poll. readback is a decoded 76 2 value. When fromCanary and the device
// rebooted (readback 10.00) it fails any active job and resets connected_since
// before re-pushing.
func (d *Driver) applyThermostatReadback(readback float64, fromCanary bool) {
	if math.Abs(readback-d.thermo.mirrorValue()) <= 0.01 {
		return // in sync
	}
	rebooted := math.Abs(readback-10.0) <= 0.01
	if fromCanary && rebooted {
		slog.Warn("densitometer: reboot detected via canary — failing job, re-pushing mirror",
			"device", d.serial)
		if d.s.Jobs().Active() != nil {
			d.s.Jobs().Fail(device.ErrHardware("device rebooted mid-job (sweep data lost)"))
			d.clearSweep()
		}
		d.connectedSince = d.s.Now()
	} else {
		slog.Warn("densitometer: thermostat mismatch — re-pushing mirror",
			"device", d.serial, "readback", readback, "mirror", d.thermo)
	}
	if cerr := d.pushThermostat(d.thermo.Enabled, d.thermo.TargetC); cerr != nil {
		slog.Warn("densitometer: mirror re-push failed", "device", d.serial, "err", cerr)
	}
}
```

Wire `set_thermostat` into `Execute` (add the case):

```go
	case "set_thermostat":
		return d.setThermostat(params)
```

`clearSweep()` is defined in Task 5; for Task 3 add a minimal version to `densitometer.go` (it will be reused, not duplicated):

```go
// clearSweep resets sweep bookkeeping to idle and invalidates pending timers.
func (d *Driver) clearSweep() {
	d.sweep = nil
	d.sweepGen++
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/device/densitometer/ -run 'Thermostat|Reboot|Attach|Unknown|Register' -v`
Expected: PASS.

- [ ] **Step 5: Pre-flight and commit**

Run: `gofmt -l internal/device/densitometer/ && go vet ./internal/device/densitometer/ && go test -race ./internal/device/densitometer/`

```bash
git add internal/device/densitometer/
git commit -m "feat(densitometer): thermostat set, verify and reboot canary

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 4: `ping` and `status`

**Files:**
- Create: `internal/device/densitometer/commands.go`
- Test: `internal/device/densitometer/commands_test.go`

**Interfaces:**
- Consumes: `Driver`, gates, `applyThermostatReadback`, `decodeFixedPoint`, `pingFrame`, `tempFrame`, `thermReadFrame`, the Jobs engine.
- Produces:
  - `ping() (any, *device.CmdError)` — gate on `serialGate`; `[71,2,3,4,0]` liveness; return `{uptime_ms: now-connectedSince}`.
  - `status() (any, *device.CmdError)` — assembles state/temperature/thermostat/calibration/last_measurement. When not busy, reads live temperature + thermostat and runs the canary (`applyThermostatReadback(_, true)`); when busy, serves cached temperature with an age. Never returns `busy`.
  - `statusJob() *device.Job` — active job else the last one (via `lastJobID`, tracked from Task 5 onward — in Task 4 the field is always empty, so this returns the active job or nil).

- [ ] **Step 1: Write the failing test** (`internal/device/densitometer/commands_test.go`)

```go
package densitometer_test

import (
	"testing"
	"time"
)

func TestPingReturnsUptime(t *testing.T) {
	f := newFixture(t)
	f.clock.Advance(3 * time.Second)
	f.port.Feed([]byte{70, 5, 27, 45}) // 71 2 3 4 0 → 70 5 T_int T_frac
	resp := f.exec("ping", "")
	if resp.Status != "ok" {
		t.Fatalf("ping: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["uptime_ms"].(float64) < 2900 || m["uptime_ms"].(float64) > 3100 {
		t.Fatalf("uptime_ms = %v, want ~3000", m["uptime_ms"])
	}
	if !frameEq(f.frames()[len(f.frames())-1], 71, 2, 3, 4, 0) {
		t.Fatalf("ping frame missing: %v", f.frames())
	}
}

func TestStatusIdleReadsLiveTemperature(t *testing.T) {
	f := newFixture(t)
	f.port.Feed([]byte{5, 5, 36, 98}) // 76 0 → temperature 36.98
	f.port.Feed([]byte{5, 5, 0, 0})   // 76 2 → thermostat set-point 0 (disabled, in sync)
	m := f.resultMap(f.exec("status", ""))
	if m["state"] != "idle" {
		t.Fatalf("state: %v", m)
	}
	if m["temperature_c"].(float64) < 36.9 || m["temperature_c"].(float64) > 37.05 {
		t.Fatalf("temperature_c = %v", m["temperature_c"])
	}
	th := m["thermostat"].(map[string]any)
	if th["enabled"] != false || th["heating"] != nil || th["cooling"] != nil {
		t.Fatalf("thermostat block: %v", th)
	}
	cal := m["calibration"].(map[string]any)
	if cal["blank"] != nil || cal["tube_correction"] != 1.0 {
		t.Fatalf("calibration block: %v", cal)
	}
	if m["last_measurement"] != nil {
		t.Fatalf("last_measurement must be null before any measurement: %v", m["last_measurement"])
	}
}

func TestStatusThermostatEnabledMirror(t *testing.T) {
	f := newFixture(t)
	feedThermSet(f.port, 37)
	if resp := f.exec("set_thermostat", `{"enabled":true,"target_c":37}`); resp.Status != "ok" {
		t.Fatalf("set: %+v", resp)
	}
	f.port.Feed([]byte{5, 5, 36, 98}) // temperature
	f.port.Feed([]byte{5, 5, 37, 0})  // thermostat set-point 37 (in sync)
	th := f.resultMap(f.exec("status", ""))["thermostat"].(map[string]any)
	if th["enabled"] != true || th["target_c"] != 37.0 {
		t.Fatalf("enabled mirror: %v", th)
	}
}
```

(Tests for the busy-window cached path and the status canary-reboot path are added in Task 5/8, once a sweep can set `busy_until`.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/densitometer/ -run 'Ping|Status' -v`
Expected: `ping`/`status` return `unknown_command` → assertion failures.

- [ ] **Step 3: Write the implementation** (append to `internal/device/densitometer/commands.go`)

```go
package densitometer

import (
	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

type pingResult struct {
	UptimeMs int64 `json:"uptime_ms"`
}

// ping (TRANSLATION §4): prove liveness with 71 2 3 4 0; uptime_ms is
// translator connection age (true device uptime is unknowable).
func (d *Driver) ping() (any, *device.CmdError) {
	if cerr := d.serialGate(); cerr != nil {
		return nil, cerr
	}
	reply, err := d.s.Transact(pingFrame, 4, replyTimeout)
	if err != nil {
		return nil, device.ErrHardware("ping: " + err.Error())
	}
	if reply[0] != TypeCode {
		return nil, device.ErrHardware("ping: unexpected reply")
	}
	return pingResult{UptimeMs: d.s.Now().Sub(d.connectedSince).Milliseconds()}, nil
}

type blankStatus struct {
	Slope        float64 `json:"slope"`
	TemperatureC float64 `json:"temperature_c"`
	AgeS         float64 `json:"age_s"`
}

type calibrationStatus struct {
	Blank          *blankStatus `json:"blank"`
	TubeCorrection float64      `json:"tube_correction"`
}

type thermostatStatus struct {
	Enabled bool    `json:"enabled"`
	TargetC float64 `json:"target_c"`
	Heating *bool   `json:"heating"` // GAP: never reported → null
	Cooling *bool   `json:"cooling"` // GAP: never reported → null
}

type lastMeasurement struct {
	Seq          int64   `json:"seq"`
	Absorbance   float64 `json:"absorbance"`
	TemperatureC float64 `json:"temperature_c"`
	AgeS         float64 `json:"age_s"`
}

type statusResult struct {
	State           string             `json:"state"`
	Job             *device.Job        `json:"job"`
	TemperatureC    float64            `json:"temperature_c"`
	Thermostat      thermostatStatus   `json:"thermostat"`
	Calibration     calibrationStatus  `json:"calibration"`
	LastMeasurement *lastMeasurement   `json:"last_measurement"`
}

// statusJob returns the active job else the most recent one.
func (d *Driver) statusJob() *device.Job {
	if j := d.s.Jobs().Active(); j != nil {
		return j
	}
	if d.lastJobID != "" {
		return d.s.Jobs().Get(d.lastJobID)
	}
	return nil
}

// status (TRANSLATION §4) never blocks and never returns busy. When idle it
// reads live temperature + thermostat and runs the reboot canary; mid-sweep it
// serves the cached temperature with an age.
func (d *Driver) status() (any, *device.CmdError) {
	res := statusResult{
		State: d.stateName(),
		Job:   d.statusJob(),
		Thermostat: thermostatStatus{
			Enabled: d.thermo.Enabled, TargetC: d.thermo.TargetC,
		},
		Calibration: calibrationStatus{TubeCorrection: d.tubeCorrection},
	}
	if d.blank != nil {
		res.Calibration.Blank = &blankStatus{
			Slope: d.blank.Slope, TemperatureC: d.blank.TemperatureC,
			AgeS: d.s.Now().Sub(d.blank.MeasuredAt).Seconds(),
		}
	}
	if d.lastReading != nil {
		res.LastMeasurement = &lastMeasurement{
			Seq: d.lastReading.seq, Absorbance: d.lastReading.absorbance,
			TemperatureC: d.lastReading.temperatureC,
			AgeS:         d.s.Now().Sub(d.lastReading.measuredAt).Seconds(),
		}
	}

	if d.serialGate() != nil {
		// Mid-sweep: reuse cached temperature (flagged with its age via the
		// value only; the device cannot be read now).
		if d.haveCachTemp {
			res.TemperatureC = d.cachedTemp
		}
		return res, nil
	}

	// Idle: read live temperature and thermostat, run the canary.
	if tReply, err := d.s.Transact(tempFrame, 4, replyTimeout); err == nil {
		res.TemperatureC = decodeFixedPoint(tReply)
		d.cachedTemp, d.cachedTempAt, d.haveCachTemp = res.TemperatureC, d.s.Now(), true
	}
	if thReply, err := d.s.Transact(thermReadFrame, 4, replyTimeout); err == nil {
		d.applyThermostatReadback(decodeFixedPoint(thReply), true)
		// re-read enabled/target from the (possibly re-pushed) mirror
		res.Thermostat.Enabled = d.thermo.Enabled
		res.Thermostat.TargetC = d.thermo.TargetC
	}
	return res, nil
}

// stateName maps driver state to the JSON status.state enum.
func (d *Driver) stateName() string {
	switch {
	case d.monitoring.enabled:
		return "monitoring"
	case d.s.Jobs().Active() != nil:
		return "measuring"
	default:
		return "idle"
	}
}
```

Add `lastJobID string` to the `Driver` struct (used by `statusJob`; set in Task 5). Wire the dispatch cases:

```go
	case "ping":
		return d.ping()
	case "status":
		return d.status()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/device/densitometer/ -run 'Ping|Status|Thermostat|Reboot|Attach|Unknown|Register' -v`
Expected: PASS.

- [ ] **Step 5: Pre-flight and commit**

```bash
gofmt -l internal/device/densitometer/ && go vet ./internal/device/densitometer/ && go test -race ./internal/device/densitometer/
git add internal/device/densitometer/
git commit -m "feat(densitometer): ping and status

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 5: Sweep engine core + `measure_blank`

**Files:**
- Create: `internal/device/densitometer/sweep.go`
- Test: `internal/device/densitometer/sweep_test.go`

**Interfaces:**
- Consumes: `Driver`, `sweep`, `blankState`, gates, `parseIntensityArray`, `leastSquaresSlope`, `arrayReadFrame`, `tempFrame`, `pingFrame`, timing vars, the Jobs engine, `s.After`, `s.Conn`.
- Produces:
  - `runSweep(kind string, trigger []byte, wait time.Duration, sw sweep) (device.Job, *device.CmdError)` — busyGuard, `Jobs().Start(kind, wait+2s)`, fire the trigger (no reply), set `busyUntil = now+wait`, store `d.sweep`, schedule `s.After(wait, onSweepDone)`. Returns the running job.
  - `onSweepDone(gen)` → `livenessAttempt(gen, 1)` → on success `readSweepAndFinish(gen)`.
  - `softPing() bool` — raw `s.Conn()` liveness read that does **not** trip unreachable.
  - `readSweepAndFinish(gen)` — array read (validate + one retry) → temperature read → dispatch to the kind-specific finisher.
  - `finishBlank(gen, intensities, tempC)` — slope, persist blank, complete job with `{slope, temperature_c, sweep}`.
  - `measureBlank() (any, *device.CmdError)` — `runSweep("blank", {78,3,0,0,0}, SweepWait, …)`.
  - `clearSweep()` already added in Task 3.

**Completion-chain design (all on the loop; guarded by `d.sweepGen`):**

```
runSweep:                          # in Execute (loop)
  busyGuard; Jobs().Start; write trigger (no reply); busyUntil=now+wait
  d.sweep = &sweep{gen: ++sweepGen, kind, ...}; lastJobID = job.ID
  s.After(wait, func(){ onSweepDone(gen) })

onSweepDone(gen):                  # After callback (loop, via Post)
  if stale(gen) return
  livenessAttempt(gen, 1)

livenessAttempt(gen, n):
  if stale(gen) return
  if n < LivenessRetries:
     if softPing(): readSweepAndFinish(gen); return
     s.After(LivenessSpacing, func(){ livenessAttempt(gen, n+1) })   # one outstanding
     return
  # final attempt: hard Transact — success proceeds, failure trips unreachable
  reply, err := s.Transact(pingFrame, 4, replyTimeout)
  if err != nil { return }        # markUnreachable already failed the job
  if reply[0] != TypeCode { Jobs().Fail(hardware); clearSweep(); return }
  readSweepAndFinish(gen)

readSweepAndFinish(gen):
  if stale(gen) return
  raw, err := s.Transact(arrayReadFrame, 80, ArrayReadTimeout)
  ints, cerr := parseIntensityArray(raw)      # if err/cerr: flush+retry once
  ... on second failure: Jobs().Fail; clearSweep; return
  tReply,_ := s.Transact(tempFrame, 4, replyTimeout); tempC = decode
  cache temp; dispatch by d.sweep.kind (blank → finishBlank, etc.)
```

`stale(gen)` ≡ `gen != d.sweepGen || d.sweep == nil || d.s.Jobs().Active() == nil`.

- [ ] **Step 1: Write the failing test** (`internal/device/densitometer/sweep_test.go`)

```go
package densitometer_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/densitometer"
)

// buildArrayBytes renders a 20-record intensity array (test-side mirror of the
// package helper) for feeding the port.
func buildArrayBytes(fn func(i int) int) []byte {
	buf := make([]byte, 0, 80)
	for i := 1; i <= 20; i++ {
		v := fn(i)
		buf = append(buf, 105, byte(i), byte(v%256), byte(v/256))
	}
	return buf
}

// feedSweepCompletion feeds the completion chain: liveness reply, 80-byte
// array, temperature reply — in read order.
func feedSweepCompletion(f *fixture, slopePerLevel int, tInt, tFrac byte) {
	f.port.Feed([]byte{70, 5, tInt, tFrac})                              // liveness (71 2 3 4)
	f.port.Feed(buildArrayBytes(func(i int) int { return slopePerLevel * i })) // 79 1 0
	f.port.Feed([]byte{5, 5, tInt, tFrac})                              // temperature (76 0)
}

func jobResult(t *testing.T, f *fixture, id string) map[string]any {
	t.Helper()
	resp := f.exec("get_job", `{"job_id":"`+id+`"}`)
	if resp.Status != "ok" {
		t.Fatalf("get_job: %+v", resp)
	}
	return f.resultMap(resp)
}

func startJob(t *testing.T, f *fixture, cmd, params string) string {
	t.Helper()
	resp := f.exec(cmd, params)
	if resp.Status != "ok" {
		t.Fatalf("%s: %+v", cmd, resp)
	}
	job := f.resultMap(resp)["job"].(map[string]any)
	return job["job_id"].(string)
}

func TestMeasureBlankHappyPath(t *testing.T) {
	dir := t.TempDir()
	f := newFixture(t, withStateDir(dir))
	id := startJob(t, f, "measure_blank", "")
	// trigger frame 78 3 0 0 0 must have fired
	if !frameEq(f.frames()[len(f.frames())-1], 78, 3, 0, 0, 0) {
		t.Fatalf("blank trigger: %v", f.frames())
	}
	feedSweepCompletion(f, 100, 27, 45) // slope 100, 27.45 °C
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "blank success", func() bool {
		return jobResult(t, f, id)["state"] == "succeeded"
	})
	res := jobResult(t, f, id)["result"].(map[string]any)
	if res["slope"].(float64) < 99 || res["slope"].(float64) > 101 {
		t.Fatalf("slope = %v, want ~100", res["slope"])
	}
	if res["temperature_c"].(float64) < 27.4 || res["temperature_c"].(float64) > 27.5 {
		t.Fatalf("temperature_c = %v", res["temperature_c"])
	}
	if sweep, ok := res["sweep"].([]any); !ok || len(sweep) != 20 {
		t.Fatalf("blank result must include the 20-point sweep: %v", res["sweep"])
	}
	// blank persisted
	st := device.NewStore(dir, "densitometer-25-006")
	var ps struct {
		Blank *struct {
			Slope float64 `json:"slope"`
		} `json:"blank"`
	}
	if _, err := st.Load(&ps); err != nil || ps.Blank == nil {
		t.Fatalf("blank not persisted: %+v err=%v", ps, err)
	}
}

func TestSweepBusyFailFast(t *testing.T) {
	f := newFixture(t)
	startJob(t, f, "measure_blank", "")
	// mid-sweep (busy_until in the future): a serial-touching command fails fast.
	// ping is gated by serialGate; set_led's mid-sweep busy path is covered in
	// Task 7 (set_led is not wired into dispatch until then — testing it here
	// would return unknown_command, not busy).
	if resp := f.exec("ping", ""); resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("ping mid-sweep must be busy: %+v", resp)
	}
	// a second sweep is rejected by the active-job guard
	if resp := f.exec("measure_blank", ""); resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("second blank must be busy: %+v", resp)
	}
}

func TestSweepLivenessRetry(t *testing.T) {
	f := newFixture(t)
	id := startJob(t, f, "measure_blank", "")
	// No liveness reply yet: first soft attempt fails, schedules a retry.
	f.clock.Advance(densitometer.SweepWait)
	// still running (device "finishing")
	if jobResult(t, f, id)["state"] != "running" {
		t.Fatalf("job must still be running after failed liveness")
	}
	// now the device answers; the retry succeeds and the sweep completes
	feedSweepCompletion(f, 100, 27, 45)
	f.clock.Advance(densitometer.LivenessSpacing)
	waitFor(t, "blank success after retry", func() bool {
		return jobResult(t, f, id)["state"] == "succeeded"
	})
}

func TestSweepUnusableDetectorFailsJob(t *testing.T) {
	f := newFixture(t)
	id := startJob(t, f, "measure_blank", "")
	// liveness ok, array all-zero (dark), temperature ok → slope error
	f.port.Feed([]byte{70, 5, 27, 45})
	f.port.Feed(buildArrayBytes(func(i int) int { return 0 }))
	f.port.Feed([]byte{5, 5, 27, 45})
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "blank failed", func() bool {
		return jobResult(t, f, id)["state"] == "failed"
	})
	js := jobResult(t, f, id)
	if js["error"].(map[string]any)["code"] != "hardware_error" {
		t.Fatalf("unusable sweep must fail with hardware_error: %v", js["error"])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/densitometer/ -run 'MeasureBlank|Sweep' -v`
Expected: `measure_blank` returns `unknown_command`.

- [ ] **Step 3: Write the implementation** (`internal/device/densitometer/sweep.go`)

```go
package densitometer

import (
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

var blankTrigger = []byte{78, 3, 0, 0, 0}

// runSweep implements TRANSLATION §4 RUN_SWEEP: start a job, fire the trigger
// fire-and-forget (the firmware never acks), open the busy_until window, and
// schedule the After completion chain. No reply-expecting traffic touches the
// port until busy_until passes.
func (d *Driver) runSweep(kind string, trigger []byte, wait time.Duration, sw sweep) (device.Job, *device.CmdError) {
	if cerr := d.busyGuard(); cerr != nil {
		return device.Job{}, cerr
	}
	job, cerr := d.s.Jobs().Start(kind, wait+2*time.Second)
	if cerr != nil {
		return device.Job{}, cerr // unreachable: busyGuard ran first
	}
	if _, err := d.s.Transact(trigger, 0, replyTimeout); err != nil {
		// Transact double-fail already failed the job + flipped unreachable.
		return device.Job{}, device.ErrHardware("sweep trigger: " + err.Error())
	}
	d.sweepGen++
	sw.gen, sw.kind = d.sweepGen, kind
	d.sweep = &sw
	d.busyUntil = d.s.Now().Add(wait)
	d.lastJobID = job.ID
	gen := d.sweepGen
	d.s.After(wait, func() { d.onSweepDone(gen) })
	return job, nil
}

// stale reports whether a completion callback is for a superseded sweep.
func (d *Driver) stale(gen int) bool {
	return gen != d.sweepGen || d.sweep == nil || d.s.Jobs().Active() == nil
}

func (d *Driver) onSweepDone(gen int) {
	if d.stale(gen) {
		return
	}
	d.livenessAttempt(gen, 1)
}

// softPing does one bounded liveness read via the raw port. Unlike Transact it
// does NOT trip the session unreachable — the completion chain retries it up to
// LivenessRetries because the device may still be finishing its sweep
// (TRANSLATION §4 step 6). Loop-only; bounded to a few PerByteTimeouts.
func (d *Driver) softPing() bool {
	port := d.s.Conn()
	if err := port.Drain(device.DrainWindow); err != nil {
		return false
	}
	if _, err := port.Write(pingFrame); err != nil {
		return false
	}
	if err := port.SetReadTimeout(device.PerByteTimeout); err != nil {
		return false
	}
	buf := make([]byte, 0, 4)
	deadline := d.s.Now().Add(replyTimeout)
	for len(buf) < 4 {
		if d.s.Now().After(deadline) {
			return false
		}
		chunk := make([]byte, 4-len(buf))
		n, err := port.Read(chunk)
		if err != nil || n == 0 {
			return false
		}
		buf = append(buf, chunk[:n]...)
	}
	return buf[0] == TypeCode
}

func (d *Driver) livenessAttempt(gen, n int) {
	if d.stale(gen) {
		return
	}
	if n < LivenessRetries {
		if d.softPing() {
			d.readSweepAndFinish(gen)
			return
		}
		d.s.After(LivenessSpacing, func() { d.livenessAttempt(gen, n+1) })
		return
	}
	// Final attempt is a hard Transact: on failure it trips unreachable and
	// fails the active job (TRANSLATION §5); on success we proceed.
	reply, err := d.s.Transact(pingFrame, 4, replyTimeout)
	if err != nil {
		d.clearSweep() // markUnreachable already failed the job; clear d.sweep so the gate invariant holds
		return
	}
	if reply[0] != TypeCode {
		d.s.Jobs().Fail(device.ErrHardware("liveness: unexpected reply"))
		d.clearSweep()
		return
	}
	d.readSweepAndFinish(gen)
}

// readSweepAndFinish reads the 20-point array (validate + one retry) and the
// sweep-time temperature, then dispatches to the kind-specific finisher.
func (d *Driver) readSweepAndFinish(gen int) {
	if d.stale(gen) {
		return
	}
	intensities, cerr := d.readIntensityArray()
	if cerr != nil {
		d.s.Jobs().Fail(cerr)
		d.clearSweep()
		return
	}
	tempC := d.blank.mirrorTempFallback() // placeholder; replaced below
	if tReply, err := d.s.Transact(tempFrame, 4, replyTimeout); err == nil {
		tempC = decodeFixedPoint(tReply)
		d.cachedTemp, d.cachedTempAt, d.haveCachTemp = tempC, d.s.Now(), true
	} else {
		d.s.Jobs().Fail(device.ErrHardware("sweep temperature read: " + err.Error()))
		d.clearSweep()
		return
	}
	switch d.sweep.kind {
	case "blank":
		d.finishBlank(gen, intensities, tempC)
	// "measure", "monitor", "read_raw" wired in Tasks 6/7
	default:
		d.s.Jobs().Fail(device.ErrInternal("unknown sweep kind: " + d.sweep.kind))
		d.clearSweep()
	}
}

// readIntensityArray reads and validates the 80-byte array, flushing and
// retrying once on a header/index mismatch (button-session interleave).
func (d *Driver) readIntensityArray() ([20]int, *device.CmdError) {
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := d.s.Transact(arrayReadFrame, 80, ArrayReadTimeout)
		if err != nil {
			// A timeout trips unreachable inside Transact; surface hardware_error.
			return [20]int{}, device.ErrHardware("array read: " + err.Error())
		}
		intensities, cerr := parseIntensityArray(raw)
		if cerr == nil {
			return intensities, nil
		}
		if attempt == 1 {
			return [20]int{}, cerr
		}
	}
	return [20]int{}, device.ErrInternal("array read: unreachable")
}

type blankJobResult struct {
	Slope        float64 `json:"slope"`
	TemperatureC float64 `json:"temperature_c"`
	Sweep        []int   `json:"sweep"`
}

func (d *Driver) finishBlank(gen int, intensities [20]int, tempC float64) {
	if d.stale(gen) {
		return
	}
	slope, cerr := leastSquaresSlope(intensities)
	if cerr != nil {
		d.s.Jobs().Fail(cerr)
		d.clearSweep()
		return
	}
	now := d.s.Now()
	d.blank = &blankState{Slope: slope, TemperatureC: tempC, MeasuredAt: now}
	if err := d.persist(); err != nil {
		d.s.Jobs().Fail(device.ErrInternal("persist blank: " + err.Error()))
		d.clearSweep()
		return
	}
	d.s.Jobs().Complete(blankJobResult{
		Slope: slope, TemperatureC: tempC, Sweep: sliceOf(intensities),
	})
	d.clearSweep()
}

// sliceOf converts the fixed array to a slice for JSON marshaling.
func sliceOf(a [20]int) []int {
	out := make([]int, 20)
	copy(out, a[:])
	return out
}

func (d *Driver) measureBlank() (any, *device.CmdError) {
	job, cerr := d.runSweep("blank", blankTrigger, SweepWait, sweep{})
	if cerr != nil {
		return nil, cerr
	}
	return map[string]any{"job": job}, nil
}
```

**Correction to `readSweepAndFinish`**: the `d.blank.mirrorTempFallback()` placeholder is wrong (blank may be nil). Replace that line — temperature is always read from the device, so initialize `var tempC float64` and only use the device read:

```go
	var tempC float64
	if tReply, err := d.s.Transact(tempFrame, 4, replyTimeout); err == nil {
		tempC = decodeFixedPoint(tReply)
		d.cachedTemp, d.cachedTempAt, d.haveCachTemp = tempC, d.s.Now(), true
	} else {
		d.s.Jobs().Fail(device.ErrHardware("sweep temperature read: " + err.Error()))
		d.clearSweep()
		return
	}
```

Wire dispatch:

```go
	case "measure_blank":
		return d.measureBlank()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/device/densitometer/ -run 'MeasureBlank|Sweep|Status|Ping|Thermostat|Attach' -v`
Expected: PASS. (`TestStatusIdleReadsLiveTemperature` still passes; add a `mid-sweep cached temp` assertion only if desired.)

- [ ] **Step 5: Pre-flight and commit**

```bash
gofmt -l internal/device/densitometer/ && go vet ./internal/device/densitometer/ && go test -race ./internal/device/densitometer/
git add internal/device/densitometer/
git commit -m "feat(densitometer): sweep engine and measure_blank

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 6: `measure`, `set_tube_correction`, `calibrate_tube`

**Files:**
- Modify: `internal/device/densitometer/sweep.go` (add `measure`, `finishMeasure`, the ring append), `internal/device/densitometer/commands.go` (add `set_tube_correction`, `calibrate_tube`)
- Test: `internal/device/densitometer/sweep_test.go` (measure cases), `internal/device/densitometer/commands_test.go` (tube cases)

**Interfaces:**
- Consumes: `absorbance`, `blankState`, `reading`, ring buffer append (real ring lands in Task 8 — for Task 6 the ring append can go through a helper `appendReading(r)` that Task 8 backs with the real ring; in Task 6 back it with a minimal in-memory slice so `measure`/`calibrate_tube` work and their tests pass).
- Produces:
  - `measure(params) (any, *device.CmdError)` — require blank (`not_calibrated`), `runSweep("measure", {78,4,0,0,0}, SweepWait, sweep{includeRaw})`.
  - `finishMeasure(gen, intensities, tempC)` — slope, absorbance math, `seq++`, append reading, complete with the full measurement object; include the 20-point sweep iff `includeRaw`.
  - `setTubeCorrection(params) (any, *device.CmdError)` — validate `0.5 ≤ factor ≤ 2.0`, persist, return `{tube_correction}`. Pure (no serial).
  - `calibrateTube(params) (any, *device.CmdError)` — `factor = reference_absorbance / (last.absorbance / last.tubeCorrectionAt)`; require a last reading (`not_calibrated`); clamp to `[0.5,2.0]`; persist; return `{tube_correction, based_on_seq}`.
  - `appendReading(r reading)` — sets `lastReading` and pushes to the ring (real ring in Task 8).

- [ ] **Step 1: Write the failing tests**

To `sweep_test.go`:

```go
func TestMeasureRequiresBlank(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("measure", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeNotCalibrated {
		t.Fatalf("measure without blank must be not_calibrated: %+v", resp)
	}
}

// measureAfterBlank runs a blank (slope 100 @ 27.45) then a measure, returning
// the completed measure job result.
func measureAfterBlank(t *testing.T, f *fixture, params string, sampleSlope int, tInt, tFrac byte) map[string]any {
	t.Helper()
	bid := startJob(t, f, "measure_blank", "")
	feedSweepCompletion(f, 100, 27, 45)
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "blank done", func() bool { return jobResult(t, f, bid)["state"] == "succeeded" })

	mid := startJob(t, f, "measure", params)
	if !frameEq(f.frames()[len(f.frames())-1], 78, 4, 0, 0, 0) {
		t.Fatalf("measure trigger 78 4: %v", f.frames())
	}
	feedSweepCompletion(f, sampleSlope, tInt, tFrac)
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "measure done", func() bool { return jobResult(t, f, mid)["state"] == "succeeded" })
	return jobResult(t, f, mid)["result"].(map[string]any)
}

func TestMeasureAbsorbance(t *testing.T) {
	f := newFixture(t)
	// sample slope 50 vs blank 100, same temp → |log10(2)| ≈ 0.30103, tube 1.0
	res := measureAfterBlank(t, f, `{"include_raw":false}`, 50, 27, 45)
	if res["absorbance"].(float64) < 0.30 || res["absorbance"].(float64) > 0.302 {
		t.Fatalf("absorbance = %v, want ~0.30103", res["absorbance"])
	}
	if res["blank_slope"].(float64) < 99 || res["blank_slope"].(float64) > 101 {
		t.Fatalf("blank_slope = %v", res["blank_slope"])
	}
	if res["slope"].(float64) < 49 || res["slope"].(float64) > 51 {
		t.Fatalf("slope = %v", res["slope"])
	}
	if res["seq"].(float64) != 1 {
		t.Fatalf("seq = %v, want 1", res["seq"])
	}
	if res["raw"] != nil {
		t.Fatalf("raw must be null when include_raw=false: %v", res["raw"])
	}
}

func TestMeasureIncludeRaw(t *testing.T) {
	f := newFixture(t)
	res := measureAfterBlank(t, f, `{"include_raw":true}`, 50, 27, 45)
	if sw, ok := res["raw"].([]any); !ok || len(sw) != 20 {
		t.Fatalf("include_raw must attach the 20-point sweep: %v", res["raw"])
	}
}

func TestMeasureTemperatureCompensation(t *testing.T) {
	f := newFixture(t)
	// sample temp 37.45 vs blank 27.45 → +10 °C → +0.022 over raw
	res := measureAfterBlank(t, f, "", 50, 37, 45)
	if res["absorbance"].(float64) < 0.322 || res["absorbance"].(float64) > 0.324 {
		t.Fatalf("temperature-compensated absorbance = %v, want ~0.32303", res["absorbance"])
	}
}
```

To `commands_test.go`:

```go
func TestSetTubeCorrection(t *testing.T) {
	dir := t.TempDir()
	f := newFixture(t, withStateDir(dir))
	m := f.resultMap(f.exec("set_tube_correction", `{"factor":1.03}`))
	if m["tube_correction"] != 1.03 {
		t.Fatalf("result: %v", m)
	}
	st := device.NewStore(dir, "densitometer-25-006")
	var ps struct {
		TubeCorrection float64 `json:"tube_correction"`
	}
	if _, err := st.Load(&ps); err != nil || ps.TubeCorrection != 1.03 {
		t.Fatalf("tube correction not persisted: %v err=%v", ps.TubeCorrection, err)
	}
}

func TestSetTubeCorrectionRange(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{`{"factor":0.4}`, `{"factor":2.1}`} {
		resp := f.exec("set_tube_correction", p)
		if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
			t.Fatalf("%s: %+v", p, resp)
		}
	}
}

func TestCalibrateTubeNoMeasurement(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("calibrate_tube", `{"reference_absorbance":0.5}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeNotCalibrated {
		t.Fatalf("calibrate_tube without measurement: %+v", resp)
	}
}

func TestCalibrateTubeFromLastMeasurement(t *testing.T) {
	f := newFixture(t)
	// measure gives absorbance ~0.30103 at tube 1.0; reference 0.60206 → factor 2.0
	measureAfterBlank(t, f, "", 50, 27, 45)
	m := f.resultMap(f.exec("calibrate_tube", `{"reference_absorbance":0.60206}`))
	if m["tube_correction"].(float64) < 1.99 || m["tube_correction"].(float64) > 2.01 {
		t.Fatalf("tube_correction = %v, want ~2.0", m["tube_correction"])
	}
	if m["based_on_seq"].(float64) != 1 {
		t.Fatalf("based_on_seq = %v", m["based_on_seq"])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/densitometer/ -run 'Measure|TubeCorrection|CalibrateTube' -v`
Expected: `measure`/`set_tube_correction`/`calibrate_tube` return `unknown_command`.

- [ ] **Step 3: Write the implementation**

Add to `sweep.go`:

```go
var measureTrigger = []byte{78, 4, 0, 0, 0}

func (d *Driver) measure(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		IncludeRaw bool `json:"include_raw"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
		}
	}
	if d.blank == nil {
		return nil, device.ErrNotCalibrated("no blank measured — run measure_blank first")
	}
	job, cerr := d.runSweep("measure", measureTrigger, SweepWait, sweep{includeRaw: p.IncludeRaw})
	if cerr != nil {
		return nil, cerr
	}
	return map[string]any{"job": job}, nil
}

type measureJobResult struct {
	Absorbance     float64 `json:"absorbance"`
	AbsorbanceRaw  float64 `json:"absorbance_raw"`
	Slope          float64 `json:"slope"`
	BlankSlope     float64 `json:"blank_slope"`
	TemperatureC   float64 `json:"temperature_c"`
	TubeCorrection float64 `json:"tube_correction"`
	Seq            int64   `json:"seq"`
	Raw            []int   `json:"raw"`
}

// finishMeasure computes absorbance, records the reading, and completes the
// job. Shared by the measure command and the monitoring scheduler (kind
// "measure" and "monitor" both land here).
func (d *Driver) finishMeasure(gen int, intensities [20]int, tempC float64) {
	if d.stale(gen) {
		return
	}
	slope, cerr := leastSquaresSlope(intensities)
	if cerr != nil {
		d.s.Jobs().Fail(cerr)
		d.clearSweep()
		return
	}
	final, raw := absorbance(d.blank.Slope, slope, tempC, d.blank.TemperatureC, d.tubeCorrection)
	now := d.s.Now()
	d.seqCounter++
	r := reading{
		seq: d.seqCounter, measuredAt: now,
		uptimeMs:     now.Sub(d.connectedSince).Milliseconds(),
		absorbance:   final, temperatureC: tempC, tubeCorrectionAt: d.tubeCorrection,
	}
	d.appendReading(r)

	var rawSweep []int
	if d.sweep.includeRaw {
		rawSweep = sliceOf(intensities)
	}
	d.s.Jobs().Complete(measureJobResult{
		Absorbance: final, AbsorbanceRaw: raw, Slope: slope,
		BlankSlope: d.blank.Slope, TemperatureC: tempC,
		TubeCorrection: d.tubeCorrection, Seq: r.seq, Raw: rawSweep,
	})
	d.clearSweep()
}
```

In `readSweepAndFinish`'s `switch`, add:

```go
	case "measure", "monitor":
		d.finishMeasure(gen, intensities, tempC)
```

Add to `commands.go`:

```go
func (d *Driver) setTubeCorrection(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Factor float64 `json:"factor"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if p.Factor < 0.5 || p.Factor > 2.0 {
		return nil, device.ErrInvalidParams("factor", p.Factor, "factor must be between 0.5 and 2.0")
	}
	d.tubeCorrection = p.Factor
	if err := d.persist(); err != nil {
		return nil, device.ErrInternal("persist tube correction: " + err.Error())
	}
	return map[string]any{"tube_correction": p.Factor}, nil
}

func (d *Driver) calibrateTube(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		ReferenceAbsorbance float64 `json:"reference_absorbance"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if d.lastReading == nil {
		return nil, device.ErrNotCalibrated("no measurement to calibrate from")
	}
	if p.ReferenceAbsorbance <= 0 {
		return nil, device.ErrInvalidParams("reference_absorbance", p.ReferenceAbsorbance,
			"reference_absorbance must be positive")
	}
	uncorrected := d.lastReading.absorbance / d.lastReading.tubeCorrectionAt
	if uncorrected == 0 {
		return nil, device.ErrInvalidParams("reference_absorbance", p.ReferenceAbsorbance,
			"last measurement absorbance is zero — cannot calibrate")
	}
	factor := p.ReferenceAbsorbance / uncorrected
	// A materially out-of-range factor means a bad reference or a unit error —
	// reject it rather than silently corrupt every later measurement. A factor
	// that only overshoots by float noise (the boundary case) is snapped to bound.
	const tol = 1e-6
	if factor < 0.5-tol || factor > 2.0+tol {
		return nil, device.ErrInvalidParams("reference_absorbance", p.ReferenceAbsorbance,
			"resulting tube correction out of range [0.5, 2.0]")
	}
	factor = math.Max(0.5, math.Min(2.0, factor))
	d.tubeCorrection = factor
	if err := d.persist(); err != nil {
		return nil, device.ErrInternal("persist tube correction: " + err.Error())
	}
	return map[string]any{"tube_correction": factor, "based_on_seq": d.lastReading.seq}, nil
}
```

Add `appendReading` to `commands.go` (or `sweep.go`), backed for now by `lastReading`; Task 8 extends it to push to the real ring:

```go
// appendReading records the newest measurement. Task 8 also pushes it to the
// readings ring buffer.
func (d *Driver) appendReading(r reading) {
	rr := r
	d.lastReading = &rr
	d.ring.push(rr)
}
```

For Task 6, `ringBuffer` is still the Task-2 stub. Give the stub a no-op `push` so this compiles; Task 8 replaces it:

```go
// TEMPORARY (Task 8 replaces with the real ring): no-op push.
func (rb *ringBuffer) push(reading) {}
```

Wire dispatch:

```go
	case "measure":
		return d.measure(params)
	case "set_tube_correction":
		return d.setTubeCorrection(params)
	case "calibrate_tube":
		return d.calibrateTube(params)
```

Add `import "encoding/json"` to `sweep.go` if not already present.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/device/densitometer/ -run 'Measure|TubeCorrection|CalibrateTube|Blank|Sweep' -v`
Expected: PASS.

- [ ] **Step 5: Pre-flight and commit**

```bash
gofmt -l internal/device/densitometer/ && go vet ./internal/device/densitometer/ && go test -race ./internal/device/densitometer/
git add internal/device/densitometer/
git commit -m "feat(densitometer): measure, absorbance and tube correction

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 7: `read_raw`, `set_led`, `stop`, `stop_monitoring`

**Files:**
- Modify: `internal/device/densitometer/sweep.go` (`readRaw`, `finishReadRaw`), `internal/device/densitometer/commands.go` (`setLED`, `stop`, `stopMonitoring`)
- Test: `internal/device/densitometer/sweep_test.go` (read_raw), `internal/device/densitometer/commands_test.go` (led/stop)

**Interfaces:**
- Produces:
  - `readRaw(params) (any, *device.CmdError)` — `level==null` → `runSweep("read_raw", {78,4,0,0,0}, SweepWait, sweep{level:0})`; `level==n` (1–20) → `runSweep("read_raw", {75,1,n,0,0}, SingleLevelWait, sweep{level:n})`.
  - `finishReadRaw(gen, intensities, tempC)` — `level==0` → `{intensities:[20], levels:[1..20], temperature_c}`; `level==n` → `{intensities:[mean], levels:[n], temperature_c}` (the firmware fills all 20 slots at the one brightness; return the mean per flagged reconciliation of TRANSLATION "or its mean" with JSON "single-element array").
  - `setLED(params) (any, *device.CmdError)` — gate on `serialGate`; validate `0 ≤ level ≤ 20`; `[75,0,level,0,0]`; optimistic `{level}`.
  - `stop() (any, *device.CmdError)` — `[70,0,0,0,0]` (buffers during a sweep); cancel active job; disable monitoring; bump `sweepGen`; `{state:"idle", cancelled_job_id}`. `stop` is exempt from `serialGate`.
  - `stopMonitoring() (any, *device.CmdError)` — bookkeeping only: disable the scheduler; `{state, ...}`. No serial.

- [ ] **Step 1: Write the failing tests**

To `sweep_test.go`:

```go
func TestReadRawFullSweep(t *testing.T) {
	f := newFixture(t)
	id := startJob(t, f, "read_raw", `{"level":null}`)
	if !frameEq(f.frames()[len(f.frames())-1], 78, 4, 0, 0, 0) {
		t.Fatalf("full read_raw must trigger 78 4: %v", f.frames())
	}
	feedSweepCompletion(f, 100, 27, 45)
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "read_raw done", func() bool { return jobResult(t, f, id)["state"] == "succeeded" })
	res := jobResult(t, f, id)["result"].(map[string]any)
	if len(res["intensities"].([]any)) != 20 || len(res["levels"].([]any)) != 20 {
		t.Fatalf("full sweep must return 20 intensities+levels: %v", res)
	}
}

func TestReadRawSingleLevel(t *testing.T) {
	f := newFixture(t)
	id := startJob(t, f, "read_raw", `{"level":7}`)
	if !frameEq(f.frames()[len(f.frames())-1], 75, 1, 7, 0, 0) {
		t.Fatalf("single-level read_raw must trigger 75 1 7: %v", f.frames())
	}
	// firmware fills all 20 slots at brightness 7; feed a flat array of 500
	f.port.Feed([]byte{70, 5, 27, 45})
	f.port.Feed(buildArrayBytes(func(i int) int { return 500 }))
	f.port.Feed([]byte{5, 5, 27, 45})
	f.clock.Advance(densitometer.SingleLevelWait)
	waitFor(t, "single-level done", func() bool { return jobResult(t, f, id)["state"] == "succeeded" })
	res := jobResult(t, f, id)["result"].(map[string]any)
	ints := res["intensities"].([]any)
	levels := res["levels"].([]any)
	if len(ints) != 1 || ints[0].(float64) != 500 || len(levels) != 1 || levels[0].(float64) != 7 {
		t.Fatalf("single-level result: %v", res)
	}
}

func TestReadRawInvalidLevel(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("read_raw", `{"level":21}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
		t.Fatalf("level 21: %+v", resp)
	}
}
```

To `commands_test.go`:

```go
func TestSetLED(t *testing.T) {
	f := newFixture(t)
	m := f.resultMap(f.exec("set_led", `{"level":12}`))
	if m["level"] != 12.0 {
		t.Fatalf("result: %v", m)
	}
	if !frameEq(f.frames()[len(f.frames())-1], 75, 0, 12, 0, 0) {
		t.Fatalf("led frame: %v", f.frames())
	}
}

func TestSetLEDRange(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("set_led", `{"level":21}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
		t.Fatalf("%+v", resp)
	}
}

// TestSetLEDBusyMidSweep: set_led touches the port, so it fails fast with busy
// during a sweep's busy window (the mid-sweep case deferred from Task 5, where
// set_led was not yet wired into dispatch).
func TestSetLEDBusyMidSweep(t *testing.T) {
	f := newFixture(t)
	startJob(t, f, "measure_blank", "")
	resp := f.exec("set_led", `{"level":5}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("set_led mid-sweep must be busy: %+v", resp)
	}
}

func TestStopCancelsSweep(t *testing.T) {
	f := newFixture(t)
	id := startJob(t, f, "measure_blank", "")
	// stop mid-sweep: writes 70 (buffers), cancels the job bookkeeping now
	f.port.Feed([]byte{}) // stop's 70 is write-only, no reply needed
	resp := f.exec("stop", "")
	if resp.Status != "ok" {
		t.Fatalf("stop: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["state"] != "idle" || m["cancelled_job_id"] != id {
		t.Fatalf("stop result: %v", m)
	}
	if jobResult(t, f, id)["state"] != "cancelled" {
		t.Fatalf("job must be cancelled")
	}
	// stop cancels bookkeeping but the firmware cannot abort a sweep — it
	// physically finishes (~6 s). busy_until is deliberately NOT reset, so a
	// serial-touching command must still fail fast with busy until the window
	// elapses (TRANSLATION §stop). Guards against a future reset regression.
	if resp := f.exec("set_led", `{"level":5}`); resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("serial command right after stop (device still sweeping) must be busy: %+v", resp)
	}
	// a stale completion callback must not resurrect the job
	feedSweepCompletion(f, 100, 27, 45)
	f.clock.Advance(densitometer.SweepWait + densitometer.LivenessSpacing)
	if jobResult(t, f, id)["state"] != "cancelled" {
		t.Fatalf("cancelled job must stay cancelled after the stale timer fires")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/densitometer/ -run 'ReadRaw|SetLED|Stop' -v`
Expected: `unknown_command` for these commands.

- [ ] **Step 3: Write the implementation**

Add to `sweep.go`:

```go
func (d *Driver) readRaw(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Level *int `json:"level"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
		}
	}
	if p.Level == nil {
		job, cerr := d.runSweep("read_raw", measureTrigger, SweepWait, sweep{level: 0})
		if cerr != nil {
			return nil, cerr
		}
		return map[string]any{"job": job}, nil
	}
	n := *p.Level
	if n < 1 || n > 20 {
		return nil, device.ErrInvalidParams("level", n, "level must be 1..20 or null")
	}
	trigger := []byte{75, 1, byte(n), 0, 0} // #nosec G115 -- n is 1..20
	job, cerr := d.runSweep("read_raw", trigger, SingleLevelWait, sweep{level: n})
	if cerr != nil {
		return nil, cerr
	}
	return map[string]any{"job": job}, nil
}

type readRawResult struct {
	Intensities  []int   `json:"intensities"`
	Levels       []int   `json:"levels"`
	TemperatureC float64 `json:"temperature_c"`
}

func (d *Driver) finishReadRaw(gen int, intensities [20]int, tempC float64) {
	if d.stale(gen) {
		return
	}
	var res readRawResult
	res.TemperatureC = tempC
	if d.sweep.level == 0 {
		res.Intensities = sliceOf(intensities)
		res.Levels = make([]int, 20)
		for i := range res.Levels {
			res.Levels[i] = i + 1
		}
	} else {
		sum := 0
		for _, v := range intensities {
			sum += v
		}
		res.Intensities = []int{sum / 20}
		res.Levels = []int{d.sweep.level}
	}
	d.s.Jobs().Complete(res)
	d.clearSweep()
}
```

In `readSweepAndFinish`'s `switch`, add:

```go
	case "read_raw":
		d.finishReadRaw(gen, intensities, tempC)
```

Add to `commands.go`:

```go
func (d *Driver) setLED(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Level int `json:"level"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if cerr := d.serialGate(); cerr != nil {
		return nil, cerr
	}
	if p.Level < 0 || p.Level > 20 {
		return nil, device.ErrInvalidParams("level", p.Level, "level must be 0..20")
	}
	if _, err := d.s.Transact([]byte{75, 0, byte(p.Level), 0, 0}, 0, replyTimeout); err != nil { // #nosec G115 -- 0..20
		return nil, device.ErrHardware("set_led: " + err.Error())
	}
	return map[string]any{"level": p.Level}, nil // GAP: no readback, optimistic
}

type stopResult struct {
	State          string `json:"state"`
	CancelledJobID string `json:"cancelled_job_id,omitempty"`
}

// stop (TRANSLATION §4): sends 70 (LED off / stop continuous mode) — during a
// sweep this buffers in the device RX and runs when the sweep ends — then
// cancels the job bookkeeping immediately and disables monitoring. Exempt from
// serialGate: the 70 frame is write-only and safe to buffer.
func (d *Driver) stop() (any, *device.CmdError) {
	if _, err := d.s.Transact(stopFrame, 0, replyTimeout); err != nil {
		return nil, device.ErrHardware("stop: " + err.Error())
	}
	res := stopResult{State: "idle"}
	if a := d.s.Jobs().Active(); a != nil {
		cancelled := d.s.Jobs().Cancel()
		res.CancelledJobID = cancelled.ID
	}
	d.monitoring = monitoringState{}
	d.clearSweep() // bumps sweepGen → pending completion callbacks no-op
	return res, nil
}

type stopMonitoringResult struct {
	State string `json:"state"`
}

// stopMonitoring disables the scheduler (bookkeeping only). Also invoked by stop.
func (d *Driver) stopMonitoring() (any, *device.CmdError) {
	d.monitoring = monitoringState{}
	return stopMonitoringResult{State: d.stateName()}, nil
}
```

Wire dispatch:

```go
	case "read_raw":
		return d.readRaw(params)
	case "set_led":
		return d.setLED(params)
	case "stop":
		return d.stop()
	case "stop_monitoring":
		return d.stopMonitoring()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/device/densitometer/ -run 'ReadRaw|SetLED|Stop|Measure|Blank|Sweep' -v`
Expected: PASS.

- [ ] **Step 5: Pre-flight and commit**

```bash
gofmt -l internal/device/densitometer/ && go vet ./internal/device/densitometer/ && go test -race ./internal/device/densitometer/
git add internal/device/densitometer/
git commit -m "feat(densitometer): read_raw, set_led and stop

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 8: Monitoring — ring buffer, `start_monitoring`, `get_readings`, `Tick`

**Files:**
- Create: `internal/device/densitometer/monitoring.go`
- Modify: `internal/device/densitometer/densitometer.go` (delete the Task-2 `ringBuffer` stub + `newRingBuffer`; delete the Task-6 no-op `push`), `internal/device/densitometer/sweep.go` (add the `monitoring.enabled → busy` guard to `measure`; add a `startMonitorMeasure` internal starter)
- Test: `internal/device/densitometer/monitoring_test.go`

**Interfaces:**
- Produces:
  - `ringBuffer` (real): fixed capacity `ringCap = 64`; `newRingBuffer() *ringBuffer`; `push(reading)`; `oldestSeq() int64`; `since(sinceSeq int64, limit int) []reading`.
  - `startMonitoring(params) (any, *device.CmdError)` — validate `interval_s ≥ 10` (default 60), require blank (`not_calibrated`), set `monitoring = {true, interval, nextTickAt: now}`, return `{state:"monitoring", interval_s}`.
  - `getReadings(params) (any, *device.CmdError)` — `{readings:[…], dropped}` with `since_seq`/`limit` semantics.
  - `Tick(now)` (real) — mid-sweep/active-job → no-op; else run the monitoring scheduler (start an internal `measure` when due) then the idle reboot-canary poll.
  - `startMonitorMeasure()` — internal `runSweep("monitor", {78,4}, SweepWait, sweep{})`; disables monitoring + logs if the blank vanished or the trigger fails.
- Modify `measure` to reject with `busy` while `monitoring.enabled`.

- [ ] **Step 1: Write the failing tests** (`internal/device/densitometer/monitoring_test.go`)

```go
package densitometer_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/densitometer"
)

// runBlank completes a blank so measures are allowed.
func runBlank(t *testing.T, f *fixture) {
	t.Helper()
	bid := startJob(t, f, "measure_blank", "")
	feedSweepCompletion(f, 100, 27, 45)
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "blank", func() bool { return jobResult(t, f, bid)["state"] == "succeeded" })
}

func TestStartMonitoringRequiresBlank(t *testing.T) {
	f := newFixture(t)
	resp := f.exec("start_monitoring", `{"interval_s":30}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeNotCalibrated {
		t.Fatalf("start_monitoring without blank: %+v", resp)
	}
}

func TestStartMonitoringRejectsShortInterval(t *testing.T) {
	f := newFixture(t)
	runBlank(t, f)
	resp := f.exec("start_monitoring", `{"interval_s":5}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
		t.Fatalf("interval < 10: %+v", resp)
	}
}

func TestMeasureRejectedWhileMonitoring(t *testing.T) {
	f := newFixture(t)
	runBlank(t, f)
	if resp := f.exec("start_monitoring", `{"interval_s":30}`); resp.Status != "ok" {
		t.Fatalf("start_monitoring: %+v", resp)
	}
	resp := f.exec("measure", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("measure while monitoring must be busy: %+v", resp)
	}
}

func TestGetReadingsSinceSeqAndDropped(t *testing.T) {
	f := newFixture(t)
	runBlank(t, f)
	// two one-off measures → seq 1, 2
	for _, slope := range []int{50, 60} {
		mid := startJob(t, f, "measure", "")
		feedSweepCompletion(f, slope, 27, 45)
		f.clock.Advance(densitometer.SweepWait)
		waitFor(t, "measure", func() bool { return jobResult(t, f, mid)["state"] == "succeeded" })
	}
	m := f.resultMap(f.exec("get_readings", `{"since_seq":0,"limit":100}`))
	if len(m["readings"].([]any)) != 2 || m["dropped"].(float64) != 0 {
		t.Fatalf("get_readings since 0: %v", m)
	}
	m = f.resultMap(f.exec("get_readings", `{"since_seq":1}`))
	rs := m["readings"].([]any)
	if len(rs) != 1 || rs[0].(map[string]any)["seq"].(float64) != 2 {
		t.Fatalf("get_readings since 1: %v", m)
	}
}

func TestMonitoringSchedulerRunsMeasure(t *testing.T) {
	f := newFixture(t)
	runBlank(t, f)
	if resp := f.exec("start_monitoring", `{"interval_s":10}`); resp.Status != "ok" {
		t.Fatalf("start_monitoring: %+v", resp)
	}
	// Pre-feed the scheduled measure's completion, then fire a Tick and the
	// sweep completion.
	feedSweepCompletion(f, 50, 27, 45)
	f.clock.Advance(device.HeartbeatInterval) // Tick starts the monitor measure
	waitFor(t, "monitor measure started", func() bool {
		// the 78 4 trigger appears once the scheduler fires
		for _, fr := range f.frames() {
			if frameEq(fr, 78, 4, 0, 0, 0) {
				return true
			}
		}
		return false
	})
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "monitor reading buffered", func() bool {
		m := f.resultMap(f.exec("get_readings", `{"since_seq":0}`))
		return len(m["readings"].([]any)) >= 1
	})
}

func TestTickCanaryDetectsReboot(t *testing.T) {
	dir := t.TempDir()
	st := device.NewStore(dir, "densitometer-25-006")
	if err := st.Save(map[string]any{
		"schema_version": 1, "tube_correction": 1.0,
		"thermostat": map[string]any{"enabled": true, "target_c": 37.0},
	}); err != nil {
		t.Fatal(err)
	}
	shrinkTimeouts(t)
	clock := device.NewFakeClock(timeUnix1000())
	port := newPort("COM8")
	opener := newOpener(port)
	// Attach: serial, wavelength, force-tube, thermostat readback 37 (in sync).
	port.Feed([]byte{5, 7, 25, 6})
	port.Feed([]byte{1, 2, 6, 0})
	port.Feed([]byte{5, 5, 37, 0})
	f := startFixture(t, clock, port, opener, dir)
	// Idle canary poll fires ~CanaryInterval later: feed a rebooted readback
	// (10.00) then the re-push verify (37.00).
	port.Feed([]byte{5, 5, 10, 0}) // canary read → rebooted
	port.Feed([]byte{5, 5, 37, 0}) // re-push verify
	f.clock.Advance(densitometer.CanaryInterval + device.HeartbeatInterval)
	waitFor(t, "canary re-push", func() bool {
		for _, fr := range f.frames() {
			if frameEq(fr, 75, 2, 37, 0, 0) {
				return true
			}
		}
		return false
	})
}
```

Note: `TestTickCanaryDetectsReboot` uses `CanaryInterval` unshrunk (30 s of FakeClock is instant). Ensure `startFixture` builds `nextCanaryAt = connectedSince + CanaryInterval`; the `Advance(CanaryInterval + HeartbeatInterval)` fires one heartbeat with `now` past `nextCanaryAt`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/densitometer/ -run 'Monitoring|GetReadings|TickCanary' -v`
Expected: `start_monitoring`/`get_readings` return `unknown_command`; `TestMeasureRejectedWhileMonitoring` and the scheduler tests fail.

- [ ] **Step 3: Write the implementation** (`internal/device/densitometer/monitoring.go`)

```go
package densitometer

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

const ringCap = 64

// ringBuffer holds the most recent ringCap readings (TRANSLATION §1 volatile
// state). Loop-only.
type ringBuffer struct {
	buf   []reading
	start int // index of the oldest entry
	count int
}

func newRingBuffer() *ringBuffer { return &ringBuffer{buf: make([]reading, ringCap)} }

func (rb *ringBuffer) push(r reading) {
	if rb.count < ringCap {
		rb.buf[(rb.start+rb.count)%ringCap] = r
		rb.count++
		return
	}
	rb.buf[rb.start] = r
	rb.start = (rb.start + 1) % ringCap
}

func (rb *ringBuffer) oldestSeq() int64 {
	if rb.count == 0 {
		return 0
	}
	return rb.buf[rb.start].seq
}

func (rb *ringBuffer) since(sinceSeq int64, limit int) []reading {
	var out []reading
	for i := 0; i < rb.count; i++ {
		r := rb.buf[(rb.start+i)%ringCap]
		if r.seq > sinceSeq {
			out = append(out, r)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

type startMonitoringResult struct {
	State     string `json:"state"`
	IntervalS int    `json:"interval_s"`
}

func (d *Driver) startMonitoring(params json.RawMessage) (any, *device.CmdError) {
	p := struct {
		IntervalS int `json:"interval_s"`
	}{IntervalS: 60}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
		}
	}
	if p.IntervalS < 10 {
		return nil, device.ErrInvalidParams("interval_s", p.IntervalS,
			"interval_s must be at least 10 (the sweep duration bound)")
	}
	if d.blank == nil {
		return nil, device.ErrNotCalibrated("no blank measured — run measure_blank first")
	}
	d.monitoring = monitoringState{enabled: true, intervalS: p.IntervalS, nextTickAt: d.s.Now()}
	return startMonitoringResult{State: "monitoring", IntervalS: p.IntervalS}, nil
}

type readingWire struct {
	Seq          int64   `json:"seq"`
	UptimeMs     int64   `json:"uptime_ms"`
	Absorbance   float64 `json:"absorbance"`
	TemperatureC float64 `json:"temperature_c"`
}

func (d *Driver) getReadings(params json.RawMessage) (any, *device.CmdError) {
	p := struct {
		SinceSeq int64 `json:"since_seq"`
		Limit    int   `json:"limit"`
	}{}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
		}
	}
	entries := d.ring.since(p.SinceSeq, p.Limit)
	out := make([]readingWire, 0, len(entries))
	for _, r := range entries {
		out = append(out, readingWire{
			Seq: r.seq, UptimeMs: r.uptimeMs,
			Absorbance: r.absorbance, TemperatureC: r.temperatureC,
		})
	}
	dropped := int64(0)
	if oldest := d.ring.oldestSeq(); oldest > p.SinceSeq+1 {
		dropped = oldest - p.SinceSeq - 1
	}
	return map[string]any{"readings": out, "dropped": dropped}, nil
}

// Tick runs ~1/s while attached: the monitoring scheduler then the idle reboot
// canary. Both need the port, so both are skipped while a sweep is in flight or
// a job is active.
func (d *Driver) Tick(now time.Time) {
	if now.Before(d.busyUntil) || d.s.Jobs().Active() != nil {
		return
	}
	if d.monitoring.enabled && !now.Before(d.monitoring.nextTickAt) {
		d.monitoring.nextTickAt = now.Add(time.Duration(d.monitoring.intervalS) * time.Second)
		d.startMonitorMeasure()
		return // a measure now owns the port; the canary waits for the next idle Tick
	}
	if !now.Before(d.nextCanaryAt) {
		d.nextCanaryAt = now.Add(CanaryInterval)
		if reply, err := d.s.Transact(thermReadFrame, 4, replyTimeout); err == nil {
			d.applyThermostatReadback(decodeFixedPoint(reply), true)
		}
	}
}

// startMonitorMeasure fires an internal measure sweep whose completion lands a
// reading in the ring (kind "monitor" → finishMeasure).
func (d *Driver) startMonitorMeasure() {
	if d.blank == nil {
		slog.Warn("densitometer: monitoring active without a blank — disabling", "device", d.serial)
		d.monitoring = monitoringState{}
		return
	}
	if _, cerr := d.runSweep("monitor", measureTrigger, SweepWait, sweep{}); cerr != nil {
		slog.Warn("densitometer: monitoring measure failed to start", "device", d.serial, "err", cerr)
	}
}
```

Delete from `densitometer.go` the Task-2 stub block (`func newRingBuffer`, `type ringBuffer struct{}`) and from `sweep.go`/`commands.go` the Task-6 no-op `push`. Modify `measure` (in `sweep.go`) to add the monitoring guard right after JSON parse:

```go
	if d.monitoring.enabled {
		return nil, device.ErrBusy("monitoring is active — stop it before a one-off measure",
			map[string]any{"state": "monitoring"})
	}
```

Wire dispatch:

```go
	case "start_monitoring":
		return d.startMonitoring(params)
	case "get_readings":
		return d.getReadings(params)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/densitometer/ -run 'Monitoring|GetReadings|TickCanary|Measure' -v`
Expected: PASS.

- [ ] **Step 5: Pre-flight and commit**

```bash
gofmt -l internal/device/densitometer/ && go vet ./internal/device/densitometer/ && go test -race ./internal/device/densitometer/
git add internal/device/densitometer/
git commit -m "feat(densitometer): monitoring scheduler, readings ring and Tick canary

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

### Task 9: Integration test, busy-window status, full pre-flight & PR

**Files:**
- Create: `internal/device/densitometer/integration_test.go`
- Test: also add the deferred busy-window status assertion and a stop-during-monitoring test.

**Interfaces:** none new — this task exercises the whole driver end-to-end and runs the repo-wide gates.

- [ ] **Step 1: Write the integration + remaining edge tests** (`internal/device/densitometer/integration_test.go`)

```go
package densitometer_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/densitometer"
)

// TestFullSession walks the JSON_PROTOCOL §8 typical session:
// identify → set_thermostat → status → measure_blank → measure → monitoring →
// get_readings → stop.
func TestFullSession(t *testing.T) {
	f := newFixture(t)

	if f.exec("identify", "").Status != "ok" {
		t.Fatal("identify")
	}

	feedThermSet(f.port, 37)
	if f.exec("set_thermostat", `{"enabled":true,"target_c":37}`).Status != "ok" {
		t.Fatal("set_thermostat")
	}

	f.port.Feed([]byte{5, 5, 36, 98}) // status temperature
	f.port.Feed([]byte{5, 5, 37, 0})  // status thermostat (in sync)
	if st := f.resultMap(f.exec("status", "")); st["state"] != "idle" {
		t.Fatalf("status before measuring: %v", st)
	}

	bid := startJob(t, f, "measure_blank", "")
	feedSweepCompletion(f, 100, 37, 0)
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "blank", func() bool { return jobResult(t, f, bid)["state"] == "succeeded" })

	mid := startJob(t, f, "measure", "")
	feedSweepCompletion(f, 50, 37, 0)
	f.clock.Advance(densitometer.SweepWait)
	waitFor(t, "measure", func() bool { return jobResult(t, f, mid)["state"] == "succeeded" })

	if f.exec("start_monitoring", `{"interval_s":60}`).Status != "ok" {
		t.Fatal("start_monitoring")
	}
	rd := f.resultMap(f.exec("get_readings", `{"since_seq":0}`))
	if len(rd["readings"].([]any)) != 1 {
		t.Fatalf("expected the one-off measure reading: %v", rd)
	}

	stop := f.resultMap(f.exec("stop", ""))
	if stop["state"] != "idle" {
		t.Fatalf("stop: %v", stop)
	}
	// monitoring disabled by stop
	if f.resultMap(f.exec("status", ""))["state"] == "monitoring" {
		// status needs live reads when idle; feed them
	}
}

func TestStatusMidSweepServesCachedTemperature(t *testing.T) {
	f := newFixture(t)
	// prime the cache with an idle status read
	f.port.Feed([]byte{5, 5, 30, 0}) // temperature 30.00
	f.port.Feed([]byte{5, 5, 0, 0})  // thermostat 0
	if f.resultMap(f.exec("status", ""))["temperature_c"].(float64) != 30.0 {
		t.Fatal("prime cache")
	}
	// start a sweep → busy window
	startJob(t, f, "measure_blank", "")
	m := f.resultMap(f.exec("status", ""))
	if m["state"] != "measuring" {
		t.Fatalf("state during sweep: %v", m)
	}
	if m["temperature_c"].(float64) != 30.0 {
		t.Fatalf("mid-sweep status must serve cached temperature, got %v", m["temperature_c"])
	}
}

func TestStopDuringMonitoring(t *testing.T) {
	f := newFixture(t)
	runBlank(t, f)
	if f.exec("start_monitoring", `{"interval_s":30}`).Status != "ok" {
		t.Fatal("start_monitoring")
	}
	if f.resultMap(f.exec("stop", ""))["state"] != "idle" {
		t.Fatal("stop must return idle")
	}
	// a subsequent Tick must not start a measure (monitoring disabled)
	before := len(f.frames())
	f.clock.Advance(device.HeartbeatInterval)
	time.Sleep(20 * time.Millisecond)
	for _, fr := range f.frames()[before:] {
		if frameEq(fr, 78, 4, 0, 0, 0) {
			t.Fatal("stop did not disable monitoring — a measure fired")
		}
	}
}
```

- [ ] **Step 2: Run the full package test suite with the race detector**

Run: `go test -race -count=1 ./internal/device/densitometer/ -v`
Expected: every test PASSES.

- [ ] **Step 3: Run the complete repo pre-flight (CLAUDE.md), macOS**

Run each; every one must be clean:

```
gofmt -l .
go vet ./...
golangci-lint run
go test -race -count=1 ./...
govulncheck ./...
```

Fix anything they surface (common: an unused field/import from an intermediate task, an errcheck on a `Transact` whose error is deliberately ignored — wrap in `_, _ =`; a gosec G115 needing its `// #nosec` justification). Do **not** silence a real finding.

- [ ] **Step 4: Confirm nothing outside the new package changed**

Run: `git diff --stat origin/main -- ':!internal/device/densitometer' ':!docs/superpowers/plans'`
Expected: **empty**. This PR adds only `internal/device/densitometer/**` and the plan doc. (If the core needed a change, this plan was wrong — stop and reconsider; the design deliberately requires none.)

- [ ] **Step 5: Push the branch and open the PR**

```bash
git push -u origin densitometer-driver
gh pr create --title "feat: add densitometer device driver" --body "$(cat <<'BODY'
## Summary

PR 3 of 5 in the v2 JSON device protocol effort (spec: docs/superpowers/specs/2026-07-05-json-device-protocol-design.md §7).

- `internal/device/densitometer`: the densitometer Driver implementing docs/protocol_translation_docs/densitometer/TRANSLATION.md — the device is a sensor/actuator only; all slope fitting, absorbance math, temperature compensation, tube correction, job tracking, monitoring, and buffering live in the driver. Sweep completion is an `s.After`-based callback chain on the session loop (liveness retry → 80-byte array read → slope/absorbance → job completion); a `busy_until` window fails serial-touching commands fast with `busy`. Reboot canary (thermostat readback == 10.00) drives job failure + mirror re-push from `Attach`, `status`, and an idle `Tick` poll. Serial-keyed persistence: blank slope, tube correction, thermostat mirror.
- **No core changes**: the driver is built entirely on the merged `internal/device` API.

## Flagged deviations from the JSON_PROTOCOL doc

1. `identify.serial` rendered `"25-006"` (`%d-%03d`, matching the doc example), not TRANSLATION's literal `"25-6"`.
2. `firmware_version` reported as the fleet-fixed `"legacy"` (a configured constant), consistent with the pump driver.
3. `set_thermostat` blocks the loop ~`ThermoSettle` (1.5 s, shrinkable) to verify the applied set-point — a bounded loop-block for a rare human-paced command, consistent with the accepted valve-`stop` deviation in spec §3.
4. `start_monitoring` returns `not_calibrated` without a blank (the scheduler's internal `measure` needs one).
5. Post-sweep liveness is retried up to 3× spaced 1 s (raw-port soft polls, then a decisive hard transaction) per TRANSLATION §4 step 6.

## Test plan

- [x] `go test -race -count=1 ./...` (macOS locally; Windows via CI)
- [x] `gofmt -l .`, `go vet ./...`, `golangci-lint run`, `govulncheck ./...`

Nothing consumes the new package yet; wiring happens in the v2 API cutover PR (PR 5).

🤖 Generated with [Claude Code](https://claude.com/claude-code)
BODY
)"
```

- [ ] **Step 6: Watch CI to green**

Run: `gh pr checks --watch`
Expected: all checks pass. If the release-please/verify job is missing checks, see CLAUDE.md ("close-and-reopen" workaround) — but do not touch release automation files.

---

## Self-review (run against the spec/contracts before executing)

**Spec/contract coverage — every JSON_PROTOCOL command has a task:**

| Command | Task |
|---|---|
| `ping` | 4 |
| `identify` | 2 (session-served from `info()`) |
| `status` | 4 (+ busy-cache assertion in 9) |
| `get_job` | core (session-served) — no driver work |
| `stop` | 7 |
| `measure_blank` | 5 |
| `measure` | 6 |
| `start_monitoring` / `stop_monitoring` / `get_readings` | 8 / 7 / 8 |
| `set_thermostat` | 3 |
| `set_tube_correction` / `calibrate_tube` | 6 |
| `set_led` | 7 |
| `read_raw` | 7 |
| Reboot canary (Attach / status / Tick) | 3 / 4 / 8 |
| Persistence (blank, tube, thermostat) | 2 (recover) / 3, 5, 6 (mutate) |
| busy_until fail-fast | 5 (+ gate defined in 2) |
| Unreachable/backoff | core (`Transact`) — driver relies on it, exercised via liveness final-attempt |

**Type consistency check:** `sweep.kind` values `"blank"|"measure"|"monitor"|"read_raw"` are produced by `runSweep` callers (Tasks 5–8) and consumed only in `readSweepAndFinish`'s switch (Tasks 5–7). `finishMeasure` handles both `"measure"` and `"monitor"`. `thermostatMirror.mirrorValue()` (Task 2) is used by `syncThermostat`/`applyThermostatReadback` (Task 3). `reading.tubeCorrectionAt` (Task 2) is written in `finishMeasure` (Task 6) and read in `calibrateTube` (Task 6). `ringBuffer` starts as a Task-2 stub, gains a no-op `push` in Task 6, and is fully replaced in Task 8 — the stub-then-replace is called out explicitly at each step.

**Concurrency invariants:** every driver method runs on the session goroutine; no mutexes. The only cross-goroutine hop is `s.After`'s timer, which `Post`s its callback back to the loop — so `onSweepDone`/`livenessAttempt`/canary all execute on the loop. At most one `After` is outstanding per sweep (liveness re-arm replaces, not accumulates), honoring decision 6's bounded-`Post` rule. `softPing` reads the raw port synchronously on the loop (not a watcher) and never trips unreachable, so the liveness retry window is honored without the core's fail-fast firing early.

**Placeholder scan:** the two intentional temporaries (`syncThermostat` stub in Task 2 → real in Task 3; `ringBuffer` stub in Task 2/6 → real in Task 8) are the only forward references, each with an explicit replacement step. No "TODO"/"handle errors"/"similar to" placeholders remain; every code step shows complete code.
