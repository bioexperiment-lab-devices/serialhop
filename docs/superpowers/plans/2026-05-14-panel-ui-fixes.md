# Panel UI Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the visual regressions in the panel UI (faux window frame, fixed sizes, broken alignment, broken help popover), make the layout responsive, add a macOS preview path for fast iteration, and add a Playwright CI job that asserts rendered-UI invariants on PRs that touch UI paths.

**Architecture:** Six small PRs, each independently reviewable and releasable: (1) remove faux frame + fluid sizing, (2) reconcile tab class names to `.shp-*` design tokens, (3) responsive layout pass, (4) Help popover rewrite with portal + hover + clamp, (5) Vite + Wails-shim macOS preview, (6) Playwright UI-invariants CI job. Each PR contains its own pre-flight checks and a single Conventional Commits title; release-please reads the squash-commit title to decide version bumps.

**Tech Stack:** React 18 + TypeScript (`internal/panel/frontend/`), Vite 5 bundler, Wails v2 (Go-side WebView2 host), Vitest + @testing-library/react for unit tests, Playwright (added in phase 6) for headless Chromium rendered-UI checks, GitHub Actions for CI.

**Spec:** `docs/superpowers/specs/2026-05-14-panel-ui-fixes-design.md`. Read sections 3-7 alongside this plan.

**Worktree note:** Use the `superpowers:using-git-worktrees` skill to create an isolated worktree before starting Phase 1. All work happens off `main`; each phase ends with a push + PR.

---

## File map

| File | Status | Responsibility |
|---|---|---|
| `internal/panel/frontend/src/styles/global.css` | Modify | All CSS edits (Phases 1-3) |
| `internal/panel/wails_app.go` | Modify | `MinWidth`/`MinHeight` bump (Phase 1) |
| `internal/panel/frontend/src/tabs/StatusTab.tsx` | Modify | Tab class reconciliation (Phase 2) |
| `internal/panel/frontend/src/tabs/ConfigTab.tsx` | Modify | Tab class reconciliation (Phase 2) |
| `internal/panel/frontend/src/tabs/DevicesTab.tsx` | Modify | Tab class reconciliation (Phase 2) |
| `internal/panel/frontend/src/tabs/PortsTab.tsx` | Modify | Tab class reconciliation (Phase 2) |
| `internal/panel/frontend/src/tabs/LogsTab.tsx` | Modify | Tab class reconciliation (Phase 2) |
| `internal/panel/frontend/src/components/Help.tsx` | Rewrite | Hover state machine + portal + clamp (Phase 4) |
| `internal/panel/frontend/src/components/Help.test.tsx` | Create | Vitest coverage for Help (Phase 4) |
| `internal/panel/frontend/src/test/setup.ts` | Modify | Inject `<div id="popover-root">` for vitest (Phase 4) |
| `internal/panel/frontend/index.html` | Modify | Add `<div id="popover-root">` (Phase 4) |
| `internal/panel/frontend/preview.html` | Create | macOS preview entry HTML (Phase 5) |
| `internal/panel/frontend/src/preview-entry.ts` | Create | Installs shim globals, then boots SPA (Phase 5) |
| `internal/panel/frontend/src/preview-shim/bindings.ts` | Create | Fakes `window.go.main.App` (Phase 5) |
| `internal/panel/frontend/src/preview-shim/events.ts` | Create | Fakes `window.runtime` event bus (Phase 5) |
| `internal/panel/frontend/src/preview-shim/seed.ts` | Create | Realistic seed data + scenario state (Phase 5) |
| `internal/panel/frontend/src/preview-shim/Scenarios.tsx` | Create | Dev-only scenario switcher (Phase 5) |
| `internal/panel/frontend/vite.config.ts` | Modify | Multi-entry build for `preview.html` (Phase 5) |
| `internal/panel/frontend/package.json` | Modify | Scripts + `@playwright/test` dep (Phases 5, 6) |
| `Taskfile.yaml` | Modify | `task preview` target (Phase 5) |
| `internal/panel/frontend/.gitignore` | Modify | Ignore `dist-preview/` + Playwright outputs (Phase 5) |
| `internal/panel/frontend/playwright.config.ts` | Create | Playwright test config (Phase 6) |
| `internal/panel/frontend/playwright/frame.spec.ts` | Create | No-faux-frame invariant (Phase 6) |
| `internal/panel/frontend/playwright/overflow.spec.ts` | Create | No-unexpected-scrollbar invariant (Phase 6) |
| `internal/panel/frontend/playwright/help.spec.ts` | Create | Help popover state-machine invariants (Phase 6) |
| `internal/panel/frontend/playwright/popover-clip.spec.ts` | Create | Popover-fits-in-viewport invariant (Phase 6) |
| `internal/panel/frontend/playwright/tabs.spec.ts` | Create | Tab navigation invariant (Phase 6) |
| `.github/workflows/pr.yml` | Modify | New `ui-checks` job, gated by paths-filter (Phase 6) |

---

## Conventions for every phase

**Pre-flight checks** (run from repo root before opening any PR):

```
gofmt -l .                                 # must print nothing
go vet ./...
golangci-lint run                          # may not be installed locally; fine
go test -race -count=1 ./...
cd internal/panel/frontend && npm run build
cd internal/panel/frontend && npm test
```

**Branch naming:** `fix/panel-<phase>` or `feat/panel-<phase>` or `ci/panel-<phase>`.

**Commit + PR rule:** one PR per phase; PR title is Conventional Commits, exactly as listed in each phase header. `pr.yml` enforces the prefix.

**Don't push to main.** Push to your branch and open a PR. Squash-merge is performed by the maintainer.

---

# Phase 1 — `fix: remove faux window frame and fluid sizing`

**Branch:** `fix/panel-faux-frame`

**Outcome:** The panel fills the native Wails window. No more 1080×680 inner box with rounded border. No scrollbars unless content genuinely overflows. Themed scrollbars where they do appear. Base font scales gently with viewport.

### Task 1.1: Remove the faux `.shp-window` frame

**Files:**
- Modify: `internal/panel/frontend/src/styles/global.css:88-101`

- [ ] **Step 1: Open `internal/panel/frontend/src/styles/global.css` and locate the `.shp-window` rule.**

The current rule is the one beginning at line 89:

```css
.shp-window {
  width: 1080px;
  height: 680px;
  background: var(--surface);
  border: 1px solid var(--border-strong);
  border-radius: 6px;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  color: var(--text);
  font-family: 'IBM Plex Sans', system-ui, sans-serif;
  position: relative;
}
```

- [ ] **Step 2: Replace it with this fluid container:**

```css
.shp-window {
  min-height: 100vh;
  width: 100%;
  background: var(--surface);
  display: flex;
  flex-direction: column;
  color: var(--text);
  font-family: 'IBM Plex Sans', system-ui, sans-serif;
}
```

The deleted properties (`width: 1080px`, `height: 680px`, `border`, `border-radius`, `overflow: hidden`, `position: relative`) are the source of the "frame in frame" + scrollbar bug.

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/src/styles/global.css
git commit -m "fix(panel): remove faux 1080x680 .shp-window frame"
```

### Task 1.2: Fluid base font on `<body>`

**Files:**
- Modify: `internal/panel/frontend/src/styles/global.css:78-86`

- [ ] **Step 1: Find the existing `body` rule (the one with `font-size: 13px`).**

- [ ] **Step 2: Replace its `font-size` line:**

`font-size: 13px;` → `font-size: clamp(12.5px, 0.85vw + 8px, 15.5px);`

All other properties (`font-family`, `line-height`, `color`, `background`, `-webkit-font-smoothing`, `text-rendering`) stay the same.

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/src/styles/global.css
git commit -m "fix(panel): scale base font with viewport (12.5-15.5px clamp)"
```

### Task 1.3: Reposition the modal scrim to the viewport

**Files:**
- Modify: `internal/panel/frontend/src/styles/global.css:949-957` (`.shp-modal-scrim` rule)

The scrim was previously `position: absolute; inset: 0` and relied on `.shp-window` being its positioned ancestor. We removed `position: relative` from `.shp-window` in Task 1.1, so switch the scrim to viewport-anchored.

- [ ] **Step 1: In `.shp-modal-scrim`, change `position: absolute;` to `position: fixed;`. Leave `inset: 0` and the rest of the rule intact.**

- [ ] **Step 2: Commit.**

```bash
git add internal/panel/frontend/src/styles/global.css
git commit -m "fix(panel): anchor modal scrim to viewport (position: fixed)"
```

### Task 1.4: Themed scrollbars

**Files:**
- Modify: `internal/panel/frontend/src/styles/global.css` (append at end-of-file)

- [ ] **Step 1: Append this block at the end of `global.css`:**

