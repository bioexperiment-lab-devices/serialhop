# Panel Crash Safety Net — Design

**Date:** 2026-05-15
**Status:** Approved (brainstorming complete)

## 1. Purpose & scope

On 2026-05-15 we discovered a class of failure where the panel's React tree
crashes during render and the **entire window goes blank**, including the
custom `TitleBar` (minimize/close). Because the panel is `Frameless: true`
the OS-drawn caption is absent — when React unmounts the root, there is no
way for the user to close the window short of Task Manager, and no error
surfaces in the Go service logs (the Go side returned successfully; the
crash is a JS render error with no boundary to catch it).

The specific trigger we already know about — `DevicesTab` reading
`resp.devices.length` when `resp.devices` is `null` — has its own fix in
PR2. This work is the **safety net** that makes such situations *impossible
to leave the user stranded* regardless of which tab introduces the next
render error.

In scope:

- A React `ErrorBoundary` component wrapping each tab body individually, so
  a render error in one tab cannot tear down `TitleBar`, `TabBar`,
  `Warning`, or `Footer`.
- A fallback UI inside the boundary: error message, expandable stack,
  **Copy report** button, **Open logs folder** (reuses existing
  `OpenLogsFolder` binding), **Try again** button (resets boundary state).
- Global `window.error` and `window.unhandledrejection` listeners in
  `main.tsx` that record async crashes into the same journal and surface a
  one-line non-blocking footer toast.
- A new Wails binding `RecordFrontendCrash(message, source, stack string)`
  that appends a single JSON line per crash to a new
  `SerialHop_panel_crash.log` file under `%ProgramData%\SerialHop\logs\`.
- A size cap so the journal file cannot grow unbounded (trim to last
  ~64 KiB on each append).
- Tests: Vitest for the boundary + global handlers; Go unit tests for the
  journal write + size cap; existing
  `TestApp_NoBoundMethodTakesContextContext` continues to gate the new
  binding (it must not take `context.Context`).

Out of scope (YAGNI):

- Fixing the underlying `null` slice bug in `DevicesTab` — that's PR2.
- Per-component (sub-tab) error boundaries. Tab-level granularity is
  enough: TitleBar/TabBar always survive, and the operator can switch tabs
  to recover.
- A structured remote-reporting pipeline. The crash journal stays on disk;
  operators paste it when they file a report.
- Log rotation across files. One file, one size cap, oldest-first
  truncation.
- Replacing `Frameless: true` with native caption as a defensive measure.
  That's a UX regression; the safety net solves the same problem better.

## 2. Architecture

Three independent pieces, no cross-coupling beyond a one-method binding:

```
┌─────────────────────────────────────────────────────────────┐
│  React (internal/panel/frontend/src/)                       │
│    main.tsx                                                 │
│     • global error + unhandledrejection listeners           │
│     • → RecordFrontendCrash(message, source, stack)         │
│     • → footer toast via global UI store                    │
│    App.tsx                                                  │
│     • TitleBar / TabBar / Warning / Footer (always mounted) │
│     • Per-tab <ErrorBoundary><TabBody /></ErrorBoundary>    │
│    components/ErrorBoundary.tsx                             │
│     • getDerivedStateFromError + componentDidCatch          │
│     • Fallback UI with Copy / Open logs / Try again         │
│     • Calls RecordFrontendCrash on caught error             │
└────────────────────────┬────────────────────────────────────┘
                         │ Wails binding
