# Installer UI Redesign — Design

**Date:** 2026-05-16
**Status:** Approved (brainstorming complete; pending spec review before plan)

## 1. Purpose & scope

The installer shipped in PR #107 works correctly but presents a bad first
impression: a raw `github.com/lxn/walk` window (Win32 boxy gray controls,
no styling) accompanied by a console window (PE built without the
`windowsgui` subsystem). The control panel itself was rewritten as a Wails
v2 app with a modern HTML/CSS/JS UI in earlier work, so the installer is
now visually inconsistent with the rest of the product.

This work replaces the installer's UI shell with a small Wails v2 app
matching the panel's visual style, kills the console window, and adds an
auto-close behavior so the installer dies cleanly once the panel is
launched.

In scope:

- Rewrite `tools/installer/ui_windows.go` and `tools/installer/ui_other.go`
  to use `github.com/wailsapp/wails/v2` instead of `github.com/lxn/walk`.
- New `tools/installer/frontend/` with a single vanilla HTML/CSS/JS bundle.
  No npm, no Vite, no TypeScript, no build step beyond `cp` into an
  embedded directory.
- Frontend reuses a small subset of design tokens from the panel's
  `internal/panel/frontend/src/styles/global.css` (colors, fonts, button
  styling, spacing) — copied with intent, not via cross-package import.
- Compile the installer with `-ldflags="-H windowsgui"` so no console
  window appears on launch.
- After a successful install + launch step, wait 1500ms then call
  `wailsruntime.Quit(ctx)` so the installer window closes itself once the
  panel is visible.
- Remove `github.com/lxn/walk` and `github.com/lxn/win` from `go.mod`.

Out of scope (deliberately YAGNI):

- A full React/TypeScript frontend mirroring the panel's component
  library. The installer is one dialog with three buttons — the boilerplate
  cost of Vite + npm install in CI is not justified for that surface area.
- A shared design-system package between the panel and the installer.
  The small CSS duplication is acceptable for now; we extract later if the
  two surfaces diverge.
- A separate design system. The installer reuses the panel's existing
  title-bar HTML + CSS (the `.shp-titlebar` element from `TitleBar.tsx`
  + `global.css`) so the chrome matches without introducing a shared
  component package.
- Changes to the install dispatch logic, version check, payload extract,
  shortcut writer, or SCM interaction. `Runner.Run` and the existing
  cross-platform test matrix stay untouched.
- Localization / i18n.
- Theming / dark mode toggle in the installer (it inherits the panel's
  light-theme defaults from the copied tokens).
- Replacing `realLauncher` with a more sophisticated "wait until panel
  window appears" check. The 1500ms delay is intentional simplicity.

## 2. Architecture

The split is unchanged from PR #107: cross-platform install logic in
`install.go` (no GUI dependencies), Windows-only UI shell in
`ui_windows.go`. The new UI shell embeds a vanilla HTML/CSS/JS bundle and
exposes a tiny binding surface to JavaScript.

```
┌───────────────────────────────────────────────┐
│ ui_windows.go (Wails MainWindow + bindings)   │
│  • wails.Run with options.App                 │
│  • Bound methods: InstallerApp.BrowseFolder, │
│    InstallerApp.Install, InstallerApp.Cancel │
│  • Window: 480×280, framed, non-resizable    │
└────────────────────┬──────────────────────────┘
                     │ JS ↔ Go bridge
┌────────────────────┴──────────────────────────┐
│ tools/installer/frontend/                     │
│  • index.html (the entire UI)                 │
│  • Inline <style>: copied tokens + dialog CSS │
│  • Inline <script>: form handlers, status     │
│    updates, calls window.go.main.*            │
└───────────────────────────────────────────────┘
```

The frontend is one file. No router, no virtual DOM, no state library.
Form values live in DOM. Status updates flow Go → JS via Wails events
(`wailsruntime.EventsEmit(ctx, "status", payload)`); JS subscribes via
`window.runtime.EventsOn("status", ...)`. Button clicks call bound Go
methods via `window.go.main.InstallerApp.<Method>(...)`.

