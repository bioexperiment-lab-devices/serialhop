# Lamp refresh triggers implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire user-initiated actions (Install / Uninstall / Restart / Update install) to fire on-demand network probes so the Server and Tunnel lamps reflect new state in <1 s instead of waiting up to 10 s for the next tick. Slow the periodic tick from 10 s to 30 s now that triggers carry the responsiveness burden.

**Architecture:** Add a `trigger <-chan struct{}` parameter to `probeLoop` (one extra `select` case). Allocate one trigger channel per net lamp inside `Run()`. Action handlers call a local `kickProbes(server, tunnel bool)` closure that flips the relevant lamps to `lampChecking` (gray "Checking…"), repaints, and non-blocking-sends on the trigger channels. Buffer=1 + non-blocking send coalesces burst sends naturally. The 30 s ticker stays as a fallback for state drift the UI can't observe.

**Tech Stack:** Go 1.x, channels for signalling, `lxn/walk` for the UI (Windows-only via build tags), existing `lampState` mutex-guarded state machine.

**Spec:** `docs/superpowers/specs/2026-05-12-lamp-refresh-triggers-design.md`

---

## File map

- **Modify:** `internal/panel/probe.go` — add `trySend` helper, add `trigger` parameter to `probeLoop`, introduce `probeInterval = 30 * time.Second` constant.
- **Modify:** `internal/panel/panel.go` — allocate trigger channels, add `kickProbes` closure, wire it into `performAdmin` and `ctlInstall`, replace `10*time.Second` literals with `probeInterval`.
- **Create:** `internal/panel/probe_test.go` — tests for `trySend`, `probeLoop` trigger behavior, coalescing, ticker-still-fires, and ctx cancellation. No build tag (platform-neutral).

No other files are touched. The lamp state machine (`lampstate.go`), credentials dialog, and update state machine (`update_state.go`) are unchanged.

---

## Task 1: Add `trySend` helper with test

**Files:**
- Create: `internal/panel/probe_test.go`
- Modify: `internal/panel/probe.go` (append after existing helpers)

- [ ] **Step 1: Write the failing test**

Append to a new file `internal/panel/probe_test.go`:

```go
package panel

import (
	"testing"
)

func TestTrySend_DeliversToEmptyBuffer(t *testing.T) {
	ch := make(chan struct{}, 1)
	trySend(ch)
	select {
	case <-ch:
		// got it
	default:
		t.Fatal("trySend did not deliver to empty buffered channel")
	}
}

func TestTrySend_DropsWhenBufferFull(t *testing.T) {
	ch := make(chan struct{}, 1)
	trySend(ch) // fills the buffer
	trySend(ch) // must not block; must not panic
	// Buffer should still hold exactly one item.
	<-ch
	select {
	case <-ch:
		t.Fatal("trySend queued more than one item in a buffer=1 channel")
	default:
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/panel/... -run TestTrySend -v`
Expected: FAIL with `undefined: trySend`

- [ ] **Step 3: Add `trySend` to `probe.go`**

Append at the bottom of `internal/panel/probe.go`:

```go
// trySend delivers one signal to ch if its buffer has room; otherwise
// drops the signal. Used by UI-thread callers to wake a probe goroutine
// without ever blocking. Pair with a chan struct{} of buffer=1 — that
// combination naturally coalesces bursts to at most one extra run.
func trySend(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/panel/... -run TestTrySend -v`
Expected: PASS for both `TestTrySend_DeliversToEmptyBuffer` and `TestTrySend_DropsWhenBufferFull`.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/probe.go internal/panel/probe_test.go
git commit -m "$(cat <<'EOF'
test: add trySend helper for coalesced channel signalling

Helper that pairs with a buffer=1 channel to give UI-thread callers
a non-blocking way to wake probe goroutines. Burst sends coalesce
to at most one queued item.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Add `trigger` parameter to `probeLoop`

**Files:**
- Modify: `internal/panel/probe.go:85-105` (probeLoop signature and body)
- Modify: `internal/panel/panel.go:349,358` (both probeLoop call sites — pass `nil` for now; Task 3 wires the real channels)
- Modify: `internal/panel/probe_test.go` (add trigger test)

- [ ] **Step 1: Write the failing test**

Append to `internal/panel/probe_test.go`:

```go
import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeLoop_TriggerFiresCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	trigger := make(chan struct{}, 1)

	// Use a very long tick interval so any callback invocation we see
	// can only have come from the trigger (or the initial priming call).
	done := make(chan struct{})
	go func() {
		probeLoop(ctx, time.Hour, trigger, func(context.Context) {
			calls.Add(1)
		})
		close(done)
	}()

	// Wait for the priming call (probeLoop runs fn once before entering
	// the ticker select).
	waitFor(t, func() bool { return calls.Load() >= 1 }, time.Second)

	// Send a trigger; expect a second invocation.
	trigger <- struct{}{}
	waitFor(t, func() bool { return calls.Load() >= 2 }, time.Second)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("probeLoop did not return after ctx cancel")
	}
}

// waitFor polls cond until it returns true or timeout elapses.
// Test helper kept private to this file.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not satisfied within %v", timeout)
}
```

Merge the new `import` block with the existing one at the top of the file (single `import (...)` block, sorted).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/panel/... -run TestProbeLoop_TriggerFiresCallback -v`
Expected: FAIL — compile error (`too few arguments to probeLoop` or similar) because `probeLoop` doesn't yet accept a trigger channel.

- [ ] **Step 3: Update `probeLoop` signature and add the trigger select case**

Replace the existing `probeLoop` function in `internal/panel/probe.go` (lines 79–105) with:

```go
// probeLoop runs fn(ctx) immediately, then again on every tick of a
// time.Ticker(interval), until ctx is canceled. fn is expected to be
// short-running (a single HTTP request with its own timeout); if fn
// outlasts a tick, the next tick simply waits — no concurrent invocations.
// A defer/recover wraps each call so a panic in net/http or JSON parsing
// doesn't kill the panel; panics are reported via writePanelDebugLog.
//
// A receive on trigger also invokes fn — used by UI-thread callers
// (action handlers) to refresh a lamp without waiting for the next tick.
// trigger may be nil, in which case the trigger case never selects.
func probeLoop(ctx context.Context, interval time.Duration, trigger <-chan struct{}, fn func(context.Context)) {
	call := func() {
		defer func() {
			if r := recover(); r != nil {
				writePanelDebugLog("probe_panic", errors.New(panicString(r)))
			}
		}()
		fn(ctx)
	}
	call()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			call()
		case <-trigger:
			call()
		}
	}
}
```

- [ ] **Step 4: Update both call sites in `panel.go` to pass `nil` trigger**

At `internal/panel/panel.go:349`, change:

```go
go probeLoop(probeCtx, 10*time.Second, func(ctx context.Context) {
```

to:

```go
go probeLoop(probeCtx, 10*time.Second, nil, func(ctx context.Context) {
```

At `internal/panel/panel.go:358`, change:

```go
go probeLoop(probeCtx, 10*time.Second, func(ctx context.Context) {
```

to:

```go
go probeLoop(probeCtx, 10*time.Second, nil, func(ctx context.Context) {
```

(Real channels go in Task 3 — wiring them now would conflict with `nil` checks in the new test.)

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/panel/... -run TestProbeLoop -v`
Expected: PASS for `TestProbeLoop_TriggerFiresCallback`.

Also run: `go build ./...` to confirm panel.go still compiles on Windows builds. (On macOS, `internal/panel/panel.go` is excluded by the `//go:build windows` tag, so `go build` skips it — the change must still be made because the Windows CI verify job will catch it.)

Run: `GOOS=windows go vet ./internal/panel/...`
Expected: no errors. (`go vet` does the build-tag-aware compilation.)

- [ ] **Step 6: Commit**

```bash
git add internal/panel/probe.go internal/panel/probe_test.go internal/panel/panel.go
git commit -m "$(cat <<'EOF'
feat(panel): allow probeLoop to be triggered on demand

Adds a trigger <-chan struct{} parameter so callers can wake a probe
goroutine without waiting for the next tick. Existing call sites pass
nil (preserving current behavior); the next commit wires real channels.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Test coalescing and ticker-still-fires

**Files:**
- Modify: `internal/panel/probe_test.go`

- [ ] **Step 1: Write the coalescing test**

Append to `internal/panel/probe_test.go`:

```go
func TestProbeLoop_TriggerCoalescesViaBufferOne(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gate := make(chan struct{})
	var calls atomic.Int32
	trigger := make(chan struct{}, 1)

	done := make(chan struct{})
	go func() {
		probeLoop(ctx, time.Hour, trigger, func(context.Context) {
			calls.Add(1)
			<-gate // block until the test releases
		})
		close(done)
	}()

	// Wait for the priming call to enter and block on the gate.
	waitFor(t, func() bool { return calls.Load() == 1 }, time.Second)

	// Fire 5 trySends rapidly. The buffer=1 + non-blocking send means
	// at most one signal is queued.
	for i := 0; i < 5; i++ {
		trySend(trigger)
	}

	// Release the gate once. probeLoop returns from the in-flight call,
	// enters the select, sees one queued trigger, runs fn a second time
	// (which will block on gate again).
	gate <- struct{}{}
	waitFor(t, func() bool { return calls.Load() == 2 }, time.Second)

	// Release the gate again. probeLoop returns, enters select. The
	// trigger channel is empty (coalesced), the ticker is at 1h, and
	// ctx is still live — so no further calls happen.
	gate <- struct{}{}
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 2 {
		t.Fatalf("trigger spam did not coalesce: got %d calls, want 2", got)
	}

	cancel()
	// Drain a final gate release in case the goroutine is between calls.
	select {
	case gate <- struct{}{}:
	default:
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("probeLoop did not return after ctx cancel")
	}
}

func TestProbeLoop_TickerKeepsFiring(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		probeLoop(ctx, 20*time.Millisecond, nil, func(context.Context) {
			calls.Add(1)
		})
		close(done)
	}()

	// Initial priming call + at least two ticker-driven calls within 200 ms.
	waitFor(t, func() bool { return calls.Load() >= 3 }, 500*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("probeLoop did not return after ctx cancel")
	}
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/panel/... -run TestProbeLoop -v -race`
Expected: PASS for all four `TestProbeLoop_*` tests (the trigger test from Task 2 plus the two new ones).

`-race` matters: probe goroutines write to atomics; the race detector catches improper synchronization.

- [ ] **Step 3: Commit**

```bash
git add internal/panel/probe_test.go
git commit -m "$(cat <<'EOF'
test(panel): verify probe trigger coalescing and ticker behavior

Coalescing test fires 5 sends while the callback is gated, releases,
asserts the callback ran exactly twice (the in-flight call + one queued).
Ticker test asserts periodic invocations continue with a nil trigger.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Introduce `probeInterval` constant (30 s) and wire trigger channels

**Files:**
- Modify: `internal/panel/probe.go` (add `probeInterval` constant near `probeTimeout`)
- Modify: `internal/panel/panel.go` (allocate trigger channels, pass them to `probeLoop`, replace `10*time.Second` literals, add `kickProbes` closure)

- [ ] **Step 1: Add `probeInterval` constant**

In `internal/panel/probe.go`, just below the `probeTimeout` constant (currently at line 12–14), add:

```go
// probeInterval is the periodic-tick cadence for runServerProbe /
// runTunnelProbe. Slow (30 s) because explicit triggers from action
// handlers now cover the responsive cases — the periodic tick exists
// only to detect drift the UI can't observe (server going down, etc.).
const probeInterval = 30 * time.Second
```

The block becomes:

```go
const probeTimeout = 5 * time.Second

const probeInterval = 30 * time.Second
```

- [ ] **Step 2: Allocate trigger channels and `kickProbes` closure in `Run()`**

In `internal/panel/panel.go`, locate the block at lines 343–347:

```go
probeCtx, probeCancel := context.WithCancel(context.Background())
defer probeCancel()
mw.Closing().Attach(func(_ *bool, _ walk.CloseReason) { probeCancel() })

probeHC := &http.Client{Timeout: 30 * time.Second} // fallback; per-call 5s ctx in probe.go still primary
```

Immediately after this block (before the two `go probeLoop(...)` calls), insert:

```go
serverTrigger := make(chan struct{}, 1)
tunnelTrigger := make(chan struct{}, 1)

// kickProbes flips the affected lamps to "Checking…" and wakes their
// probe goroutines. Must be called from the UI thread (it mutates
// lamp state and repaints). Non-blocking — safe to call from button
// handlers and from inside performAdmin.
kickProbes := func(server, tunnel bool) {
    if server {
        state.setServer(netLamp{kind: lampChecking})
        trySend(serverTrigger)
    }
    if tunnel {
        state.setTunnel(netLamp{kind: lampChecking})
        trySend(tunnelTrigger)
    }
    repaintLamps()
}
```

- [ ] **Step 3: Replace `10*time.Second` literals and pass the trigger channels**

At what is now `internal/panel/panel.go:349` (the server probeLoop call), change:

```go
go probeLoop(probeCtx, 10*time.Second, nil, func(ctx context.Context) {
```

to:

```go
go probeLoop(probeCtx, probeInterval, serverTrigger, func(ctx context.Context) {
```

At the tunnel probeLoop call (was line 358), change:

```go
go probeLoop(probeCtx, 10*time.Second, nil, func(ctx context.Context) {
```

to:

```go
go probeLoop(probeCtx, probeInterval, tunnelTrigger, func(ctx context.Context) {
```

- [ ] **Step 4: Suppress unused-variable warning during this commit**

`kickProbes` is added now but not yet called (Task 5 wires it). To avoid a `declared and not used` compile error, add a single discard line right after the closure definition:

```go
_ = kickProbes
```

Place it immediately after the `kickProbes := func(...) { ... }` block. Task 5 removes this line when it adds the first real call site.

- [ ] **Step 5: Build for Windows and confirm panel.go compiles**

Run: `GOOS=windows go vet ./internal/panel/...`
Expected: no errors.

Run: `go test ./internal/panel/... -race`
Expected: all platform-neutral tests still pass.

- [ ] **Step 6: Commit**

```bash
git add internal/panel/probe.go internal/panel/panel.go
git commit -m "$(cat <<'EOF'
feat(panel): wire on-demand probe triggers; slow tick to 30s

Introduces probeInterval = 30s (was 10s, hardcoded). Allocates one
trigger channel per net lamp and a kickProbes closure that flips
the affected lamps to "Checking…" and wakes the probe goroutines.
Action-handler wiring follows in the next commit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Fire `kickProbes` around `performAdmin`

**Files:**
- Modify: `internal/panel/panel.go:207-227` (the `performAdmin` closure)

- [ ] **Step 1: Remove the `_ = kickProbes` discard line from Task 4**

Delete the line `_ = kickProbes` added in Task 4 Step 4.

- [ ] **Step 2: Add kickProbes calls in `performAdmin`**

Replace the existing `performAdmin` closure (currently `panel.go:207-227`):

```go
performAdmin := func(action, successMsg string) {
    btnInstall.SetEnabled(false)
    btnUninstall.SetEnabled(false)
    btnRestart.SetEnabled(false)
    statusBar.SetText("Working…")

    errMsg, err := RunElevatedAdminAction(action)
    switch {
    case errors.Is(err, ErrUserCancelled):
        statusBar.SetText("Cancelled.")
    case err != nil:
        walk.MsgBox(mw, "Error", err.Error(), walk.MsgBoxIconError)
        statusBar.SetText("Failed.")
    case errMsg != "":
        walk.MsgBox(mw, "Error", errMsg, walk.MsgBoxIconError)
        statusBar.SetText("Failed.")
    default:
        statusBar.SetText(successMsg + " at " + time.Now().Format("15:04:05"))
    }
    refresh()
}
```

with:

```go
performAdmin := func(action, successMsg string) {
    btnInstall.SetEnabled(false)
    btnUninstall.SetEnabled(false)
    btnRestart.SetEnabled(false)
    statusBar.SetText("Working…")
    kickProbes(true, true) // gray the lamps before the UAC subprocess starts

    errMsg, err := RunElevatedAdminAction(action)
    switch {
    case errors.Is(err, ErrUserCancelled):
        statusBar.SetText("Cancelled.")
    case err != nil:
        walk.MsgBox(mw, "Error", err.Error(), walk.MsgBoxIconError)
        statusBar.SetText("Failed.")
    case errMsg != "":
        walk.MsgBox(mw, "Error", errMsg, walk.MsgBoxIconError)
        statusBar.SetText("Failed.")
    default:
        statusBar.SetText(successMsg + " at " + time.Now().Format("15:04:05"))
    }
    refresh()
    kickProbes(true, true) // re-probe to settle to the new actual state
}
```

The two changes: the new `kickProbes` line right after `statusBar.SetText("Working…")`, and the new `kickProbes` line after the trailing `refresh()`.

- [ ] **Step 3: Build for Windows**

Run: `GOOS=windows go vet ./internal/panel/...`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add internal/panel/panel.go
git commit -m "$(cat <<'EOF'
feat(panel): trigger lamp refresh around Install/Uninstall/Restart

performAdmin now calls kickProbes twice — once before the elevated
subprocess so the lamps go gray immediately (instant UX feedback),
once after it returns to discover the new tunnel state. Fixes the
"Tunnel still shows Connected after clicking Uninstall" staleness.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Fire `kickProbes` around `ctlInstall` (update install path)

**Files:**
- Modify: `internal/panel/panel.go:646-679` (the `ctlInstall` function)

- [ ] **Step 1: Add `mw.Synchronize`-wrapped kickProbes calls**

`ctlInstall` runs in a goroutine (it's launched via `go ctlInstall(...)` from the Install-update button at `panel.go:289`), so lamp mutations must be marshalled onto the UI thread via `mw.Synchronize`. The closure capture is already there — `mw` is a parameter.

But `kickProbes` is a closure inside `Run()` and is not currently passed to `ctlInstall`. The signature needs to gain it.

Update `ctlInstall`'s signature (`panel.go:646-651`) from:

```go
func ctlInstall(
    mw *walk.MainWindow,
    ctl *updateCtl,
    statusBar *walk.Label,
    apply func(UpdateEvent),
) {
```

to:

```go
func ctlInstall(
    mw *walk.MainWindow,
    ctl *updateCtl,
    statusBar *walk.Label,
    apply func(UpdateEvent),
    kickProbes func(server, tunnel bool),
) {
```

Update the single caller at `panel.go:289-292`:

```go
PushButton{AssignTo: &btnInstall2, Text: "Install update", Visible: false, OnClicked: func() {
    go ctlInstall(mw, ctl, statusBar,
        applyUpdateRow(mw, ctl, updateRow, updateLabel, btnDownload, btnInstall2, btnRelease, btnRetry, btnCancelDL))
}},
```

becomes:

```go
PushButton{AssignTo: &btnInstall2, Text: "Install update", Visible: false, OnClicked: func() {
    go ctlInstall(mw, ctl, statusBar,
        applyUpdateRow(mw, ctl, updateRow, updateLabel, btnDownload, btnInstall2, btnRelease, btnRetry, btnCancelDL),
        kickProbes)
}},
```

- [ ] **Step 2: Call `kickProbes` before and after `RunElevatedAdminAction("update", …)`**

In `ctlInstall`, currently:

```go
apply(EvInstallStart)
mw.Synchronize(func() { _ = statusBar.SetText("Installing update…") })

errMsg, err := RunElevatedAdminAction("update", "--update-src="+src)
switch {
case errors.Is(err, ErrUserCancelled):
    mw.Synchronize(func() { _ = statusBar.SetText("Cancelled.") })
    apply(EvCancel)
    return
// ... etc
```

Change to:

```go
apply(EvInstallStart)
mw.Synchronize(func() {
    _ = statusBar.SetText("Installing update…")
    kickProbes(true, true) // gray lamps before the UAC subprocess
})

errMsg, err := RunElevatedAdminAction("update", "--update-src="+src)
switch {
case errors.Is(err, ErrUserCancelled):
    mw.Synchronize(func() {
        _ = statusBar.SetText("Cancelled.")
        kickProbes(true, true) // re-probe even on cancel — the elevated child may have partial side effects
    })
    apply(EvCancel)
    return
case err != nil:
    mw.Synchronize(func() {
        _ = statusBar.SetText("Failed: " + err.Error())
        kickProbes(true, true)
    })
    apply(EvInstallFail)
    return
case errMsg != "":
    mw.Synchronize(func() {
        _ = statusBar.SetText("Failed: " + errMsg)
        kickProbes(true, true)
    })
    apply(EvInstallFail)
    return
}

mw.Synchronize(func() {
    _ = statusBar.SetText("Update applied at " + time.Now().Format("15:04:05"))
    kickProbes(true, true) // re-probe to settle to post-update state
})
apply(EvInstallOK)
```

- [ ] **Step 3: Build for Windows**

Run: `GOOS=windows go vet ./internal/panel/...`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add internal/panel/panel.go
git commit -m "$(cat <<'EOF'
feat(panel): trigger lamp refresh around update install

ctlInstall now gates the elevated update subprocess with kickProbes
(through mw.Synchronize since ctlInstall runs in a goroutine). Lamps
gray immediately on click and re-probe on completion — including the
cancel and failure paths, where partial side effects are possible.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Pre-flight checks and manual smoke test

**Files:** none modified — verification only.

- [ ] **Step 1: Run the full CI verify suite locally**

```bash
gofmt -l internal/panel/
go vet ./...
GOOS=windows go vet ./...
golangci-lint run ./...
go test -race -count=1 ./...
govulncheck ./...
```

All must pass with no output (gofmt) or no errors (the rest). Fix anything that flags.

- [ ] **Step 2: Build the Windows binary**

```bash
task build
```

Expected: produces `dist/SerialHop.exe` with no errors.

- [ ] **Step 3: Document manual smoke test for the Windows reviewer**

The lamp behavior cannot be verified on macOS. Add a "Test plan" section to the PR description (no file change) with these steps:

1. Install SerialHop (Install button) → before clicking, Tunnel lamp is gray "Not configured" or green "Connected".
2. Click **Uninstall**. Within ~100 ms: Tunnel lamp goes gray "Checking…" *and* Server lamp goes gray "Checking…". Status bar reads "Working…".
3. UAC prompt appears; approve.
4. After UAC subprocess completes (a few seconds): Tunnel lamp settles to "Disconnected" (red) or "Not configured" (gray) — *not* the stale "Connected" it would have shown previously.
5. Click **Install**. Same gray flicker. After the subprocess: Tunnel lamp settles to "Connected" (green) within ~5 s, without waiting 10 s for the next tick.
6. Watch the lamps with no interaction. They should refresh every **30 s** now (was 10 s). Confirm via debug log timestamps or simply observing that the lamp updates feel less frequent.

- [ ] **Step 4: No commit needed — verification only**

If Steps 1–2 caught any issues, they got fixed inline in this task; commit them with an appropriate `fix:` or `chore:` message. Otherwise nothing to commit.

---

## Self-review

Re-read the spec (`docs/superpowers/specs/2026-05-12-lamp-refresh-triggers-design.md`) and verify every requirement maps to a task:

- **Goal 1: Triggered refresh on action.** Tasks 5 (Install/Uninstall/Restart) + Task 6 (Update install).
- **Goal 2: Transition state within ~100 ms.** Tasks 5+6 — `kickProbes` flips lamps synchronously on UI thread before any async work begins.
- **Goal 3: 30s periodic ticker as drift fallback.** Task 4 introduces `probeInterval = 30s`.
- **Goal 4: Cross-platform tests still pass.** Tasks 1–3 add tests under `internal/panel/probe_test.go` (no build tag), Task 7 runs the cross-platform vet.
- **Probe interval lives in `probe.go` next to `probeTimeout`.** Task 4 Step 1.
- **`trySend` lives in `probe.go`.** Task 1 Step 3.
- **`kickProbes` lives in `Run()` as a closure.** Task 4 Step 2.
- **Both call sites (server + tunnel) updated.** Task 4 Step 3.
- **Coalescing test.** Task 3 Step 1 (`TestProbeLoop_TriggerCoalescesViaBufferOne`).
- **Ticker-still-fires test.** Task 3 Step 1 (`TestProbeLoop_TickerKeepsFiring`).
- **ctx-cancellation behavior verified.** Task 2 trigger test's `cancel()`/`done` pattern covers it (the deferred goroutine must return after cancel). No separate test required.
- **No new trigger sites speculated.** Spec table is fixed at 4 sites (Install/Uninstall/Restart/Update install); deferred creds trigger noted as future.

No gaps. No placeholders. Type names consistent (`netLamp{kind: lampChecking}` used throughout; `serverTrigger` / `tunnelTrigger` consistent; `kickProbes` signature `func(server, tunnel bool)` consistent across all references).
