# Status lamps — design

**Date:** 2026-05-11
**Status:** approved
**Scope:** SerialHop control panel UI

## Problem

The panel today shows a single "Status: ● Running" line at the top, derived from a local SCM query. Two things are wrong:

1. **The colored dot doesn't render in color** — it's always black. `walk.Label.SetTextColor` doesn't take effect without an explicit repaint, or the label's `WM_CTLCOLORSTATIC` path doesn't honor it under our setup.
2. **It only tells you about the local service.** Operators have no in-panel visibility into (a) whether the lab-bridge VPS itself is healthy, and (b) whether the VPS currently sees an active reverse tunnel from this agent.

The lab-bridge public API exposes two endpoints that answer (a) and (b) cheaply. This change surfaces them in the panel as a row of three status lamps in a dedicated `Status` group.

## Goals

- Three at-a-glance lamps in the panel: **Service** (local service state), **Server** (chisel-server liveness on the VPS), **Tunnel** (does the VPS currently see this agent's reverse tunnel).
- Each lamp is a colored dot plus a short state word.
- Lamps update on a slow tick (10 s) for network probes; 1 s tick for the local service.
- Fix the broken color rendering on the existing service-status dot in the same pass.

## Non-goals

- **Dynamic reverse-tunnel port at service startup.** The lab-bridge `GET /api/public/clients/{user}` endpoint also returns the agent's assigned `port`, which the spec suggests using as the `-R <port>:…` argument to chisel client. That's a service-worker change with its own retry / error-surfacing concerns; deferred to a follow-up.
- **Operator-facing migration tooling.** This change introduces a breaking config schema. No real users yet, so no migration path is provided.
- **Configurable HTTPS port for the public API.** Hard-coded to 443 (YAGNI; easy to add later).

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│ Panel (internal/panel, //go:build windows)                  │
│                                                             │
│  ┌─ Status group ───────────────────────────────┐           │
│  │ Service: ●  Running                          │           │
│  │ Server:  ●  Up                               │           │
│  │ Tunnel:  ●  Connected                        │           │
│  └──────────────────────────────────────────────┘           │
│                                                             │
│  refresh() (1 s tick) ── reads ──► lampState (mutex)        │
│                                       ▲   ▲                 │
│                              writes ──┘   └── writes        │
│                                                             │
│  probeServerLoop  (10 s tick)   probeTunnelLoop (10 s tick) │
│         │                              │                    │
└─────────┼──────────────────────────────┼────────────────────┘
          ▼                              ▼
   labbridge.FetchHealth        labbridge.FetchClient
          │                              │
          └─────────► VPS (HTTPS) ◄──────┘
```

Three components:

- **`internal/labbridge`** *(new package)* — stateless HTTP client for the two public API endpoints. Cross-platform; no walk / no Windows dependency.
- **`internal/panel`** *(modified)* — owns polling goroutines, `lampState`, the new `Status` group widget, and the relocated service-status indicator.
- **`internal/config`** *(modified, breaking)* — new `lab_bridge` section absorbs `host`, `user`, `pass`. `chisel` keeps `port`, `remote_port`.

## Config schema (breaking)

**Before:**

```yaml
chisel:
  server: "111.88.145.138:7000"   # host:port combined
  remote_port: 8081
  user: "devices_coordinator"
  pass: ""
```

**After:**

```yaml
lab_bridge:
  host: "111.88.145.138"          # host or domain — used for chisel + public HTTPS API
  user: "devices_coordinator"     # chisel auth user; also bearer-token identity for the public API
  pass: ""                        # chisel password; also bearer token for /api/public/clients/{user}

chisel:
  port: 7000                      # chisel server port
  remote_port: 8081               # reverse-tunnel port assigned to this agent
```

`internal/config/config.go` types:

```go
type Config struct {
    LabBridge  LabBridgeConfig  `yaml:"lab_bridge"`
    Chisel     ChiselConfig     `yaml:"chisel"`
    Rest       RestConfig       `yaml:"rest"`
    Discovery  DiscoveryConfig  `yaml:"discovery"`
    Log        LogConfig        `yaml:"log"`
    RawSerial  RawSerialConfig  `yaml:"raw_serial"`
    AutoUpdate AutoUpdateConfig `yaml:"auto_update"`
}
type LabBridgeConfig struct {
    Host string `yaml:"host"`
    User string `yaml:"user"`
    Pass string `yaml:"pass"`
}
type ChiselConfig struct {
    Port       int `yaml:"port"`
    RemotePort int `yaml:"remote_port"`
}
```

Call-site updates:

- `internal/chisel/client.go` — `Config.Server` becomes `net.JoinHostPort(host, strconv.Itoa(port))`; `User` / `Pass` come from `cfg.LabBridge`.
- `internal/logship/*` — any reference to `cfg.Chisel.User` repointed to `cfg.LabBridge.User`.
- `internal/panel/panel.go` — the `Chisel server:` configuration-display line composes `<host>:<port>` from the two fields.
- `internal/config/config.go` — `Default()` and the embedded scaffold template updated.

Loader behavior: `gopkg.in/yaml.v3` silently ignores unknown fields. An old config that still has `chisel.server` / `chisel.user` / `chisel.pass` loads with `lab_bridge.host` empty → validator returns "host required" → panel surfaces the existing config-invalid warning row. No migration path; no real users.

## `internal/labbridge` package

Single file, stateless functions. Caller supplies `*http.Client` and `context.Context` — per-request timeouts live at the call site, not in the package.

```go
package labbridge

import (
    "context"
    "errors"
    "net/http"
)

const (
    healthPath   = "/api/public/health"
    clientsPath  = "/api/public/clients/"
    maxBodyBytes = 64 << 10
)

var (
    ErrUnauthorized = errors.New("labbridge: unauthorized")
    ErrServerError  = errors.New("labbridge: server error")
)

// Health is the result of GET /api/public/health.
//
// The endpoint always returns HTTP 200; the chisel up/down signal is the
// JSON body, not the status code.
type Health struct {
    ChiselOK bool   // body.chisel == "ok"
    Detail   string // body.error, if present (e.g. "connection refused")
}

// ClientInfo is the result of GET /api/public/clients/{user}.
type ClientInfo struct {
    Port      int
    Connected bool
}

func FetchHealth(ctx context.Context, hc *http.Client, base, userAgent string) (Health, error)
func FetchClient(ctx context.Context, hc *http.Client, base, user, pass, userAgent string) (ClientInfo, error)
```

Behavior:

- Base URL is composed by the caller as `"https://" + cfg.LabBridge.Host` (port 443 implicit).
- Body read uses `io.LimitReader(resp.Body, maxBodyBytes)`.
- User-Agent passed in from the panel: `"SerialHop/<ver> (status-probe)"`.
- Username URL-escaped via `url.PathEscape`.
- 401 → `ErrUnauthorized` (the spec intentionally makes "unknown user", "wrong token", "missing header", "non-Bearer scheme" indistinguishable; we don't try to disambiguate).
- 5xx → `fmt.Errorf("...: %w", ErrServerError)`.
- Network failure, non-{200,401,5xx} status, JSON parse error → plain `error`. Caller uses `errors.Is` to branch.

## Panel changes

### Layout — new `Status` group

The existing top "Status: ●" row is removed and reconstituted inside a `walk.GroupBox` titled `Status`, holding three rows × three columns: name label, colored dot, state text.

```
┌─ Status ───────────────────────────────┐
│ Service: ●  Running                    │
│ Server:  ●  Up                         │
│ Tunnel:  ●  Connected                  │
└────────────────────────────────────────┘
─── Configuration ─────────────────────────
Chisel server:    111.88.145.138:7000
…
```

Window `MinSize.Height` bumps to accommodate the extra two rows.

### State holder

```go
type lampState struct {
    mu      sync.Mutex
    service serviceLamp
    server  netLamp
    tunnel  netLamp
}
type serviceLamp struct {
    state    winsvc.ServiceState
    cfgValid bool
}
type netLamp struct {
    kind   lampKind
    detail string // optional, surfaced via tooltip on hover
}
type lampKind int
const (
    lampChecking lampKind = iota
    lampOK
    lampDisconnected
    lampAuthFailed
    lampServerError
    lampUnreachable
    lampNotConfigured
    lampChiselDown
)
```

Three pure presentation functions in `state.go` map state values → `(color, text)`: `serviceLampPresentation(serviceLamp)`, `serverLampPresentation(netLamp)`, `tunnelLampPresentation(netLamp)`. The Server and Tunnel functions handle their per-lamp text differences (e.g. `lampOK` renders as `Up` for Server and `Connected` for Tunnel; `lampUnreachable` renders identically). Table-driven, easy to unit-test. All three live outside `//go:build windows` so the macOS test runner exercises them.

### State → color/text mapping

**Service lamp** (derived from existing `StatusIndicator` + SCM query):

| SCM state                          | Color  | Text             |
|------------------------------------|--------|------------------|
| Running                            | green  | `Running`        |
| Start/Stop pending                 | yellow | `Starting…` / `Stopping…` |
| Stopped                            | grey   | `Stopped`        |
| Not installed, config valid        | grey   | `Not installed`  |
| Not installed, config invalid      | red    | `Not installed`  |

**Server lamp** (`GET /api/public/health` every 10 s):

| Probe outcome                                          | Color  | Text          |
|--------------------------------------------------------|--------|---------------|
| 200 + `chisel == "ok"`                                 | green  | `Up`          |
| 200 + `chisel == "down"`                               | red    | `Chisel down` |
| Network error / timeout / non-200 / unparseable        | grey   | `Unreachable` |
| Before first probe                                     | grey   | `Checking…`   |

**Tunnel lamp** (`GET /api/public/clients/{user}` every 10 s):

| Probe outcome                                  | Color  | Text             |
|------------------------------------------------|--------|------------------|
| 200 + `connected: true`                        | green  | `Connected`      |
| 200 + `connected: false`                       | red    | `Disconnected`   |
| 401                                            | red    | `Auth failed`    |
| 500                                            | yellow | `Server error`   |
| Network error / timeout                        | grey   | `Unreachable`    |
| `lab_bridge.pass` empty (short-circuit)        | grey   | `Not configured` |
| Before first probe                             | grey   | `Checking…`      |

Notable choices:

- **`Disconnected` is red, not yellow** — chisel reconnects on its own, but operators should notice and not be lulled by a yellow.
- **`Unreachable` is grey, not red** — distinguishes "we can't tell" from "we know it's bad." When the operator's laptop has no internet, both Server and Tunnel lamps go grey simultaneously; useful diagnostic.

### Polling

In `Run()`, after `mw.Create()` succeeds:

```go
ctx, cancel := context.WithCancel(context.Background())
mw.Closing().Attach(func(_ *bool, _ walk.CloseReason) { cancel() })

state := &lampState{
    server: netLamp{kind: lampChecking},
    tunnel: netLamp{kind: lampChecking},
}
hc := &http.Client{} // per-call timeout via ctx

go probeLoop(ctx, "server", 10*time.Second, func(ctx context.Context) {
    runServerProbe(ctx, hc, cfgPath, ua, state)
})
go probeLoop(ctx, "tunnel", 10*time.Second, func(ctx context.Context) {
    runTunnelProbe(ctx, hc, cfgPath, ua, state)
})
```

`probeLoop` runs an immediate first probe, then loops on a `time.Ticker(10 s)`. Each goroutine wraps its body in `defer recover()` and logs panics via the existing `writePanelDebugLog` helper. The existing 1 s `refresh()` reads from `state` under `state.mu` and paints — worst-case lag between probe completion and UI repaint is 1 s.

`runServerProbe`:

1. `context.WithTimeout(ctx, 5*time.Second)`.
2. `cfg, err := config.LoadPartial(cfgPath)` — re-read on every tick, so config edits take effect without a panel restart. On error, no-op for this tick (existing `refresh()` already surfaces the load failure in the warning row).
3. If `cfg.LabBridge.Host == ""` → write `netLamp{kind: lampUnreachable}` and return.
4. `labbridge.FetchHealth(ctx, hc, "https://"+host, ua)` → map to `lampKind`, write under `state.mu`.

`runTunnelProbe`: same shape, plus a short-circuit before step 4:

- If `cfg.LabBridge.Pass == ""` → `lampNotConfigured`.
- Otherwise call `labbridge.FetchClient`; map `ErrUnauthorized` → `lampAuthFailed`, `ErrServerError` → `lampServerError`, plain error → `lampUnreachable`, `Connected==true` → `lampOK`, `Connected==false` → `lampDisconnected`.

### Painting in `refresh()`

`refresh()` keeps its existing 1 s tick. It now reads from `state` and paints the three lamps:

```go
state.mu.Lock()
svc, srv, tun := state.service, state.server, state.tunnel
state.mu.Unlock()

paintLamp(dot[0], lbl[0], serviceLampPresentation(svc))
paintLamp(dot[1], lbl[1], serverLampPresentation(srv))
paintLamp(dot[2], lbl[2], tunnelLampPresentation(tun))
// existing button-state and config-display logic continues unchanged
```

The local SCM query stays in `refresh()` — it's cheap and still drives `ComputeButtons(...)`. The result is written into `state.service` so the painting path is uniform across all three lamps.

### Service-lamp color fix

The existing `walk.Label` + `SetTextColor` approach renders black in practice. Implementation tries fixes in order:

1. **`statusDot.Invalidate()`** after every `SetTextColor`. Forces a repaint via `WM_PAINT`; often sufficient.
2. **Fallback** if (1) does not produce color on a real Windows build: replace each `●` Label with a small `walk.CustomWidget` that paints a filled colored circle in its `Paint` handler (~30 LoC). One implementation, applied to all three lamps.

This is an implementation detail; the plan picks whichever works on a real Windows build. Either way, all three lamps render with their intended color.

## Error handling & edge cases

- **Per-probe timeout: 5 s.** A slow VPS can't stall the goroutine longer than that; the next 10 s tick proceeds normally.
- **Concurrent ticks impossible by construction.** Each probe goroutine is a single ticker loop. If a probe is still running when the next tick fires, the tick simply waits (`time.Ticker` semantics).
- **Transient errors don't latch.** Every tick overwrites state. A 401 → green → red → green sequence as the operator fixes config is automatic; no retry/backoff logic.
- **`recover()` on each probe goroutine.** A panic writes one line to `SerialHop_panel_error.log` and the loop continues — the panel stays alive.
- **Window close cancels probes.** `mw.Closing` fires `cancel()`; in-flight HTTP calls abort via the request context; goroutines exit cleanly.
- **Empty `lab_bridge.pass`** → tunnel lamp short-circuits to `Not configured` (grey) without hitting the network.
- **Empty `lab_bridge.host`** → both network lamps show `Unreachable` (grey).
- **Config re-read errors inside a probe loop** → no-op for that tick; state unchanged.
- **No log on successful or failed probes.** Operators see the lamp; they don't need a log entry per 10 s. Panics still log — those are bugs.

## Testing

### Unit tests

`internal/labbridge/client_test.go` (cross-platform):

- `FetchHealth`: chisel OK; chisel down with detail; malformed JSON; non-200; canceled context.
- `FetchClient`: 200 with `{port, connected}`; 401 → `errors.Is(err, ErrUnauthorized)`; 500 → `errors.Is(err, ErrServerError)`; malformed JSON; canceled context.
- Username URL-escaping: `"foo bar"` → request path `/api/public/clients/foo%20bar`.
- All via `httptest.NewServer`; no real network.

`internal/panel/state_test.go` (cross-platform — `state.go` carries no build tag):

- Existing `ComputeButtons` / `StatusIndicator` cases retained.
- New: `serviceLampPresentation` / `serverLampPresentation` / `tunnelLampPresentation` mapping tests, one case per `lampKind` per function. Verifies the per-lamp text divergence (`Up` vs `Connected` etc.) and the shared cases (`Unreachable`, `Checking…`).
- New: `labbridge` result + error → `lampKind` mapping test, covering the branches in `runServerProbe` / `runTunnelProbe`.

`internal/config/load_test.go` (extend):

- Round-trip the new scaffold through `Load`.
- Old-format YAML (`chisel.server` / `chisel.user` / `chisel.pass`) loads with `lab_bridge.host` empty → validator returns "host required".

No unit test for the polling goroutine itself — its logic is `time.Ticker` + two functions that are independently tested.

### Manual verification on Windows (before PR)

1. Fresh install with valid config → all three lamps green.
2. Stop the service via the panel button → Service lamp grey; Server stays green; Tunnel goes red within ~10 s as the VPS sees the tunnel drop.
3. Set `lab_bridge.pass` to a wrong value → Tunnel lamp `Auth failed` (red) within 10 s.
4. Blank out `lab_bridge.pass` → Tunnel lamp `Not configured` (grey) within 10 s.
5. Disconnect laptop from network → both network lamps go grey (`Unreachable`); Service lamp unaffected.
6. Reconnect → lamps recover within 10 s.

### CI

`task test` covers all of the above. `internal/labbridge` runs on the macOS runner. `internal/panel/state.go` (no build tag) runs on the macOS runner. No new CI plumbing.

## Files touched

**New:**

- `internal/labbridge/client.go`
- `internal/labbridge/client_test.go`

**Modified:**

- `internal/config/config.go` — schema change, scaffold update.
- `internal/config/load.go` / `load_test.go` — validator + tests.
- `internal/chisel/client.go` — `Server` composed from host+port; user/pass from `LabBridge`.
- `internal/logship/*.go` — any `cfg.Chisel.User` → `cfg.LabBridge.User`.
- `internal/panel/panel.go` — Status group layout; polling goroutines; `refresh()` reads `lampState`.
- `internal/panel/state.go` / `state_test.go` — `lampKind`, presentation function, mapping tests.

No changes to: `cmd/`, `Taskfile.yaml`, `release-please-config.json`, CI workflows.