┌────────────────────────┴────────────────────────────────────┐
│  Go (internal/panel/)                                       │
│    bindings.go                                              │
│     • RecordFrontendCrash(message, source, stack string)    │
│       — string-only; NO context.Context (embedding gotcha)  │
│    crash_journal.go (new)                                   │
│     • appendCrash(entry crashEntry)                         │
│     • capJournal(path, maxBytes int64)                      │
│       — read tail, rewrite if over cap                      │
│    paths/paths.go                                           │
│     • PanelCrashJournalFileName = "SerialHop_panel_crash.log" │
│     • PanelCrashJournalPath() string                        │
│       — same fallback shape as PanelErrorLogPath            │
└─────────────────────────────────────────────────────────────┘
```

### 2.1 Failure model

| Failure                                              | Caught by              | User sees                              |
|------------------------------------------------------|------------------------|----------------------------------------|
| Render-time throw inside a tab                       | Per-tab ErrorBoundary  | Boundary fallback inside the tab area  |
| Async throw (promise rejection) anywhere             | window.unhandledrejection | Footer toast, journal entry          |
| Sync error outside React (e.g. event handler throw)  | window.error           | Footer toast, journal entry            |
| Binding failure (e.g. `Diagnostics` rejects)         | Existing per-tab try/catch | Existing banner (unchanged)         |
| `RecordFrontendCrash` itself throws / binding fails  | swallowed inside listener | nothing extra — never throw on the throw |

Importantly: **the global listeners and the ErrorBoundary do NOT throw or
reject**. They catch and record. A bug inside the safety net cannot make
the window go blank, by construction.

## 3. Components

### 3.1 `ErrorBoundary` (new — `components/ErrorBoundary.tsx`)

Class component (functional API isn't enough for `componentDidCatch`).

Props:
```ts
interface ErrorBoundaryProps {
  scope: string;                         // "tab:devices", "tab:ports", "app"
  children: React.ReactNode;
}
```

State:
```ts
interface State {
  error: Error | null;
  info: { componentStack: string } | null;
}
```

Behavior:
- `getDerivedStateFromError(err)` → `{ error: err, info: null }`
- `componentDidCatch(err, info)` → `setState({ info })`, fire-and-forget
  `RecordFrontendCrash(err.message, scope, info.componentStack + "\n" + (err.stack ?? ""))`.
- On render: if `state.error` is set, show fallback UI; else render
  `children`.
- `reset()` zeroes `state` and is bound to the **Try again** button.

Fallback markup (uses existing classes `shp-empty`, `shp-btn-row`, `shp-mono-view`):
```
┌─ shp-empty ─────────────────────────────────────────────┐
│  Something went wrong in the {scope} view.              │
│                                                         │
│  ▸ Show details                                         │
│   (expanded: <pre>{message + stack}</pre>)              │
│                                                         │
│  [ Copy report ] [ Open logs folder ] [ Try again ]     │
└─────────────────────────────────────────────────────────┘
```

`Copy report` writes a plain-text bundle (timestamp, scope, message,
stack, component stack, panel version) to clipboard via
`navigator.clipboard.writeText`.

### 3.2 `App.tsx` wrapping

Replace each tab line with a boundary:

```tsx
{tab === "devices" && (
  <ErrorBoundary scope="tab:devices"><DevicesTab /></ErrorBoundary>
)}
```

The `pendingTab` Modal at the bottom of `App.tsx` stays outside any
boundary — it's the existing dirty-config dialog and shouldn't be
re-mounted when a tab boundary resets.

A second **outer** boundary wraps the whole `<div className="shp-window">`
contents (below `TitleBar`) with `scope="app"` as a last-line defense
against errors in `TabBar`, `Warning`, `Footer`, or the dirty-config
Modal. TitleBar itself stays outside both boundaries so the user always
has window controls.

### 3.3 Global listeners (`main.tsx`)

Before `createRoot().render(...)`:

```ts
window.addEventListener("error", (ev) => {
  void RecordFrontendCrash(
    ev.message ?? String(ev.error ?? "unknown error"),
    "window.error",
    ev.error?.stack ?? "",
  ).catch(() => {});
  emitFooterToast(...);
});
window.addEventListener("unhandledrejection", (ev) => {
  const r = ev.reason;
  const msg = r instanceof Error ? r.message : String(r);
  const stk = r instanceof Error ? (r.stack ?? "") : "";
  void RecordFrontendCrash(msg, "unhandledrejection", stk).catch(() => {});
  emitFooterToast(...);
});
```

The toast surfaces via the existing footer store (`useGlobalUiState`'s
`footer` slot — same mechanism `footer:set` events drive). We add a tiny
`recordCrashAndToast(scope, err)` helper to keep main.tsx readable.

### 3.4 `RecordFrontendCrash` binding (`bindings.go`)

```go
// RecordFrontendCrash appends one JSON line to the panel crash journal.
// Called by the React ErrorBoundary and the JS global error / unhandled
// rejection listeners. Best effort: any error is swallowed (logged via
// writePanelDebugLog) — the panel must never crash inside a crash-recording
// path.
//
// Parameters are string-only; this method does NOT take context.Context
// to satisfy the embedded-method binding gotcha (see
// TestApp_NoBoundMethodTakesContextContext).
func (a *App) RecordFrontendCrash(message, source, stack string) {
    appendCrashJournal(message, source, stack, version.Base(), time.Now().UTC())
}
```

### 3.5 `crash_journal.go` (new) — append + cap

```go
type crashEntry struct {
    Time    string `json:"time"`     // RFC3339Nano
    Version string `json:"version"`  // panel version
    Source  string `json:"source"`   // "tab:devices" / "window.error" / ...
    Message string `json:"message"`
    Stack   string `json:"stack"`
}

const crashJournalMaxBytes = 64 * 1024

func appendCrashJournal(message, source, stack, ver string, now time.Time) {
    path := paths.PanelCrashJournalPath()
    if path == "" {
        return
    }
    entry := crashEntry{
        Time:    now.Format(time.RFC3339Nano),
        Version: ver,
        Source:  source,
        Message: message,
        Stack:   stack,
    }
    line, err := json.Marshal(&entry)
    if err != nil {
        return
    }
    line = append(line, '\n')
    if err := appendCapped(path, line, crashJournalMaxBytes); err != nil {
        writePanelDebugLog("crash_journal_write_failed", err)
    }
}