## 3. Window properties

```go
err := wails.Run(&options.App{
    Title:             "SerialHop Installer",
    Width:             480,
    Height:            300,
    MinWidth:          480,
    MinHeight:         300,
    MaxWidth:          480,
    MaxHeight:         300,
    DisableResize:     true,
    Fullscreen:        false,
    Frameless:         true,  // SPA owns the chrome via .shp-titlebar (matches panel)
    StartHidden:       false,
    BackgroundColour:  &options.RGBA{R: 245, G: 245, B: 247, A: 255}, // panel's --bg
    AssetServer:       &assetserver.Options{Assets: frontendAssets},
    Bind:              []any{installerApp},
    OnStartup:         installerApp.OnStartup,
})
```

- **Size 480×300** with all four `{Min,Max}{Width,Height}` set equal and
  `DisableResize: true` produces a fixed-size dialog. Operators can't
  resize, maximize, or full-screen it.
- **Frameless** (`Frameless: true`) hands the chrome to the SPA. The
  HTML's `.shp-titlebar` element renders the title and a close button
  that calls `InstallerApp.Cancel` (which fires `wailsruntime.Quit`).
  The drag region uses `--wails-draggable: drag` on the title text
  container, matching the panel.
- **BackgroundColour** matches the panel's `--bg` token so there's no
  flash of a different background while the WebView2 control initializes.

## 4. Visual layout

```
┌─ SerialHop Installer ──────────────────────────[X]┐
│                                                    │
│   Install location                                 │
│   ┌────────────────────────────────────┐ ┌──────┐ │
│   │ C:\Program Files\SerialHop         │ │Browse│ │
│   └────────────────────────────────────┘ └──────┘ │
│                                                    │
│   Ready.                                           │
│                                                    │
│                                                    │
│                                                    │
│                              ┌────────┐ ┌──────┐  │
│                              │ Cancel │ │Install│ │
│                              └────────┘ └──────┘  │
└────────────────────────────────────────────────────┘
```

Tokens copied verbatim from `internal/panel/frontend/src/styles/global.css`:

| Token | Use |
|---|---|
| `--bg` (#f5f5f7) | Window background |
| `--fg` (#1c1c1e) | Text |
| `--muted` (#6e6e73) | Secondary text (status line in info state) |
| `--accent` (#0a84ff) | Install button background |
| `--accent-fg` (#ffffff) | Install button text |
| `--border` (#d1d1d6) | Input border, panel border |
| `--error` (#d70015) | Status line in error state |
| `--success` (#34c759) | Status line on success |
| Font stack | Same `Inter`, system sans-serif fallbacks |
| Border radius `6px` | Inputs and buttons |
| Spacing 8/12/16 px | Standard panel rhythm |

These ~20 token values plus button hover/focus styles total ~80 lines of
CSS. The full panel `global.css` (26KB) is not copied — only the subset
the installer needs.

States the status line shows (driven by Go → JS events):

| State | Text | Color |
|---|---|---|
| Idle | `Ready.` | `--muted` |
| Installing | `Installing…` (with a tiny `::after` spinner using CSS keyframes) | `--fg` |
| Same-version | `SerialHop v0.19.0 is already installed.` | `--muted` |
| Downgrade refused | `Installed version (v0.20.0) is newer than this installer (v0.19.0).` | `--error` |
| Success | `Installed SerialHop v0.19.0. Launching…` | `--success` |
| Generic error | `<error message from Result.Err>` | `--error` |

The Install button is disabled while `Installing…` is showing. The
Browse and path-input remain enabled so the operator can fix a bad path
on retry.

## 5. Go ↔ JS bindings

`tools/installer/ui_windows.go` declares an `InstallerApp` struct with
three methods that the Wails bridge exposes to JS:

```go
type InstallerApp struct {
    ctx        context.Context
    opts       *options // initial defaults from CLI flags
    runResult  *Result  // set by Install handler; consulted by auto-close
}

func (a *InstallerApp) OnStartup(ctx context.Context) {
    a.ctx = ctx
}

// BrowseFolder opens the system folder picker and returns the chosen
// path, or "" if the operator cancelled.
func (a *InstallerApp) BrowseFolder(current string) string

// Install runs the install flow with the operator's chosen install
// directory. Returns when the flow completes (success or error). Emits
// "status" events along the way for the UI to render.
func (a *InstallerApp) Install(installDir string) Result

// Cancel closes the window. Same effect as clicking the system close
// button. Currently a no-op while Install is in flight (we do not
// abort an in-progress install).
func (a *InstallerApp) Cancel()
```

The `Result` return value is auto-serialized to JSON by Wails. JS uses it
to decide what to display: success state, error state, etc.

### Initial path: server-side or client-side?

The default install path (`C:\Program Files\SerialHop`) lives in the Go
side already (`defaultInstallDir` in `main.go`). The JS frontend asks the
Go side for it on startup rather than hard-coding it twice:

```js
window.go.main.InstallerApp.InitialPath().then(p => pathInput.value = p);
```

A fourth bound method `InitialPath() string` exposes this.

## 6. Auto-close behavior

After `Runner.Run` returns successfully and the panel launch step (inside
`maybeLaunch`) has fired the detached process, the Go-side `Install`
handler does:

```go
// Emit final status to the UI so the operator briefly sees the success
// message, then quit the installer process so the window closes on its
// own once the panel is visible.
wailsruntime.EventsEmit(a.ctx, "status", successPayload)
time.Sleep(1500 * time.Millisecond)
wailsruntime.Quit(a.ctx)
```

The 1500ms delay is deliberate:

- It's long enough for the operator to register "Installed. Launching SerialHop…"
- It's short enough that the window doesn't linger feeling forgotten.
- It's roughly when WebView2 typically finishes painting the new
  SerialHop panel window — so the visual handoff feels intentional.

Failure paths do NOT auto-close. If `Result.Err != nil` (any install or
launch failure), the UI shows the error and the window stays open until
the operator clicks Cancel or the system close button. `realLauncher`
failure is non-fatal per `install.go` — the message becomes "Install
succeeded but launching SerialHop failed…", and the installer stays
open so the operator can still see what happened.

The same-version path (`StateSame` — "already installed") DOES launch
the panel (per current behavior in `runSameVersion`) and DOES auto-close
on the same 1500ms timer.

The downgrade-refused path does NOT auto-close (Result.Err is set).

## 7. Subsystem flags

The current installer build hits `tools/buildcmd` which forwards flags
to `go build`. We pass `-ldflags="-H windowsgui"` for the installer
target. Two paths:

- **Option A**: hard-code `-H windowsgui` in `buildcmd` when the target
  package is `./tools/installer` (or when called with a new `-gui` flag).
- **Option B**: extend the Taskfile's `installer` target to pass
  `-ldflags="-H windowsgui"` through buildcmd's existing `-ldflags` plumbing.

Option B is cleaner — keeps `buildcmd` package-agnostic and puts the
"this binary is a GUI app" decision in the Taskfile where the install
target lives. `buildcmd` already accepts `-ldflags` (or needs to; we
check during implementation and extend if not — small addition).

The Wails Go runtime imports `golang.org/x/sys/windows` and registers a
WndProc — it doesn't write anything to stdout itself. With
`-H windowsgui`, the parent shell does not get a console; if the operator
launched the installer from cmd.exe, stderr writes (e.g. our `log/slog`
in `--silent` mode) go to nowhere unless we reattach. That's the same
trade-off the main `SerialHop.exe` makes (`windowsgui` subsystem + log
files in `%ProgramData%`). For interactive mode the installer routes all
status to the UI label, not stdout — so this is not a regression.

For `--silent` invocations from a script, the installer still needs to
produce diagnostic output. Two approaches:

- The existing `%TEMP%\SerialHop-installer-<version>.log` covers
  structured diagnostics already.
- For the human-readable success/error line, `--silent` mode reattaches
  the parent console via `AttachConsole(ATTACH_PARENT_PROCESS)` (same
  trick the main binary uses for `--foreground`) and writes to stdout/
  stderr from there. If no parent console exists, the call is a no-op
  and the line goes to the log file.

`--silent` mode does not show the Wails window at all; it runs the
existing silent path in `main.go` which calls `Runner.Run` directly.
Only the dialog path goes through Wails.

## 8. Removing `github.com/lxn/walk`

After the rewrite:

- `tools/installer/ui_windows.go` no longer imports walk.
- No other file in the repo imports walk.

Action:

1. Delete the walk-using code in `ui_windows.go` and replace with the
   Wails implementation.
2. Update `ui_other.go` (the cross-platform stub) signature to keep
   compile parity. (Currently `func runDialog(_ *options) int`; that
   signature stays.)
3. Run `go mod tidy` to drop `github.com/lxn/walk` and
   `github.com/lxn/win` from `go.mod` and `go.sum`.
4. Verify no test or other file references walk.

## 9. Frontend file layout

```
tools/installer/frontend/
  dist/
    index.html        # the entire UI (HTML + inline <style> + inline <script>)
```

The `dist/` directory is **checked in**, not generated by a build step.
This is the key simplification: there is no npm install, no Vite build,
no `task installer-frontend` task. The HTML file is hand-written and
edited like any other source file. `//go:embed all:tools/installer/frontend/dist`
in `ui_windows.go` picks it up at compile time.

If/when the design diverges enough that we need build tooling, we can
introduce it then. Until then, one HTML file is the entire frontend.

A `dev/` subdirectory may exist for separate developer notes or sketches
(e.g. a standalone `preview.html` that mocks the bindings in pure JS so
designers can iterate without rebuilding the Go binary). That's
optional; we'll add it if it proves useful, not preemptively.

## 10. Internal package layout

| Path | Status | Purpose |
|---|---|---|
| `tools/installer/install.go` | unchanged | Cross-platform install dispatch (no UI deps) |
| `tools/installer/install_test.go` | unchanged | Cross-platform tests against fakes |
| `tools/installer/main.go` | modified | Dispatch to Wails dialog vs `--silent` path. Imports Wails runtime for the silent path's `AttachConsole` helper. |
| `tools/installer/ui_windows.go` | rewritten | Wails `wails.Run` + `InstallerApp` bindings + `//go:embed` |
| `tools/installer/ui_other.go` | unchanged | Cross-platform stub (panics if invoked) |
| `tools/installer/frontend/dist/index.html` | new | Single-file UI |
| `tools/installer/elevation_windows.go` | unchanged | `IsElevated` check |
| `tools/installer/elevation_other.go` | unchanged | Stub |
| `tools/installer/peversion_windows.go` | unchanged | PE FileVersion reader |
| `tools/installer/peversion_other.go` | unchanged | Stub |
| `tools/installer/shortcut_windows.go` | unchanged | COM IShellLink wrapper |
| `tools/installer/shortcut_other.go` | unchanged | Stub |
| `tools/installer/payload_production.go` | unchanged | `//go:embed` of staged SerialHop.exe (production tag) |
| `tools/installer/payload_dev.go` | unchanged | Empty payload var (default tag) |
| `tools/installer/main_stub.go` | (already deleted in Task 8 of prior PR) | n/a |
| `go.mod` | modified | Remove walk + win indirect deps; add explicit Wails v2 ref if not already direct |
| `Taskfile.yaml` | modified | Add `-ldflags="-H windowsgui"` to installer build |
| `tools/buildcmd/main.go` | possibly modified | Verify it accepts `-ldflags`; add if absent |

No changes to `internal/winsvc/`, `internal/updater/`, `internal/panel/`,
`internal/version/`, or any other internal package. This is purely the
installer's UI shell + build flags.

## 11. Testing

- `tools/installer/install_test.go` keeps passing unchanged (425 → 425
  tests). The Wails UI shell is not unit-tested.
- The UI itself is verified manually on Windows during release-build
  smoke testing (per `task installer` + double-click). There is no
  automated test for the Wails frontend.
- Cross-platform CI (`pr.yml`) continues to pass on Linux: the new
  `frontend/dist/index.html` is just a checked-in asset; `//go:embed`
  works on any host; tests target `Runner.Run` and don't open a window.
- `go build` for Windows is the same compile-only smoke (CI `pr.yml`
  already builds windows/amd64 via `wails build`; we extend that or rely
  on the existing `tools/installer` package build).
- Manual test plan on Windows:
  - Double-click installer → window matches panel style, no console.
  - Click Browse, pick alternate path, click Install → installs there.
  - Reuse same installer on installed box → "already installed", auto-closes.
  - Older installer over newer → refused, error visible, no auto-close.
  - Click Cancel mid-idle → window closes immediately.
  - `--silent` mode → no window, runs to completion, logs to %TEMP%.

## 12. Build pipeline changes

Single Taskfile change in the `installer` task:

Before:
```yaml
  installer:
    deps: [build, installer-resource]
    cmds:
      - cp dist/SerialHop.exe tools/installer/payload/SerialHop.exe
      - go run ./tools/buildcmd -tags production -o dist/SerialHop-Setup.exe -goos windows -goarch amd64 ./tools/installer
```

After:
```yaml
  installer:
    deps: [build, installer-resource]
    cmds:
      - cp dist/SerialHop.exe tools/installer/payload/SerialHop.exe
      - go run ./tools/buildcmd -tags production -ldflags="-H windowsgui" -o dist/SerialHop-Setup.exe -goos windows -goarch amd64 ./tools/installer
```

If `tools/buildcmd` does not yet accept `-ldflags`, we add a passthrough
flag. The change to `buildcmd` is small: define `-ldflags` as a string
flag, forward it to `go build`. Default `""` preserves current behavior
for all other call sites.

No new CI steps. `release-build` already runs `task installer`; the new
flag is invisible to it.

## 13. Compatibility & risk

- No changes to install dispatch, version check, payload extract, SCM
  interaction, or rollback. Behavior on a fresh install / upgrade /
  same-version / downgrade is identical to PR #107.
- No changes to `--silent` flag semantics, `--dir`, `--no-launch`, or
  `--no-shortcut`. `--no-launch` skips both the panel spawn AND the
  auto-close timer (the installer stays open with the success message).
- Binary size: walk is small (~few hundred KB). Wails v2 adds ~5MB to
  the binary because the WebView2 bridge and Go runtime bindings are
  pulled in. Estimated installer size: ~17MB → ~22MB, plus the
  ~13MB embedded payload, totaling ~35MB. Well within "tiny installer"
  expectations.
- WebView2 runtime requirement: the panel already depends on WebView2
  being installed on the target machine (Windows 10 May 2020 Update or
  later ships it; older Windows 10 needs it explicitly installed). The
  installer inherits this dependency. If WebView2 is missing, the
  installer window fails to render and the operator sees a Wails error.
  This is the same failure mode the panel hits. The README's install
  prerequisites mention Windows 10 1607+ (a stricter constraint than
  needed); no doc update required.
- The `--silent` path is unchanged — no Wails initialization, no
  WebView2 dependency at install time. Useful escape hatch for sites
  that pre-stage installs.

## 14. Release / commits

This lands as a single PR titled
`feat(installer): replace walk dialog with Wails-styled window; auto-close on launch`.
release-please will bump minor on the next release. No `BREAKING CHANGE`
markers. No changes to version metadata or release-please config.

## 15. Open questions (resolved during brainstorming)

- **Vanilla HTML vs full React**: vanilla. The installer is one dialog;
  the boilerplate of React + Vite + npm + bindings generation is not
  justified. Decided in brainstorming Q (a).
- **Auto-close timing**: 1500ms after the panel launch step succeeds.
  Decided in brainstorming Q (b).
- **Framed vs frameless window**: frameless. The SPA renders the same
  `.shp-titlebar` markup the panel uses so the chrome is visually
  consistent across both apps. Initial draft proposed framed for
  simplicity; revised to frameless after user feedback during the
  preview iteration.