```css
/* ===== Themed scrollbars ===== */
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

The existing `--border-strong`, `--surface-sunken`, and `--text-muted` tokens already reshape for dark mode in `[data-theme="dark"]`, so no extra dark-mode rule is needed.

- [ ] **Step 2: Commit.**

```bash
git add internal/panel/frontend/src/styles/global.css
git commit -m "fix(panel): theme scrollbars to match design tokens"
```

### Task 1.5: Bump Wails minimum window dimensions

**Files:**
- Modify: `internal/panel/wails_app.go:211-216`

- [ ] **Step 1: Find this block:**

```go
err := wails.Run(&options.App{
    Title:     "SerialHop v" + version.Base(),
    Width:     980,
    Height:    700,
    MinWidth:  860,
    MinHeight: 580,
```

- [ ] **Step 2: Change `MinWidth: 860` to `MinWidth: 720` and `MinHeight: 580` to `MinHeight: 480`. The `Width` and `Height` defaults stay at 980/700.**

These minima match the smallest viewport the responsive pass (Phase 3) is targeted at.

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/wails_app.go
git commit -m "fix(panel): lower min window size to 720x480 for responsive collapse"
```

### Task 1.6: Local verification

- [ ] **Step 1: Run frontend build:**

```bash
cd internal/panel/frontend && npm run build
```

Expected: PASS (tsc + vite emits to `dist/`).

- [ ] **Step 2: Run frontend tests:**

```bash
cd internal/panel/frontend && npm test
```

Expected: existing tests still pass; no new tests in Phase 1.

- [ ] **Step 3: Run Go checks:**

```bash
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

Expected: gofmt prints nothing, vet clean, tests pass.

### Task 1.7: PR

- [ ] **Step 1: Push branch + open PR:**

```bash
git push -u origin fix/panel-faux-frame
gh pr create --title "fix(panel): remove faux window frame and fluid sizing" --body "$(cat <<'EOF'
## Summary
- Drop the inner `1080x680` `.shp-window` border/radius/overflow that produced the "frame in frame" look and forced both scrollbars.
- Make the panel container fluid: `min-height: 100vh`, `width: 100%`.
- Scale base font with viewport via `clamp(12.5px, 0.85vw + 8px, 15.5px)`.
- Anchor the modal scrim to the viewport (`position: fixed`).
- Themed scrollbars matching design tokens; dark-mode aware via existing variables.
- Lower Wails `MinWidth`/`MinHeight` to `720x480` to give the responsive pass (separate PR) room to collapse.

Spec: \`docs/superpowers/specs/2026-05-14-panel-ui-fixes-design.md\` §3.

## Test plan
- [ ] Local: \`npm run build\` + \`npm test\` pass.
- [ ] Local: \`gofmt -l .\`, \`go vet ./...\`, \`go test -race -count=1 ./...\` pass.
- [ ] After merge of phase 5 (preview shim), visually re-check on macOS at 720×480, 980×700, 1920×1080.
- [ ] Manual Windows verification: build \`SerialHop.exe\` from the PR artifact, confirm no inner border, no spurious scrollbars at default size.
EOF
)"
```

- [ ] **Step 2: Wait for `pr-title` and `verify` jobs to pass. Address any failures.**

---

# Phase 2 — `fix: reconcile tab class names to design tokens`

**Branch:** `fix/panel-tab-class-names`

**Outcome:** Each tab uses the `.shp-*` classes that `global.css` actually styles. Today the tabs reference `.status-tab`, `.lamps`, `.actions`, `.banner-row`, `.devices-tab`, `.logs-tab`, etc. — none of which have CSS rules. Result: tabs are unstyled, which is the root cause of "spacing and alignment broken".

**Cross-tab mapping** (memorize before starting):

| Tab's local class | Replace with | Why |
|---|---|---|
| outer `.<tab>-tab` div | remove the wrapping `<div>` (the `.shp-content__pad` parent already provides padding) | The wrapper has no rule. Stripping it avoids invisible gutter inconsistencies. |
| `.lamps` (Status) | `.shp-lamps` | existing 3-col grid lamp rule (global.css:290-295) |
| `.actions` (Status, Devices, Ports, Logs) | `.shp-btn-row` | gap-8, flex-wrap row of buttons (global.css:481-485) |
| `.update-row` (Status) | `.shp-update` plus `data-tone="green|red|blue"` per `UpdateState` | existing banner rule (global.css:488-499) |
| `.update-buttons` (Status) | `.shp-update__actions` | gap-6 button row inside update banner (global.css:545) |
| `.config-actions` (Config) | `.shp-btn-row` with `style={{ marginTop: 16 }}` | matches service-actions spacing (the row is at the bottom of the form) |
| `.list-field` (Config) | use `<div>` with no class; rows inside become `.shp-listrow` | the existing `.shp-listrow` rule (global.css:685-689) covers the row layout |
| `.list-field__row` (Config) | `.shp-listrow` | existing rule |
| `.banner-row` (Devices) | `.shp-toolbar` | gap-10 banner-and-action row (global.css:812-817) |
| `.empty-banner` (Devices, Ports) | `.shp-toolbar__banner` inside a `.shp-toolbar`, OR an `.shp-empty` block when the tab is otherwise empty | global.css:818-823 / 781-810 |
| `.devices-table`, `.ports-table` | wrap in `.shp-table-wrap` and apply `.shp-table` | existing table rules (global.css:738-779) |
| `.logs-controls` (Logs) | `.shp-logs-controls` | gap-8 flex row (global.css:838-846) |
| `.logs-search` (Logs) | `.shp-input` | existing input rule, sized by the `.shp-logs-controls .shp-input` selector |
| `.logs-view` (Logs) | wrap the table in `.shp-table-wrap` so it scrolls horizontally on small viewports | |
| `.logs-table` (Logs) | `.shp-table shp-logs-table` (both classes; the latter overrides font for mono rendering, global.css:881-908) | |
| `.logs-raw` (Logs, the `<pre>` for stderr) | `.shp-mono-view` | global.css:930-946 |
| `.logs-details` (Logs) | `.shp-mono-view` | same component, used for selected-row JSON detail |
| `.logs-actions` (Logs) | `.shp-btn-row` | |

Each task below applies this mapping to one tab.

### Task 2.1: StatusTab

**Files:**
- Modify: `internal/panel/frontend/src/tabs/StatusTab.tsx`

- [ ] **Step 1: Replace the `return (...)` block of `StatusTab`.** The new body (drop-in replacement for lines 36-69):

```tsx
  return (
    <>
      <section className="shp-lamps">
        <Lamp name="Service" tone={lamps.service.tone} label={lamps.service.label} sub={lamps.service.sub}>
          <Help title="Service" what="Local SerialHop Windows service state." />
        </Lamp>
        <Lamp name="Server" tone={lamps.server.tone} label={lamps.server.label} sub={lamps.server.sub}>
          <Help title="Server" what="Reachability + health of the configured lab-bridge server." />
        </Lamp>
        <Lamp name="Tunnel" tone={lamps.tunnel.tone} label={lamps.tunnel.label} sub={lamps.tunnel.sub}>
          <Help title="Tunnel" what="State of this machine's Chisel reverse tunnel into the lab-bridge." />
        </Lamp>
      </section>

      <div className="shp-btn-row" style={{ marginTop: 16 }}>
        <Button elevated disabled={busy || !buttons.install} onClick={() => adminAction(InstallService)}>Install</Button>
        <Button elevated disabled={busy || !buttons.uninstall} onClick={() => adminAction(UninstallService)}>Uninstall</Button>
        <Button elevated disabled={busy || !buttons.restart} onClick={() => adminAction(RestartService, true)}>Restart</Button>
      </div>

      {update.state !== UpdateState.Idle && (
        <div className="shp-update" data-tone={updateTone(update.state)}>
          <div className="shp-update__msg"><UpdateLabel update={update} /></div>
          <div className="shp-update__actions">
            <UpdateButtons
              update={update}
              onDownload={() => DownloadUpdate()}
              onCancel={() => CancelDownload()}
              onInstall={() => InstallUpdate()}
              onReleaseNotes={() => OpenReleaseNotes()}
            />
          </div>
        </div>
      )}
    </>
  );
```

- [ ] **Step 2: Add this helper above the `return` (right before line 36):**

```tsx
function updateTone(s: UpdateState): "green" | "red" | "blue" | undefined {
  if (s === UpdateState.Installed) return "green";
  if (s === UpdateState.DownloadFailed || s === UpdateState.InstallFailed) return "red";
  if (s === UpdateState.Available || s === UpdateState.Downloading || s === UpdateState.Ready || s === UpdateState.Installing) return "blue";
  return undefined;
}
```

- [ ] **Step 3: Update `UpdateButtons` to drop the wrapping `<div className="update-buttons">`** so the buttons render directly inside `.shp-update__actions`. New body:

```tsx
function UpdateButtons(props: {
  update: UpdateStatePayload;
  onDownload: () => void;
  onCancel: () => void;
  onInstall: () => void;
  onReleaseNotes: () => void;
}) {
  const s = props.update.state;
  return (
    <>
      {s === UpdateState.Available && <>
        <Button variant="primary" onClick={props.onDownload}>Download</Button>
        <Button variant="ghost" onClick={props.onReleaseNotes}>Release notes</Button>
      </>}
      {s === UpdateState.Downloading && <Button variant="ghost" onClick={props.onCancel}>Cancel</Button>}
      {s === UpdateState.DownloadFailed && <Button variant="primary" onClick={props.onDownload}>Retry</Button>}
      {s === UpdateState.Ready && <>
        <Button variant="primary" elevated onClick={props.onInstall}>Install update</Button>
        <Button variant="ghost" onClick={props.onReleaseNotes}>Release notes</Button>
      </>}
      {s === UpdateState.InstallFailed && <Button variant="primary" elevated onClick={props.onInstall}>Retry</Button>}
    </>
  );
}
```

- [ ] **Step 4: Verify build:**

```bash
cd internal/panel/frontend && npm run build
```

Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add internal/panel/frontend/src/tabs/StatusTab.tsx
git commit -m "fix(panel): reconcile StatusTab to .shp-* design tokens"
```

### Task 2.2: ConfigTab

**Files:**
- Modify: `internal/panel/frontend/src/tabs/ConfigTab.tsx`

- [ ] **Step 1: In the top-level `return`, change `<div className="config-tab">` to `<>` (fragment) and `</div>` to `</>` at the closing tag (line 314).** This removes an unstyled wrapper.

- [ ] **Step 2: Change `<div className="config-actions">` (line 292) to `<div className="shp-btn-row" style={{ marginTop: 16 }}>`.**

- [ ] **Step 3: In the `ListField` helper at the bottom of the file (line 333), change `<div className="list-field">` to `<div className="shp-form-section__body" style={{ padding: 0, gap: 8 }}>` and `<div key={i} className="list-field__row">` (line 336) to `<div key={i} className="shp-listrow">`.**

- [ ] **Step 4: Verify build:**

```bash
cd internal/panel/frontend && npm run build && npm test
```

Expected: PASS (ConfigTab has tests in `ConfigTab.test.tsx` that don't care about CSS classes; they still pass).

- [ ] **Step 5: Commit.**

```bash
git add internal/panel/frontend/src/tabs/ConfigTab.tsx
git commit -m "fix(panel): reconcile ConfigTab to .shp-* design tokens"
```

### Task 2.3: DevicesTab

**Files:**
- Modify: `internal/panel/frontend/src/tabs/DevicesTab.tsx`

- [ ] **Step 1: Replace the `return (...)` block (lines 42-62) with:**

```tsx
  return (
    <>
      <div className="shp-toolbar">
        <div className="shp-toolbar__banner">
          {resp.discovered_at ? <>Discovered at <code>{fmtTime(resp.discovered_at)}</code></> : <span>Never run</span>}
        </div>
        <div className="shp-btn-row">
          <Button onClick={rediscover} disabled={busy || !resp.status.reachable}>Rediscover</Button>
          <Button onClick={disconnect} disabled={busy || !resp.status.reachable || empty}>Disconnect all</Button>
          <Button variant="ghost" onClick={refresh} disabled={busy}>Refresh</Button>
        </div>
      </div>
      {banner && (
        <div className="shp-empty">
          <div className="shp-empty__body">{banner}</div>
        </div>
      )}
      {!banner && (
        <div className="shp-table-wrap">
          <table className="shp-table">
            <thead><tr><th>ID</th><th>Type</th><th>Port</th></tr></thead>
            <tbody>
              {[...resp.devices].sort((a, b) => a.id.localeCompare(b.id)).map(d => (
                <tr key={d.id}><td>{d.id}</td><td>{d.type}</td><td>{d.port}</td></tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
```

- [ ] **Step 2: Verify build:**

```bash
cd internal/panel/frontend && npm run build
```

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/src/tabs/DevicesTab.tsx
git commit -m "fix(panel): reconcile DevicesTab to .shp-* design tokens"
```

### Task 2.4: PortsTab

**Files:**
- Modify: `internal/panel/frontend/src/tabs/PortsTab.tsx`

- [ ] **Step 1: Replace the `return (...)` block (lines 43-79) with:**

```tsx
  return (
    <>
      <div className="shp-btn-row" style={{ marginBottom: 12 }}>
        <Button variant="ghost" onClick={refresh} disabled={busy}>Refresh</Button>
        <Button onClick={rediscover} disabled={busy || !resp.status.reachable}>Rediscover</Button>
      </div>
      {banner && (
        <div className="shp-empty">
          <div className="shp-empty__body">{banner}</div>
        </div>
      )}
      {!banner && (
        <div className="shp-table-wrap">
          <table className="shp-table">
            <thead>
              <tr>
                <th>Name</th>
                <th>USB</th>
                <th>VID <Help title="VID" what="USB vendor ID in hexadecimal." /></th>
                <th>PID <Help title="PID" what="USB product ID in hexadecimal." /></th>
                <th>Serial <Help title="Serial number" what="USB serial string if the device reports one." /></th>
                <th>Product <Help title="Product" what="USB product descriptor string." /></th>
                <th>Discovered <Help title="Discovered" what="True if discovery matched a SerialHop device on this port." /></th>
                <th>Device ID <Help title="Device ID" what="The logical device ID this port was bound to during the last discovery." /></th>
              </tr>
            </thead>
            <tbody>
              {[...resp.ports].sort((a, b) => a.name.localeCompare(b.name)).map(p => (
                <tr key={p.name}>
                  <td>{p.name}</td>
                  <td>{p.is_usb ? <span className="shp-check">✓</span> : <span className="shp-dim">—</span>}</td>
                  <td>{p.vid}</td>
                  <td>{p.pid}</td>
                  <td>{p.serial_number}</td>
                  <td>{p.product}</td>
                  <td>{p.discovered ? <span className="shp-check">✓</span> : <span className="shp-dim">—</span>}</td>
                  <td>{p.device_id || <span className="shp-dim">—</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
```

- [ ] **Step 2: Verify build, then commit.**

```bash
cd internal/panel/frontend && npm run build
git add internal/panel/frontend/src/tabs/PortsTab.tsx
git commit -m "fix(panel): reconcile PortsTab to .shp-* design tokens"
```

### Task 2.5: LogsTab

**Files:**
- Modify: `internal/panel/frontend/src/tabs/LogsTab.tsx`

- [ ] **Step 1: Replace the `return (...)` block (lines 73-124) with:**

```tsx
  return (
    <>
      <div className="shp-logs-controls">
        <label className="shp-row">
          <span style={{ marginRight: 4 }}>Stream:</span>
          <select className="shp-select" value={stream} onChange={e => setStream(e.target.value as StreamID)}>
            <option value="service">Service log</option>
            <option value="stderr">Stderr</option>
            <option value="panel">Panel errors</option>
          </select>
          <Help title={streamHelp[stream].title} what={streamHelp[stream].what} />
        </label>
        <label className="shp-row">
          <span style={{ marginRight: 4 }}>Level:</span>
          <select className="shp-select" value={level} onChange={e => setLevel(e.target.value as LevelFilter)} disabled={stream !== "service"}>
            <option>all</option><option>debug</option><option>info</option><option>warn</option><option>error</option>
          </select>
        </label>
        <label className="shp-toggle" data-on={follow}>
          <span className="shp-toggle__sw" />
          <input type="checkbox" style={{ display: "none" }} checked={follow} onChange={e => setFollow(e.target.checked)} />
          Follow
        </label>
        <input className="shp-input" placeholder="Search…" value={search} onChange={e => setSearch(e.target.value)} />
      </div>
      {stream === "service" ? (
        <div className="shp-table-wrap">
          <table className="shp-table shp-logs-table">
            <thead><tr><th className="col-time">Time</th><th className="col-level">Level</th><th>Message</th></tr></thead>
            <tbody>
              {filtered.map((l, i) => l.record && (
                <tr key={i} onClick={() => setSelected(l)} data-selected={selected === l}>
                  <td className="col-time">{String(l.record.time || "")}</td>
                  <td className="col-level"><span className="shp-level-pill" data-level={String(l.record.level || "info").toLowerCase()}>{String(l.record.level || "")}</span></td>
                  <td>{String(l.record.msg || "")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <pre className="shp-mono-view">
          {filtered.map((l, i) => <div key={i}>{l.raw}</div>)}
        </pre>
      )}
      <div ref={endRef} />
      {selected?.record && (
        <pre className="shp-mono-view" style={{ height: "auto", maxHeight: 200, marginTop: 12 }}>
          {JSON.stringify(selected.record, null, 2)}
        </pre>
      )}
      <div className="shp-btn-row" style={{ marginTop: 12 }}>
        <Button variant="ghost" onClick={() => OpenLogsFolder()}>Open logs folder</Button>
      </div>
    </>
  );
```

- [ ] **Step 2: Verify build, then commit.**

```bash
cd internal/panel/frontend && npm run build
git add internal/panel/frontend/src/tabs/LogsTab.tsx
git commit -m "fix(panel): reconcile LogsTab to .shp-* design tokens"
```

### Task 2.6: Local pre-flight + PR

- [ ] **Step 1: Run all pre-flight checks:**

```bash
cd /Users/khamitovdr/lab_devices_client
gofmt -l .
go vet ./...
go test -race -count=1 ./...
cd internal/panel/frontend && npm run build && npm test
```

Expected: all pass.

- [ ] **Step 2: Push and open PR:**

```bash
git push -u origin fix/panel-tab-class-names
gh pr create --title "fix(panel): reconcile tab class names to design tokens" --body "$(cat <<'EOF'
## Summary
The five tab components referenced local class names (\`.status-tab\`, \`.lamps\`, \`.actions\`, \`.config-tab\`, \`.devices-tab\`, \`.logs-tab\`, ...) that have no corresponding rules in \`global.css\` — which is entirely \`.shp-*\` design tokens. As a result the tabs were unstyled containers; this is the root cause of the "spacing and alignment broken" report.

This PR rewires every tab to the existing \`.shp-*\` tokens (lamps, btn-row, update banner, toolbar, table-wrap/table, logs-controls, mono-view, etc.). No CSS changes; no component-API changes; no behavior changes.

Spec: \`docs/superpowers/specs/2026-05-14-panel-ui-fixes-design.md\` §4.

## Test plan
- [ ] \`npm run build\` + \`npm test\` pass.
- [ ] After merge of phase 5 (preview shim), visually re-check all five tabs at 720×480, 980×700, 1920×1080.
- [ ] Manual Windows verification: build \`SerialHop.exe\`, navigate each tab.
EOF
)"
```

---

# Phase 3 — `feat: responsive panel layout`

**Branch:** `feat/panel-responsive-layout`

**Outcome:** One collapse breakpoint at 720 px. Fixed pixel widths replaced with `min-width`/`flex`/`clamp`. Form gets a reading-width cap so 1920-px monitors don't stretch inputs to comically long.

### Task 3.1: Field grid responsive

**Files:**
- Modify: `internal/panel/frontend/src/styles/global.css:594-609` (`.shp-field` rule)

- [ ] **Step 1: Find the current `.shp-field` rule:**

```css
.shp-field {
  display: grid;
  grid-template-columns: 180px 1fr;
  gap: 14px;
  align-items: start;
}
```

- [ ] **Step 2: Replace it with:**

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

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/src/styles/global.css
git commit -m "feat(panel): responsive .shp-field grid collapse at 720px"
```

### Task 3.2: Form section reading-width cap

**Files:**
- Modify: `internal/panel/frontend/src/styles/global.css:588-593` (`.shp-form-section__body`)

- [ ] **Step 1: Find the current rule:**

```css
.shp-form-section__body {
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
```

- [ ] **Step 2: Add `max-width: 880px;` and `width: 100%;` so the body never stretches edge-to-edge on a 1920-px monitor:**

```css
.shp-form-section__body {
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  max-width: 880px;
  width: 100%;
}
```

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/src/styles/global.css
git commit -m "feat(panel): cap form section reading width at 880px"
```

### Task 3.3: Fluid inputs and selects

**Files:**
- Modify: `internal/panel/frontend/src/styles/global.css:844-846` (`.shp-logs-controls .shp-input` and the `.shp-logs-controls .shp-select` rule)

- [ ] **Step 1: Find:**

```css
.shp-logs-controls .shp-select { width: auto; min-width: 0; padding-right: 26px; }
.shp-logs-controls .shp-input { width: 200px; }
```

- [ ] **Step 2: Replace the second line with:**

```css
.shp-logs-controls .shp-input {
  width: auto;
  min-width: 160px;
  flex: 1 1 200px;
  max-width: 320px;
}
```

The select rule is left intact.

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/src/styles/global.css
git commit -m "feat(panel): fluid log search input width"
```

### Task 3.4: Footer progress + modal + table-wrap

**Files:**
- Modify: `internal/panel/frontend/src/styles/global.css:255-262` (`.shp-footer__progress`)
- Modify: `internal/panel/frontend/src/styles/global.css:958-965` (`.shp-modal`)
- Modify: `internal/panel/frontend/src/styles/global.css:773-778` (`.shp-table-wrap`)

- [ ] **Step 1: Change `.shp-footer__progress` `width: 80px;` to `width: clamp(60px, 8vw, 140px);`.**

- [ ] **Step 2: Change `.shp-modal` `width: 420px;` to `width: min(420px, calc(100vw - 32px));`.**

- [ ] **Step 3: Change `.shp-table-wrap` `overflow: hidden;` to `overflow: auto;`.**

- [ ] **Step 4: Commit.**

```bash
git add internal/panel/frontend/src/styles/global.css
git commit -m "feat(panel): fluid footer progress, modal width, table-wrap scroll"
```

### Task 3.5: Local pre-flight + PR

- [ ] **Step 1: Run pre-flight:**

```bash
cd /Users/khamitovdr/lab_devices_client
gofmt -l .
go vet ./...
go test -race -count=1 ./...
cd internal/panel/frontend && npm run build && npm test
```

Expected: all pass.

- [ ] **Step 2: Push and open PR:**

```bash
git push -u origin feat/panel-responsive-layout
gh pr create --title "feat(panel): responsive layout with 720px collapse breakpoint" --body "$(cat <<'EOF'
## Summary
Adds one collapse breakpoint at 720 px and replaces remaining fixed widths with fluid alternatives. Form sections gain a 880-px reading-width cap so large monitors don't stretch inputs unreadably wide. Tables, logs search, footer progress, and modals all become viewport-aware.

Spec: \`docs/superpowers/specs/2026-05-14-panel-ui-fixes-design.md\` §4 (renamed from §3 in spec; check the up-to-date spec for current section number).

## Test plan
- [ ] \`npm run build\` + \`npm test\` pass.
- [ ] After preview shim merges (phase 5), visually verify each tab at 720×480, 980×700, 1920×1080. Label column should collapse to single column under 720 px; form fields should not stretch edge-to-edge at 1920 px.
EOF
)"
```

---

# Phase 4 — `fix: help popover hover, portal, and viewport clamp`

**Branch:** `fix/panel-help-popover`

**Outcome:** `<Help>` opens on hover (with 120-ms grace), closes on leave, sticky on click, dismisses on Esc, accessible via keyboard. Portal-rendered so it never clips on container edges. Viewport-edge clamped so it's always fully visible.

### Task 4.1: Add portal root to index.html and test setup

**Files:**
- Modify: `internal/panel/frontend/index.html`
- Modify: `internal/panel/frontend/src/test/setup.ts`

- [ ] **Step 1: In `index.html`, after `<div id="root"></div>`, add a sibling:**

```html
<div id="root"></div>
<div id="popover-root"></div>
<script type="module" src="/src/main.tsx"></script>
```

- [ ] **Step 2: In `src/test/setup.ts`, add a `beforeEach` that ensures `popover-root` exists in jsdom:**

```ts
import "@testing-library/jest-dom";
import { beforeEach, afterEach } from "vitest";

beforeEach(() => {
  if (!document.getElementById("popover-root")) {
    const root = document.createElement("div");
    root.id = "popover-root";
    document.body.appendChild(root);
  }
});

afterEach(() => {
  document.getElementById("popover-root")?.replaceChildren();
});
```

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/index.html internal/panel/frontend/src/test/setup.ts
git commit -m "fix(panel): add #popover-root host for portal rendering"
```

### Task 4.2: Write failing tests for Help hover behavior

**Files:**
- Create: `internal/panel/frontend/src/components/Help.test.tsx`

- [ ] **Step 1: Create the test file with these tests:**

```tsx
import { render, screen, fireEvent, act } from "@testing-library/react";
import { describe, test, expect, vi, beforeEach, afterEach } from "vitest";
import { Help } from "./Help";

describe("Help", () => {
  beforeEach(() => { vi.useFakeTimers({ shouldAdvanceTime: true }); });
  afterEach(() => { vi.useRealTimers(); });

  test("renders ? anchor with no popover initially", () => {
    render(<Help title="T" what="W" />);
    expect(screen.getByRole("button")).toHaveTextContent("?");
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  test("opens immediately on anchor mouseEnter", () => {
    render(<Help title="T" what="W" />);
    fireEvent.mouseEnter(screen.getByRole("button"));
    expect(screen.getByRole("tooltip")).toHaveTextContent("W");
  });

  test("closes 120ms after anchor mouseLeave", () => {
    render(<Help title="T" what="W" />);
    fireEvent.mouseEnter(screen.getByRole("button"));
    fireEvent.mouseLeave(screen.getByRole("button"));
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
    act(() => { vi.advanceTimersByTime(119); });
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
    act(() => { vi.advanceTimersByTime(2); });
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  test("popover mouseEnter cancels pending close", () => {
    render(<Help title="T" what="W" />);
    fireEvent.mouseEnter(screen.getByRole("button"));
    fireEvent.mouseLeave(screen.getByRole("button"));
    fireEvent.mouseEnter(screen.getByRole("tooltip"));
    act(() => { vi.advanceTimersByTime(500); });
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
  });

  test("popover mouseLeave schedules close", () => {
    render(<Help title="T" what="W" />);
    fireEvent.mouseEnter(screen.getByRole("button"));
    fireEvent.mouseEnter(screen.getByRole("tooltip"));
    fireEvent.mouseLeave(screen.getByRole("tooltip"));
    act(() => { vi.advanceTimersByTime(121); });
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  test("click makes popover sticky; mouseLeave does not close it", () => {
    render(<Help title="T" what="W" />);
    fireEvent.click(screen.getByRole("button"));
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
    fireEvent.mouseLeave(screen.getByRole("button"));
    act(() => { vi.advanceTimersByTime(500); });
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
  });

  test("Esc closes sticky popover", () => {
    render(<Help title="T" what="W" />);
    fireEvent.click(screen.getByRole("button"));
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  test("focus opens, blur schedules close after 120ms", () => {
    render(<Help title="T" what="W" />);
    fireEvent.focus(screen.getByRole("button"));
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
    fireEvent.blur(screen.getByRole("button"));
    act(() => { vi.advanceTimersByTime(121); });
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  test("click on sticky toggles back to closed", () => {
    render(<Help title="T" what="W" />);
    const anchor = screen.getByRole("button");
    fireEvent.click(anchor);
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
    fireEvent.click(anchor);
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  test("renders defaultVal and when blocks when provided", () => {
    render(<Help title="T" what="W" defaultVal="DV" when="WH" />);
    fireEvent.mouseEnter(screen.getByRole("button"));
    expect(screen.getByText("DV")).toBeInTheDocument();
    expect(screen.getByText("WH")).toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run tests to verify they fail:**

```bash
cd internal/panel/frontend && npm test -- Help.test
```

Expected: tests fail because the current `Help.tsx` is click-toggle, not hover. Capture the failures — they are the spec.

- [ ] **Step 3: Commit failing tests.**

```bash
git add internal/panel/frontend/src/components/Help.test.tsx
git commit -m "test(panel): failing tests for Help hover state machine"
```

### Task 4.3: Implement the new Help component

**Files:**
- Modify (full rewrite): `internal/panel/frontend/src/components/Help.tsx`

- [ ] **Step 1: Replace `Help.tsx` with this implementation:**

```tsx
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

interface HelpProps {
  title: string;
  what: string;
  defaultVal?: string;
  when?: string;
}

type OpenState = "closed" | "hover" | "sticky";

interface Coords { top: number; left: number; flipped: boolean; arrowLeft: number; }

const CLOSE_DELAY_MS = 120;
const OFFSET_PX = 8;
const VIEWPORT_PAD_PX = 8;
const DEFAULT_WIDTH_PX = 280;

export function Help({ title, what, defaultVal, when }: HelpProps) {
  const [state, setState] = useState<OpenState>("closed");
  const [coords, setCoords] = useState<Coords | null>(null);
  const anchorRef = useRef<HTMLSpanElement | null>(null);
  const popoverRef = useRef<HTMLDivElement | null>(null);
  const closeTimerRef = useRef<number | null>(null);

  const cancelClose = useCallback(() => {
    if (closeTimerRef.current !== null) {
      window.clearTimeout(closeTimerRef.current);
      closeTimerRef.current = null;
    }
  }, []);

  // We read `state` inside scheduleClose by deferring the read to the timer
  // callback so the latest value is honored.
  const scheduleClose = useCallback(() => {
    cancelClose();
    closeTimerRef.current = window.setTimeout(() => {
      setState(prev => (prev === "sticky" ? prev : "closed"));
      closeTimerRef.current = null;
    }, CLOSE_DELAY_MS);
  }, [cancelClose]);

  const computePosition = useCallback(() => {
    const anchor = anchorRef.current;
    if (!anchor) return;
    const ar = anchor.getBoundingClientRect();
    const pop = popoverRef.current;
    const pw = pop?.offsetWidth ?? DEFAULT_WIDTH_PX;
    const ph = pop?.offsetHeight ?? 100;
    const vw = window.innerWidth;
    const vh = window.innerHeight;

    let left = ar.left - OFFSET_PX;
    let top = ar.bottom + OFFSET_PX;
    let flipped = false;

    if (left + pw > vw - VIEWPORT_PAD_PX) left = vw - VIEWPORT_PAD_PX - pw;
    if (left < VIEWPORT_PAD_PX) left = VIEWPORT_PAD_PX;
    if (top + ph > vh - VIEWPORT_PAD_PX) {
      top = ar.top - ph - OFFSET_PX;
      flipped = true;
    }
    if (top < VIEWPORT_PAD_PX) top = VIEWPORT_PAD_PX;

    const anchorMidX = ar.left + ar.width / 2;
    const arrowLeft = Math.max(8, Math.min(pw - 18, anchorMidX - left - 5));
    setCoords({ top, left, flipped, arrowLeft });
  }, []);

  // Recompute position on open, on resize, and on any ancestor scroll.
  useLayoutEffect(() => {
    if (state === "closed") { setCoords(null); return; }
    computePosition();
    const onResize = () => computePosition();
    const onScroll = () => computePosition();
    window.addEventListener("resize", onResize);
    window.addEventListener("scroll", onScroll, true);
    return () => {
      window.removeEventListener("resize", onResize);
      window.removeEventListener("scroll", onScroll, true);
    };
  }, [state, computePosition]);

  // After the popover mounts, re-measure once with the real DOM dimensions.
  useLayoutEffect(() => {
    if (state !== "closed" && popoverRef.current && coords) {
      const real = popoverRef.current.offsetWidth;
      if (Math.abs(real - DEFAULT_WIDTH_PX) > 0.5 && Math.abs(real - (coords ? popoverRef.current.offsetWidth : 0)) >= 0) {
        // Trigger one re-measure on first mount; guarded above so we don't loop.
        computePosition();
      }
    }
    // intentionally not depending on coords to avoid loops; runs once per state transition
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state]);

  // Esc + outside-click handling (sticky only for outside-click).
  useEffect(() => {
    if (state === "closed") return;
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") setState("closed"); };
    const onMouseDown = (e: MouseEvent) => {
      if (state !== "sticky") return;
      const t = e.target as Node;
      if (anchorRef.current?.contains(t)) return;
      if (popoverRef.current?.contains(t)) return;
      setState("closed");
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("mousedown", onMouseDown);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onMouseDown);
    };
  }, [state]);

  // Cancel any pending timer on unmount.
  useEffect(() => () => cancelClose(), [cancelClose]);

  const onAnchorEnter = () => { cancelClose(); if (state === "closed") setState("hover"); };
  const onAnchorLeave = () => { if (state === "hover") scheduleClose(); };
  const onPopoverEnter = () => cancelClose();
  const onPopoverLeave = () => { if (state === "hover") scheduleClose(); };
  const onAnchorFocus = () => { cancelClose(); if (state === "closed") setState("hover"); };
  const onAnchorBlur = () => { if (state === "hover") scheduleClose(); };
  const onAnchorClick = () => {
    cancelClose();
    setState(s => (s === "sticky" ? "closed" : "sticky"));
  };
  const onAnchorKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onAnchorClick();
    }
  };

  const portalRoot = typeof document !== "undefined" ? document.getElementById("popover-root") : null;
  const popover = state !== "closed" && coords && portalRoot
    ? createPortal(
        <div
          ref={popoverRef}
          className="shp-popover"
          data-flipped={coords.flipped ? "true" : "false"}
          role="tooltip"
          tabIndex={-1}
          style={{
            position: "fixed",
            top: coords.top,
            left: coords.left,
            width: DEFAULT_WIDTH_PX,
            // CSS var consumed by the ::before arrow rule
            ["--shp-arrow-left" as never]: `${coords.arrowLeft}px`,
          }}
          onMouseEnter={onPopoverEnter}
          onMouseLeave={onPopoverLeave}
        >
          <h5>{title}</h5>
          <p>{what}</p>
          {defaultVal && (
            <dl>
              <dt>Default</dt>
              <dd>{defaultVal}</dd>
            </dl>
          )}
          {when && <p style={{ marginTop: 6 }}>{when}</p>}
        </div>,
        portalRoot,
      )
    : null;

  return (
    <span style={{ position: "relative", display: "inline-flex" }}>
      <span
        ref={anchorRef}
        className="shp-help"
        data-open={state !== "closed"}
        role="button"
        tabIndex={0}
        onMouseEnter={onAnchorEnter}
        onMouseLeave={onAnchorLeave}
        onFocus={onAnchorFocus}
        onBlur={onAnchorBlur}
        onClick={onAnchorClick}
        onKeyDown={onAnchorKeyDown}
      >
        ?
      </span>
      {popover}
    </span>
  );
}
```

- [ ] **Step 2: Update the `.shp-popover` CSS** in `internal/panel/frontend/src/styles/global.css:372-398` to honor the new arrow CSS var and the flipped state. Replace the existing rule:

```css
.shp-popover {
  /* position: fixed is set inline; width is set inline; only style here */
  background: var(--surface);
  border: 1px solid var(--border-strong);
  border-radius: 6px;
  padding: 12px 14px;
  font-size: 12px;
  color: var(--text-secondary);
  line-height: 1.5;
  z-index: 50;
  box-shadow: var(--shadow-popover);
  text-align: left;
  cursor: default;
}
.shp-popover::before {
  content: "";
  position: absolute;
  top: -6px;
  left: var(--shp-arrow-left, 12px);
  width: 10px; height: 10px;
  background: var(--surface);
  border-left: 1px solid var(--border-strong);
  border-top: 1px solid var(--border-strong);
  transform: rotate(45deg);
}
.shp-popover[data-flipped="true"]::before {
  top: auto;
  bottom: -6px;
  border-left: none;
  border-top: none;
  border-right: 1px solid var(--border-strong);
  border-bottom: 1px solid var(--border-strong);
}
```

- [ ] **Step 3: Run tests:**

```bash
cd internal/panel/frontend && npm test -- Help.test
```

Expected: all tests in `Help.test.tsx` PASS.

- [ ] **Step 4: Run the full test suite and build:**

```bash
cd internal/panel/frontend && npm test && npm run build
```

Expected: all tests pass; build succeeds.

- [ ] **Step 5: Commit.**

```bash
git add internal/panel/frontend/src/components/Help.tsx internal/panel/frontend/src/styles/global.css
git commit -m "fix(panel): hover-driven help popover with portal and viewport clamp"
```

### Task 4.4: Pre-flight + PR

- [ ] **Step 1: Run pre-flight:**

```bash
cd /Users/khamitovdr/lab_devices_client
gofmt -l . && go vet ./... && go test -race -count=1 ./...
cd internal/panel/frontend && npm run build && npm test
```

- [ ] **Step 2: Push and open PR:**

```bash
git push -u origin fix/panel-help-popover
gh pr create --title "fix(panel): help popover hover + portal + viewport clamp" --body "$(cat <<'EOF'
## Summary
Rewrites \`<Help>\` to fix three reported issues at once:
- Open on hover (with 120 ms close grace so the pointer can travel from \`?\` to popover), not on click.
- Render via React portal into a dedicated \`#popover-root\` so the popover escapes every \`overflow: auto\` ancestor — fixes the cropping report.
- Position with \`getBoundingClientRect()\` + viewport-edge collision: shift left when overflowing right, flip above when overflowing bottom, clamp to padding.

Also preserves: keyboard access (focus/blur + Esc), sticky-on-click for touchscreens, outside-click closes sticky.

Spec: \`docs/superpowers/specs/2026-05-14-panel-ui-fixes-design.md\` §5.

## Test plan
- [ ] \`npm test\` (new \`Help.test.tsx\` covers hover/sticky/keyboard/Esc).
- [ ] After preview-shim merge, hover and click Help icons in all five tabs, both at narrow and wide viewports — popover should never clip.
- [ ] Manual Windows verification: confirm the WebView2 build behaves identically to Chromium.
EOF
)"
```

---

# Phase 5 — `feat: vite dev preview with wails-shim for macOS`

**Branch:** `feat/panel-preview-shim`

**Outcome:** `task preview` (or `npm run preview:mac` from the frontend dir) opens the panel in any desktop browser on macOS with mock Wails bindings + an event-bus shim. Same preview build powers the Playwright CI job in Phase 6.

### Task 5.1: Seed data + scenario store

**Files:**
- Create: `internal/panel/frontend/src/preview-shim/seed.ts`

- [ ] **Step 1: Create the file with this content:**

```ts
// Seed data and a tiny in-memory store for the macOS preview shim.
// Mirrors the DTOs the SPA expects, with realistic-looking values.
// VITE_PREVIEW=1 only — never bundled into the Wails-targeted build.

import type { ButtonStatePayload, FooterPayload, LampWhich, LampPayload, LogLinePayload, Tone, UpdateStatePayload } from "../types";
import { UpdateState } from "../types";

export type ScenarioId =
  | "default"
  | "service-stopped"
  | "config-invalid"
  | "update-available"
  | "downloading-update";

export interface ConfigShape {
  lab_bridge: { host: string; user: string; pass: string };
  rest: { port: number };
  discovery: { include: string[]; exclude: string[]; post_open_settle_ms: number };
  log: { level: string };
  raw_serial: { enabled: boolean };
  auto_update: { enabled: boolean };
  flashing: { enabled: boolean; backup_dir: string; keep_n: number };
}

export const defaultConfig: ConfigShape = {
  lab_bridge: { host: "111.88.145.138", user: "preview-user", pass: "preview-pass" },
  rest: { port: 0 },
  discovery: { include: [], exclude: [], post_open_settle_ms: 2000 },
  log: { level: "info" },
  raw_serial: { enabled: false },
  auto_update: { enabled: true },
  flashing: { enabled: false, backup_dir: "", keep_n: 10 },
};

export const fakeDevices = [
  { id: "petri-A", type: "Petri Camera", type_code: 1, port: "COM3" },
  { id: "incubator-B", type: "Incubator", type_code: 2, port: "COM4" },
  { id: "balance-C", type: "Balance", type_code: 3, port: "COM7" },
];

export const fakePorts = [
  { name: "COM3", is_usb: true, vid: "2341", pid: "0043", serial_number: "AB-001", product: "Arduino Uno", discovered: true, device_id: "petri-A" },
  { name: "COM4", is_usb: true, vid: "1A86", pid: "7523", serial_number: "",       product: "CH340",      discovered: true, device_id: "incubator-B" },
  { name: "COM7", is_usb: true, vid: "10C4", pid: "EA60", serial_number: "C-3201", product: "Silicon Labs CP210x", discovered: true, device_id: "balance-C" },
  { name: "COM1", is_usb: false, vid: "",    pid: "",    serial_number: "",       product: "",           discovered: false },
];

interface Store {
  config: ConfigShape;
  scenario: ScenarioId;
  lamps: Record<LampWhich, { tone: Tone; label: string; sub?: string }>;
  buttons: ButtonStatePayload;
  warn: string | null;
  footer: FooterPayload | null;
  update: UpdateStatePayload;
  logLines: LogLinePayload[];
  activeLogStream: "service" | "stderr" | "panel" | null;
}

export const store: Store = {
  config: structuredClone(defaultConfig),
  scenario: "default",
  lamps: {
    service: { tone: "green", label: "Running", sub: "Up since 09:14" },
    server:  { tone: "green", label: "Reachable", sub: "118 ms" },
    tunnel:  { tone: "green", label: "Connected", sub: "RTT 33 ms" },
  },
  buttons: { install: false, uninstall: true, restart: true },
  warn: null,
  footer: { kind: "ok", text: "All systems nominal.", time: new Date().toISOString() },
  update: { state: UpdateState.Idle, release_tag: "" },
  logLines: [],
  activeLogStream: null,
};

export function applyScenario(s: ScenarioId): void {
  store.scenario = s;
  switch (s) {
    case "default":
      store.lamps.service = { tone: "green", label: "Running" };
      store.lamps.server  = { tone: "green", label: "Reachable" };
      store.lamps.tunnel  = { tone: "green", label: "Connected" };
      store.buttons = { install: false, uninstall: true, restart: true };
      store.warn = null;
      store.update = { state: UpdateState.Idle, release_tag: "" };
      break;
    case "service-stopped":
      store.lamps.service = { tone: "red", label: "Stopped" };
      store.lamps.server  = { tone: "yellow", label: "Reachable (service down)" };
      store.lamps.tunnel  = { tone: "red", label: "Disconnected" };
      store.buttons = { install: true, uninstall: false, restart: false };
      break;
    case "config-invalid":
      store.warn = "⚠ Config file is malformed (line 12: unexpected indentation).";
      store.lamps.service = { tone: "yellow", label: "Running (config invalid)" };
      break;
    case "update-available":
      store.update = { state: UpdateState.Available, release_tag: "v0.15.0" };
      break;
    case "downloading-update":
      store.update = { state: UpdateState.Downloading, release_tag: "v0.15.0" };
      break;
  }
}
```

- [ ] **Step 2: Verify it typechecks (no need to run yet; will be exercised by the entry-point task).**

```bash
cd internal/panel/frontend && npx tsc --noEmit
```

Expected: PASS.

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/src/preview-shim/seed.ts
git commit -m "feat(panel): preview-shim seed data and scenario store"
```

### Task 5.2: Bindings shim

**Files:**
- Create: `internal/panel/frontend/src/preview-shim/bindings.ts`

- [ ] **Step 1: Create the file:**

```ts
// In-memory fakes for window.go.main.App. The SPA's binding wrapper at
// src/wails/go/main/App.ts dispatches every call through this object, so
// methods must be named exactly as exported there.

import type { FieldErrorDTO } from "../types";
import { store, fakeDevices, fakePorts } from "./seed";
import { emit } from "./events";

const delay = (ms: number) => new Promise<void>(r => setTimeout(r, ms));
const ok = { ok: true };

export const App: Record<string, (...args: any[]) => Promise<any>> = {
  GetVersion: async () => "0.14.4-preview",

  LoadConfigFromDisk: async () => structuredClone(store.config),

  ValidateConfig: async (cfg: any): Promise<FieldErrorDTO[]> => {
    const errs: FieldErrorDTO[] = [];
    if (!cfg?.lab_bridge?.user) errs.push({ field: "lab_bridge.user", detail: "Username is required." });
    if (!cfg?.lab_bridge?.pass) errs.push({ field: "lab_bridge.pass", detail: "Password is required." });
    return errs;
  },

  SaveConfig: async (cfg: any) => {
    await delay(200);
    store.config = structuredClone(cfg);
    emit("footer:set", { kind: "ok", text: "Saved.", time: new Date().toISOString() });
    return ok;
  },

  VerifyCredentials: async (_host: string, user: string, pass: string) => {
    await delay(150);
    if (user === "bad") return { outcome: "unauthorized", detail: "rejected" };
    if (!user || !pass) return { outcome: "unauthorized", detail: "blank" };
    return { outcome: "ok" };
  },

  OpenConfigInEditor: async () => { console.info("[preview] would open config in editor"); },
  OpenLogsFolder:     async () => { console.info("[preview] would open logs folder"); },
  OpenReleaseNotes:   async () => { console.info("[preview] would open release notes"); },
  PickBackupDir:      async () => "C:/ProgramData/SerialHop/backups",

  InstallService: async () => { await delay(200); emit("footer:set", { kind: "ok", text: "Service installed.", time: new Date().toISOString() }); return ok; },
  UninstallService: async () => { await delay(200); emit("footer:set", { kind: "ok", text: "Service uninstalled.", time: new Date().toISOString() }); return ok; },
  RestartService: async () => { await delay(200); emit("footer:set", { kind: "ok", text: "Service restarted.", time: new Date().toISOString() }); return ok; },

  TriggerProbe: async () => {},
  CheckForUpdate: async () => {},
  DownloadUpdate: async () => {},
  CancelDownload: async () => {},
  InstallUpdate: async () => ok,

  GetDevices: async () => ({
    devices: structuredClone(fakeDevices),
    discovered_at: new Date().toISOString(),
    status: { reachable: store.lamps.service.tone !== "red" },
  }),
  Discover: async () => ({
    devices: structuredClone(fakeDevices),
    discovered_at: new Date().toISOString(),
    status: { reachable: store.lamps.service.tone !== "red" },
  }),
  DisconnectAll: async () => ok,
  GetPorts: async () => ({
    ports: structuredClone(fakePorts),
    status: { reachable: store.lamps.service.tone !== "red" },
  }),

  StartLogStream: async (id: string) => {
    store.activeLogStream = id as any;
  },
  StopLogStream: async () => {
    store.activeLogStream = null;
  },
};
```

- [ ] **Step 2: Commit.**

```bash
git add internal/panel/frontend/src/preview-shim/bindings.ts
git commit -m "feat(panel): preview-shim faked window.go.main.App"
```

### Task 5.3: Events shim

**Files:**
- Create: `internal/panel/frontend/src/preview-shim/events.ts`

- [ ] **Step 1: Create the file:**

```ts
// In-memory implementation of window.runtime. Mirrors the surface used by
// src/wails/runtime/runtime.ts: EventsOn, EventsOff, EventsEmit.
//
// Also provides the periodic event simulator that keeps the SPA's lamps,
// footer, and log views fed when the preview is open.

import { store, fakeDevices } from "./seed";
import type { LogLinePayload } from "../types";

type Listener = (...data: any[]) => void;
const listeners = new Map<string, Set<Listener>>();

export const runtime = {
  EventsOn(name: string, cb: Listener) {
    if (!listeners.has(name)) listeners.set(name, new Set());
    listeners.get(name)!.add(cb);
    return () => runtime.EventsOff(name);
  },
  EventsOff(...names: string[]) {
    for (const n of names) listeners.delete(n);
  },
  EventsEmit(name: string, ...data: any[]) {
    emit(name, ...data);
  },
};

export function emit(name: string, ...data: any[]): void {
  const set = listeners.get(name);
  if (!set) return;
  for (const cb of [...set]) {
    try { cb(...data); } catch (e) { console.error("[preview] listener error:", e); }
  }
}

let started = false;

export function startSimulator(): void {
  if (started) return;
  started = true;

  // Emit initial state once any listener attaches. Use a microtask so
  // App.tsx has time to subscribe.
  queueMicrotask(() => {
    emitInitial();
  });

  // Periodic log lines while a service stream is active.
  let logSeq = 0;
  setInterval(() => {
    if (store.activeLogStream !== "service") return;
    const device = fakeDevices[logSeq % fakeDevices.length];
    const line: LogLinePayload = {
      stream: "service",
      record: {
        time: new Date().toISOString(),
        level: ["info", "info", "info", "warn", "error"][logSeq % 5],
        msg: `heartbeat from ${device.id}`,
        device_id: device.id,
        port: device.port,
      },
    };
    emit("log:line", line);
    logSeq++;
  }, 1500);
}

function emitInitial(): void {
  for (const which of ["service", "server", "tunnel"] as const) {
    emit("status:lamp", { which, ...store.lamps[which] });
  }
  emit("buttons:state", store.buttons);
  if (store.warn) emit("warn:set", store.warn); else emit("warn:clear");
  if (store.footer) emit("footer:set", store.footer);
  emit("update:state", store.update);
}

export function resyncAll(): void { emitInitial(); }
```

- [ ] **Step 2: Commit.**

```bash
git add internal/panel/frontend/src/preview-shim/events.ts
git commit -m "feat(panel): preview-shim faked window.runtime event bus"
```

### Task 5.4: Scenario switcher component

**Files:**
- Create: `internal/panel/frontend/src/preview-shim/Scenarios.tsx`

- [ ] **Step 1: Create:**

```tsx
import { useState } from "react";
import { applyScenario, store, type ScenarioId } from "./seed";
import { resyncAll } from "./events";

const OPTIONS: { id: ScenarioId; label: string }[] = [
  { id: "default", label: "Default (healthy)" },
  { id: "service-stopped", label: "Service stopped" },
  { id: "config-invalid", label: "Config invalid" },
  { id: "update-available", label: "Update available" },
  { id: "downloading-update", label: "Downloading update" },
];

export function Scenarios() {
  const [s, setS] = useState<ScenarioId>(store.scenario);
  return (
    <div style={{
      position: "fixed", top: 10, right: 14, zIndex: 100,
      background: "var(--surface)", border: "1px solid var(--border-strong)",
      borderRadius: 4, padding: "6px 10px", boxShadow: "var(--shadow-popover)",
      fontFamily: "'IBM Plex Sans', system-ui, sans-serif", fontSize: 12,
    }}>
      <label style={{ display: "flex", gap: 6, alignItems: "center" }}>
        <span style={{ color: "var(--text-muted)" }}>preview:</span>
        <select
          value={s}
          onChange={e => { const v = e.target.value as ScenarioId; setS(v); applyScenario(v); resyncAll(); }}
          style={{ font: "inherit" }}
        >
          {OPTIONS.map(o => <option key={o.id} value={o.id}>{o.label}</option>)}
        </select>
      </label>
    </div>
  );
}
```

- [ ] **Step 2: Commit.**

```bash
git add internal/panel/frontend/src/preview-shim/Scenarios.tsx
git commit -m "feat(panel): preview-shim scenario switcher control"
```

### Task 5.5: Preview HTML entry + boot script

**Files:**
- Create: `internal/panel/frontend/preview.html`
- Create: `internal/panel/frontend/src/preview-entry.tsx`

- [ ] **Step 1: Create `internal/panel/frontend/preview.html`:**

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>SerialHop (preview)</title>
  </head>
  <body>
    <div id="root"></div>
    <div id="popover-root"></div>
    <script type="module" src="/src/preview-entry.tsx"></script>
  </body>
</html>
```

- [ ] **Step 2: Create `internal/panel/frontend/src/preview-entry.tsx`:**

```tsx
import { createRoot } from "react-dom/client";
import { App as ShimApp } from "./preview-shim/bindings";
import { runtime, startSimulator } from "./preview-shim/events";
import { Scenarios } from "./preview-shim/Scenarios";
import { App } from "./App";
import "./styles/global.css";

// Install Wails-runtime globals BEFORE the SPA's modules execute. The SPA's
// runtime wrapper modules (src/wails/...) call into these globals lazily on
// each invocation, so installation order matters only relative to the first
// call — but earlier is safer.
declare global {
  interface Window {
    go?: { main: { App: typeof ShimApp } };
    runtime?: typeof runtime;
  }
}
window.go = { main: { App: ShimApp } };
window.runtime = runtime;

startSimulator();

const root = createRoot(document.getElementById("root")!);
root.render(
  <>
    <App />
    <Scenarios />
  </>,
);
```

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/preview.html internal/panel/frontend/src/preview-entry.tsx
git commit -m "feat(panel): preview.html entry installs shim globals before SPA boot"
```

### Task 5.6: Vite config multi-entry support

**Files:**
- Modify: `internal/panel/frontend/vite.config.ts`

- [ ] **Step 1: Replace with:**

```ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { resolve } from "node:path";

export default defineConfig(({ mode }) => ({
  plugins: [react()],
  build: {
    outDir: mode === "preview" ? "dist-preview" : "dist",
    emptyOutDir: true,
    rollupOptions: {
      input: mode === "preview"
        ? { preview: resolve(__dirname, "preview.html") }
        : { main: resolve(__dirname, "index.html") },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
  },
}));
```

- [ ] **Step 2: Verify config typechecks (vite reads it as ESM, so syntax errors surface on next build):**

```bash
cd internal/panel/frontend && npx vite build --mode=production --outDir dist
```

Expected: PASS (production build still emits to `dist/`).

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/vite.config.ts
git commit -m "feat(panel): vite config supports preview multi-entry build"
```

### Task 5.7: npm scripts + Taskfile target + gitignore

**Files:**
- Modify: `internal/panel/frontend/package.json`
- Modify: `Taskfile.yaml`
- Modify: `internal/panel/frontend/.gitignore`

- [ ] **Step 1: In `internal/panel/frontend/package.json`, add to `scripts`:**

```json
"preview:mac":   "VITE_PREVIEW=1 vite --host --open /preview.html",
"preview:build": "VITE_PREVIEW=1 vite build --mode preview"
```

Make sure existing scripts are preserved. The full `scripts` block should look like:

```json
"scripts": {
  "dev": "vite",
  "build": "tsc --noEmit && vite build",
  "test": "vitest run",
  "test:watch": "vitest",
  "lint": "eslint src --ext .ts,.tsx",
  "preview:mac":   "VITE_PREVIEW=1 vite --host --open /preview.html",
  "preview:build": "VITE_PREVIEW=1 vite build --mode preview"
}
```

- [ ] **Step 2: Add a `preview` task to `Taskfile.yaml`:**

Read the current `Taskfile.yaml`, find the `tasks:` section, and add a sibling to existing tasks:

```yaml
  preview:
    desc: "Run the panel UI in a desktop browser (macOS / Linux). Uses a Wails-runtime shim — no Windows or WebView2 needed."
    dir: internal/panel/frontend
    cmds:
      - npm install
      - npm run preview:mac
```

- [ ] **Step 3: Append to `internal/panel/frontend/.gitignore`:**

```
dist-preview/
```

- [ ] **Step 4: Smoke-test the preview locally on this macOS host:**

```bash
cd internal/panel/frontend && npm run preview:mac
```

Expected: vite starts at `http://localhost:5173/preview.html`, opens in your default browser, and the panel renders with the seeded data. The scenario dropdown appears top-right. Confirm:
- No "frame in frame" (Phase 1 already merged, or visible only after rebase).
- Tabs render with `.shp-*` styles (Phase 2 already merged, or visible only after rebase).
- Hovering `?` icons shows popovers (Phase 4).

Press Ctrl-C to stop vite.

- [ ] **Step 5: Smoke-test the preview build:**

```bash
cd internal/panel/frontend && npm run preview:build
ls dist-preview/
```

Expected: `dist-preview/` contains `preview.html` and an `assets/` subdirectory.

- [ ] **Step 6: Commit.**

```bash
git add internal/panel/frontend/package.json Taskfile.yaml internal/panel/frontend/.gitignore
git commit -m "feat(panel): preview:mac npm script and task preview target"
```

### Task 5.8: Pre-flight + PR

- [ ] **Step 1: Run pre-flight:**

```bash
cd /Users/khamitovdr/lab_devices_client
gofmt -l . && go vet ./... && go test -race -count=1 ./...
cd internal/panel/frontend && npm run build && npm test
```

- [ ] **Step 2: Push and open PR:**

```bash
git push -u origin feat/panel-preview-shim
gh pr create --title "feat(panel): vite dev preview with wails-shim for macOS" --body "$(cat <<'EOF'
## Summary
\`wails dev\` requires WebView2 (Windows). This PR adds a path to iterate on the panel UI on macOS by booting the SPA in plain Chrome/Edge via Vite, after a shim installs \`window.go.main.App\` + \`window.runtime\` globals with realistic seeded data.

- New shim under \`src/preview-shim/\` (bindings, events, seed, scenarios).
- New \`preview.html\` + \`preview-entry.tsx\` swap the boot entry under \`VITE_PREVIEW=1\`.
- New scripts: \`npm run preview:mac\` (HMR) and \`npm run preview:build\` (used by phase 6 CI).
- New \`task preview\` for parity with \`task build\` / \`task test\`.
- Scenario switcher (top-right) lets you toggle service-stopped, config-invalid, update-available, downloading-update.

Spec: \`docs/superpowers/specs/2026-05-14-panel-ui-fixes-design.md\` §6.

## Test plan
- [ ] Local on macOS: \`task preview\` opens the panel in the browser. All five tabs render. Scenario switcher cycles state.
- [ ] \`npm run preview:build\` emits \`dist-preview/preview.html\`.
- [ ] Production builds unaffected: \`npm run build\` still emits \`dist/index.html\`.
- [ ] \`tsc --noEmit\` clean (the shim TS is included in the SPA tsconfig).
EOF
)"
```

---

# Phase 6 — `ci: playwright UI invariant checks on PR`

**Branch:** `ci/panel-ui-checks`

**Outcome:** A new `ui-checks` job in `pr.yml` runs Playwright headless Chromium against the preview build at three viewport sizes. Asserts no faux-frame, no spurious scrollbars, help popover hover/sticky/keyboard, popover always inside viewport, no console errors during tab navigation. Gated by `dorny/paths-filter` so PRs that don't touch UI exit in ~2 s.

### Task 6.1: Install Playwright

**Files:**
- Modify: `internal/panel/frontend/package.json`

- [ ] **Step 1: Add `@playwright/test` to devDependencies:**

```bash
cd internal/panel/frontend
npm install --save-dev @playwright/test@latest
```

This will update `package.json` and `package-lock.json`.

- [ ] **Step 2: Add a test script entry. Update the `scripts` block to include:**

```json
"playwright": "playwright test"
```

So the full block reads:

```json
"scripts": {
  "dev": "vite",
  "build": "tsc --noEmit && vite build",
  "test": "vitest run",
  "test:watch": "vitest",
  "lint": "eslint src --ext .ts,.tsx",
  "preview:mac":   "VITE_PREVIEW=1 vite --host --open /preview.html",
  "preview:build": "VITE_PREVIEW=1 vite build --mode preview",
  "playwright": "playwright test"
}
```

- [ ] **Step 3: Install Chromium locally for testing:**

```bash
cd internal/panel/frontend && npx playwright install chromium
```

- [ ] **Step 4: Commit.**

```bash
git add internal/panel/frontend/package.json internal/panel/frontend/package-lock.json
git commit -m "ci: add @playwright/test dev dependency"
```

### Task 6.2: Playwright config

**Files:**
- Create: `internal/panel/frontend/playwright.config.ts`

- [ ] **Step 1: Create:**

```ts
import { defineConfig, devices } from "@playwright/test";

const PORT = 4173;

export default defineConfig({
  testDir: "./playwright",
  timeout: 30_000,
  expect: { timeout: 5_000 },
  fullyParallel: true,
  reporter: process.env.CI ? [["github"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: `http://localhost:${PORT}`,
    actionTimeout: 5_000,
  },
  projects: [
    { name: "min",     use: { ...devices["Desktop Chrome"], viewport: { width: 720, height: 480 } } },
    { name: "default", use: { ...devices["Desktop Chrome"], viewport: { width: 980, height: 700 } } },
    { name: "large",   use: { ...devices["Desktop Chrome"], viewport: { width: 1920, height: 1080 } } },
  ],
  webServer: {
    command: `npx vite preview --mode preview --outDir dist-preview --port ${PORT}`,
    url: `http://localhost:${PORT}/preview.html`,
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
    stdout: "pipe",
    stderr: "pipe",
  },
});
```

- [ ] **Step 2: Add Playwright artifacts to `.gitignore`:**

Append to `internal/panel/frontend/.gitignore`:

```
playwright-report/
test-results/
```

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/playwright.config.ts internal/panel/frontend/.gitignore
git commit -m "ci: playwright config for three viewport projects"
```

### Task 6.3: `frame.spec.ts` — no faux frame

**Files:**
- Create: `internal/panel/frontend/playwright/frame.spec.ts`

- [ ] **Step 1: Create:**

```ts
import { test, expect } from "@playwright/test";

test("no faux window border or border-radius on .shp-window", async ({ page }) => {
  await page.goto("/preview.html");
  await page.waitForSelector(".shp-window");
  const computed = await page.evaluate(() => {
    const w = document.querySelector(".shp-window")!;
    const s = getComputedStyle(w as Element);
    return { borderTop: s.borderTopWidth, borderLeft: s.borderLeftWidth, borderRadius: s.borderTopLeftRadius };
  });
  expect(computed.borderTop).toBe("0px");
  expect(computed.borderLeft).toBe("0px");
  expect(computed.borderRadius).toBe("0px");
});
```

- [ ] **Step 2: Run it:**

```bash
cd internal/panel/frontend && npx playwright test playwright/frame.spec.ts
```

Expected: PASS (assuming Phase 1 has been merged; if running ahead of Phase 1 merge, this fails — which is the point).

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/playwright/frame.spec.ts
git commit -m "ci: playwright assert no faux .shp-window frame"
```

### Task 6.4: `overflow.spec.ts` — no spurious scrollbars

**Files:**
- Create: `internal/panel/frontend/playwright/overflow.spec.ts`

- [ ] **Step 1: Create:**

```ts
import { test, expect } from "@playwright/test";

const tabs = [
  { id: "status",  label: "Status" },
  { id: "config",  label: "Config" },
  { id: "devices", label: "Devices" },
  { id: "ports",   label: "Ports" },
  { id: "logs",    label: "Logs" },
];

for (const tab of tabs) {
  test(`no horizontal overflow on ${tab.label}`, async ({ page }) => {
    await page.goto("/preview.html");
    await page.getByRole("button", { name: tab.label }).click();
    await page.waitForTimeout(150);
    const overflow = await page.evaluate(() => {
      const html = document.documentElement;
      return { scrollW: html.scrollWidth, clientW: html.clientWidth };
    });
    expect(overflow.scrollW).toBeLessThanOrEqual(overflow.clientW + 1);
  });
}
```

- [ ] **Step 2: Run it:**

```bash
cd internal/panel/frontend && npx playwright test playwright/overflow.spec.ts
```

Expected: PASS (after Phase 1+2+3 merged).

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/playwright/overflow.spec.ts
git commit -m "ci: playwright assert no horizontal overflow on every tab"
```

### Task 6.5: `help.spec.ts` — popover state machine

**Files:**
- Create: `internal/panel/frontend/playwright/help.spec.ts`

- [ ] **Step 1: Create:**

```ts
import { test, expect } from "@playwright/test";

test("hover opens popover, mouseout closes after grace", async ({ page }) => {
  await page.goto("/preview.html");
  await page.getByRole("button", { name: "Config" }).click();
  const help = page.locator(".shp-help").first();
  await help.hover();
  await expect(page.getByRole("tooltip")).toBeVisible({ timeout: 500 });
  // Move pointer away.
  await page.mouse.move(0, 0);
  await expect(page.getByRole("tooltip")).toBeHidden({ timeout: 500 });
});

test("click makes popover sticky; Esc closes it", async ({ page }) => {
  await page.goto("/preview.html");
  await page.getByRole("button", { name: "Config" }).click();
  const help = page.locator(".shp-help").first();
  await help.click();
  await expect(page.getByRole("tooltip")).toBeVisible();
  await page.mouse.move(0, 0);
  await page.waitForTimeout(300);
  await expect(page.getByRole("tooltip")).toBeVisible();        // still sticky
  await page.keyboard.press("Escape");
  await expect(page.getByRole("tooltip")).toBeHidden({ timeout: 300 });
});

test("keyboard focus opens; Esc closes", async ({ page }) => {
  await page.goto("/preview.html");
  await page.getByRole("button", { name: "Config" }).click();
  await page.locator(".shp-help").first().focus();
  await expect(page.getByRole("tooltip")).toBeVisible({ timeout: 500 });
  await page.keyboard.press("Escape");
  await expect(page.getByRole("tooltip")).toBeHidden({ timeout: 300 });
});
```

- [ ] **Step 2: Run it:**

```bash
cd internal/panel/frontend && npx playwright test playwright/help.spec.ts
```

Expected: PASS (after Phase 4 merged).

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/playwright/help.spec.ts
git commit -m "ci: playwright assert help popover hover/sticky/keyboard"
```

### Task 6.6: `popover-clip.spec.ts` — popover stays in viewport

**Files:**
- Create: `internal/panel/frontend/playwright/popover-clip.spec.ts`

- [ ] **Step 1: Create:**

```ts
import { test, expect } from "@playwright/test";

test("every help popover stays inside the viewport", async ({ page }) => {
  await page.goto("/preview.html");
  // Iterate tabs that have visible ? icons and check each.
  for (const label of ["Config", "Ports"]) {
    await page.getByRole("button", { name: label }).click();
    await page.waitForTimeout(100);
    const helps = page.locator(".shp-help");
    const count = await helps.count();
    for (let i = 0; i < count; i++) {
      // Scroll the icon into view so hover lands on something real.
      await helps.nth(i).scrollIntoViewIfNeeded();
      await helps.nth(i).hover();
      const tooltip = page.getByRole("tooltip");
      await expect(tooltip).toBeVisible({ timeout: 500 });
      const inside = await tooltip.evaluate((el) => {
        const r = (el as HTMLElement).getBoundingClientRect();
        const vw = window.innerWidth;
        const vh = window.innerHeight;
        return r.left >= 0 && r.top >= 0 && r.right <= vw && r.bottom <= vh;
      });
      expect(inside, `popover #${i} on ${label} clipped`).toBe(true);
      // Close before moving on.
      await page.mouse.move(0, 0);
      await expect(tooltip).toBeHidden({ timeout: 500 });
    }
  }
});
```

- [ ] **Step 2: Run it.** Expected: PASS at all three viewport sizes.

```bash
cd internal/panel/frontend && npx playwright test playwright/popover-clip.spec.ts
```

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/playwright/popover-clip.spec.ts
git commit -m "ci: playwright assert popovers stay inside viewport"
```

### Task 6.7: `tabs.spec.ts` — tab navigation has no console errors

**Files:**
- Create: `internal/panel/frontend/playwright/tabs.spec.ts`

- [ ] **Step 1: Create:**

```ts
import { test, expect, ConsoleMessage } from "@playwright/test";

const tabs = [
  { id: "status",  label: "Status",  identifier: ".shp-lamps" },
  { id: "config",  label: "Config",  identifier: ".shp-form-section" },
  { id: "devices", label: "Devices", identifier: ".shp-table-wrap, .shp-empty" },
  { id: "ports",   label: "Ports",   identifier: ".shp-table-wrap, .shp-empty" },
  { id: "logs",    label: "Logs",    identifier: ".shp-logs-controls" },
];

test("tab navigation has no console errors and renders identifying element", async ({ page }) => {
  const errors: string[] = [];
  page.on("console", (msg: ConsoleMessage) => {
    if (msg.type() === "error") errors.push(msg.text());
  });
  await page.goto("/preview.html");
  for (const t of tabs) {
    await page.getByRole("button", { name: t.label }).click();
    await expect(page.locator(t.identifier).first()).toBeVisible({ timeout: 2000 });
  }
  expect(errors, errors.join("\n")).toEqual([]);
});
```

- [ ] **Step 2: Run it.** Expected: PASS.

```bash
cd internal/panel/frontend && npx playwright test playwright/tabs.spec.ts
```

- [ ] **Step 3: Commit.**

```bash
git add internal/panel/frontend/playwright/tabs.spec.ts
git commit -m "ci: playwright assert tab navigation has no console errors"
```

### Task 6.8: Add `ui-checks` job to `pr.yml`

**Files:**
- Modify: `.github/workflows/pr.yml`

- [ ] **Step 1: At the bottom of the file (after the existing `verify` job), append:**

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
        run: npm run playwright
        working-directory: internal/panel/frontend

      - name: upload playwright report on failure
        if: failure() && steps.changes.outputs.ui == 'true'
        uses: actions/upload-artifact@v7
        with:
          name: playwright-report
          path: internal/panel/frontend/playwright-report
          retention-days: 7
```

- [ ] **Step 2: Commit.**

```bash
git add .github/workflows/pr.yml
git commit -m "ci: add ui-checks job running playwright on UI-relevant PRs"
```

### Task 6.9: Final pre-flight + PR

- [ ] **Step 1: Local Playwright run (full suite, all three viewports):**

```bash
cd internal/panel/frontend && npm run preview:build && npx playwright test
```

Expected: 5 test files, all PASS across `min`, `default`, `large` projects. Total ~30-45 individual assertions.

- [ ] **Step 2: Run pre-flight:**

```bash
cd /Users/khamitovdr/lab_devices_client
gofmt -l . && go vet ./... && go test -race -count=1 ./...
cd internal/panel/frontend && npm run build && npm test
```

- [ ] **Step 3: Push and open PR:**

```bash
git push -u origin ci/panel-ui-checks
gh pr create --title "ci: playwright UI invariant checks on PR" --body "$(cat <<'EOF'
## Summary
Adds a \`ui-checks\` job to \`pr.yml\` (parallel with \`verify\`) running Playwright headless Chromium against the preview build (from PR 4) at three viewport sizes: 720×480, 980×700, 1920×1080.

Behavioral assertions only — no screenshot baselines (see spec §7.3 for rationale).

Invariants asserted:
- \`.shp-window\` has no border or border-radius (no faux frame).
- No horizontal overflow on any tab at any viewport.
- Help popover opens on hover, closes on mouseout, sticky on click, dismisses on Esc, opens on keyboard focus.
- Every help popover, when open, fits fully inside the viewport.
- Tab navigation produces no console errors.

Gated by \`dorny/paths-filter\` watching \`internal/panel/frontend/**\` and the workflow file itself — non-UI PRs exit in ~2 s. Full path is ~90 s.

Depends on: PRs 1, 2, 3, 4, 5 merging first (the assertions match the post-fix state).

Spec: \`docs/superpowers/specs/2026-05-14-panel-ui-fixes-design.md\` §7.

## Test plan
- [ ] Full Playwright suite passes locally on macOS.
- [ ] Job runs in CI on this PR; report upload works on intentional failure (validate by inverting one assertion temporarily — revert before merge).
- [ ] Skip path: open a docs-only follow-up PR after merging; \`ui-checks\` exits in ~2 s.
EOF
)"
```

---

## Self-review

Running the checklist from the writing-plans skill against this plan:

**1. Spec coverage:**
- Spec §3 (faux frame removal, fluid sizing) → Phase 1 ✅
- Spec §3 (themed scrollbars) → Task 1.4 ✅
- Spec §3 (Wails MinWidth/MinHeight bump) → Task 1.5 ✅
- Spec §4 (responsive layout, .shp-field collapse, form max-width, fluid inputs, modal width, table-wrap scroll) → Phase 3 ✅
- Spec §5 (Help: hover, portal, viewport clamp, sticky, keyboard) → Phase 4 ✅
- Spec §6 (preview shim, scenarios, scripts, Taskfile) → Phase 5 ✅
- Spec §7 (Playwright config, 5 spec files, ui-checks job, paths-filter gating) → Phase 6 ✅
- Plan addition: tab class reconciliation (Phase 2) — found during planning; called out in user-visible chat before plan written.

**2. Placeholder scan:** No "TBD", "TODO", "implement later", "appropriate error handling". Every code step shows the full code.

**3. Type consistency:**
- `ScenarioId` defined in `seed.ts`, used in `Scenarios.tsx` ✅
- `ConfigShape` defined in `seed.ts` — Wails-side uses `any` in ConfigTab.tsx, so the shim shape is compatible ✅
- `emit` exported from `events.ts`, imported in `bindings.ts` ✅
- Playwright test files reference selectors (`.shp-window`, `.shp-help`, `.shp-form-section`, `.shp-lamps`, `.shp-table-wrap`, `.shp-empty`, `.shp-logs-controls`) all of which are defined in `global.css` ✅
- `OpenState` / `Coords` types in Help.tsx are local to the file ✅
- `runtime` exported from `events.ts`, used in `preview-entry.tsx` ✅

**4. Cross-phase rebase note:** Phases 4 and onward should be rebased on Phase 1+2 to see their fixes. PRs can land in any order; the order in the plan is the natural one.

No issues found. Plan is ready.