// appendCapped appends data to path, then if the resulting file exceeds
// max bytes, rewrites it keeping only the trailing `max` bytes aligned to
// the next line boundary. Single-process panel ⇒ no concurrency contract.
func appendCapped(path string, data []byte, max int64) error { ... }
```

`appendCapped` is a small pure-ish helper — easy to unit test against a
temp directory.

### 3.6 `paths.PanelCrashJournalPath()`

Mirrors `PanelErrorLogPath`'s shape:

```go
const PanelCrashJournalFileName = "SerialHop_panel_crash.log"

func PanelCrashJournalPath() string {
    if d := DataDir(); d != "" {
        return filepath.Join(LogsDir(), PanelCrashJournalFileName)
    }
    return ""
}
```

The startup `EnsureDirs()` already creates `LogsDir()` so we don't need a
new directory step. If `DataDir()` is empty (no `%ProgramData%` access),
the binding silently no-ops — matching how `writePanelStartupError`
already handles that environment.

## 4. Data flow

```
[render error in DevicesTab]
        │
        ▼
React calls ErrorBoundary.getDerivedStateFromError
        │
        ▼
Boundary state → { error, info=null } → fallback renders this paint
        │
        ▼
React calls ErrorBoundary.componentDidCatch(err, info)
        │
        ▼
window.go.main.App.RecordFrontendCrash(msg, "tab:devices", stack)
        │
        ▼ (Wails bridge)
panel.App.RecordFrontendCrash(msg, source, stack)
        │
        ▼
appendCrashJournal → appendCapped → SerialHop_panel_crash.log
```

The boundary never awaits the binding — the fallback paint happens
synchronously; the journal write is fire-and-forget.

## 5. Testing

### 5.1 Vitest (`components/ErrorBoundary.test.tsx`)

- Renders a child that throws → fallback markup shows, child gone.
- The mocked `RecordFrontendCrash` is called once with `scope`, message,
  and a stack containing the throw site.
- Clicking **Try again** re-mounts the child (use a counter so the second
  mount doesn't throw).
- The outer chrome (a stub `<TitleBar />` rendered as a sibling to the
  boundary) is unaffected when the inner child throws.

### 5.2 Vitest (`main.test.tsx` — new, focused on the listener helpers)

Pull the `recordCrashAndToast` helpers into a tested module
(`crashReporter.ts`) so we can call them without rendering React:
- `recordCrashFromError(err, "window.error")` → mock binding called with
  expected fields; helper returns synchronously.
- Mock binding rejects → no exception bubbles out of helper.

### 5.3 Go (`internal/panel/crash_journal_test.go`)

- `appendCapped` writes a single short line → file size grows.
- Writing many lines that exceed the cap → file shrinks to last
  `<= max` bytes; surviving content starts at a newline (no partial
  line at top).
- Calling `appendCrashJournal` with a non-existent
  `PanelCrashJournalPath` (`""`) is a no-op (no panic, no error returned
  to caller).

### 5.4 Reflection gate (existing — re-runs unchanged)

`TestApp_NoBoundMethodTakesContextContext` in
`bindings_e2e_windows_test.go` already enumerates every bound `*panel.App`
method and asserts none take `context.Context`. The new
`RecordFrontendCrash` is covered automatically.

## 6. Verification with the bug still present

PR1 is mergeable while PR2 (the actual `null`-slice fix) is in flight.
End-to-end check:

1. Install the panel `SerialHop-vX.Y.Z.exe` from this branch on a clean
   Windows VM **without** running `Install service`.
2. Open the panel → click **Devices** tab.
3. Expected:
   - TitleBar still visible and responsive (Minimize / Close work).
   - Devices tab area shows the ErrorBoundary fallback:
     "Something went wrong in the Devices view." with Copy / Open logs /
     Try again buttons.
   - **Open logs folder** opens `%ProgramData%\SerialHop\logs\` and a
     fresh `SerialHop_panel_crash.log` is present with one JSON line
     whose `source` is `tab:devices` and `message` references
     `Cannot read properties of null (reading 'length')`.
   - Clicking **Status** / **Config** / **Ports** in TabBar switches
     tabs normally — TabBar was never unmounted.

If any of these fail, the safety net is incomplete and PR1 should be
held.

## 7. Files touched

New:
- `internal/panel/crash_journal.go`
- `internal/panel/crash_journal_test.go`
- `internal/panel/frontend/src/components/ErrorBoundary.tsx`
- `internal/panel/frontend/src/components/ErrorBoundary.test.tsx`
- `internal/panel/frontend/src/crashReporter.ts`
- `internal/panel/frontend/src/crashReporter.test.ts`

Modified:
- `internal/paths/paths.go` — add `PanelCrashJournalFileName` +
  `PanelCrashJournalPath()`.
- `internal/panel/bindings.go` — add `RecordFrontendCrash`.
- `internal/panel/frontend/src/wails/go/main/App.ts` — add manual binding
  stub for `RecordFrontendCrash`.
- `internal/panel/frontend/src/preview-shim/bindings.ts` — add no-op
  fake.
- `internal/panel/frontend/src/main.tsx` — wire global listeners.
- `internal/panel/frontend/src/App.tsx` — wrap each tab + the inner
  shell in `<ErrorBoundary>`.

No release-please / build / CI files touched.
