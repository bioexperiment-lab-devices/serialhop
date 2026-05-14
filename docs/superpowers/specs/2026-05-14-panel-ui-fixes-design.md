# Panel UI Fixes — Design

**Date:** 2026-05-14
**Status:** Approved (brainstorming complete; pending spec review before plan)

> Follow-up to `2026-05-13-panel-ui-redesign-design.md`. Supersedes that
> spec's CSS-level decisions (window framing, fixed widths, click-to-open
> help). Tab structure, field semantics, lamp behavior, and the bindings
> contract are unchanged.

## 1. Purpose & scope

After shipping the panel redesign (v0.14.4), operators reported:

1. The panel renders with a visible inner frame around a fixed-size content
   block, inside the native Wails window — "frame in frame".
2. The inner frame is `1080×680` but the OS window is `980×700`, so both
   axes show scrollbars even with no real overflow.
3. Layout is non-responsive: tiny on large monitors, content overflows on
   small ones, label columns dominate narrow windows.
4. Spacing and alignment look broken because fixed pixel widths fight the
   dynamic window size.
5. The `(?)` help affordance is click-toggle; operators expect hover-open,
   hover-out-close (the 2026-05-13 spec settled on click; in practice
   operators read it on hover).
6. Help popovers near the right/bottom edges are clipped by the
   `.shp-content` scroll container or by the WebView viewport itself.

In parallel, there is no rendered-UI check on PRs: TypeScript typecheck,
vitest, and Go tests all pass even when the panel is visually broken. The
only signal today is a manual Windows test from a built artifact.

This spec covers the surgical CSS / component fixes, a fluid layout pass,
a macOS preview workflow that does not require a Windows machine or Wails,
and a CI job that asserts rendered-UI invariants on PRs that touch
UI-relevant paths.

**In scope:**

- Remove the faux `.shp-window` frame and replace fixed dimensions with
  fluid sizing rooted at the native Wails window.
- Convert fixed pixel widths in the design tokens to fluid widths with one
  collapse breakpoint at 720 px.
- Themed scrollbars on every scrollable surface, dark-mode aware.
- Rewrite the `<Help>` component: hover-with-grace open/close, sticky-click,
  keyboard focus/Esc, portal rendering, viewport-edge collision shifting.
- Vite-driven macOS preview that boots the panel in a desktop browser, with
  an in-memory Wails-shim that fakes every binding and event the SPA uses.
- New `ui-checks` job in `pr.yml` running Playwright headless Chromium
  against the preview build at three viewport sizes, gated by
  `dorny/paths-filter` so non-UI PRs skip the job in ~2 s.

**Out of scope:**

- Visual redesign or new components — the design vocabulary in
  `docs/serialhop-ui/project/` stays as-is.
- Screenshot baselines / visual regression diffs — Playwright assertions are
  behavioral only (see §6.3 rationale).
- Cross-browser testing — WebView2 is Chromium, so Chromium-only suffices.
- Storybook — explicitly chosen against in brainstorming (heavier tooling,
  covers components in isolation but not the composition bugs reported).
- Density toggle / wall-mount mode — YAGNI for a five-tab utility app.
- Real Wails dev mode on macOS — Wails depends on WebView2 (Windows-only);
  the shim path covers UI iteration without it.
- Changes to Go-side bindings, event emission, or DTO shapes.

## 2. Delivery shape

Five PRs, each independently reviewable and releasable. Conventional Commit
types are load-bearing per `CLAUDE.md` — patch bumps where the user sees a
fix, no bump for infrastructure:

| # | Title | Type | What lands |
|---|---|---|---|
| 1 | remove faux window frame, fluid sizing | `fix` | §3 |
| 2 | responsive panel layout | `feat` | §4 |
| 3 | help popover hover + portal + viewport clamp | `fix` | §5 |
| 4 | vite dev preview with wails-shim for macOS | `feat` | §6 |
| 5 | playwright UI invariant checks on PR | `ci` | §7 |

