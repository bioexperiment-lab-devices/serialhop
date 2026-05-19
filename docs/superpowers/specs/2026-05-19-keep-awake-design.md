# Keep-awake button on the Status tab — design

**Date:** 2026-05-19
**Status:** Approved (design)
**Scope:** Panel + service. Windows-only feature surface; non-Windows test stubs.

## 1. Goal

Operators run unattended experiments on lab boxes. Windows' default power
plan idles into sleep / hibernate / scheduled-shutdown, which kills the
service mid-run. We need a single button on the panel's Status tab that
keeps the box awake until the operator explicitly turns it off.

Out of scope: blocking user-initiated shutdown / restart / log-off,
holding the display on, preventing forced reboots from Windows Update.

## 2. Decisions

These were settled during brainstorming and frame the rest of the
design:

| Question | Decision |
|---|---|
| Sleep, shutdown, or user-initiated shutdown? | **Sleep + automatic / unattended shutdown.** Don't block user-initiated shutdown. |
| Where does the power request live? | **In the local SerialHop service** (not the panel process). Panel toggles via HTTP. |
| Timed expiry or indefinite? | **Indefinite.** Stays on until explicitly disabled. |
| Persist across service restart? | **No.** Power request is in-memory only; service restart / crash / auto-update drops it. |
| Display on too? | **No.** System awake only; display blanks per the user's power plan. |
| Win32 API choice | **`PowerCreateRequest` + `PowerSetRequest(PowerRequestSystemRequired)`.** Process-bound; visible in `powercfg /requests`. |
| UI shape | **Lamp + button pair** on the Status tab, matching the existing health-lamp pattern. |

Rationale notes:
- Service-driven, not panel-driven: the panel is a UI; closing the
  window must not drop the request. The service is the long-lived
  process and is the only one that should hold OS-level resources of
  this kind.
- Power Requests (modern API) over `SetThreadExecutionState`: the
  modern API's handle is process-bound, so we don't need to pin a
  goroutine to an OS thread; and it surfaces in `powercfg /requests`,
  which is exactly what lab ops will look at when asking "why isn't
  this box ever sleeping?".
- Ephemeral by design: an indefinite-but-not-persisted toggle means the
  worst failure mode is "machine sleeps when the operator expected it
  not to" (recoverable, observable). A persisted toggle's worst failure
  is "machine stays awake for months because someone forgot" (silent,
  expensive).

## 3. Architecture

Three layers, each with a narrow responsibility:

```
[ React Status tab ]    "Power" section: lamp + Enable/Disable button
        │
        │  Wails binding (GetKeepAwake / EnableKeepAwake / DisableKeepAwake)
        ▼
[ panel.App ]           bindings.go: wraps ServiceCli + footer events
        │
        │  HTTP loopback (existing 127.0.0.1:<ActualRestPort> path)
        ▼
[ api.Server ]          three routes on the existing mux
        │
        │  KeepAwake interface
        ▼
[ internal/power ]      Win32 power request lifecycle (or no-op fake)
```

Files touched / added:

- `internal/power/keepawake.go`           — interface + factory (new)
- `internal/power/keepawake_windows.go`   — real impl (new, `//go:build windows`)
- `internal/power/keepawake_other.go`     — fake impl (new, `//go:build !windows`)
- `internal/power/keepawake_other_test.go` — cross-platform state tests (new)
- `internal/power/keepawake_windows_test.go` — Windows smoke test (new)
- `internal/api/handlers.go`              — three new routes
- `internal/api/handlers_power_test.go`   — handler tests (new)
- `internal/api/handlers.go` constructor  — `api.New` gains a `KeepAwake` param
- `cmd/serialhop/main_windows.go` (or wherever `api.New` is wired) — construct + inject `power.New()` instance, defer `Close()`
- `internal/panel/servicecli.go`          — three thin client methods
- `internal/panel/bindings.go`            — `KeepAwakeResult` DTO + three bound methods
- `internal/panel/bindings_keepawake_test.go` — bindings tests (new)
- `internal/panel/frontend/src/types.ts`  — `KeepAwakePayload` shape
- `internal/panel/frontend/src/App.tsx`   — load + hold keep-awake state
- `internal/panel/frontend/src/tabs/StatusTab.tsx` — new "Power" section
- `internal/panel/frontend/src/tabs/StatusTab.test.tsx` — render/click tests (new)

