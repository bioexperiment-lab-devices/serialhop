# Status-lamp refresh triggers — design

**Date:** 2026-05-12
**Status:** approved
**Scope:** SerialHop control panel UI — Server and Tunnel lamps
**Related:** `2026-05-11-status-lamps-design.md` (introduces the lamps), `2026-05-11-config-server-info-design.md` (first-run credentials dialog).

## Problem

The Server and Tunnel lamps refresh on a fixed 10-second ticker (`probeLoop` in `internal/panel/probe.go:85`). User actions that change tunnel state — most visibly clicking **Uninstall** in the Update / service controls — don't influence the lamps until the next tick. The Tunnel lamp continues to render "Connected" (green) for up to ~10 s after the chisel tunnel has already been torn down by the elevated subprocess.

The same staleness applies to **Install** (tunnel comes up — lamp lags green), **Restart** (tunnel briefly drops — lamp shows steady green throughout), and **Credentials saved** in the first-run dialog (new auth doesn't get re-probed until the next tick).

The fix needs two pieces:

1. **Event triggers.** Action handlers should be able to ask the relevant probe loops to re-run *now*.
2. **A transition state.** Re-probes can take up to 5 s (HTTP timeout). During that window the lamp must visually acknowledge that the previous reading is stale — otherwise the user clicks, sees the lamp unchanged for several seconds, and assumes nothing happened.

## Goals

- After a user-initiated action that can change tunnel/server state, the affected lamp(s) visibly enter a "Checking…" state within ~100 ms.
- Probes re-run on demand, not only on the 10-second ticker.
- The periodic ticker stays as a drift-detection fallback (covers state changes the UI cannot observe — e.g., chisel server going down on its own), and is **slowed from 10 s to 30 s** since user-initiated changes are now covered by triggers.
- No regressions in cross-platform test coverage (changes compile and unit-test on macOS + Windows).

## Non-goals

- **Window focus / wake-from-sleep triggers.** Rejected during brainstorming — adds complexity without solving the user-visible bug.
- **Replacing the periodic ticker with pure on-demand probing.** Loses the safety net for state drift.
- **Refactoring the lamp state machine into a pub/sub event bus.** Three lamps and five trigger sites don't justify the abstraction.
- **Service lamp triggers.** The service lamp already updates on a 1-second ticker against the local SCM (`refresh()` in `panel.go:162`), which is authoritative and fast. No change needed there.

## Design

### Architecture

Three pieces, all inside `internal/panel`:

1. **`probeLoop` gains a trigger channel.** The loop `select`s on `ticker.C OR trigger OR ctx.Done()`. Periodic ticking is preserved.
2. **Panel owns two trigger channels** (one per net lamp) plus a `kickProbes(server, tunnel bool)` helper that flips the affected lamps to `netStateChecking`, repaints, and does a non-blocking send on the trigger channels.
3. **Action handlers call `kickProbes` at well-defined moments.** For admin actions, this fires twice — once on click (for instant UX), once after the elevated subprocess returns (to settle to real state).

No new production files. Touches `panel.go`, `probe.go`, and the credentials-dialog save handler from #59. Adds one test file (`probe_test.go`).

### Trigger plumbing

**`probeLoop` signature change** (`internal/panel/probe.go:85`)

```go
func probeLoop(ctx context.Context, interval time.Duration,
    trigger <-chan struct{}, fn func(context.Context)) {
    call := func() {
        defer func() {
            if r := recover(); r != nil {
                writePanelDebugLog("probe_panic", errors.New(panicString(r)))
            }
        }()
        fn(ctx)
    }
    call() // existing behavior: prime the lamp before the first tick
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

The change vs. the current loop (`probe.go:85–105`) is the added `trigger <-chan struct{}` parameter and the matching `<-trigger` case in the select. The signature for `fn` (`func(context.Context)`) is preserved. UI-thread marshalling stays inside the `fn` closure (which calls `mw.Synchronize(repaintLamps)` after the probe writes to `state`).

**Trigger channels owned by the panel** (replacing the goroutine spawn at `panel.go:349–366`)

```go
serverTrigger := make(chan struct{}, 1)
tunnelTrigger := make(chan struct{}, 1)
go probeLoop(probeCtx, probeInterval, serverTrigger, func(ctx context.Context) { ... existing body ... })
go probeLoop(probeCtx, probeInterval, tunnelTrigger, func(ctx context.Context) { ... existing body ... })
```

The two channels are closure-captured locals inside `Run()` — alongside `state`, `repaintLamps`, etc. There is no `panel` struct today; the whole panel lives inside `Run()`'s closure scope.

**Non-blocking send helper** — a local closure inside `Run()`:

```go
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

```go
// trySend sends one value if the buffer has room; otherwise drops it.
// Used so callers on the UI thread can wake a probe loop without
// blocking. Buffer=1 + non-blocking send coalesces bursts.
func trySend(ch chan<- struct{}) {
    select {
    case ch <- struct{}{}:
    default:
    }
}
```

`trySend` lives in `probe.go` since it pairs conceptually with `probeLoop`. `kickProbes` stays inside `Run()` as a closure (matches the existing pattern for `refresh` and `repaintLamps`).

Coalescing falls out of buffer=1 + non-blocking send: spamming any trigger source queues at most one extra probe regardless of how many sends arrive.

### Transition state

The lamp kind `lampChecking` already exists in the lamp state machine (`lampstate.go`) and renders gray with label "Checking…" — the same look the user sees at app startup before the first probe completes. Setting `state.setServer(netLamp{kind: lampChecking})` (or `setTunnel`) reuses this; zero new visual vocabulary.

The flow is:

1. User clicks Uninstall.
2. `kickProbes(true, true)` runs **synchronously on the UI thread**: state flips to `netStateChecking`, lamps repaint gray → user sees instant feedback (<100 ms).
3. The non-blocking send unblocks the probe goroutine, which runs the HTTP probe.
4. Probe completes (within 5 s); `mw.Synchronize(repaintLamps)` paints the real result (green / red / etc.).

A brief gray flicker when state hasn't actually changed is accepted as honest "we checked" feedback rather than masked with previous-value retention.

### Trigger sites

| Site | File:line | When to fire | Lamps to kick |
|---|---|---|---|
| Service Install | `panel.go:317` (`performAdmin("install", …)`) | Once before the UAC subprocess, once after it returns | Server + Tunnel |
| Service Uninstall | `panel.go:318` (`performAdmin("uninstall", …)`) | Same | Server + Tunnel |
| Service Restart | `panel.go:319` (`performAdmin("restart", …)`) | Same | Server + Tunnel |
| Update install | `panel.go:661` (`ctlInstall` → `RunElevatedAdminAction("update", …)`) | Once before the subprocess, once after | Server + Tunnel |

**Credentials-saved trigger — deferred.** The first-run credentials dialog (`credsdialog_windows.go`) runs synchronously *before* `MainWindow.Create()` at `panel.go:339` — i.e., before the probe goroutines exist. When the panel opens, `probeLoop`'s initial `call()` (`probe.go:94`) already fetches with the fresh credentials. No trigger site exists today. If a post-launch "Edit credentials" button is added later, its save handler should call `kickProbes(true, true)`.

The double-fire for admin actions is deliberate: the UAC subprocess can take 5–30 s. Without the pre-trigger the lamp would stay green throughout — defeating the "instant response" goal. The post-trigger is what discovers the new actual state.

`performAdmin` (`panel.go:207`) becomes:

```go
performAdmin := func(action, successMsg string) {
    btnInstall.SetEnabled(false)
    btnUninstall.SetEnabled(false)
    btnRestart.SetEnabled(false)
    statusBar.SetText("Working…")

    kickProbes(true, true) // NEW: instant gray, instant feedback

    errMsg, err := RunElevatedAdminAction(action) // existing
    // ... existing status-bar handling ...
    refresh()              // existing — service lamp + buttons
    kickProbes(true, true) // NEW: re-probe for real state
}
```

`ctlInstall` (`panel.go:646`) gets the same treatment around its `RunElevatedAdminAction("update", …)` call.

The panel currently exposes only Install / Uninstall / Restart admin actions (`panel.go:317–319`) — no Service Start / Stop buttons. The trigger surface is fixed at four sites.

### Threading

- `kickProbes` runs on the UI thread (called from button click handlers and from `performAdmin`, both of which the Walk main loop already dispatches there). Lamp state mutations and `repaintLamps()` are therefore main-thread, matching the existing model.
- The non-blocking send is safe from any thread; channels are the synchronization primitive.
- The probe goroutine's `mw.Synchronize(repaintLamps)` call (already in `runServerProbe` / `runTunnelProbe`) handles the result paint.

### Probe interval

The probe-loop ticker is **slowed from 10 s to 30 s** as part of this change. Rationale:

- Every action that can change Server/Tunnel state from the client side now fires an explicit trigger; the ticker no longer carries the responsiveness burden.
- The ticker's remaining job is detecting drift the UI can't observe — chisel-server crashes, transient network loss, auth expiry. Half-a-minute visibility for those events is acceptable for an ambient indicator.
- Network chatter to the lab-bridge VPS drops 3× (from ~6 probes/min to ~2 per lamp), with negligible UX cost.

No named constant exists today — the value `10*time.Second` is duplicated as a literal at `panel.go:349` and `panel.go:358`. This plan introduces `probeInterval = 30 * time.Second` (in `probe.go`, alongside the existing `probeTimeout`) and replaces both literals with it.

### Coalescing & edge cases

- **Rapid repeat triggers.** Buffer=1 + non-blocking send caps queued probes at one. Bounded work.
- **Trigger arrives after panel shutdown.** `ctx.Done()` wins over `<-trigger` once the context is cancelled. A trigger sent between cancel and goroutine exit is absorbed by the buffered channel and discarded with the channel itself.
- **Probe in flight when trigger fires.** The trigger queues; probeLoop picks it up immediately after the current run returns. No interruption of the in-flight probe.
- **Lamp briefly shows gray when nothing changed.** Accepted (Q2 decision during brainstorming).

## Testing

Tests must pass on macOS + Windows (per `CLAUDE.md`). All new code is platform-neutral Go (channels, structs, function calls) — no new `_windows.go` files needed.

New `internal/panel/probe_test.go`:

1. **`probeLoop` trigger fires the callback.** Fake `run` callback records invocations. Send on the trigger channel, assert the callback runs once.
2. **Coalescing.** Block the fake `run` callback on a gate. Fire 5 sends rapidly. Release the gate. Assert the callback ran at most 2 times total (the in-flight one + one queued).
3. **Ticker still fires.** Use a short interval (e.g., 20 ms) with a 100 ms test budget; assert at least 2 ticker-driven invocations with no trigger sends.
4. **Context cancellation wins.** Cancel the context, assert the goroutine returns within a short timeout.

New unit test for `trySend`: send twice to a buffer=1 channel via `trySend`, verify neither call blocks and only one item is queued.

`kickProbes` lives as a closure inside `Run()` and is only reachable from a Windows build (depends on `walk.MainWindow`). Direct unit testing of it would require exfiltrating it from the closure; not worth the surgery. Its three behaviors — flip lamp to `lampChecking`, send on trigger, repaint — are simple enough to verify by manual smoke test (click Uninstall, observe gray lamp within ~100 ms). The platform-neutral `trySend` and `probeLoop` carry the testable invariants.

## Risks

- **Trigger during startup before goroutines spawn.** Not currently possible — trigger sites are user-driven; the panel is fully constructed before the UI thread services events.
- **Future trigger sites forget to call `kickProbes`.** Mitigated by colocating the helper with the lamp state and naming it after its effect; covered in PR review.

## Out of scope

Listed in Non-goals above. The most likely follow-up — auto-refresh on window focus regain — is left for a separate spec if it ever becomes a felt need.
