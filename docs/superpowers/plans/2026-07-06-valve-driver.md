# Distribution Valve Driver Implementation Plan (v2 PR 4 of 5)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `internal/device/valve` — the distribution-valve (radial flow switch) driver implementing `docs/protocol_translation_docs/distribution_valve/JSON_PROTOCOL.md` on the legacy 5-byte firmware per `TRANSLATION.md`, on top of the merged `internal/device` core.

**Architecture:** The firmware has no homing sensor and assumes position 0 at every boot, so homing is **virtual**: the driver tracks `physical_position` and `device_belief` separately and translates every target through the offset between them (all arithmetic mod S = N+1). `CHECK_BELIEF` — a position-counter consistency check before every move and on idle ticks — turns silent reboots into automatic recovery or an explicit unhomed state. The valve services serial during motion but mid-move replies reflect the *target*, not the rotor: there are **no watcher goroutines, no HoldReader** — all completion is clock-driven via `s.After` with a post-motion readback. `stop` is the documented spec deviation: the firmware cannot abort, so stop waits out the move (≤ ~6 s, blocking that session's loop, accepted per spec §3).

**Tech Stack:** Go stdlib only. Existing `internal/device` core (Session/Jobs/Store/FakeClock) and `internal/serial` fakes. Two small core additions ride along: `ErrNotHomed` constructor and `Session.Sleep` (PR-2 precedent: `WriteFrame`).

## Global Constraints

- Branch: `valve-driver` (already created off fresh `main`, commit `af9ed16`). PR title: `feat: add distribution valve device driver` — plain `feat:`; the word "BREAKING" must appear **nowhere** in any commit message or the PR body (reserved for PR 5).
- Module path: `github.com/bioexperiment-lab-devices/serialhop`.
- Pre-flight before the PR (CLAUDE.md): `gofmt -l .` prints nothing; `go vet ./...`; `golangci-lint run`; `go test -race -count=1 ./...`; `govulncheck ./...`.
- Tests: stdlib `testing` only, no testify. Must pass on macOS **and** Windows; no OS-specific code (FakePort/FakeClock only). Pure-math tests use `package valve`; fixture tests use `package valve_test` (pump precedent: `convert_test.go` vs the rest).
- Canonical behavior source: `docs/protocol_translation_docs/distribution_valve/{PROTOCOL,JSON_PROTOCOL,TRANSLATION}.md`; spec `docs/superpowers/specs/2026-07-05-json-device-protocol-design.md` §2.4/§3/§5. TRANSLATION.md is the algorithm — implement it exactly; carry its wording (esp. the transit-path gap) into doc comments, do not "fix" documented hardware limitations.
- Persistent state is **port-keyed** (`s.Store(s.PortName())` → `valve-COM7.json`): the firmware has no serial-number command. `identify` omits `serial`. Persist on every successful move, home, configure, and detach.
- Firmware config is RAM-only and write-only: re-push the mirror at attach and on every reboot detection. Hold-torque encoding is **inverted**: frame `35 2 0` = hold ON, `35 2 1` = hold OFF.
- Timing knobs are package `var`s so tests can shrink them: `SlotDuration` (0.92 s), `MoveMargin` (0.3 s), `CheckInterval` (30 s).
- Loop discipline: at most one `s.After` per move (bounded — never Post unboundedly from loop context; posts buffer is 64). No watcher goroutines. Mid-move replies must never be interpreted as "arrived".
- Test discipline: the valve has no watchers, so **pre-feeding** replies before `exec` is race-free and avoids `transact()`'s retry-window duplicate-frame trap (PR-2 lesson). Sync on observable state via `waitFor`; never bare sleeps for correctness.
- gosec: int→byte conversions carry `// #nosec G115 -- <bound proof>` comments (pump precedent). File permissions are core-owned (`Store`).
- Commit messages end with `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.

**Context note:** PR 3 (densitometer) is *not* merged as of writing; nothing here depends on it. The worked example is `internal/device/pump`.

## Flagged deviations (spec §8 discipline: small, documented)

1. **Registered hub type name is `"valve"`** (state files `valve-COM7.json` per spec §5's own example; future API ids `valve_1`), while the identify block reports `device_type: "distribution_valve"` per JSON_PROTOCOL.md.
2. **No-motion `set_position` job result omits `direction`** and reports `duration_s: 0` — the JSON doc's enum (`increasing`/`decreasing`) defines no value for a degenerate move.
3. **`capabilities.seconds_per_position` = 0.92** (TRANSLATION.md's measured constant), not the JSON doc's illustrative `0.9`.
4. `stop` is settle-and-report — already documented in TRANSLATION.md §4 and spec §8.4; JSON doc's MAY clause covers it.
5. Two core additions: `device.ErrNotHomed(msg)` (constructor for the existing `CodeNotHomed`) and `Session.Sleep(d)` (loop-blocking wait on the injectable clock, used only by valve `stop`).
6. **Post-review amendment:** `Detach` during an in-flight move persists the settled target **except** when the move targets device-frame 0 — a restart cannot distinguish "completed" (counter 0) from "valve power-cycled mid-move" (counter reset to 0), so Detach persists unhomed there (JSON_PROTOCOL.md §7's power-loss promise wins over convenience).

## File structure

| File | Responsibility |
|---|---|
| `internal/device/envelope.go` (modify) | add `ErrNotHomed` constructor |
| `internal/device/session.go` (modify) | add `Session.Sleep` |
| `internal/device/session_sleep_test.go` (new) | Sleep tests (reuses `session_test.go` fixture) |
| `internal/device/valve/valve.go` | package doc, consts, frames, `Driver`, `New`/`Register`, `Attach`, `info`, `Execute` dispatch, `Tick`, `Detach`, `pushConfig`, `persistNow`, persist schema |
| `internal/device/valve/translate.go` | timing vars, `rotationCode`, `mod`, `movePlan`, `planMove` (pure math) |
| `internal/device/valve/belief.go` | `applyBelief`, `checkBelief` (CHECK_BELIEF core) |
| `internal/device/valve/commands.go` | `ping`, `status`, `configure`, `stateName`, result structs |
| `internal/device/valve/move.go` | `home`, `setPosition`, `moveComplete`, `verifyMove`, `stop`, `moveJob`, `moveResult`, `elapsedOf` |
| Tests | `translate_test.go` (package valve), `valve_test.go` (fixture + attach), `belief_test.go`, `home_test.go`, `move_test.go`, `move_failure_test.go`, `stop_test.go`, `configure_test.go` (package valve_test) |

---

### Task 1: Core additions — `ErrNotHomed` and `Session.Sleep`

**Files:**
- Modify: `internal/device/envelope.go` (append constructor)
- Modify: `internal/device/session.go` (append method)
- Modify: `internal/device/envelope_test.go` (append test)
- Create: `internal/device/session_sleep_test.go`

**Interfaces:**
- Consumes: existing `CodeNotHomed`, `Session.cfg.Clock`, `s.loopCtx`, `s.done`.
- Produces: `func ErrNotHomed(msg string) *CmdError`; `func (s *Session) Sleep(d time.Duration)` — blocks the calling goroutine for `d` on the injectable clock; wakes early on session shutdown. Valve `stop` (Task 8) is its only intended caller.

- [ ] **Step 1: Write the failing tests**

Append to `internal/device/envelope_test.go` (package `device`):

```go
func TestErrNotHomed(t *testing.T) {
	e := ErrNotHomed("valve is unhomed")
	if e.Code != CodeNotHomed || e.Message != "valve is unhomed" {
		t.Fatalf("ErrNotHomed: %+v", e)
	}
}
```

Create `internal/device/session_sleep_test.go` (package `device_test` — reuses `newFixture`/`waitFor` from `session_test.go`):

```go
package device_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// TestSessionSleepWakesOnClockAdvance: Sleep must block on the injectable
// clock (not real time) and return once the clock passes the deadline.
func TestSessionSleepWakesOnClockAdvance(t *testing.T) {
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			drv.s.Sleep(5 * time.Second)
			return "woke", nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	results := make(chan device.Response, 1)
	go func() {
		results <- f.s.Execute(context.Background(), device.Request{ID: "r", Cmd: "nap"})
	}()
	var resp device.Response
	waitFor(t, "sleep wakes", func() bool {
		f.clock.Advance(time.Second)
		select {
		case resp = <-results:
			return true
		default:
			return false
		}
	})
	if resp.Status != "ok" || resp.Result != "woke" {
		t.Fatalf("resp: %+v", resp)
	}
}

// TestSessionSleepInterruptedByClose: a Close during a Sleep must not hang
// until the (hour-long) deadline — shutdown wakes the sleeper.
func TestSessionSleepInterruptedByClose(t *testing.T) {
	entered := make(chan struct{})
	f := newFixture(t, func(cfg *device.SessionConfig, drv *stubDriver) {
		drv.exec = func(cmd string, params json.RawMessage) (any, *device.CmdError) {
			close(entered)
			drv.s.Sleep(time.Hour)
			return "woke", nil
		}
	})
	waitFor(t, "attach", f.s.Connected)
	go f.s.Execute(context.Background(), device.Request{ID: "r", Cmd: "nap"})
	<-entered
	f.s.Close() // hangs (test timeout) if Sleep ignores shutdown
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/ -run 'TestErrNotHomed|TestSessionSleep' -v`
Expected: FAIL — package does not compile: `undefined: ErrNotHomed`, `s.Sleep undefined`.

- [ ] **Step 3: Implement**

Append to `internal/device/envelope.go`:

```go
func ErrNotHomed(msg string) *CmdError {
	return &CmdError{Code: CodeNotHomed, Message: msg}
}
```

Append to `internal/device/session.go` (after `WriteFrame`):

```go
// Sleep blocks the calling goroutine for d via the injectable clock.
// Calling it ON the session goroutine deliberately stalls the loop — that
// is reserved for the valve's documented stop deviation (spec §3: the
// firmware cannot abort a move, so stop waits it out, ≤ ~6 s). Session
// shutdown wakes the sleeper early so Close never waits the full duration.
func (s *Session) Sleep(d time.Duration) {
	var ctxDone <-chan struct{}
	if s.loopCtx != nil {
		ctxDone = s.loopCtx.Done()
	}
	select {
	case <-s.cfg.Clock.After(d):
	case <-ctxDone:
	case <-s.done:
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/ -count=1`
Expected: PASS (all existing core tests still green).

- [ ] **Step 5: Commit**

```bash
git add internal/device/envelope.go internal/device/envelope_test.go internal/device/session.go internal/device/session_sleep_test.go
git commit -m "feat: add ErrNotHomed and Session.Sleep to device core

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Valve translation math (virtual-homing offset)

**Files:**
- Create: `internal/device/valve/translate.go`
- Test: `internal/device/valve/translate_test.go` (package `valve` — pure math, no fixture)

**Interfaces:**
- Consumes: nothing (stdlib `time` only).
- Produces: `var SlotDuration, MoveMargin time.Duration`; `func rotationCode(mode string) (byte, bool)`; `func mod(x, size int) int`; `type movePlan struct{ targetDevice, slots int; direction string; estimate time.Duration }`; `func planMove(target, physical, belief, size int, mode string) movePlan`. Task 6 calls `planMove`; Tasks 3/9 call `rotationCode`.

- [ ] **Step 1: Write the failing tests**

Create `internal/device/valve/translate_test.go`:

```go
package valve

import (
	"testing"
	"time"
)

func TestMod(t *testing.T) {
	cases := []struct{ x, size, want int }{
		{3, 7, 3}, {-3, 7, 4}, {7, 7, 0}, {-7, 7, 0}, {13, 7, 6}, {0, 7, 0},
	}
	for _, c := range cases {
		if got := mod(c.x, c.size); got != c.want {
			t.Errorf("mod(%d,%d) = %d, want %d", c.x, c.size, got, c.want)
		}
	}
}

func TestRotationCode(t *testing.T) {
	for mode, want := range map[string]byte{"direct": 1, "wrap": 2, "shortest": 3} {
		code, ok := rotationCode(mode)
		if !ok || code != want {
			t.Errorf("rotationCode(%q) = %d %v", mode, code, ok)
		}
	}
	for _, bad := range []string{"", "spiral", "Shortest"} {
		if _, ok := rotationCode(bad); ok {
			t.Errorf("rotationCode(%q) must be rejected", bad)
		}
	}
}

// All cases use S = 7 (the 6-output build: rotor detents 0..6).
// delta = (target − physical) mod S; targetDevice = (belief + delta) mod S;
// d = targetDevice − belief (signed). Slots/direction mirror the firmware:
// direct |d| (increasing iff d > 0), wrap S−|d| (increasing iff d < 0),
// shortest picks the smaller arc.
func TestPlanMove(t *testing.T) {
	cases := []struct {
		name                     string
		target, physical, belief int
		mode                     string
		wantDevice, wantSlots    int
		wantDir                  string
	}{
		// zero offset (belief == physical)
		{"direct zero-offset", 4, 2, 2, "direct", 4, 2, "increasing"},
		{"wrap zero-offset", 4, 2, 2, "wrap", 4, 5, "decreasing"},
		{"shortest near arc", 4, 2, 2, "shortest", 4, 2, "increasing"},
		// nonzero offset: physical 4 sits at device 0 → delta 4, d = 4,
		// shortest takes the complementary arc (3 slots, decreasing)
		{"shortest far arc", 1, 4, 0, "shortest", 4, 3, "decreasing"},
		// transit-path gap illustration: physical 5→0 crosses the 6↔0
		// boundary, but the device frame moves 1→3 without crossing it
		{"direct offset boundary", 0, 5, 1, "direct", 3, 2, "increasing"},
		// negative device-frame difference: delta 5, td 3, d = −2
		{"direct negative d", 5, 0, 5, "direct", 3, 2, "decreasing"},
		{"wrap negative d", 5, 0, 5, "wrap", 3, 5, "increasing"},
		{"shortest negative d", 5, 0, 5, "shortest", 3, 2, "decreasing"},
		// the device target itself wraps mod S: delta 6, td (6+6) mod 7 = 5
		{"target wraps mod S", 6, 0, 6, "shortest", 5, 1, "decreasing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planMove(c.target, c.physical, c.belief, 7, c.mode)
			if got.targetDevice != c.wantDevice || got.slots != c.wantSlots ||
				got.direction != c.wantDir {
				t.Fatalf("planMove = %+v", got)
			}
			want := time.Duration(c.wantSlots)*SlotDuration + MoveMargin
			if got.estimate != want {
				t.Fatalf("estimate = %v, want %v", got.estimate, want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/valve/ -v`
Expected: FAIL — package does not compile (`undefined: mod`, `rotationCode`, `planMove`).

- [ ] **Step 3: Implement**

Create `internal/device/valve/translate.go`:

```go
package valve

import "time"

// Timing knobs are vars so tests can shrink them (core precedent:
// PerByteTimeout / DrainWindow).
var (
	// SlotDuration is the rotor travel time per adjacent position: 460
	// step-pin toggles × 2000 µs (PROTOCOL.md §4 cmd 36; TRANSLATION.md §1
	// SECONDS_PER_SLOT = 0.92 s).
	SlotDuration = 920 * time.Millisecond
	// MoveMargin pads the clock-simulated completion estimate
	// (TRANSLATION.md §4 set_position step 8).
	MoveMargin = 300 * time.Millisecond
)

// rotationCode maps a JSON rotation mode to the firmware's 35-1-R code
// (PROTOCOL.md §4 cmd 35): direct=1, wrap=2, shortest=3.
func rotationCode(mode string) (byte, bool) {
	switch mode {
	case "direct":
		return 1, true
	case "wrap":
		return 2, true
	case "shortest":
		return 3, true
	}
	return 0, false
}

// mod returns x modulo size, always in [0, size).
func mod(x, size int) int { return ((x % size) + size) % size }

// movePlan is the resolved device-frame plan for one move.
type movePlan struct {
	targetDevice int
	slots        int
	direction    string // "increasing" | "decreasing"
	estimate     time.Duration
}

// planMove implements TRANSLATION.md §4 set_position steps 5–6: translate
// the physical target through the virtual-homing offset and mirror the
// firmware's arc arithmetic for the duration estimate.
//
// Correctness: every firmware mode moves the rotor by a step count
// CONGRUENT to (targetDevice − belief) mod size, so the final position is
// always right.
//
// Transit-path gap (direct/wrap modes only, documented hardware
// limitation): every port the rotor transits is momentarily opened, so the
// *path* can matter to the plumbing, not just the destination. Direct and
// wrap choose their arc from the SIGNED device-frame difference; with a
// nonzero virtual-homing offset that arc can differ from what the physical
// position numbers suggest (e.g. a physical 2→4 move may travel the long
// way around through 0). The offset never changes on its own — it is fixed
// at home time and only a device reboot disturbs it. Mitigation for
// path-sensitive installations: establish a ZERO offset — bring the rotor
// physically to position 0, power-cycle the valve (device belief resets to
// 0), then home {position: 0}. Shortest mode is frame-invariant.
func planMove(target, physical, belief, size int, mode string) movePlan {
	delta := mod(target-physical, size)
	targetDevice := mod(belief+delta, size)
	d := targetDevice - belief // signed, in −(size−1)..(size−1); never 0 (Δ=0 is guarded upstream)
	abs := d
	if abs < 0 {
		abs = -abs
	}
	var slots int
	var increasing bool
	switch mode {
	case "direct":
		slots, increasing = abs, d > 0
	case "wrap":
		slots, increasing = size-abs, d < 0
	default: // shortest — the firmware's default. On an equal-arc tie the
		// firmware's pick is unspecified; mirror its direct arc. The
		// duration is identical either way, only the reported direction
		// could differ.
		if abs <= size-abs {
			slots, increasing = abs, d > 0
		} else {
			slots, increasing = size-abs, d < 0
		}
	}
	dir := "decreasing"
	if increasing {
		dir = "increasing"
	}
	return movePlan{
		targetDevice: targetDevice,
		slots:        slots,
		direction:    dir,
		estimate:     time.Duration(slots)*SlotDuration + MoveMargin,
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/device/valve/ -v`
Expected: PASS (TestMod, TestRotationCode, TestPlanMove ×9 subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/device/valve/
git commit -m "feat: add valve translation math (virtual-homing offset)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Driver skeleton — Attach, identify, Detach, fixture

**Files:**
- Create: `internal/device/valve/valve.go`
- Test: `internal/device/valve/valve_test.go` (package `valve_test`)

**Interfaces:**
- Consumes: `device.Session` services (`Transact`, `Store`, `PortName`, `Now`), `rotationCode` (Task 2).
- Produces: `const TypeCode = 30`; `func New(s *device.Session) device.Driver`; `func Register()`; types `configBlock{DefaultRotation string; HoldTorque bool}` (json `default_rotation`/`hold_torque`), `persistState{SchemaVersion int; PhysicalPosition *int; DeviceBeliefAtShutdown int; Config configBlock}` (json `schema_version`/`physical_position`/`device_belief_at_shutdown`/`config`), `moveJob{id string; fromPhysical, targetPhysical, targetDevice int; direction string; estimate time.Duration}`; `Driver` fields as below; methods `Attach`, `Execute` (dispatch skeleton), `Tick` (stub), `Detach`, `pushConfig() *device.CmdError`, `persistNow() error`, `info() device.Info`; frames `queryPosFrame`, `rotationFrame(code byte)`, `holdFrame(on bool)`; `var CheckInterval`; `const replyTimeout`. Later tasks add `Execute` cases, the `Tick` body, `pingFrame` (Task 4), `moveFrame`/`slots()` (Task 6) — deferred so the `unused` linter stays quiet at each commit.

- [ ] **Step 1: Write the failing tests**

Create `internal/device/valve/valve_test.go`:

```go
package valve_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/valve"
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
	return func(cfg *device.SessionConfig) { cfg.ProbeReply = r }
}

func shrinkTimeouts(t *testing.T) {
	t.Helper()
	oldPB, oldDW := device.PerByteTimeout, device.DrainWindow
	device.PerByteTimeout, device.DrainWindow = 10*time.Millisecond, 0
	t.Cleanup(func() { device.PerByteTimeout, device.DrainWindow = oldPB, oldDW })
}

// newFixture boots a real Session hosting the valve driver. Attach consumes
// one position-query transaction; devicePos is the position counter the
// device reports there (DrainWindow is 0, so the pre-fed reply survives the
// transaction's drain step). The valve driver has no watcher goroutines, so
// pre-feeding replies is race-free.
func newFixture(t *testing.T, devicePos byte, opts ...fixtureOpt) *fixture {
	t.Helper()
	shrinkTimeouts(t)
	clock := device.NewFakeClock(time.Unix(1000, 0))
	port := serial.NewFakePort("COM9")
	opener := serial.NewFakeOpener()
	opener.Add(port)
	conn, err := opener.Open("COM9")
	if err != nil {
		t.Fatal(err)
	}
	cfg := device.SessionConfig{
		ID: "valve_1", Type: "valve", TypeCode: valve.TypeCode, PortName: "COM9",
		Conn: conn, Opener: opener, Clock: clock, StateDir: t.TempDir(),
		Factory:    valve.New,
		ProbeReply: []byte{30, 1, 1, 6}, // radial-6 build
		Reprobe:    func(p serial.Port) ([]byte, error) { return []byte{30, 1, 1, 6}, nil },
	}
	for _, o := range opts {
		o(&cfg)
	}
	port.Feed([]byte{30, 1, 1, devicePos}) // Attach's position-query reply
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

// readState decodes the port-keyed persistent state file.
func readState(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "valve-COM9.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAttachQueriesPositionAndPushesConfig(t *testing.T) {
	f := newFixture(t, 0)
	fr := f.frames()
	// TRANSLATION §3: position query, then the RAM-only config mirror —
	// default shortest (code 3) and hold OFF (N3=1: inverted encoding)
	if len(fr) != 3 || !frameEq(fr[0], 33, 1, 0, 0, 0) ||
		!frameEq(fr[1], 35, 1, 3, 0, 0) || !frameEq(fr[2], 35, 2, 1, 0, 0) {
		t.Fatalf("attach frames: %v", fr)
	}
}

func TestIdentifyOmitsSerialAndDerivesCapabilities(t *testing.T) {
	f := newFixture(t, 0)
	resp := f.exec("identify", "")
	if resp.Status != "ok" {
		t.Fatalf("identify: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["device_type"] != "distribution_valve" || m["model"] != "radial-6" ||
		m["firmware_version"] != "legacy" || m["protocol_version"] != "1.0" {
		t.Fatalf("identify result: %v", m)
	}
	if _, ok := m["serial"]; ok {
		t.Fatalf("valve identify must omit serial (no serial command): %v", m)
	}
	caps := m["capabilities"].(map[string]any)
	if caps["positions"] != 6.0 || caps["seconds_per_position"] != 0.92 {
		t.Fatalf("capabilities: %v", caps)
	}
	modes := caps["rotation_modes"].([]any)
	if len(modes) != 3 || modes[0] != "shortest" || modes[1] != "direct" || modes[2] != "wrap" {
		t.Fatalf("rotation_modes: %v", caps)
	}
}

// TestAttachTwoPositionBuild: the position count comes from probeReply[3],
// not a constant — a 2-output build reports positions 2, model radial-2.
func TestAttachTwoPositionBuild(t *testing.T) {
	f := newFixture(t, 0, withProbeReply([]byte{30, 1, 1, 2}))
	m := f.resultMap(f.exec("identify", ""))
	caps := m["capabilities"].(map[string]any)
	if m["model"] != "radial-2" || caps["positions"] != 2.0 {
		t.Fatalf("2-position build: %v", m)
	}
}

func TestUnknownCommand(t *testing.T) {
	f := newFixture(t, 0)
	resp := f.exec("frobnicate", "")
	if resp.Status != "error" || resp.Error.Code != device.CodeUnknownCommand {
		t.Fatalf("resp: %+v", resp)
	}
}

func TestRegister(t *testing.T) {
	valve.Register()
	name, factory, ok := device.LookupDriver(valve.TypeCode)
	if !ok || name != "valve" || factory == nil {
		t.Fatalf("LookupDriver(30) = %q %v %v", name, factory, ok)
	}
}

// TestDetachPersistsIdleState: Detach persists {physical_position,
// device_belief_at_shutdown, config} — and needs NO serial I/O.
func TestDetachPersistsIdleState(t *testing.T) {
	f := newFixture(t, 5)
	n := len(f.port.Written())
	f.s.Close()
	m := readState(t, f.dir)
	if m["schema_version"] != 1.0 || m["physical_position"] != nil ||
		m["device_belief_at_shutdown"] != 5.0 {
		t.Fatalf("persisted state: %v", m)
	}
	cfg := m["config"].(map[string]any)
	if cfg["default_rotation"] != "shortest" || cfg["hold_torque"] != false {
		t.Fatalf("persisted config: %v", cfg)
	}
	if len(f.port.Written()) != n {
		t.Fatal("Detach must not write to the serial port")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/valve/ -v`
Expected: FAIL — `undefined: valve.New`, `valve.TypeCode`, `valve.Register`.

- [ ] **Step 3: Implement**

Create `internal/device/valve/valve.go`:

```go
// Package valve implements the distribution-valve (radial flow switch)
// driver for the JSON device protocol, translating
// docs/protocol_translation_docs/distribution_valve/JSON_PROTOCOL.md onto
// the unmodified legacy 5-byte firmware per TRANSLATION.md.
//
// The firmware has no homing sensor and blindly assumes position 0 at every
// boot, so homing is VIRTUAL: the driver tracks the rotor's true physical
// position and the device's belief (its internal position counter)
// separately and translates every target through the offset between them;
// all position arithmetic is modulo S = N+1 rotor slots. CHECK_BELIEF — a
// position-counter consistency check run before every move and on idle
// ticks — turns silent device reboots into either automatic recovery or an
// explicit unhomed state.
//
// The firmware keeps servicing serial while the motor runs, but replies
// sent mid-move reflect the TARGET (the counter is bumped the instant a
// move command is parsed), not the rotor — so the driver never interprets
// them as "arrived". There are no watcher goroutines: motion completion is
// purely clock-driven (s.After) with a post-motion readback that verifies
// the device processed the command. A stalled motor is undetectable (no
// encoder) — inherent hardware gap.
package valve

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// TypeCode is the valve's probe identify code (PROTOCOL.md §3).
const TypeCode = 30

const (
	typeName     = "valve"              // hub type name: registry, state files, API ids
	deviceType   = "distribution_valve" // JSON identify device_type
	firmwareVer  = "legacy"
	protocolVer  = "1.0"
	schemaV      = 1
	replyTimeout = 2 * time.Second // 4-byte replies arrive within ~80 ms
)

// CheckInterval is the idle CHECK_BELIEF cadence (TRANSLATION.md §5): how
// often Tick verifies the device's position counter while no move runs, so
// silent reboots are detected promptly rather than at the next move.
var CheckInterval = 30 * time.Second

// Command frames (PROTOCOL.md §4).
var queryPosFrame = []byte{33, 1, 0, 0, 0} // read the position counter

// rotationFrame configures the rotation method (35 1 R): direct=1, wrap=2,
// shortest=3.
func rotationFrame(code byte) []byte { return []byte{35, 1, code, 0, 0} }

// holdFrame configures hold torque (35 2 H). The firmware encoding is
// INVERTED: H=0 keeps the stepper energized after a move (hold ON), H=1
// releases it (hold OFF).
func holdFrame(on bool) []byte {
	h := byte(1)
	if on {
		h = 0
	}
	return []byte{35, 2, h, 0, 0}
}

// Register binds the valve driver into the device registry under the hub
// type name "valve" (state files valve-<port>.json per spec §5, future API
// ids valve_N); the JSON identify block reports device_type
// "distribution_valve" per JSON_PROTOCOL.md. Called at app wiring time
// (PR 5); nothing calls it in this PR.
func Register() { device.Register(TypeCode, typeName, New) }

// New is the device.Factory for distribution valves.
func New(s *device.Session) device.Driver { return &Driver{s: s} }

// configBlock is the JSON config mirror (status/configure payloads). The
// firmware's config is RAM-only and write-only: this mirror is
// authoritative by construction — it is re-pushed at attach and on every
// reboot detection (TRANSLATION.md §4 configure).
type configBlock struct {
	DefaultRotation string `json:"default_rotation"`
	HoldTorque      bool   `json:"hold_torque"`
}

// persistState is the port-keyed on-disk schema (spec §5): the valve has no
// serial-number command, so state is keyed by the COM port. Persisted on
// every successful move so a SerialHop restart can recover homed state
// (TRANSLATION.md §3 step 3).
type persistState struct {
	SchemaVersion          int         `json:"schema_version"`
	PhysicalPosition       *int        `json:"physical_position"` // null while unhomed
	DeviceBeliefAtShutdown int         `json:"device_belief_at_shutdown"`
	Config                 configBlock `json:"config"`
}

// moveJob carries the driver-side details of the active move (the Jobs
// engine owns lifecycle/progress).
type moveJob struct {
	id             string
	fromPhysical   int
	targetPhysical int
	targetDevice   int
	direction      string
	estimate       time.Duration
}

// Driver implements device.Driver for the distribution valve. All fields
// are loop-owned: every method runs on the session goroutine (spec §3).
type Driver struct {
	s *device.Session

	positions int // N: outputs 1..N; the rotor has N+1 detents (0 = all closed)
	store     *device.Store

	homed        bool
	physicalPos  int // last verified true rotor position; valid only while homed
	deviceBelief int // the firmware's position counter (boot = 0 + every commanded move)

	config     configBlock
	lastPushed string // rotation mode most recently pushed to the firmware

	connectedSince time.Time
	lastCheck      time.Time // last CHECK_BELIEF, for Tick's idle cadence

	jobGen    int // bumps on job start/end/attach; guards stale After callbacks
	moveJob   *moveJob
	lastJobID string // most recent job (for status embedding)
}

// Attach implements TRANSLATION.md §3: derive the position count from the
// probe reply, read the device's position counter, recover port-keyed
// persistent state, and push the RAM-only config mirror (the firmware
// forgets it on every reboot).
func (d *Driver) Attach(ctx context.Context, probeReply []byte) (device.Info, error) {
	if len(probeReply) != 4 || probeReply[0] != TypeCode || probeReply[3] == 0 {
		return device.Info{}, fmt.Errorf("valve: unexpected probe reply %v", probeReply)
	}
	d.positions = int(probeReply[3])

	reply, err := d.s.Transact(queryPosFrame, 4, replyTimeout)
	if err != nil {
		return device.Info{}, fmt.Errorf("valve: position query: %w", err)
	}
	if reply[0] != TypeCode || int(reply[3]) > d.positions {
		return device.Info{}, fmt.Errorf("valve: unexpected position reply %v", reply)
	}
	d.deviceBelief = int(reply[3])

	d.store = d.s.Store(d.s.PortName())
	d.homed = false
	d.physicalPos = 0
	d.config = configBlock{DefaultRotation: "shortest", HoldTorque: false}
	var ps persistState
	found, err := d.store.Load(&ps)
	if err != nil {
		slog.Warn("valve: state file unreadable, treating as absent",
			"port", d.s.PortName(), "err", err)
		found = false
	}
	if found && ps.SchemaVersion == schemaV {
		if _, ok := rotationCode(ps.Config.DefaultRotation); ok {
			d.config = ps.Config
		}
		// TRANSLATION §3 step 3: recover homed state only when the device's
		// counter still matches the belief we persisted — proof the firmware
		// kept its counter (no reboot, no foreign host) while we were away.
		if ps.PhysicalPosition != nil && *ps.PhysicalPosition >= 0 &&
			*ps.PhysicalPosition <= d.positions &&
			d.deviceBelief == ps.DeviceBeliefAtShutdown {
			d.homed = true
			d.physicalPos = *ps.PhysicalPosition
		}
	}

	if cerr := d.pushConfig(); cerr != nil {
		return device.Info{}, fmt.Errorf("valve: config push: %s", cerr.Message)
	}

	// Volatile reset — also the recovery path after an unreachable episode.
	d.connectedSince = d.s.Now()
	d.lastCheck = d.connectedSince
	d.moveJob = nil
	d.jobGen++
	return d.info(), nil
}

type capabilities struct {
	Positions          int      `json:"positions"`
	RotationModes      []string `json:"rotation_modes"`
	SecondsPerPosition float64  `json:"seconds_per_position"`
}

func (d *Driver) info() device.Info {
	// Serial stays empty: the firmware has no serial-number command, so the
	// identify block omits it (spec §2.4, §9).
	return device.Info{
		DeviceType:      deviceType,
		Model:           fmt.Sprintf("radial-%d", d.positions),
		FirmwareVersion: firmwareVer,
		ProtocolVersion: protocolVer,
		Capabilities: capabilities{
			Positions:          d.positions,
			RotationModes:      []string{"shortest", "direct", "wrap"},
			SecondsPerPosition: SlotDuration.Seconds(),
		},
	}
}

// pushConfig sends the config mirror to the firmware (TRANSLATION.md §3
// step 4). The firmware's config is RAM-only: this runs at attach and after
// every reboot detection. Write-only — there is no config readback.
func (d *Driver) pushConfig() *device.CmdError {
	code, _ := rotationCode(d.config.DefaultRotation) // validated at every entry point
	if _, err := d.s.Transact(rotationFrame(code), 0, time.Second); err != nil {
		return device.ErrHardware("rotation config frame: " + err.Error())
	}
	d.lastPushed = d.config.DefaultRotation
	if _, err := d.s.Transact(holdFrame(d.config.HoldTorque), 0, time.Second); err != nil {
		return device.ErrHardware("hold config frame: " + err.Error())
	}
	return nil
}

// persistNow snapshots the persistent fields (TRANSLATION.md §1). Written
// on every successful move, home, configure, and detach — rare, human-paced
// events (spec §5).
func (d *Driver) persistNow() error {
	ps := persistState{
		SchemaVersion:          schemaV,
		DeviceBeliefAtShutdown: d.deviceBelief,
		Config:                 d.config,
	}
	if d.homed {
		pos := d.physicalPos // copy: never persist a pointer into live driver state
		ps.PhysicalPosition = &pos
	}
	return d.store.Save(ps)
}

// Execute dispatches one JSON command (identify/get_job are session-served).
func (d *Driver) Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *device.CmdError) {
	switch cmd {
	default:
		return nil, device.ErrUnknownCommand(cmd)
	}
}

// Tick runs the idle CHECK_BELIEF; body lands with the belief logic.
func (d *Driver) Tick(now time.Time) {}

// Detach persists the final position knowledge — deliberately NO serial
// I/O: the firmware needs no goodbye and the port may already be dead.
func (d *Driver) Detach() {
	if d.store == nil {
		return // attach never got far enough to bind the store
	}
	if d.moveJob != nil {
		// An in-flight move finishes autonomously after we disconnect: the
		// frame was already accepted (its Transact succeeded), so the rotor
		// settles at the target. Persist that outcome; if the valve instead
		// loses power mid-move, the belief check refuses recovery on the
		// next attach — fail-safe either way.
		d.physicalPos = d.moveJob.targetPhysical
		d.deviceBelief = d.moveJob.targetDevice
	}
	if err := d.persistNow(); err != nil {
		slog.Warn("valve: detach persist failed", "port", d.s.PortName(), "err", err)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/valve/ -count=1 -v`
Expected: PASS (all Task 2 + Task 3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/device/valve/
git commit -m "feat: add valve driver skeleton with attach recovery

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: CHECK_BELIEF, ping, status, idle Tick

**Files:**
- Create: `internal/device/valve/belief.go`
- Create: `internal/device/valve/commands.go`
- Modify: `internal/device/valve/valve.go` (Execute cases `ping`/`status`, Tick body)
- Test: `internal/device/valve/belief_test.go`

**Interfaces:**
- Consumes: `pushConfig`, `queryPosFrame`, `pingFrame`, `Driver` fields (Task 3).
- Produces: `applyBelief(pos int)`, `checkBelief() *device.CmdError` (nil on consistent/recovered; CmdError only for serial failures — mismatches are absorbed into state), `ping()`, `status()`, `stateName() string`, `statusJob() *device.Job`, `pingResult{UptimeMs int64}` (json `uptime_ms`), `statusResult{State string; Homed bool; Position, TargetPosition *int; Job *device.Job; Config configBlock}` (json `state`/`homed`/`position`/`target_position`/`job`/`config`). Task 6's `setPosition` calls `checkBelief`; Task 8's `stop` calls `stateName`.

- [ ] **Step 1: Write the failing tests**

Create `internal/device/valve/belief_test.go`:

```go
package valve_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device/valve"
)

// countFrames counts written 5-byte frames with the given command byte.
func countFrames(fr [][]byte, cmd byte) int {
	n := 0
	for _, f := range fr {
		if len(f) == 5 && f[0] == cmd {
			n++
		}
	}
	return n
}

func TestStatusUnhomedFresh(t *testing.T) {
	f := newFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0}) // idle status runs CHECK_BELIEF
	m := f.resultMap(f.exec("status", ""))
	if m["state"] != "unhomed" || m["homed"] != false || m["position"] != nil ||
		m["target_position"] != nil || m["job"] != nil {
		t.Fatalf("status: %v", m)
	}
	cfg := m["config"].(map[string]any)
	if cfg["default_rotation"] != "shortest" || cfg["hold_torque"] != false {
		t.Fatalf("config: %v", cfg)
	}
	fr := f.frames()
	if !frameEq(fr[len(fr)-1], 33, 1, 0, 0, 0) {
		t.Fatalf("idle status must run CHECK_BELIEF: %v", fr)
	}
}

// TestBeliefRebootAutoRecovery: pos==0 while belief≠0 and no move in flight
// is the reboot signature — belief resets to 0 and the RAM-only config is
// re-pushed; no alarm (TRANSLATION §2 step 3, recovery branch).
func TestBeliefRebootAutoRecovery(t *testing.T) {
	f := newFixture(t, 3)              // attach: belief = 3
	f.port.Feed([]byte{30, 1, 1, 0})   // reboot: counter reset to 0
	if resp := f.exec("status", ""); resp.Status != "ok" {
		t.Fatalf("status: %+v", resp)
	}
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 35, 1, 3, 0, 0) || !frameEq(fr[n-1], 35, 2, 1, 0, 0) {
		t.Fatalf("reboot recovery must re-push the config mirror: %v", fr)
	}
	// belief resynced to 0: a second consistent read stays quiet
	f.port.Feed([]byte{30, 1, 1, 0})
	f.exec("status", "")
	if got := countFrames(f.frames(), 35); got != 4 { // 2 at attach + 2 at recovery
		t.Fatalf("no further config pushes expected, got %d", got)
	}
}

// TestBeliefForeignMismatchAlarms: pos ≠ 0 and ≠ belief means a lost
// command or a foreign host — no config re-push, belief resyncs to reality
// (TRANSLATION §2 step 4).
func TestBeliefForeignMismatchAlarms(t *testing.T) {
	f := newFixture(t, 3)
	f.port.Feed([]byte{30, 1, 1, 5})
	f.exec("status", "")
	if got := countFrames(f.frames(), 35); got != 2 { // attach only
		t.Fatalf("mismatch must not re-push config, got %d frames", got)
	}
	f.port.Feed([]byte{30, 1, 1, 5}) // belief resynced: now consistent
	f.exec("status", "")
	if got := countFrames(f.frames(), 35); got != 2 {
		t.Fatalf("resynced belief must stay quiet, got %d frames", got)
	}
}

func TestPingReportsUptime(t *testing.T) {
	f := newFixture(t, 0)
	f.clock.Advance(5 * time.Second) // one heartbeat fires; CheckInterval (30 s) not due
	f.port.Feed([]byte{30, 1, 1, 0})
	resp := f.exec("ping", "")
	if resp.Status != "ok" {
		t.Fatalf("ping: %+v", resp)
	}
	if m := f.resultMap(resp); m["uptime_ms"] != 5000.0 {
		t.Fatalf("uptime: %v", m)
	}
	fr := f.frames()
	if !frameEq(fr[len(fr)-1], 31, 2, 3, 4, 5) {
		t.Fatalf("ping frame: %v", fr)
	}
}

// TestPingFeedsBelief: ping's reply position is fed into the CHECK_BELIEF
// logic opportunistically (TRANSLATION §4 ping).
func TestPingFeedsBelief(t *testing.T) {
	f := newFixture(t, 3)
	f.port.Feed([]byte{30, 1, 1, 0}) // reboot signature via a ping reply
	if resp := f.exec("ping", ""); resp.Status != "ok" {
		t.Fatalf("ping: %+v", resp)
	}
	if got := countFrames(f.frames(), 35); got != 4 { // attach + recovery re-push
		t.Fatalf("ping must trigger reboot recovery, got %d config frames", got)
	}
}

// TestTickRunsIdleCheckBelief: Tick runs CHECK_BELIEF only once
// CheckInterval has elapsed. Early ticks must stay serial-silent — a
// premature CHECK_BELIEF would find an empty RX buffer, double-fail, and
// flip the session unreachable, which the final asserts catch loudly.
func TestTickRunsIdleCheckBelief(t *testing.T) {
	old := valve.CheckInterval
	valve.CheckInterval = 3 * time.Second
	t.Cleanup(func() { valve.CheckInterval = old })
	f := newFixture(t, 0)
	f.clock.Advance(time.Second) // tick at +1 s: below interval
	time.Sleep(10 * time.Millisecond)
	f.clock.Advance(time.Second) // tick at +2 s: below interval
	time.Sleep(10 * time.Millisecond)
	f.port.Feed([]byte{30, 1, 1, 0})
	f.clock.Advance(time.Second) // tick at ≥ +3 s: CHECK_BELIEF due
	waitFor(t, "idle CHECK_BELIEF", func() bool {
		return countFrames(f.frames(), 33) >= 2 // attach's + the tick's
	})
	if got := countFrames(f.frames(), 33); got != 2 {
		t.Fatalf("expected exactly one idle check, got %d queries", got)
	}
	if !f.s.Connected() {
		t.Fatal("session must stay connected (no premature CHECK_BELIEF)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/valve/ -run 'TestStatus|TestBelief|TestPing|TestTick' -v`
Expected: FAIL — `status`/`ping` return `unknown_command` (dispatch has no cases yet).

- [ ] **Step 3: Implement**

Create `internal/device/valve/belief.go`:

```go
package valve

import (
	"log/slog"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// applyBelief reconciles a freshly read position counter with the tracked
// belief — TRANSLATION.md §2 CHECK_BELIEF steps 2–4. Callers guarantee no
// move is in flight.
//
// pos==0 with a nonzero belief is the reboot signature: the firmware
// assumed position 0 at power-up and lost its RAM-only config. No move was
// interrupted, so the rotor did not actually turn — belief resets to 0, the
// config is re-pushed, and the virtual-homing offset math absorbs the reset
// (homed and physical_position stand). Any other mismatch means a lost
// command or a foreign host on the port: position knowledge is void →
// unhomed + alarm log.
func (d *Driver) applyBelief(pos int) {
	d.lastCheck = d.s.Now()
	switch {
	case pos == d.deviceBelief:
		// consistent
	case pos == 0 && d.deviceBelief != 0:
		slog.Warn("valve: device reboot detected while idle — auto-recovering",
			"port", d.s.PortName(), "belief", d.deviceBelief)
		d.deviceBelief = 0
		_ = d.pushConfig() // a failure here trips the session's unreachable handling
	default:
		slog.Error("valve: position counter mismatch — valve is now unhomed",
			"port", d.s.PortName(), "reported", pos, "belief", d.deviceBelief)
		d.deviceBelief = pos
		d.homed = false
	}
}

// checkBelief runs the consistency check (TRANSLATION.md §2): query the
// position counter and reconcile. Returns a CmdError only for serial
// failures; belief mismatches are absorbed into driver state (callers that
// need homed must re-check it afterwards).
func (d *Driver) checkBelief() *device.CmdError {
	reply, err := d.s.Transact(queryPosFrame, 4, replyTimeout)
	if err != nil {
		return device.ErrHardware("position query: " + err.Error())
	}
	if reply[0] != TypeCode {
		return device.ErrHardware("position query: unexpected reply")
	}
	d.applyBelief(int(reply[3]))
	return nil
}
```

In `internal/device/valve/valve.go`, extend the frame block:

```go
// pingFrame is the side-effect-free liveness probe (31): the reply's last
// byte is the position counter, fed opportunistically into CHECK_BELIEF.
var pingFrame = []byte{31, 2, 3, 4, 5}
```

Create `internal/device/valve/commands.go`:

```go
package valve

import (
	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

type pingResult struct {
	UptimeMs int64 `json:"uptime_ms"`
}

// ping proves liveness with the side-effect-free ping frame and
// opportunistically feeds the reported position into the CHECK_BELIEF logic
// — but only while idle: a mid-move reply reflects the target the firmware
// is already counting from, not the rotor (TRANSLATION.md §4 ping).
// uptime_ms is connection age; true device uptime is unknowable.
func (d *Driver) ping() (any, *device.CmdError) {
	reply, err := d.s.Transact(pingFrame, 4, replyTimeout)
	if err != nil {
		if d.moveJob != nil {
			// The in-flight move's outcome is unknown (TRANSLATION.md §5);
			// the session has already failed the job.
			d.homed = false
			d.moveJob = nil
			d.jobGen++
		}
		return nil, device.ErrHardware("ping: " + err.Error())
	}
	if reply[0] != TypeCode {
		return nil, device.ErrHardware("ping: unexpected reply")
	}
	if d.moveJob == nil {
		d.applyBelief(int(reply[3]))
	}
	return pingResult{UptimeMs: d.s.Now().Sub(d.connectedSince).Milliseconds()}, nil
}

func (d *Driver) stateName() string {
	switch {
	case !d.homed:
		return "unhomed"
	case d.moveJob != nil:
		return "moving"
	default:
		return "idle"
	}
}

type statusResult struct {
	State          string      `json:"state"`
	Homed          bool        `json:"homed"`
	Position       *int        `json:"position"`
	TargetPosition *int        `json:"target_position"`
	Job            *device.Job `json:"job"`
	Config         configBlock `json:"config"`
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

// status (TRANSLATION.md §4): an idle status runs CHECK_BELIEF so reboots
// are caught even when nothing is happening; during a move it is served
// entirely from memory. position is reported only when the rotor is
// verifiably settled (homed and idle) — never the target of an in-flight
// move.
func (d *Driver) status() (any, *device.CmdError) {
	if d.moveJob == nil {
		if cerr := d.checkBelief(); cerr != nil {
			return nil, cerr
		}
	}
	res := statusResult{State: d.stateName(), Homed: d.homed, Config: d.config}
	if d.homed && d.moveJob == nil {
		pos := d.physicalPos // copy — never return a pointer into live driver state
		res.Position = &pos
	}
	if d.moveJob != nil {
		tp := d.moveJob.targetPhysical
		res.TargetPosition = &tp
	}
	res.Job = d.statusJob()
	return res, nil
}
```

In `internal/device/valve/valve.go`, replace the `Execute` switch and `Tick`:

```go
// Execute dispatches one JSON command (identify/get_job are session-served).
func (d *Driver) Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *device.CmdError) {
	switch cmd {
	case "ping":
		return d.ping()
	case "status":
		return d.status()
	default:
		return nil, device.ErrUnknownCommand(cmd)
	}
}

// Tick runs the idle CHECK_BELIEF (TRANSLATION.md §5): every CheckInterval
// while no move is in flight, so silent reboots surface promptly. Never
// during a move — mid-move replies reflect the target, not the rotor.
func (d *Driver) Tick(now time.Time) {
	if d.moveJob != nil || now.Sub(d.lastCheck) < CheckInterval {
		return
	}
	_ = d.checkBelief() // a serial failure trips the session's unreachable handling
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/valve/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/device/valve/
git commit -m "feat: add valve belief tracking, ping, status

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: `home` — virtual homing declaration + restart recovery

**Files:**
- Create: `internal/device/valve/move.go`
- Modify: `internal/device/valve/valve.go` (Execute case `home`)
- Test: `internal/device/valve/home_test.go`

**Interfaces:**
- Consumes: `persistNow`, `queryPosFrame`, Driver fields.
- Produces: `home(params json.RawMessage) (any, *device.CmdError)`; test helper `newHomedFixture(t *testing.T, at int) *fixture` (homed at `at`, device belief = 0) used by Tasks 6–9.

- [ ] **Step 1: Write the failing tests**

Create `internal/device/valve/home_test.go`:

```go
package valve_test

import (
	"fmt"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// newHomedFixture returns a fixture homed at `at` with device belief 0
// (fresh-boot device counter).
func newHomedFixture(t *testing.T, at int) *fixture {
	t.Helper()
	f := newFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0}) // home's belief-resync reply
	if resp := f.exec("home", fmt.Sprintf(`{"position":%d}`, at)); resp.Status != "ok" {
		t.Fatalf("home: %+v", resp)
	}
	return f
}

// TestHomeDeclaresPositionAndPersists: home is a translator-side
// declaration — no motion frame; it resyncs belief from the device, sets
// physical_position, and persists both (TRANSLATION §4 home).
func TestHomeDeclaresPositionAndPersists(t *testing.T) {
	f := newFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 5}) // device counter drifted to 5 meanwhile
	resp := f.exec("home", `{"position":2}`)
	if resp.Status != "ok" {
		t.Fatalf("home: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["homed"] != true || m["position"] != 2.0 {
		t.Fatalf("home result: %v", m)
	}
	fr := f.frames()
	if !frameEq(fr[len(fr)-1], 33, 1, 0, 0, 0) {
		t.Fatalf("home must resync belief with a position query: %v", fr)
	}
	if countFrames(fr, 36) != 0 {
		t.Fatal("home must not move the rotor")
	}
	st := readState(t, f.dir)
	if st["physical_position"] != 2.0 || st["device_belief_at_shutdown"] != 5.0 {
		t.Fatalf("persisted: %v", st)
	}
	f.port.Feed([]byte{30, 1, 1, 5}) // status's idle CHECK_BELIEF
	sm := f.resultMap(f.exec("status", ""))
	if sm["state"] != "idle" || sm["homed"] != true || sm["position"] != 2.0 {
		t.Fatalf("status after home: %v", sm)
	}
}

func TestHomeValidation(t *testing.T) {
	f := newFixture(t, 0)
	n := len(f.port.Written())
	for name, params := range map[string]string{
		"out of range": `{"position":7}`,
		"negative":     `{"position":-1}`,
		"missing":      `{}`,
	} {
		resp := f.exec("home", params)
		if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
			t.Fatalf("%s: %+v", name, resp)
		}
	}
	if len(f.port.Written()) != n {
		t.Fatal("validation failures must not reach the device")
	}
}

// TestAttachRecoversHomedState: restart recovery (TRANSLATION §3 step 3) —
// persisted physical position + matching device counter → homed without
// operator involvement.
func TestAttachRecoversHomedState(t *testing.T) {
	f := newHomedFixture(t, 4)
	dir := f.dir
	f.s.Close()

	f2 := newFixture(t, 0, withStateDir(dir)) // device counter still 0 == persisted belief
	f2.port.Feed([]byte{30, 1, 1, 0})
	sm := f2.resultMap(f2.exec("status", ""))
	if sm["state"] != "idle" || sm["position"] != 4.0 {
		t.Fatalf("recovery must restore homed state: %v", sm)
	}
}

// TestAttachRefusesStaleRecovery: the device counter no longer matches the
// persisted belief (reboot or foreign host while we were away) → require an
// explicit home.
func TestAttachRefusesStaleRecovery(t *testing.T) {
	f := newHomedFixture(t, 4)
	dir := f.dir
	f.s.Close()

	f2 := newFixture(t, 3, withStateDir(dir)) // counter 3 ≠ persisted belief 0
	f2.port.Feed([]byte{30, 1, 1, 3})
	sm := f2.resultMap(f2.exec("status", ""))
	if sm["state"] != "unhomed" || sm["position"] != nil {
		t.Fatalf("stale recovery must be refused: %v", sm)
	}
}

// TestAttachPushesRecoveredConfig: the persisted config mirror (not the
// defaults) is what attach re-pushes — including the inverted hold-ON
// encoding (35 2 0).
func TestAttachPushesRecoveredConfig(t *testing.T) {
	dir := t.TempDir()
	st := device.NewStore(dir, "valve-COM9")
	if err := st.Save(map[string]any{
		"schema_version": 1, "physical_position": 4,
		"device_belief_at_shutdown": 2,
		"config":                    map[string]any{"default_rotation": "direct", "hold_torque": true},
	}); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, 2, withStateDir(dir))
	fr := f.frames()
	if !frameEq(fr[1], 35, 1, 1, 0, 0) || !frameEq(fr[2], 35, 2, 0, 0, 0) {
		t.Fatalf("recovered config frames: %v", fr)
	}
	f.port.Feed([]byte{30, 1, 1, 2})
	sm := f.resultMap(f.exec("status", ""))
	if sm["position"] != 4.0 {
		t.Fatalf("recovered homed state: %v", sm)
	}
	cfg := sm["config"].(map[string]any)
	if cfg["default_rotation"] != "direct" || cfg["hold_torque"] != true {
		t.Fatalf("recovered config: %v", cfg)
	}
}

// TestRebootWhileIdlePreservesHoming: the idle auto-recovery keeps homed
// and physical_position — the offset math absorbs the counter reset.
func TestRebootWhileIdlePreservesHoming(t *testing.T) {
	f := newFixture(t, 2) // attach: belief 2
	f.port.Feed([]byte{30, 1, 1, 2})
	if resp := f.exec("home", `{"position":3}`); resp.Status != "ok" {
		t.Fatalf("home: %+v", resp)
	}
	f.port.Feed([]byte{30, 1, 1, 0}) // silent reboot: counter reset
	sm := f.resultMap(f.exec("status", ""))
	if sm["state"] != "idle" || sm["homed"] != true || sm["position"] != 3.0 {
		t.Fatalf("auto-recovery must preserve homing: %v", sm)
	}
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 35, 1, 3, 0, 0) || !frameEq(fr[n-1], 35, 2, 1, 0, 0) {
		t.Fatalf("config not re-pushed after reboot: %v", fr)
	}
}

// TestForeignMoveUnhomes: a mismatched, nonzero counter voids position
// knowledge even when homed.
func TestForeignMoveUnhomes(t *testing.T) {
	f := newFixture(t, 2)
	f.port.Feed([]byte{30, 1, 1, 2})
	if resp := f.exec("home", `{"position":3}`); resp.Status != "ok" {
		t.Fatalf("home: %+v", resp)
	}
	f.port.Feed([]byte{30, 1, 1, 6}) // someone else moved the valve
	sm := f.resultMap(f.exec("status", ""))
	if sm["state"] != "unhomed" || sm["position"] != nil {
		t.Fatalf("foreign mismatch must unhome: %v", sm)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/valve/ -run 'TestHome|TestAttachRecovers|TestAttachRefuses|TestAttachPushes|TestReboot|TestForeign' -v`
Expected: FAIL — `home` returns `unknown_command`.

- [ ] **Step 3: Implement**

Create `internal/device/valve/move.go`:

```go
package valve

import (
	"encoding/json"
	"fmt"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// home declares the rotor's current physical position (TRANSLATION.md §4
// home). No motion frame is sent — homing is purely a translator-side
// declaration; all future moves are computed relative to it. The device's
// counter is re-read first so the belief↔physical offset is anchored to
// reality, then both are persisted for restart recovery.
func (d *Driver) home(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Position *int `json:"position"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if j := d.s.Jobs().Active(); j != nil {
		return nil, device.ErrBusy("a move is in progress", map[string]any{"job_id": j.ID})
	}
	if p.Position == nil || *p.Position < 0 || *p.Position > d.positions {
		return nil, device.ErrInvalidParams("position", p.Position,
			fmt.Sprintf("position must be between 0 and %d", d.positions))
	}
	reply, err := d.s.Transact(queryPosFrame, 4, replyTimeout)
	if err != nil {
		return nil, device.ErrHardware("position query: " + err.Error())
	}
	if reply[0] != TypeCode {
		return nil, device.ErrHardware("position query: unexpected reply")
	}
	d.deviceBelief = int(reply[3])
	d.lastCheck = d.s.Now()
	d.physicalPos = *p.Position
	d.homed = true
	if err := d.persistNow(); err != nil {
		return nil, device.ErrInternal("persist home: " + err.Error())
	}
	return map[string]any{"homed": true, "position": *p.Position}, nil
}
```

In `valve.go`'s `Execute` switch, add before `default`:

```go
	case "home":
		return d.home(params)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/valve/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/device/valve/
git commit -m "feat: add valve home command with persistence

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: `set_position` — offset translation, safety rules, clock-driven completion

**Files:**
- Modify: `internal/device/valve/move.go` (add `setPosition`, `moveComplete`, `verifyMove`, `moveResult`)
- Modify: `internal/device/valve/valve.go` (Execute case `set_position`)
- Test: `internal/device/valve/move_test.go`

**Interfaces:**
- Consumes: `planMove` (Task 2), `checkBelief` (Task 4), `home` fixture helper (Task 5), `ErrNotHomed`/`Session.After` (core).
- Produces: `setPosition(params json.RawMessage) (any, *device.CmdError)`, `moveComplete(gen int)`, `verifyMove(cancelled bool) *device.CmdError` (Task 8's `stop` reuses it), `moveResult{Position, FromPosition int; Direction string; DurationS float64}` (json `position`/`from_position`/`direction,omitempty`/`duration_s`); test helpers `startMove`, `jobState`.

- [ ] **Step 1: Write the failing tests**

Create `internal/device/valve/move_test.go`:

```go
package valve_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// startMove issues a set_position and returns the job id. The caller must
// pre-feed a CHECK_BELIEF reply matching the current belief.
func startMove(t *testing.T, f *fixture, params string) string {
	t.Helper()
	resp := f.exec("set_position", params)
	if resp.Status != "ok" {
		t.Fatalf("set_position: %+v", resp)
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

// TestSetPositionTranslatesThroughOffset — the heart of virtual homing:
// homed at 4 while the device believes 0, a move to physical 1 must be sent
// as device-frame target 4 (delta = (1−4) mod 7 = 4), travel the shortest
// arc (3 slots, decreasing), and complete on the clock with a verified
// readback.
func TestSetPositionTranslatesThroughOffset(t *testing.T) {
	f := newHomedFixture(t, 4) // physical 4, device belief 0
	f.port.Feed([]byte{30, 1, 1, 0})
	id := startMove(t, f, `{"position":1}`)
	fr := f.frames()
	n := len(fr)
	// CHECK_BELIEF precedes the move; default mode (shortest) was already
	// pushed at attach, so no 35 frame here
	if !frameEq(fr[n-2], 33, 1, 0, 0, 0) {
		t.Fatalf("no CHECK_BELIEF before move: %v", fr)
	}
	if !frameEq(fr[n-1], 36, 1, 4, 0, 0) {
		t.Fatalf("move frame must target the device frame: %v", fr)
	}

	st := f.resultMap(f.exec("status", ""))
	if st["state"] != "moving" || st["position"] != nil || st["target_position"] != 1.0 {
		t.Fatalf("moving status: %v", st)
	}
	if countFrames(f.frames(), 33) != countFrames(fr, 33) {
		t.Fatal("status during a move must not touch the serial port")
	}

	// 3 slots × 0.92 s + 0.3 s margin = 3.06 s
	if js := jobState(t, f, id); js["estimated_duration_s"] != 3.06 {
		t.Fatalf("estimate: %v", js)
	}
	f.port.Feed([]byte{30, 1, 1, 4}) // post-move readback: device at target
	f.clock.Advance(3060 * time.Millisecond)
	waitFor(t, "job success", func() bool {
		return jobState(t, f, id)["state"] == "succeeded"
	})
	res := jobState(t, f, id)["result"].(map[string]any)
	if res["position"] != 1.0 || res["from_position"] != 4.0 ||
		res["direction"] != "decreasing" || res["duration_s"] != 3.06 {
		t.Fatalf("result: %v", res)
	}
	ps := readState(t, f.dir)
	if ps["physical_position"] != 1.0 || ps["device_belief_at_shutdown"] != 4.0 {
		t.Fatalf("move must persist position knowledge: %v", ps)
	}
	f.port.Feed([]byte{30, 1, 1, 4})
	sm := f.resultMap(f.exec("status", ""))
	if sm["state"] != "idle" || sm["position"] != 1.0 {
		t.Fatalf("status after move: %v", sm)
	}
	if sm["job"].(map[string]any)["job_id"] != id {
		t.Fatalf("status must embed the last job: %v", sm)
	}
}

// TestSetPositionCurrentPositionSucceedsInstantly — the Δ=0 guard: in wrap
// mode the firmware would interpret "move to the current position" as a
// full 360° revolution, so that frame must NEVER go out; the driver returns
// an already-succeeded job instead.
func TestSetPositionCurrentPositionSucceedsInstantly(t *testing.T) {
	f := newHomedFixture(t, 3)
	f.port.Feed([]byte{30, 1, 1, 0}) // CHECK_BELIEF still runs first
	resp := f.exec("set_position", `{"position":3,"rotation":"wrap"}`)
	if resp.Status != "ok" {
		t.Fatalf("set_position: %+v", resp)
	}
	job := f.resultMap(resp)["job"].(map[string]any)
	if job["state"] != "succeeded" || job["progress"] != 1.0 {
		t.Fatalf("job: %v", job)
	}
	res := job["result"].(map[string]any)
	if res["position"] != 3.0 || res["from_position"] != 3.0 || res["duration_s"] != 0.0 {
		t.Fatalf("result: %v", res)
	}
	if _, ok := res["direction"]; ok {
		t.Fatalf("no-motion move must omit direction: %v", res)
	}
	if countFrames(f.frames(), 36) != 0 {
		t.Fatal("a move to the current position must never reach the device")
	}
}

// TestSetPositionRotationModeDedup: the 35 mode frame is pushed only when
// the requested mode differs from what the firmware last received.
func TestSetPositionRotationModeDedup(t *testing.T) {
	f := newHomedFixture(t, 0)
	// 1st move: wrap override differs from attach's shortest → 35 pushed.
	// d = 2 → wrap slots 7−2 = 5 → estimate 4.9 s, decreasing.
	f.port.Feed([]byte{30, 1, 1, 0})
	id := startMove(t, f, `{"position":2,"rotation":"wrap"}`)
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 35, 1, 2, 0, 0) || !frameEq(fr[n-1], 36, 1, 2, 0, 0) {
		t.Fatalf("wrap move frames: %v", fr)
	}
	f.port.Feed([]byte{30, 1, 1, 2})
	f.clock.Advance(4900 * time.Millisecond)
	waitFor(t, "first move", func() bool { return jobState(t, f, id)["state"] == "succeeded" })
	if res := jobState(t, f, id)["result"].(map[string]any); res["direction"] != "decreasing" {
		t.Fatalf("wrap direction: %v", res)
	}

	// 2nd move: wrap again → no 35 re-push; target (2+2) mod 7 = 4
	before := countFrames(f.frames(), 35)
	f.port.Feed([]byte{30, 1, 1, 2})
	id2 := startMove(t, f, `{"position":4,"rotation":"wrap"}`)
	if countFrames(f.frames(), 35) != before {
		t.Fatal("unchanged mode must not be re-pushed")
	}
	fr = f.frames()
	if !frameEq(fr[len(fr)-1], 36, 1, 4, 0, 0) {
		t.Fatalf("second move frame: %v", fr)
	}
	f.port.Feed([]byte{30, 1, 1, 4})
	f.clock.Advance(4900 * time.Millisecond)
	waitFor(t, "second move", func() bool { return jobState(t, f, id2)["state"] == "succeeded" })

	// 3rd move: no rotation param → default shortest ≠ last-pushed wrap →
	// 35 pushed again
	f.port.Feed([]byte{30, 1, 1, 4})
	startMove(t, f, `{"position":5}`)
	fr = f.frames()
	n = len(fr)
	if !frameEq(fr[n-2], 35, 1, 3, 0, 0) || !frameEq(fr[n-1], 36, 1, 5, 0, 0) {
		t.Fatalf("default mode must be re-pushed: %v", fr)
	}
}

func TestSetPositionNotHomed(t *testing.T) {
	f := newFixture(t, 0)
	n := len(f.port.Written())
	resp := f.exec("set_position", `{"position":2}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeNotHomed {
		t.Fatalf("resp: %+v", resp)
	}
	if len(f.port.Written()) != n {
		t.Fatal("not_homed must fire before any serial traffic")
	}
}

func TestSetPositionValidation(t *testing.T) {
	f := newHomedFixture(t, 0)
	n := len(f.port.Written())
	for name, params := range map[string]string{
		"out of range": `{"position":7}`,
		"missing":      `{}`,
		"bad rotation": `{"position":2,"rotation":"spiral"}`,
	} {
		resp := f.exec("set_position", params)
		if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
			t.Fatalf("%s: %+v", name, resp)
		}
	}
	if len(f.port.Written()) != n {
		t.Fatal("validation must precede serial traffic")
	}
}

// TestSetPositionBusyDuringMove: no mid-move retargeting — the firmware
// would compute from its already-advanced counter while the rotor is
// between detents (TRANSLATION §5).
func TestSetPositionBusyDuringMove(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	startMove(t, f, `{"position":2}`)
	n := len(f.port.Written())
	for _, c := range []struct{ cmd, params string }{
		{"set_position", `{"position":5}`},
		{"home", `{"position":0}`},
	} {
		resp := f.exec(c.cmd, c.params)
		if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
			t.Fatalf("%s during move: %+v", c.cmd, resp)
		}
	}
	if len(f.port.Written()) != n {
		t.Fatal("busy commands must not reach the device")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/valve/ -run TestSetPosition -v`
Expected: FAIL — `set_position` returns `unknown_command`.

- [ ] **Step 3: Implement**

In `internal/device/valve/valve.go`, extend the frame block and add the
slot helper:

```go
// moveFrame rotates motor 1 to the device-frame position (36 1 P). No
// reply; the firmware bumps its position counter immediately on parse,
// before the motion completes.
func moveFrame(targetDevice byte) []byte { return []byte{36, 1, targetDevice, 0, 0} }
```

```go
// slots is S = N+1: the rotor detents (position 0 plus outputs 1..N); all
// position arithmetic is modulo this.
func (d *Driver) slots() int { return d.positions + 1 }
```

Append to `internal/device/valve/move.go`:

```go
// moveResult is the completed-move job result (JSON_PROTOCOL.md §4). A
// no-motion move (target == current) reports duration 0 and omits
// direction — neither "increasing" nor "decreasing" happened (flagged
// deviation: the JSON doc defines no direction for a degenerate move).
type moveResult struct {
	Position     int     `json:"position"`
	FromPosition int     `json:"from_position"`
	Direction    string  `json:"direction,omitempty"`
	DurationS    float64 `json:"duration_s"`
}

// setPosition implements TRANSLATION.md §4 set_position. Safety rules, in
// order: the not_homed gate, the single-job gate (no mid-move retargeting —
// the firmware would compute from its already-advanced counter while the
// rotor is between detents), parameter validation, CHECK_BELIEF, and the
// Δ=0 guard (in wrap mode the firmware interprets "move to the current
// position" as a full 360° revolution — that frame must never go out).
func (d *Driver) setPosition(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Position *int   `json:"position"`
		Rotation string `json:"rotation"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if !d.homed {
		return nil, device.ErrNotHomed("position is unknown — home the valve first")
	}
	if j := d.s.Jobs().Active(); j != nil {
		return nil, device.ErrBusy("a move is in progress", map[string]any{"job_id": j.ID})
	}
	if p.Position == nil || *p.Position < 0 || *p.Position > d.positions {
		return nil, device.ErrInvalidParams("position", p.Position,
			fmt.Sprintf("position must be between 0 and %d", d.positions))
	}
	mode := d.config.DefaultRotation
	if p.Rotation != "" {
		if _, ok := rotationCode(p.Rotation); !ok {
			return nil, device.ErrInvalidParams("rotation", p.Rotation,
				`rotation must be "shortest", "direct" or "wrap"`)
		}
		mode = p.Rotation
	}

	if cerr := d.checkBelief(); cerr != nil {
		return nil, cerr
	}
	if !d.homed { // the check itself may have just unhomed us
		return nil, device.ErrNotHomed("position counter mismatch — home the valve again")
	}

	target := *p.Position
	if target == d.physicalPos {
		// Already there: succeed without motion (the wrap-mode Δ=0 guard).
		if _, cerr := d.s.Jobs().Start("move", 0); cerr != nil {
			return nil, cerr
		}
		done := d.s.Jobs().Complete(moveResult{Position: target, FromPosition: target})
		d.lastJobID = done.ID
		return map[string]any{"job": *done}, nil
	}

	if mode != d.lastPushed {
		code, _ := rotationCode(mode)
		if _, err := d.s.Transact(rotationFrame(code), 0, time.Second); err != nil {
			return nil, device.ErrHardware("rotation mode frame: " + err.Error())
		}
		d.lastPushed = mode
	}

	plan := planMove(target, d.physicalPos, d.deviceBelief, d.slots(), mode)
	// #nosec G115 -- targetDevice ∈ [0, slots) and slots ≤ 256 (probe byte + 1)
	if _, err := d.s.Transact(moveFrame(byte(plan.targetDevice)), 0, time.Second); err != nil {
		// The write failed; whether the firmware parsed the frame first is
		// unknowable → position knowledge is void (TRANSLATION.md §5).
		d.homed = false
		return nil, device.ErrHardware("move frame: " + err.Error())
	}

	job, cerr := d.s.Jobs().Start("move", plan.estimate)
	if cerr != nil {
		return nil, cerr // unreachable: the busy gate ran above
	}
	d.moveJob = &moveJob{
		id: job.ID, fromPhysical: d.physicalPos, targetPhysical: target,
		targetDevice: plan.targetDevice, direction: plan.direction, estimate: plan.estimate,
	}
	d.lastJobID = job.ID
	// Optimistic belief update: the firmware bumps its counter the moment
	// it parses the frame (TRANSLATION.md §4 step 9).
	d.deviceBelief = plan.targetDevice
	d.jobGen++
	gen := d.jobGen
	d.s.After(plan.estimate, func() { d.moveComplete(gen) })
	return map[string]any{"job": job}, nil
}

// moveComplete is the clock-driven completion callback (s.After). Stale
// generations are ignored: stop, an unreachable episode, or a reattach may
// already have settled the job.
func (d *Driver) moveComplete(gen int) {
	if gen != d.jobGen || d.moveJob == nil {
		return
	}
	_ = d.verifyMove(false)
}

// verifyMove runs TRANSLATION.md §4 set_position step 10 — the post-motion
// readback. cancelled marks the job cancelled instead of succeeded (stop's
// settle-and-report path). The readback proves the firmware is alive and
// processed the move; it CANNOT prove the rotor physically arrived (no
// encoder) — a stalled motor is undetectable. Inherent hardware gap.
func (d *Driver) verifyMove(cancelled bool) *device.CmdError {
	mj := d.moveJob
	d.moveJob = nil
	d.jobGen++ // invalidate the pending completion timer (stop path)
	reply, err := d.s.Transact(queryPosFrame, 4, replyTimeout)
	if err != nil {
		// Double failure: the session went unreachable and failed the job.
		// The move's outcome is unknown → position knowledge is void.
		d.homed = false
		return device.ErrHardware("post-move readback: " + err.Error())
	}
	pos := int(reply[3])
	if reply[0] != TypeCode || pos != mj.targetDevice {
		d.homed = false
		d.deviceBelief = pos
		if pos == 0 {
			// Reboot signature: the firmware also lost its RAM-only config.
			_ = d.pushConfig()
		}
		cerr := device.ErrHardware(fmt.Sprintf(
			"position readback %d after move to %d — device rebooted or lost the command; valve is unhomed",
			pos, mj.targetDevice))
		d.s.Jobs().Fail(cerr)
		return cerr
	}
	d.physicalPos = mj.targetPhysical
	d.lastCheck = d.s.Now() // the readback doubles as a consistency check
	if err := d.persistNow(); err != nil {
		// The move itself succeeded — log rather than fail the job over disk.
		slog.Warn("valve: persist after move failed", "port", d.s.PortName(), "err", err)
	}
	if cancelled {
		d.s.Jobs().Cancel()
	} else {
		d.s.Jobs().Complete(moveResult{
			Position: mj.targetPhysical, FromPosition: mj.fromPhysical,
			Direction: mj.direction, DurationS: mj.estimate.Seconds(),
		})
	}
	return nil
}
```

Add `"log/slog"` and `"time"` to `move.go`'s imports. In `valve.go`'s `Execute` switch, add:

```go
	case "set_position":
		return d.setPosition(params)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/valve/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/device/valve/
git commit -m "feat: add valve set_position with clock-driven completion

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: Move failure paths — mid-move reboot, unreachable, mid-move ping, Detach mid-move

**Files:**
- Test: `internal/device/valve/move_failure_test.go` (implementation already exists — these tests pin the failure branches of Task 6's code and Task 3's Detach)

**Interfaces:**
- Consumes: `startMove`/`jobState` (Task 6), `newHomedFixture` (Task 5), `device.ReattachBase` (core).
- Produces: nothing new — verification only. If any test exposes a bug, fix it in `move.go`/`commands.go` within this task.

- [ ] **Step 1: Write the tests**

Create `internal/device/valve/move_failure_test.go`:

```go
package valve_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// TestMoveReadbackMismatchUnhomesAndFails: mid-move reboot — the readback
// reports 0 instead of the target → job failed, valve unhomed, RAM config
// re-pushed (TRANSLATION §4 step 10 mismatch + §2 reboot signature).
func TestMoveReadbackMismatchUnhomesAndFails(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	id := startMove(t, f, `{"position":2}`) // shortest: 2 slots → 2.14 s
	f.port.Feed([]byte{30, 1, 1, 0})        // readback: counter reset mid-move
	f.clock.Advance(2140 * time.Millisecond)
	waitFor(t, "job failure", func() bool {
		return jobState(t, f, id)["state"] == "failed"
	})
	em := jobState(t, f, id)["error"].(map[string]any)
	if em["code"] != "hardware_error" {
		t.Fatalf("job error: %v", em)
	}
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 35, 1, 3, 0, 0) || !frameEq(fr[n-1], 35, 2, 1, 0, 0) {
		t.Fatalf("config not re-pushed after mid-move reboot: %v", fr)
	}
	f.port.Feed([]byte{30, 1, 1, 0})
	sm := f.resultMap(f.exec("status", ""))
	if sm["state"] != "unhomed" || sm["position"] != nil {
		t.Fatalf("status: %v", sm)
	}
}

// TestUnreachableMidMoveRefusesStaleRecovery: the post-move readback gets
// no reply → session unreachable, job failed; on reattach the persisted
// belief (from home time) no longer matches the counter → recovery refused.
func TestUnreachableMidMoveRefusesStaleRecovery(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	id := startMove(t, f, `{"position":2}`)
	// no readback reply fed: the verification transaction double-fails
	f.clock.Advance(2140 * time.Millisecond)
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })
	f.port.Feed([]byte{30, 1, 1, 2}) // reattach's position-query reply
	f.clock.Advance(device.ReattachBase)
	waitFor(t, "reattach", f.s.Connected)
	if js := jobState(t, f, id); js["state"] != "failed" {
		t.Fatalf("job after unreachable: %v", js)
	}
	f.port.Feed([]byte{30, 1, 1, 2})
	sm := f.resultMap(f.exec("status", ""))
	if sm["state"] != "unhomed" {
		t.Fatalf("stale recovery must be refused: %v", sm)
	}
}

// TestPingFailureMidMoveFailsJob: any transaction double-failure while a
// move is in flight voids position knowledge (TRANSLATION §5). Ping is the
// only reply-expecting command allowed mid-move.
func TestPingFailureMidMoveFailsJob(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	id := startMove(t, f, `{"position":3}`)
	resp := f.exec("ping", "") // no reply fed
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("ping: %+v", resp)
	}
	waitFor(t, "unreachable", func() bool { return !f.s.Connected() })
	f.port.Feed([]byte{30, 1, 1, 3})
	f.clock.Advance(device.ReattachBase)
	waitFor(t, "reattach", f.s.Connected)
	if js := jobState(t, f, id); js["state"] != "failed" {
		t.Fatalf("job: %v", js)
	}
}

// TestPingDuringMoveDoesNotFeedBelief: a mid-move reply reflects the target
// the firmware already counts from — even a 0 (reboot-looking) reply must
// not trigger belief handling while a move is in flight; the post-move
// readback is the arbiter.
func TestPingDuringMoveDoesNotFeedBelief(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	startMove(t, f, `{"position":3}`)
	f.port.Feed([]byte{30, 1, 1, 0})
	if resp := f.exec("ping", ""); resp.Status != "ok" {
		t.Fatalf("ping: %+v", resp)
	}
	if countFrames(f.frames(), 35) != 2 { // attach's config push only
		t.Fatal("mid-move ping must not trigger belief recovery")
	}
}

// TestDetachMidMovePersistsSettledOutcome: the firmware finishes an
// accepted move autonomously — Detach persists the settled target, with no
// serial I/O.
func TestDetachMidMovePersistsSettledOutcome(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	startMove(t, f, `{"position":3}`)
	n := len(f.port.Written())
	f.s.Close()
	if len(f.port.Written()) != n {
		t.Fatal("Detach must not write to the serial port")
	}
	ps := readState(t, f.dir)
	if ps["physical_position"] != 3.0 || ps["device_belief_at_shutdown"] != 3.0 {
		t.Fatalf("persisted: %v", ps)
	}
}
```

- [ ] **Step 2: Run the tests**

Run: `go test -race ./internal/device/valve/ -run 'TestMoveReadback|TestUnreachable|TestPingFailure|TestPingDuring|TestDetachMidMove' -count=1 -v`
Expected: PASS (the branches were written in Tasks 3/4/6). Any failure here is a real bug — fix it in the implementation, not by weakening the test.

- [ ] **Step 3: Commit**

```bash
git add internal/device/valve/
git commit -m "test: pin valve move failure and lifecycle paths

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: `stop` — settle-and-report (documented deviation)

**Files:**
- Modify: `internal/device/valve/move.go` (add `stop`, `elapsedOf`)
- Modify: `internal/device/valve/valve.go` (Execute case `stop`)
- Test: `internal/device/valve/stop_test.go`

**Interfaces:**
- Consumes: `Session.Sleep` (Task 1), `verifyMove` (Task 6), `stateName` (Task 4).
- Produces: `stop() (any, *device.CmdError)`, `elapsedOf(j *device.Job) time.Duration`.

- [ ] **Step 1: Write the failing tests**

Create `internal/device/valve/stop_test.go`:

```go
package valve_test

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// stopAsync issues stop on a goroutine and advances the fake clock in
// slices until it returns. The slices (200 ms) are far below the move
// estimate, so the stop command reaches the loop and registers its
// Sleep(remaining) long before the cumulative advance could overshoot the
// estimate and let the completion timer settle the job first.
func stopAsync(t *testing.T, f *fixture) device.Response {
	t.Helper()
	respCh := make(chan device.Response, 1)
	go func() { respCh <- f.exec("stop", "") }()
	var resp device.Response
	waitFor(t, "stop returns", func() bool {
		f.clock.Advance(200 * time.Millisecond)
		select {
		case resp = <-respCh:
			return true
		default:
			return false
		}
	})
	return resp
}

// TestStopWaitsOutMoveAndPreservesPosition — the documented spec deviation:
// the firmware cannot abort, so stop waits out the move (blocking the
// session loop), verifies, keeps position knowledge, and marks the job
// cancelled to record intent.
func TestStopWaitsOutMoveAndPreservesPosition(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	id := startMove(t, f, `{"position":1,"rotation":"wrap"}`) // 6 slots → 5.82 s
	f.port.Feed([]byte{30, 1, 1, 1})                          // stop's settle readback
	resp := stopAsync(t, f)
	if resp.Status != "ok" {
		t.Fatalf("stop: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["state"] != "idle" || m["cancelled_job_id"] != id {
		t.Fatalf("stop result: %v", m)
	}
	if js := jobState(t, f, id); js["state"] != "cancelled" {
		t.Fatalf("job: %v", js)
	}
	ps := readState(t, f.dir)
	if ps["physical_position"] != 1.0 {
		t.Fatalf("position knowledge must be preserved: %v", ps)
	}
	f.port.Feed([]byte{30, 1, 1, 1})
	sm := f.resultMap(f.exec("status", ""))
	if sm["state"] != "idle" || sm["position"] != 1.0 {
		t.Fatalf("status after stop: %v", sm)
	}
	if countFrames(f.frames(), 36) != 1 {
		t.Fatal("stop must not send any motion/abort frame")
	}
}

func TestStopIdleIsNoop(t *testing.T) {
	f := newFixture(t, 0) // unhomed, idle
	n := len(f.port.Written())
	m := f.resultMap(f.exec("stop", ""))
	if m["state"] != "unhomed" {
		t.Fatalf("unhomed idle stop: %v", m)
	}
	if _, ok := m["cancelled_job_id"]; ok {
		t.Fatalf("nothing to cancel: %v", m)
	}
	if len(f.port.Written()) != n {
		t.Fatal("idle stop must be serial-silent")
	}

	f2 := newHomedFixture(t, 2)
	if m2 := f2.resultMap(f2.exec("stop", "")); m2["state"] != "idle" {
		t.Fatalf("homed idle stop: %v", m2)
	}
}

// TestStopVerificationMismatchUnhomes: the settle readback finds a rebooted
// device → same handling as set_position step 10 mismatch (job failed,
// unhomed), surfaced as a hardware_error response.
func TestStopVerificationMismatchUnhomes(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	id := startMove(t, f, `{"position":2}`) // 2 slots → 2.14 s
	f.port.Feed([]byte{30, 1, 1, 0})        // readback: reboot signature
	resp := stopAsync(t, f)
	if resp.Status != "error" || resp.Error.Code != device.CodeHardwareError {
		t.Fatalf("stop: %+v", resp)
	}
	if js := jobState(t, f, id); js["state"] != "failed" {
		t.Fatalf("job: %v", js)
	}
	f.port.Feed([]byte{30, 1, 1, 0})
	if sm := f.resultMap(f.exec("status", "")); sm["state"] != "unhomed" {
		t.Fatalf("status: %v", sm)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/valve/ -run TestStop -v`
Expected: FAIL — `stop` returns `unknown_command`.

- [ ] **Step 3: Implement**

Append to `internal/device/valve/move.go`:

```go
// stop implements the documented spec deviation (TRANSLATION.md §4 stop;
// JSON_PROTOCOL.md §3 stop's MAY clause; spec §8.4): the firmware has NO
// abort command — motion always runs to completion (worst case
// ≈ N × SlotDuration ≈ 5.5 s). stop therefore WAITS OUT the remaining
// motion, deliberately blocking this session's loop (accepted per spec §3;
// queued commands stall behind it, within single-client semantics), then
// runs the usual post-motion verification. Position knowledge is preserved;
// the job is marked cancelled to record intent even though the motion
// physically completed. Callers must treat stop as "settle and report",
// latency ≤ ~6 s.
func (d *Driver) stop() (any, *device.CmdError) {
	if d.moveJob == nil {
		return map[string]any{"state": d.stateName()}, nil
	}
	id := d.moveJob.id
	if a := d.s.Jobs().Active(); a != nil {
		if remaining := d.moveJob.estimate - elapsedOf(a); remaining > 0 {
			d.s.Sleep(remaining)
		}
	}
	if cerr := d.verifyMove(true); cerr != nil {
		return nil, cerr
	}
	return map[string]any{"state": d.stateName(), "cancelled_job_id": id}, nil
}

func elapsedOf(j *device.Job) time.Duration {
	return time.Duration(j.ElapsedS * float64(time.Second))
}
```

In `valve.go`'s `Execute` switch, add:

```go
	case "stop":
		return d.stop()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/valve/ -count=1`
Expected: PASS. (The pending completion timer fires during the advances and lands on the loop after stop returns; `moveComplete`'s generation guard discards it — that interleaving is exactly what `TestStopWaitsOutMoveAndPreservesPosition` exercises.)

- [ ] **Step 5: Commit**

```bash
git add internal/device/valve/
git commit -m "feat: add valve stop with settle-and-report semantics

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: `configure` — RAM-only mirror with inverted hold encoding

**Files:**
- Modify: `internal/device/valve/commands.go` (add `configure`)
- Modify: `internal/device/valve/valve.go` (Execute case `configure`)
- Test: `internal/device/valve/configure_test.go`

**Interfaces:**
- Consumes: `rotationFrame`/`holdFrame`/`rotationCode`, `persistNow`, `lastPushed`.
- Produces: `configure(params json.RawMessage) (any, *device.CmdError)` returning the full effective `configBlock`.

- [ ] **Step 1: Write the failing tests**

Create `internal/device/valve/configure_test.go`:

```go
package valve_test

import (
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// TestConfigureFramesAndEcho pins the exact frames — including the INVERTED
// hold-torque encoding: N3=0 means hold ON (stepper stays energized), N3=1
// means hold OFF.
func TestConfigureFramesAndEcho(t *testing.T) {
	f := newFixture(t, 0)
	resp := f.exec("configure", `{"default_rotation":"direct","hold_torque":true}`)
	if resp.Status != "ok" {
		t.Fatalf("configure: %+v", resp)
	}
	m := f.resultMap(resp)
	if m["default_rotation"] != "direct" || m["hold_torque"] != true {
		t.Fatalf("echo: %v", m)
	}
	fr := f.frames()
	n := len(fr)
	if !frameEq(fr[n-2], 35, 1, 1, 0, 0) || !frameEq(fr[n-1], 35, 2, 0, 0, 0) {
		t.Fatalf("configure frames: %v", fr)
	}
	cfg := readState(t, f.dir)["config"].(map[string]any)
	if cfg["default_rotation"] != "direct" || cfg["hold_torque"] != true {
		t.Fatalf("persisted config: %v", cfg)
	}

	// hold OFF → N3 = 1; omitted rotation stays untouched
	m = f.resultMap(f.exec("configure", `{"hold_torque":false}`))
	if m["default_rotation"] != "direct" || m["hold_torque"] != false {
		t.Fatalf("partial echo: %v", m)
	}
	fr = f.frames()
	if !frameEq(fr[len(fr)-1], 35, 2, 1, 0, 0) {
		t.Fatalf("hold-off frame: %v", fr)
	}
	if countFrames(fr, 35) != 5 { // 2 attach + 2 full configure + 1 partial
		t.Fatalf("unexpected config frame count: %v", fr)
	}
}

func TestConfigureEmptyEchoesCurrent(t *testing.T) {
	f := newFixture(t, 0)
	n := len(f.port.Written())
	m := f.resultMap(f.exec("configure", `{}`))
	if m["default_rotation"] != "shortest" || m["hold_torque"] != false {
		t.Fatalf("echo: %v", m)
	}
	if len(f.port.Written()) != n {
		t.Fatal("no fields provided → no frames")
	}
}

func TestConfigureValidation(t *testing.T) {
	f := newFixture(t, 0)
	resp := f.exec("configure", `{"default_rotation":"spiral"}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeInvalidParams {
		t.Fatalf("resp: %+v", resp)
	}
}

func TestConfigureBusyDuringMove(t *testing.T) {
	f := newHomedFixture(t, 0)
	f.port.Feed([]byte{30, 1, 1, 0})
	startMove(t, f, `{"position":2}`)
	resp := f.exec("configure", `{"hold_torque":true}`)
	if resp.Status != "error" || resp.Error.Code != device.CodeBusy {
		t.Fatalf("configure during move: %+v", resp)
	}
}

// TestConfigureSurvivesRestart: the JSON contract's "settings persist
// across power cycles" promise is honored by the TRANSLATOR — persisted
// mirror, re-pushed on the next attach.
func TestConfigureSurvivesRestart(t *testing.T) {
	f := newFixture(t, 0)
	if resp := f.exec("configure", `{"default_rotation":"wrap","hold_torque":true}`); resp.Status != "ok" {
		t.Fatalf("configure: %+v", resp)
	}
	dir := f.dir
	f.s.Close()
	f2 := newFixture(t, 0, withStateDir(dir))
	fr := f2.frames()
	if !frameEq(fr[1], 35, 1, 2, 0, 0) || !frameEq(fr[2], 35, 2, 0, 0, 0) {
		t.Fatalf("restart must push the persisted config: %v", fr)
	}
}

// TestConfigureUpdatesModeDedup: a configured default becomes the
// last-pushed mode, so the next default-mode move skips the 35 frame.
func TestConfigureUpdatesModeDedup(t *testing.T) {
	f := newHomedFixture(t, 0)
	if resp := f.exec("configure", `{"default_rotation":"direct"}`); resp.Status != "ok" {
		t.Fatalf("configure: %+v", resp)
	}
	before := countFrames(f.frames(), 35)
	f.port.Feed([]byte{30, 1, 1, 0})
	startMove(t, f, `{"position":2}`) // default (direct) already pushed by configure
	if countFrames(f.frames(), 35) != before {
		t.Fatal("move must not re-push the mode configure just set")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/device/valve/ -run TestConfigure -v`
Expected: FAIL — `configure` returns `unknown_command`.

- [ ] **Step 3: Implement**

Append to `internal/device/valve/commands.go` (add `"encoding/json"` and `"time"` to its imports):

```go
// configure (TRANSLATION.md §4): push the provided fields to the firmware
// and persist the mirror. The JSON contract's "settings persist across
// power cycles" promise is honored by the TRANSLATOR (persisted mirror +
// re-push at attach and on every reboot detection), not by the device — the
// firmware keeps its config in RAM only and offers no readback.
func (d *Driver) configure(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		DefaultRotation *string `json:"default_rotation"`
		HoldTorque      *bool   `json:"hold_torque"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
		}
	}
	if j := d.s.Jobs().Active(); j != nil {
		return nil, device.ErrBusy("a move is in progress", map[string]any{"job_id": j.ID})
	}
	if p.DefaultRotation != nil {
		code, ok := rotationCode(*p.DefaultRotation)
		if !ok {
			return nil, device.ErrInvalidParams("default_rotation", *p.DefaultRotation,
				`default_rotation must be "shortest", "direct" or "wrap"`)
		}
		if _, err := d.s.Transact(rotationFrame(code), 0, time.Second); err != nil {
			return nil, device.ErrHardware("rotation config frame: " + err.Error())
		}
		d.config.DefaultRotation = *p.DefaultRotation
		d.lastPushed = *p.DefaultRotation
	}
	if p.HoldTorque != nil {
		if _, err := d.s.Transact(holdFrame(*p.HoldTorque), 0, time.Second); err != nil {
			return nil, device.ErrHardware("hold config frame: " + err.Error())
		}
		d.config.HoldTorque = *p.HoldTorque
	}
	if p.DefaultRotation != nil || p.HoldTorque != nil {
		if err := d.persistNow(); err != nil {
			return nil, device.ErrInternal("persist config: " + err.Error())
		}
	}
	return d.config, nil
}
```

In `valve.go`'s `Execute` switch, add:

```go
	case "configure":
		return d.configure(params)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/device/valve/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/device/valve/
git commit -m "feat: add valve configure command

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 10: Pre-flight and PR

**Files:** none new (fixes only if pre-flight finds issues).

- [ ] **Step 1: Full pre-flight (CLAUDE.md)**

```bash
gofmt -l .                     # must print nothing
go vet ./...
golangci-lint run
go test -race -count=1 ./...
govulncheck ./...
```

Expected: all clean. Fix anything found (gofmt in place, lint by amending the offending code) and commit fixes as `fix:`/`refactor:` follow-ups.

- [ ] **Step 2: Sanity-check the diff against the contracts**

Skim `git diff main --stat` — the change set must be exactly: `internal/device/envelope.go`, `internal/device/session.go`, the two core test files, and `internal/device/valve/**`. Grep the branch for the forbidden word:

```bash
git log main..HEAD --format=%B | grep -i breaking && echo "FOUND — FIX BEFORE PR" || echo ok
```

Expected: `ok`.

- [ ] **Step 3: Push and open the PR**

```bash
git push -u origin valve-driver
gh pr create --title "feat: add distribution valve device driver" --body "$(cat <<'EOF'
## Summary
- `internal/device/valve`: distribution-valve driver implementing `docs/protocol_translation_docs/distribution_valve/JSON_PROTOCOL.md` over the legacy 5-byte firmware per `TRANSLATION.md` (v2 PR 4 of 5, spec `docs/superpowers/specs/2026-07-05-json-device-protocol-design.md`)
- Virtual homing: physical position and device belief tracked separately; every target translated through the offset (mod S = N+1); CHECK_BELIEF before every move and on 30 s idle ticks turns silent reboots into auto-recovery or an explicit unhomed state
- Port-keyed persistence (`valve-<port>.json`) on every successful move/home/configure/detach; restart recovery per TRANSLATION §3
- Clock-driven completion (slots × 0.92 s + 0.3 s margin) with post-move readback; no watcher goroutines
- `stop` = documented settle-and-report deviation (firmware cannot abort); blocks its session loop ≤ ~6 s per spec §3
- Core additions: `device.ErrNotHomed`, `Session.Sleep` (loop-blocking wait on the injectable clock)
- Flagged deviations documented in `docs/superpowers/plans/2026-07-06-valve-driver.md`

## Test plan
- [ ] `go test -race -count=1 ./...` green on macOS and Windows CI
- [ ] gofmt/vet/golangci-lint/govulncheck clean
- [ ] Table tests: offset translation (`planMove`), wrap Δ=0 guard, inverted hold-torque frames, reboot recovery paths, stop settle semantics, restart recovery

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 4: Verify CI**

Watch `gh pr checks --watch` until the `verify` job is green.

---

## Execution Handoff

Plan complete. Execute with **superpowers:subagent-driven-development** (user's chosen process): fresh subagent per task, two-stage review between tasks, on branch `valve-driver`.