Order is the natural dependency order. PR 4 unblocks PR 5 (the preview
build is what Playwright loads). PRs 1, 2, 3 are independent of each other
but practically land in this sequence because PR 1 reveals the responsive
bugs that PR 2 fixes.

## 3. Frame removal & fluid sizing (PR 1)

All edits in `internal/panel/frontend/src/styles/global.css` unless noted.

**`.shp-window`** — currently a `1080×680` boxed "fake window" with border,
border-radius, and `overflow: hidden`. The Wails OS window already provides
window chrome. Replace:

```css
.shp-window {
  min-height: 100vh;
  width: 100%;
  background: var(--surface);
  display: flex;
  flex-direction: column;
  color: var(--text);
}
```

The `border`, `border-radius`, `overflow: hidden`, fixed `width`, fixed
`height`, and `position: relative` properties are deleted. The
`position: relative` on the parent is no longer needed because the modal
scrim (see below) now anchors to the viewport directly.

**`body`** — base font becomes fluid:

```css
body {
  font-family: 'IBM Plex Sans', system-ui, sans-serif;
  font-size: clamp(12.5px, 0.85vw + 8px, 15.5px);
  line-height: 1.4;
  color: var(--text);
  background: var(--bg-page);
  -webkit-font-smoothing: antialiased;
  text-rendering: optimizeLegibility;
}
```

At 720 px viewport the base is ~14.5 px; at 1920 px it caps at 15.5 px. The
clamp is conservative — text never explodes on a 4K monitor or shrinks
illegibly on the minimum window.

**`.shp-modal-scrim`** — was `position: absolute; inset: 0` inside
`.shp-window`. Becomes `position: fixed; inset: 0` so the scrim always
covers the actual viewport regardless of where its parent ends up.

**Wails window options** (`internal/panel/wails_app.go`) — bump minimum
dimensions so the 720-px collapse breakpoint has runway:

```go
Width:     980,
Height:    700,
MinWidth:  720,
MinHeight: 480,
```

**Themed scrollbars** — applies to every scrollable surface, dark-mode
aware via the existing tokens:

```css
* {
  scrollbar-width: thin;
  scrollbar-color: var(--border-strong) var(--surface-sunken);
}
*::-webkit-scrollbar { width: 10px; height: 10px; }
*::-webkit-scrollbar-track { background: var(--surface-sunken); }
*::-webkit-scrollbar-thumb {
  background: var(--border-strong);
  border-radius: 5px;
  border: 2px solid var(--surface-sunken);
}
*::-webkit-scrollbar-thumb:hover { background: var(--text-muted); }
*::-webkit-scrollbar-corner { background: var(--surface-sunken); }
```

The `scrollbar-width: thin` Firefox-spec rule is harmless in
Chromium / WebView2 and gives correct rendering in any future Firefox-host
preview.

## 4. Responsive layout (PR 2)

One collapse breakpoint at 720 px viewport width. Above the breakpoint,
fixed columns become min-clamped; below it, two-column form fields collapse
to single-column.

**`.shp-field` — form rows.** Currently `grid-template-columns: 180px 1fr`.
Becomes:

```css
.shp-field {
  display: grid;
  grid-template-columns: minmax(160px, 18ch) 1fr;
  gap: 14px;
  align-items: start;
}
@media (max-width: 720px) {
  .shp-field {
    grid-template-columns: 1fr;
    gap: 4px;
  }
  .shp-field__label { padding-top: 0; }
}
```

**Form section width.** `.shp-form-section__body` gets a comfortable
reading-width cap so a 1920-px monitor doesn't stretch every input edge to
edge:

```css
.shp-form-section__body {
  max-width: 880px;
  /* unchanged: padding, display, gap */
}
```

Lamps and tables stay full-width — they benefit from the extra space.

**Inputs / selects** — `.shp-input { width: 100% }` stays. The
hardcoded narrow inputs change:

```css
.shp-logs-controls .shp-input {
  width: auto;
  min-width: 160px;
  flex: 1 1 200px;
}
```

**Footer progress.** `.shp-footer__progress { width: 80px }` becomes
`width: clamp(60px, 8vw, 140px)`.

**Modal width.** `.shp-modal { width: 420px }` becomes
`width: min(420px, calc(100vw - 32px))`.

**Tables** — `.shp-table` keeps `white-space: nowrap` on cells; the
`.shp-table-wrap` container is already `overflow: hidden`, change to
`overflow: auto` so its inner table scrolls horizontally on narrow viewports
without spilling.

No other breakpoints. The codebase's YAGNI norm pushes back on a multi-tier
breakpoint plan that isn't justified by a concrete operator workflow.

## 5. Help popover redesign (PR 3)

Rewrite `internal/panel/frontend/src/components/Help.tsx`. Three problems
fused into one change: hover instead of click, portal-rendered to escape
clipping, viewport-edge clamped positioning.

### 5.1 Component API

Unchanged. Existing call sites in `StatusTab.tsx`, `ConfigTab.tsx`,
`DevicesTab.tsx`, `PortsTab.tsx`, `LogsTab.tsx` continue to render as
before:

```tsx
<Help title="..." what="..." defaultVal="..." when="..." />
```

### 5.2 Render via portal

Add a single `<div id="popover-root">` in `index.html`, appended after the
existing `<div id="root">`. The Help component uses
`createPortal(popoverNode, document.getElementById('popover-root')!)` so
the popover sits at the body level. No `overflow: hidden` ancestor can
clip it. The portal root is at `z-index: 50` (above modal scrim at 40).

### 5.3 Anchor-based positioning

On open, the component captures the `?` anchor's `getBoundingClientRect()`
and positions the popover with `position: fixed`:

- Default: `top = anchorRect.bottom + 8`, `left = anchorRect.left - 8`.
- After mount, measure popover width / height; apply edge-collision shifts:
  - If `left + popoverWidth > viewportWidth - 8`: shift left so the right
    edge sits at `viewportWidth - 8`.
  - If `top + popoverHeight > viewportHeight - 8`: flip the popover above
    the anchor — `top = anchorRect.top - popoverHeight - 8`.
- The CSS `::before` arrow's `left` is computed from the original anchor
  position so the arrow always points back at the `?` after a horizontal
  shift. When flipped above, the arrow renders on the bottom edge instead
  of the top.

Position recomputes on `window resize` (`window.addEventListener('resize')`)
and on `scroll` events captured globally
(`window.addEventListener('scroll', ..., { capture: true })`) — so the
popover follows its anchor when any ancestor scrolls. Listeners attach
only while the popover is open; both are removed on close.

### 5.4 Hover state machine

```
states: closed (no popover) | hover-open (auto-close on leave) | sticky (manual close only)

closed:
  ? mouseenter   -> hover-open                (no delay)
  ? focus        -> hover-open
  ? click        -> sticky

hover-open:
  ? mouseleave   -> schedule close in 120 ms
  popover mouseenter -> cancel scheduled close
  popover mouseleave -> schedule close in 120 ms
  ? mouseenter   -> cancel scheduled close
  ? blur         -> schedule close in 120 ms
  popover focus  -> cancel scheduled close
  popover blur   -> schedule close in 120 ms (unless focus moved to ?)
  Esc            -> closed (immediate)
  ? click        -> sticky

sticky:
  ? click        -> closed
  Esc            -> closed
  click outside ? and popover -> closed
  hover events ignored
```

The 120 ms grace lets the pointer travel across the 8 px gap between `?`
and popover without the popover disappearing. `mouseenter` on `?` while
hover-open also cancels any pending close — protects against re-entering
the anchor mid-grace.

### 5.5 Accessibility

`?` keeps `role="button" tabIndex={0}`. The popover container gets
`role="tooltip"` and `tabIndex={-1}` so focus can move into it via mouse
but not from Tab cycling. `Esc` listener is on `document` while open, then
detaches.