No new dependencies. Windows syscalls go through the
already-present `golang.org/x/sys/windows`.

## 4. `internal/power` package

### 4.1 Interface (`keepawake.go`)

```go
package power

// KeepAwake holds the OS-level "keep the system awake" request. The
// underlying resource is process-bound; the service owns one instance
// for its entire lifetime.
type KeepAwake interface {
    // Enable activates the keep-awake request. Idempotent: calling
    // Enable while already active is a successful no-op. reason is
    // surfaced in `powercfg /requests` on Windows (ignored on other
    // platforms). It is captured on the first Enable call and reused
    // for the lifetime of the handle.
    Enable(reason string) error

    // Disable clears the keep-awake request. Idempotent.
    Disable() error

    // Active returns the most recent successfully-applied state.
    Active() bool

    // Close releases the underlying handle. After Close, the instance
    // is unusable. Called from service shutdown.
    Close() error
}

// New returns a platform-appropriate KeepAwake. Errors only on
// non-recoverable handle-allocation failures (Windows). The non-Windows
// fake never fails.
func New() (KeepAwake, error) { return newPlatform() }
```

### 4.2 Windows implementation (`keepawake_windows.go`)

Loads `kernel32.dll` lazily and binds:
- `PowerCreateRequest(reason *REASON_CONTEXT) (handle HANDLE, err error)`
- `PowerSetRequest(handle HANDLE, requestType uint32) (err error)`
- `PowerClearRequest(handle HANDLE, requestType uint32) (err error)`
- `CloseHandle(handle HANDLE) (err error)`

```go
type winKeepAwake struct {
    mu       sync.Mutex
    handle   windows.Handle  // INVALID_HANDLE_VALUE until first Enable
    active   atomic.Bool
}
```