## 6. macOS preview (PR 4)

`wails dev` requires WebView2 (Windows). To iterate UI changes on macOS we
boot the SPA in plain Chrome / Edge via Vite, after installing a shim that
populates the runtime globals the SPA expects (`window.go.main.App` and
`window.runtime`).

### 6.1 Shim layout

New directory `internal/panel/frontend/src/preview-shim/` with two files:

- `bindings.ts` — defines an object with every method declared in
  `src/wails/go/main/App.ts`: `GetVersion`, `LoadConfigFromDisk`,
  `ValidateConfig`, `SaveConfig`, `VerifyCredentials`,
  `OpenConfigInEditor`, `OpenLogsFolder`, `OpenReleaseNotes`,
  `PickBackupDir`, `InstallService`, `UninstallService`, `RestartService`,
  `TriggerProbe`, `CheckForUpdate`, `DownloadUpdate`, `CancelDownload`,
  `InstallUpdate`, `GetDevices`, `Discover`, `DisconnectAll`, `GetPorts`,
  `StartLogStream`, `StopLogStream`. Each returns a `Promise` resolving to
  realistic seeded data; mutating methods (`Save*`, `Install*`, `Restart*`)
  resolve after 200 ms and update an in-memory store the readers observe.
- `events.ts` — defines an `EventEmitter`-style object with `EventsOn`,
  `EventsOff`, `EventsEmit`, all stored as listener maps. A
  `setInterval` simulator emits `status:lamp`, `buttons:state`,
  `footer:set`, and a slow stream of `log:line` events so the Logs tab
  has content to render. `update:state`, `warn:set`, `warn:clear` are
  emitted in response to the matching mutation methods.

### 6.2 Install point

New entry-point file `src/preview-entry.ts`:

```ts
import { App } from "./preview-shim/bindings";
import { runtime } from "./preview-shim/events";

declare global {
  interface Window { go?: any; runtime?: any; }
}
window.go = { main: { App } };
window.runtime = runtime;

import("./main");          // boot the SPA after globals are present
```

`index.html` is amended to swap the entry script when `VITE_PREVIEW=1`.
The cleanest way is two `index.html`s sharing common markup, OR a small
build-time string substitution via `vite-plugin-html`. We pick the former
to avoid adding a Vite plugin:

- `index.html` (production): `<script type="module" src="/src/main.tsx">`.
- `preview.html` (Mac preview): `<script type="module" src="/src/preview-entry.ts">`.

Vite's default behavior opens `index.html`. The preview script passes
`--open /preview.html` so the browser lands on the right entry. The Vite
config (or a separate `vite.preview.config.ts`) declares `preview.html` as
a rollup input so `vite build` emits it; the implementation plan picks
between a single config with both entries vs. two configs (Vite supports
both — the choice is mechanical).

### 6.3 Scenario switcher

When `VITE_PREVIEW=1`, the shim mounts a small floating control (top-right,
`position: fixed`) with a dropdown for state scenarios:

| Scenario | What it sets |
|---|---|
| default | service running, config valid, three fake devices |
| service-stopped | SCM lamp red, install button enabled |
| config-invalid | warn header visible, save disabled |
| update-available | update banner with Download action |
| downloading-update | update banner with progress bar advancing |