Constants (from the Windows SDK; we declare them as Go constants in
this package since `golang.org/x/sys/windows` doesn't expose them):
- `POWER_REQUEST_CONTEXT_VERSION = 0`
- `POWER_REQUEST_CONTEXT_SIMPLE_STRING = 0x1`
- `PowerRequestSystemRequired = 1` (the `POWER_REQUEST_TYPE` enum is
  `Display=0, System=1, AwayMode=2, Execution=3` — easy to get wrong;
  callers should double-check during implementation)

`Enable(reason)`:
1. Lock mu.
2. If `active.Load()` → unlock and return nil.
3. If `handle == 0` (or `INVALID_HANDLE_VALUE`): build a
   `REASON_CONTEXT` with version=0, flags=SIMPLE_STRING, and a
   UTF-16-encoded reason. Call `PowerCreateRequest`. Store the returned
   handle on the struct. (Errors propagate.)
4. Call `PowerSetRequest(handle, PowerRequestSystemRequired)`. Errors
   propagate; `active` stays false on error.
5. `active.Store(true)`.

`Disable()`:
1. Lock mu.
2. If `!active.Load()` → return nil.
3. `PowerClearRequest(handle, PowerRequestSystemRequired)`. Errors
   propagate; `active` stays true on error so the next Enable
   short-circuits (no resource leak; the in-memory flag matches the OS
   state we attempted last).
4. `active.Store(false)`.

`Active()` returns `active.Load()`.

`Close()`:
1. Lock mu.
2. If active, call `PowerClearRequest` and clear `active`.
3. If handle != 0, call `CloseHandle`; zero the handle.

Concurrency: `mu` serializes Enable/Disable/Close; `Active()` is
lock-free. The Windows API is documented as safe to call from any
thread.

### 4.3 Non-Windows fake (`keepawake_other.go`)

```go
type fakeKeepAwake struct{ active atomic.Bool }

func (f *fakeKeepAwake) Enable(_ string) error { f.active.Store(true); return nil }
func (f *fakeKeepAwake) Disable() error        { f.active.Store(false); return nil }
func (f *fakeKeepAwake) Active() bool          { return f.active.Load() }
func (f *fakeKeepAwake) Close() error          { f.active.Store(false); return nil }
```

Exists so `internal/api` / `internal/panel` tests compile and run on
macOS / Linux per the cross-platform-testing rule in `CLAUDE.md`.

## 5. HTTP API

Three routes on the existing `api.Server` mux:

### 5.1 `GET /power/keep-awake`

- Request: none.
- Response 200:
  ```json
  { "active": false }
  ```
- Read-only; reads `KeepAwake.Active()`.

### 5.2 `POST /power/keep-awake/enable`

- Request: no body, no query params.
- Response 200 (success or already-on):
  ```json
  { "active": true }
  ```
- Response 500 on syscall failure:
  ```json
  { "error": "keep-awake enable failed", "detail": "<windows.Errno: ...>" }
  ```
  `Active()` stays unchanged on failure.

### 5.3 `POST /power/keep-awake/disable`

Symmetric. Response 200 with `"active": false` on success; 500 with
`"keep-awake disable failed"` on syscall failure.

### 5.4 Cross-cutting

- Trust model: identical to every other `/serial/*` route. The HTTP
  server binds 127.0.0.1 only; no auth.
- Reason string is hard-coded by the api handlers:
  `"SerialHop panel: operator-requested keep-awake"`. Passed into
  `KeepAwake.Enable(reason)` on the first call.
- All three handlers use the existing `writeJSON` / `writeError`
  helpers, log via `slog` (info on success, warn on syscall failure),
  and pick up the existing log middleware.

### 5.5 Constructor change

`api.New` gains one parameter:

```go
func New(
    reg *registry.Registry,
    discover DiscoverFn,
    opener labserial.Opener,
    rawSerialEnabled bool,
    fl flasher.Flasher,
    flashingEnabled bool,
    keepAwake power.KeepAwake,  // new
) *Server
```

All existing call sites (real wiring + tests) pass a `KeepAwake`. Tests
pass the non-Windows fake; production wiring passes `power.New()`.

## 6. Panel — Go side

### 6.1 `ServiceCli` (`internal/panel/servicecli.go`)

Three thin methods around the existing `do()` helper:

```go
type KeepAwakeStatus struct {
    Active bool `json:"active"`
}

func (c *ServiceCli) GetKeepAwake(ctx context.Context) (KeepAwakeStatus, ServiceCliStatus, error) {
    var out KeepAwakeStatus
    status, err := c.do(ctx, "GET", "/power/keep-awake", &out)
    return out, status, err
}

func (c *ServiceCli) EnableKeepAwake(ctx context.Context) (KeepAwakeStatus, ServiceCliStatus, error) {
    var out KeepAwakeStatus
    status, err := c.do(ctx, "POST", "/power/keep-awake/enable", &out)
    return out, status, err
}

func (c *ServiceCli) DisableKeepAwake(ctx context.Context) (KeepAwakeStatus, ServiceCliStatus, error) {
    var out KeepAwakeStatus
    status, err := c.do(ctx, "POST", "/power/keep-awake/disable", &out)
    return out, status, err
}
```

### 6.2 Wails bindings (`internal/panel/bindings.go`)

New DTO:

```go
type KeepAwakeResult struct {
    Active       bool   `json:"active"`
    Reachable    bool   `json:"reachable"`
    Reason       string `json:"reason,omitempty"`        // "service_down" | "unreachable" | ""
    ErrorMessage string `json:"error_message,omitempty"` // populated on syscall failure (500)
}
```

Three bound methods on `*App`:

```go
func (a *App) GetKeepAwake() KeepAwakeResult
func (a *App) EnableKeepAwake() KeepAwakeResult
func (a *App) DisableKeepAwake() KeepAwakeResult
```

Each method:
1. Wraps `a.logAction("keepawake_<get|enable|disable>")` (same pattern
   as existing bindings).
2. Builds `ctx, cancel := a.callCtx()`.
3. Calls the `ServiceCli` method.
4. Translates result:
   - `StatusOK` → `{Active: out.Active, Reachable: true}`.
   - `StatusServiceDown` → `{Reachable: false, Reason: "service_down"}`.
     If `err != nil`, also set `ErrorMessage = err.Error()`.
   - `StatusUnreachable` → `{Reachable: false, Reason: "unreachable"}`.
5. On Enable / Disable success, emits footer event:
   - `{"kind": "ok", "text": "Keep-awake enabled at HH:MM:SS"}` /
     `"Keep-awake disabled at HH:MM:SS"`.
   - On error: `{"kind": "err", "text": "Keep-awake failed: <detail>"}`.

Critical constraint (memory: Wails ctx-embedding gotcha): NONE of these
methods take `context.Context`. The existing
`bindings_ctx_check_test.go` regression gate catches violations
automatically.

## 7. Frontend

### 7.1 State (`App.tsx`)

Add a new piece of app-level state alongside `lamps` / `buttons` /
`update`:

```ts
type KeepAwakeState = { active: boolean; reachable: boolean };

const [keepAwake, setKeepAwake] = useState<KeepAwakeState>({
  active: false,
  reachable: false,  // until first fetch resolves
});
```

Lifecycle:
- On mount, call `GetKeepAwake()` once and seed state from the result.
- After the existing `RestartService` admin action resolves successfully,
  call `GetKeepAwake()` again (service was just restarted; in-memory
  state is now stale). Same after an `update:state == Installed`
  refresh — easiest to bundle this into a single helper called from
  both code paths.
- Subscribe to the service lamp's tone changes: any transition into
  `green` from `gray` / `red` triggers a refetch (one-shot per
  transition, not on every probe tick). This handles crash recovery
  and external service restarts.

### 7.2 `StatusTab.tsx` layout

New section between "Service health" and "Service control":

```
Service health
  ● Local service     ● Lab-bridge server     ● Reverse tunnel

Power                                              <-- new
  ● Keep system awake [On|Off|—]    [Enable | Disable]

Service control
  [Install]  [Uninstall]  [Restart]
```

### 7.3 Lamp behavior

Reuses the existing `<Lamp>` component. State is purely local — never
participates in the lamp-probing pipeline.

| `keepAwake` state | Lamp tone | Lamp label | Lamp sub-text |
|---|---|---|---|
| `{active: true,  reachable: true}` | green | "On" | "System will not sleep or auto-shutdown." |
| `{active: false, reachable: true}` | gray  | "Off" | (none) |
| `{reachable: false}`                | gray  | "—" | "Service unreachable" |

Help panel inside the lamp:

> Prevents Windows from idling into sleep, hibernate, or scheduled
> automatic shutdown while the SerialHop service is running. Has no
> effect on user-initiated shutdown, restart, or sign-out. Cleared if
> the service stops, crashes, or is updated.

### 7.4 Button behavior

| `keepAwake` state | Button label | Variant | Disabled when |
|---|---|---|---|
| `{active: false, reachable: true}` | "Enable" | primary | busy |
| `{active: true, reachable: true}`  | "Disable" | default | busy |
| `{reachable: false}`               | "Enable" | primary | always |

`busy` is a local boolean set true for the duration of the in-flight
`EnableKeepAwake` / `DisableKeepAwake` call.

Click handler:
1. `setBusy(true)`.
2. Optimistically toggle `keepAwake.active` for instant feedback.
3. Call binding. Receive `KeepAwakeResult`.
4. Reconcile: `setKeepAwake({active: res.Active, reachable: res.Reachable})`.
5. If `!res.Reachable`: footer message already fired from the Go side;
   nothing more to do — the lamp now reads "—" and the operator knows
   to check the service.
6. `setBusy(false)`.

No elevation. The bound methods are plain HTTP calls; the syscall
itself runs inside the service (`LocalSystem`).

### 7.5 Types (`types.ts`)

```ts
export interface KeepAwakePayload {
  active: boolean;
  reachable: boolean;
  reason?: string;
  error_message?: string;
}
```

## 8. Error handling matrix

| Scenario | Service | Panel | UI |
|---|---|---|---|
| `PowerSetRequest` fails on Enable | Returns 500; `Active()` stays false | `Reachable: true, Active: false, ErrorMessage: <detail>` | Lamp stays gray; footer shows "Keep-awake failed: …" |
| Service crash while on | Power request dies with process | Detects via lamp tone or next fetch | Lamp flips to "—" then "Off" once service is back |
| User-clicked Restart while on | Same as crash | Bindings refetch after `RestartService` resolves | Lamp lands on "Off" |
| Auto-update install | Same as crash | Refetch after `update:state == Installed` | Lamp lands on "Off" |
| Loopback unreachable | n/a | `Reachable: false`, `Reason: service_down` | Lamp "—"; button disabled |
| Cache stale / `ActualRestPort==0` | n/a | `Reachable: false`, `Reason: unreachable` | Same as above |
| Concurrent toggles from same panel | Idempotent on the service | `busy` flag prevents re-entry | n/a |
| User-initiated shutdown | Not blocked by design | n/a | Help text in the lamp documents this |

## 9. Testing

### 9.1 `internal/power`

- `keepawake_other_test.go` (runs on all platforms via the fake):
  Enable/Disable idempotency, Active flips correctly, Close clears,
  concurrent Enable from multiple goroutines passes `-race`.
- `keepawake_windows_test.go` (`//go:build windows`): smoke test of
  real syscall path — `Enable("test")` is nil; `Active()` is true;
  `Disable()` is nil; `Active()` is false; `Close()` is clean. Does
  not assert sleep prevention (that's a manual check).

### 9.2 `internal/api`

- `handlers_power_test.go`: table-driven coverage of all three routes
  against a fake `KeepAwake`. Asserts `GET` returns current state;
  `POST .../enable` flips Active and is idempotent; `POST .../disable`
  symmetric; Enable returns 500 with detail when the fake's `Enable`
  is configured to return an error.
- Existing handler test fixtures get one additional arg (the fake) in
  their `api.New` calls. Small mechanical sweep.

### 9.3 `internal/panel`

- `bindings_keepawake_test.go`: runs the three bound methods against
  an `httptest.Server` that emulates the routes. Covers happy path,
  500, server-closed (service down), and (via a custom `cachePath`
  pointing at a missing file) the unreachable branch. Verifies
  `Reachable` / `Reason` / `ErrorMessage` are populated correctly.
- `bindings_ctx_check_test.go` is unchanged; it automatically guards
  the new methods against accidentally accepting `context.Context`
  (which would break Wails' embedding model).

### 9.4 Frontend

- Extend the existing tab-tests (Vitest + RTL) with a small set of
  cases for the new Power section: lamp tone/label/sub for each of
  the three state permutations, button label correctness, click
  triggers the correct binding, button disabled when `busy` or
  unreachable, footer event fires on success/error.

### 9.5 Manual sanity check on Windows

Documented as a checklist on the implementation plan, not gating
tests:

1. Open panel → confirm Power section renders "Off".
2. Click Enable → footer reads "Keep-awake enabled at …", lamp turns
   green, button label flips to "Disable".
3. From an elevated cmd: `powercfg /requests` shows one `System`
   request attributed to the SerialHop service exe with reason
   "SerialHop panel: operator-requested keep-awake".
4. Click Disable → lamp gray, `powercfg /requests` clean.
5. Click Enable, then click "Restart" in Service control → after the
   service comes back, the lamp lands on "Off" without operator
   intervention.

## 10. What this design intentionally does NOT do

- Persist state to disk. Failure mode of "operator forgets" beats
  failure mode of "machine never sleeps for weeks".
- Auto-expire after a timer. Requires UX for picking duration + an
  "extend" affordance + service-side timer state; not justified by the
  use case.
- Hold the display on. Different `PowerRequestType`; can be added
  later if needed without breaking the API.
- Block user-initiated shutdown. `ShutdownBlockReasonCreate` is
  window-bound and only fires from a UI process; it's a different
  feature with different UX.
- Show on/off state on tabs other than Status. Single source of truth
  is the Status tab.