Scenarios are pure shim-side state — they don't touch SPA code. The
control is hidden in production builds (the entry doesn't exist there).

### 6.4 npm / task scripts

`internal/panel/frontend/package.json` adds:

```json
"preview:mac":   "VITE_PREVIEW=1 vite --host --open /preview.html",
"preview:build": "VITE_PREVIEW=1 vite build --outDir dist-preview"
```

`Taskfile.yaml` adds:

```yaml
preview:
  desc: "Boot the panel UI in a desktop browser (no Wails / no Windows)."
  cmds:
    - cd internal/panel/frontend && npm install && npm run preview:mac
```

`dist-preview/` is gitignored.

### 6.5 Limitations

The shim covers UI iteration only. Anything that requires the real Go
runtime — actual SCM state, real file I/O, real REST calls to the service
— still needs a Windows test. The shim is deliberately not exhaustive;
extending it to mimic specific bug repros stays an ad-hoc per-task effort
when needed.

## 7. CI rendered-UI checks (PR 5)

### 7.1 Playwright project layout

New files:

- `internal/panel/frontend/playwright.config.ts` — declares three viewport
  projects (`min: 720×480`, `default: 980×700`, `large: 1920×1080`), all
  running headless Chromium. `webServer` block boots
  `npx vite preview --outDir dist-preview --port 4173` and waits for it.
- `internal/panel/frontend/playwright/` — one file per concern:
  - `frame.spec.ts`
  - `overflow.spec.ts`
  - `help.spec.ts`
  - `popover-clip.spec.ts`
  - `tabs.spec.ts`

`@playwright/test` is added to `internal/panel/frontend/package.json`
under `devDependencies`. No new dependencies at the repo root; Playwright
lives entirely under the frontend workspace.

### 7.2 What the tests assert

**`frame.spec.ts`** — for each viewport project:
- `.shp-window` exists and has computed `border-width: 0px` (or no
  `.shp-window` element with non-zero border anywhere in the document).
- `.shp-window` has computed `border-radius: 0px`.

**`overflow.spec.ts`** — for each of the five tabs at each viewport:
- Navigate to the tab.
- Assert `document.documentElement.scrollWidth === document.documentElement.clientWidth`.
- Assert `document.documentElement.scrollHeight === document.documentElement.clientHeight`,
  OR that the only scrollable child is `.shp-content` (which is allowed to
  scroll if its content overflows legitimately).

**`help.spec.ts`** — on a tab with `?` icons (Config), pick the first one:
- Hover over `?`; assert popover visible within 200 ms.
- Move pointer to popover; wait 200 ms; assert still visible.
- Move pointer outside both; wait 250 ms; assert hidden.
- Click `?`; move pointer away; wait 250 ms; assert still visible
  (sticky-open).
- Press `Escape`; assert hidden.
- Tab into `?` via keyboard; assert popover visible. Press `Esc`; assert
  hidden.

**`popover-clip.spec.ts`** — open a `?` near each viewport edge (Config
has fields spanning the form). For each open popover:
- Assert its `getBoundingClientRect()` is fully inside
  `(0, 0, window.innerWidth, window.innerHeight)`.

**`tabs.spec.ts`** — for each tab:
- Click the tab; wait for content; assert no `console.error` events.
- Assert the identifying element of the tab is visible (`.shp-lamps` for
  Status, `.shp-form-section` for Config, etc.).

### 7.3 Why behavioral, not screenshot

Visual snapshot baselines were considered and rejected:

- Font rendering between Linux CI Chromium and local Mac / Windows Chromium
  is not pixel-identical; baselines either flake or have to be regenerated
  on CI, which is awkward.
- Snapshot failures don't say *what* is wrong — a behavioral assertion
  failure says "popover at (left=1240) extends past viewport (width=1280)"
  immediately.
- Any intentional CSS tweak invalidates every baseline; the maintenance
  cost is real.

Behavioral assertions cover every issue reported. If a real visual-
regression need emerges later, baselines can be added without rewriting
behavioral tests.

### 7.4 Workflow job

A new top-level job in `.github/workflows/pr.yml`, parallel with `verify`:

```yaml
ui-checks:
  name: ui-checks
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - uses: dorny/paths-filter@v3
      id: changes
      with:
        filters: |
          ui:
            - 'internal/panel/frontend/**'
            - '.github/workflows/pr.yml'

    - name: skip if no UI changes
      if: steps.changes.outputs.ui != 'true'
      run: echo "No UI-relevant paths changed; skipping rendered-UI checks."

    - uses: actions/setup-node@v4
      if: steps.changes.outputs.ui == 'true'
      with:
        node-version: 22
        cache: npm
        cache-dependency-path: internal/panel/frontend/package-lock.json

    - name: install deps
      if: steps.changes.outputs.ui == 'true'
      run: npm ci
      working-directory: internal/panel/frontend

    - name: install playwright browsers
      if: steps.changes.outputs.ui == 'true'
      run: npx playwright install --with-deps chromium
      working-directory: internal/panel/frontend

    - name: build preview bundle
      if: steps.changes.outputs.ui == 'true'
      run: npm run preview:build
      working-directory: internal/panel/frontend

    - name: run playwright
      if: steps.changes.outputs.ui == 'true'
      run: npx playwright test
      working-directory: internal/panel/frontend

    - name: upload report on failure
      if: failure() && steps.changes.outputs.ui == 'true'
      uses: actions/upload-artifact@v7
      with:
        name: playwright-report
        path: internal/panel/frontend/playwright-report
```

The job always reports success to GitHub regardless of branch, so branch
protection lists it as required without paths-filter edge cases. Skip path
runs in ~2 s; full path is ~90 s (browser install dominates — cached
across runs once added).

### 7.5 Path filter scope

The filter watches `internal/panel/frontend/**` and the workflow file
itself. Go-side changes are deliberately excluded: the shim doesn't follow
Go-side renames automatically, but rendered-UI checks against the shim
wouldn't catch broken Go bindings anyway — those surface as runtime errors
on Windows, not as visual regressions. Adding Go paths would produce false
positives that don't add coverage.

## 8. Testing

Existing tests stay green. New tests:

- `Help.test.tsx` — vitest, jsdom — assert hover state machine timings,
  sticky-click toggle, Esc closes, focus opens. The portal target is a
  test-only `<div id="popover-root">` injected by `src/test/setup.ts`.
- Playwright suite — see §7.2.

Manual verification path stays as a backstop: build a Windows artifact and
run it once per spec to confirm the WebView2 path matches what Chromium
renders. Documented in PR descriptions, not enforced in CI.

## 9. Risks & mitigations

| Risk | Mitigation |
|---|---|
| Playwright flake on CI (timing-dependent hover tests) | All timing assertions use Playwright's `expect.toBeVisible({ timeout })` with generous timeouts (1 s, well above the 120 ms grace and 200 ms scenario delays). |
| Shim drifts from real Go bindings | `App.ts` declarations are the source of truth — any drift causes a `tsc` failure in vitest, which already runs in `verify`. The shim file mirrors the same export names. |
| `dorny/paths-filter` skips a UI-relevant change because a Go file moved | Path filter is intentionally narrow (frontend/ + workflow). If a Go change needs UI verification, the author re-runs the job manually or touches a UI file. Documented in PR template, not enforced. |
| Themed scrollbars look wrong in dark mode | Tokens (`--border-strong`, `--surface-sunken`, `--text-muted`) are already dark-mode aware; no new dark-mode rules needed. |
| `position: fixed` modal scrim breaks on browsers that don't honor it inside a flex root | All target browsers (WebView2, Chrome, Edge, Firefox) honor `position: fixed` against the viewport regardless of ancestor layout. |
| Help popover with `position: fixed` doesn't follow its anchor on scroll | Capture-phase `scroll` listener on `window` recomputes position; verified in `popover-clip.spec.ts` with a scrolled-content scenario. |

## 10. Open questions

None.

## 11. Where to look

- Current redesign spec (superseded only in CSS / Help details):
  `docs/superpowers/specs/2026-05-13-panel-ui-redesign-design.md`.
- CI design (release flow, what `pr.yml` enforces):
  `docs/superpowers/specs/2026-05-01-ci-design.md`.
- Mockup reference: `docs/serialhop-ui/project/`.
- Wails options: `internal/panel/wails_app.go:211-236`.
- Bindings surface: `internal/panel/frontend/src/wails/go/main/App.ts`.
