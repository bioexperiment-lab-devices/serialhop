# Installer UI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the installer's `lxn/walk` dialog with a small Wails v2 window matching the panel's visual style. Compile with `-H windowsgui` to drop the console window. Auto-close 1500ms after the panel launches. Remove `github.com/lxn/walk` and `github.com/lxn/win` from `go.mod`.

**Architecture:** Cross-platform install logic in `install.go` (untouched). Windows-only UI shell rewritten in `ui_windows.go` as a `wails.Run` call with bound methods exposed to a single hand-written `tools/installer/frontend/dist/index.html`. Auto-close triggered by `wailsruntime.Quit(ctx)` from the Go side after a 1500ms timer. No npm, no React, no Vite — the frontend is one HTML file checked into the repo.

**Tech Stack:** Go 1.21+, `github.com/wailsapp/wails/v2` (already in go.mod as direct dep), WebView2 (Windows runtime — already required by the panel), `//go:embed`, vanilla HTML/CSS/JS.

**Reference spec:** `docs/superpowers/specs/2026-05-16-installer-ui-redesign-design.md`.

---

## File structure

| Path | Status | Responsibility |
|---|---|---|
| `tools/buildcmd/main.go` | modify | Add `-extra-ldflags` flag that appends to the internal version-injection ldflags string. |
| `Taskfile.yaml` | modify | The `installer` task passes `-extra-ldflags="-H windowsgui"` so the installer is compiled as a GUI subsystem (no console window). |
| `tools/installer/frontend/dist/index.html` | new | The entire UI: HTML structure + inline `<style>` + inline `<script>`. Reuses panel design tokens copied verbatim from `internal/panel/frontend/src/styles/global.css`. |
| `tools/installer/ui_windows.go` | rewrite | Wails `wails.Run` call + `InstallerApp` struct with bound methods (`BrowseFolder`, `Install`, `Cancel`, `InitialPath`). Embeds `frontend/dist`. Auto-quits 1500ms after a successful install. |
| `tools/installer/ui_other.go` | unchanged | Cross-platform stub `runDialog(_ *options) int` that panics if invoked. |
| `tools/installer/main.go` | modify | Silent path reattaches the parent console via `AttachConsole(ATTACH_PARENT_PROCESS)` so stdout/stderr writes are visible when launched from cmd.exe / PowerShell. Dialog path delegates to `runDialog` as before. |
| `go.mod` / `go.sum` | modify | `go mod tidy` after removing walk imports — drops `github.com/lxn/walk` and `github.com/lxn/win`. |

No changes to: `install.go`, `install_test.go`, `peversion_*.go`, `shortcut_*.go`, `elevation_*.go`, `payload_*.go`, `internal/winsvc/`, `internal/updater/`, `internal/panel/`, CI workflows, README, version metadata.

---

## Task 1: `buildcmd` learns `-extra-ldflags`

**Files:**
- Modify: `tools/buildcmd/main.go`

`buildcmd` currently builds its `-ldflags` string internally with just the version `-X` injection. The installer needs to append `-H windowsgui` (linker subsystem flag). Add an `-extra-ldflags` flag that space-concatenates with the existing string.

- [ ] **Step 1: Read the current buildcmd to understand the ldflags assembly**

Run: `cat tools/buildcmd/main.go | sed -n '30,90p'`
Expected: see the `flag.String` declarations and the `ldflags := fmt.Sprintf(...)` assignment.

- [ ] **Step 2: Add the new flag and append logic**

Edit `tools/buildcmd/main.go`. Find the existing flag-declaration block (around lines 37-42):

```go
out := flag.String("o", "dist/SerialHop.exe", "output binary path")
goos := flag.String("goos", os.Getenv("GOOS"), "GOOS for the build")
goarch := flag.String("goarch", os.Getenv("GOARCH"), "GOARCH for the build")
skipFrontend := flag.Bool("s", false, "skip frontend build (frontend already built)")
tags := flag.String("tags", "", "comma-separated build tags forwarded to go build / wails build")
flag.Parse()
```

Add an `extraLdflags` flag right after `tags`:

```go
out := flag.String("o", "dist/SerialHop.exe", "output binary path")
goos := flag.String("goos", os.Getenv("GOOS"), "GOOS for the build")
goarch := flag.String("goarch", os.Getenv("GOARCH"), "GOARCH for the build")
skipFrontend := flag.Bool("s", false, "skip frontend build (frontend already built)")
tags := flag.String("tags", "", "comma-separated build tags forwarded to go build / wails build")
extraLdflags := flag.String("extra-ldflags", "", "additional ldflags appended to the internal version-injection string (e.g. '-H windowsgui')")
flag.Parse()
```

Then find the `ldflags` assignment (around line 75):

```go
ldflags := fmt.Sprintf("-X %s.Version=%s", versionPkg, version)
```

Replace with:

```go
ldflags := fmt.Sprintf("-X %s.Version=%s", versionPkg, version)
if *extraLdflags != "" {
    ldflags = ldflags + " " + *extraLdflags
}
```

- [ ] **Step 3: Verify cross-platform compile**

Run: `go build ./tools/buildcmd/`
Expected: builds successfully on the host (macOS/Linux).

- [ ] **Step 4: Smoke-test the new flag is wired through**

Run: `go run ./tools/buildcmd --extra-ldflags="-H windowsgui" -o /tmp/buildcmd-test.exe -goos windows -goarch amd64 -tags production ./tools/installer 2>&1 | head -3`

Note: this will FAIL because `tools/installer/payload/SerialHop.exe` doesn't exist (we haven't built it). That's expected — we're just verifying the flag is parsed without an "unknown flag" error.

Look for "unknown flag" in the output:

Run: `go run ./tools/buildcmd --extra-ldflags="-H windowsgui" -o /tmp/x -goos linux -goarch amd64 ./tools/buildcmd 2>&1 | head -5`

Expected: no "unknown flag" message; the build either succeeds or fails on a different error (e.g., output dir issues), but the flag is accepted.

- [ ] **Step 5: Commit**

Verify branch is `worktree-installer-ui-redesign`:

```bash
git branch --show-current
```

Then:

```bash
git add tools/buildcmd/main.go
git commit -m "build(buildcmd): add -extra-ldflags flag for installer subsystem flag"
```

---

## Task 2: Taskfile installer target passes `-H windowsgui`

**Files:**
- Modify: `Taskfile.yaml`

- [ ] **Step 1: Open the Taskfile and locate the `installer` task**

Run: `grep -n "^  installer:" Taskfile.yaml`
Expected: prints the line number of the installer task definition.

- [ ] **Step 2: Edit the `installer` task**

In `Taskfile.yaml`, find the existing `installer` task:

```yaml
  installer:
    desc: Build the installer (depends on a fresh `task build` to produce the embedded payload).
    deps: [build, installer-resource]
    cmds:
      - cp dist/SerialHop.exe tools/installer/payload/SerialHop.exe
      - go run ./tools/buildcmd -o dist/SerialHop-Setup.exe -goos windows -goarch amd64 -tags production ./tools/installer
```

Change the second `cmd` to add `-extra-ldflags="-H windowsgui"`:

```yaml
  installer:
    desc: Build the installer (depends on a fresh `task build` to produce the embedded payload).
    deps: [build, installer-resource]
    cmds:
      - cp dist/SerialHop.exe tools/installer/payload/SerialHop.exe
      - go run ./tools/buildcmd -o dist/SerialHop-Setup.exe -goos windows -goarch amd64 -tags production -extra-ldflags="-H windowsgui" ./tools/installer
```

- [ ] **Step 3: Commit**

```bash
git add Taskfile.yaml
git commit -m "build(installer): build with -H windowsgui to drop console window"
```

---

## Task 3: Frontend `index.html` — the entire UI

**Files:**
- Create: `tools/installer/frontend/dist/index.html`

One vanilla HTML file with inline `<style>` and `<script>`. Reuses design tokens copied from `internal/panel/frontend/src/styles/global.css`. The Wails Go side (Task 4) will embed `tools/installer/frontend/dist` via `//go:embed all:frontend/dist`.

- [ ] **Step 1: Create the directory and file**

```bash
mkdir -p tools/installer/frontend/dist
```

Create `tools/installer/frontend/dist/index.html` with this exact content:

```html
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>SerialHop Installer</title>
<style>
  /* Tokens copied from internal/panel/frontend/src/styles/global.css */
  :root {
    --bg-page: #ECE9E0;
    --surface: #FFFFFF;
    --surface-sunken: #F8F6F0;
    --border: #E2DED2;
    --border-strong: #C8C3B5;
    --border-input: #C3BFB2;
    --text: #1A1916;
    --text-secondary: #514E47;
    --text-muted: #8A8678;
    --text-inverse: #FAF8F3;
    --accent: #1F3A8A;
    --accent-hover: #182E6F;
    --success: #2F7D3F;
    --danger: #B23A2A;
    --shadow-card: 0 1px 0 rgba(26,25,22,0.04), 0 1px 2px rgba(26,25,22,0.06);
  }
  * { box-sizing: border-box; }
  html, body {
    margin: 0;
    padding: 0;
    height: 100%;
    font-family: 'IBM Plex Sans', system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif;
    font-size: 14px;
    color: var(--text);
    background: var(--bg-page);
    -webkit-font-smoothing: antialiased;
    user-select: none;
  }
  .container {
    height: 100%;
    display: flex;
    flex-direction: column;
    padding: 20px;
    gap: 16px;
  }
  .field-label {
    font-size: 12px;
    color: var(--text-secondary);
    font-weight: 500;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .path-row {
    display: flex;
    gap: 8px;
  }
  .path-input {
    flex: 1;
    padding: 8px 10px;
    font: inherit;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border-input);
    border-radius: 6px;
    outline: none;
    user-select: text;
  }
  .path-input:focus {
    border-color: var(--accent);
    box-shadow: 0 0 0 3px rgba(31, 58, 138, 0.15);
  }
  .btn {
    padding: 8px 14px;
    font: inherit;
    border-radius: 6px;
    border: 1px solid var(--border-strong);
    background: var(--surface);
    color: var(--text);
    cursor: pointer;
    transition: background 80ms ease, border-color 80ms ease;
  }
  .btn:hover:not(:disabled) {
    background: var(--surface-sunken);
  }
  .btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .btn-primary {
    background: var(--accent);
    color: var(--text-inverse);
    border-color: var(--accent);
  }
  .btn-primary:hover:not(:disabled) {
    background: var(--accent-hover);
    border-color: var(--accent-hover);
  }
  .status {
    flex: 1;
    font-size: 13px;
    color: var(--text-muted);
    padding: 8px 0;
    min-height: 1.4em;
  }
  .status.installing {
    color: var(--text);
  }
  .status.installing::after {
    content: '';
    display: inline-block;
    width: 10px;
    height: 10px;
    margin-left: 8px;
    border: 2px solid var(--border-strong);
    border-top-color: var(--accent);
    border-radius: 50%;
    vertical-align: -2px;
    animation: spin 0.7s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
  .status.success { color: var(--success); }
  .status.error { color: var(--danger); }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
</head>
<body>
<div class="container">
  <div>
    <div class="field-label">Install location</div>
    <div class="path-row" style="margin-top: 6px;">
      <input id="path-input" type="text" class="path-input" spellcheck="false" autocomplete="off">
      <button id="browse-btn" class="btn">Browse…</button>
    </div>
  </div>

  <div id="status" class="status">Ready.</div>

  <div class="actions">
    <button id="cancel-btn" class="btn">Cancel</button>
    <button id="install-btn" class="btn btn-primary">Install</button>
  </div>
</div>

<script>
  const pathInput = document.getElementById('path-input');
  const browseBtn = document.getElementById('browse-btn');
  const installBtn = document.getElementById('install-btn');
  const cancelBtn = document.getElementById('cancel-btn');
  const statusEl = document.getElementById('status');

  function setStatus(text, kind) {
    statusEl.textContent = text;
    statusEl.className = 'status' + (kind ? ' ' + kind : '');
  }

  function setBusy(busy) {
    installBtn.disabled = busy;
    browseBtn.disabled = busy;
    pathInput.disabled = busy;
  }

  // Populate the initial path from the Go side (it knows the default and any --dir flag).
  window.go.main.InstallerApp.InitialPath().then(p => {
    pathInput.value = p;
  });

  browseBtn.addEventListener('click', async () => {
    const chosen = await window.go.main.InstallerApp.BrowseFolder(pathInput.value);
    if (chosen) {
      pathInput.value = chosen;
    }
  });

  installBtn.addEventListener('click', async () => {
    setBusy(true);
    setStatus('Installing…', 'installing');
    try {
      const res = await window.go.main.InstallerApp.Install(pathInput.value);
      if (res.Err) {
        setStatus(res.Err, 'error');
        setBusy(false);
        return;
      }
      // Pick a final message based on the state. The Go side sets Message
      // to the success line; we render it as-is.
      const msg = res.Message || 'Installed.';
      setStatus(msg, 'success');
      // Auto-close is handled by the Go side after a 1500ms delay; leave
      // the UI in its success state until then.
    } catch (e) {
      setStatus(String(e), 'error');
      setBusy(false);
    }
  });

  cancelBtn.addEventListener('click', () => {
    window.go.main.InstallerApp.Cancel();
  });

  // Optional: Go can push intermediate status updates via runtime events.
  // We subscribe so future enhancements (download progress, etc.) work
  // without JS changes.
  if (window.runtime && window.runtime.EventsOn) {
    window.runtime.EventsOn('installer:status', (text, kind) => {
      setStatus(text, kind || '');
    });
  }
</script>
</body>
</html>
```

- [ ] **Step 2: Verify the file is well-formed**

Run: `wc -l tools/installer/frontend/dist/index.html && head -5 tools/installer/frontend/dist/index.html`
Expected: ~200 lines, starts with the DOCTYPE.

- [ ] **Step 3: Commit**

```bash
git add tools/installer/frontend/dist/index.html
git commit -m "feat(installer): add Wails frontend (one HTML file, panel design tokens)"
```

---

## Task 4: Rewrite `ui_windows.go` as Wails app

**Files:**
- Rewrite: `tools/installer/ui_windows.go`

Replaces the walk-based `runDialog` with a Wails `wails.Run` call. Defines `InstallerApp` with four bound methods: `OnStartup`, `BrowseFolder`, `Install`, `Cancel`, plus the helper `InitialPath`.

- [ ] **Step 1: Read the current ui_windows.go to know what's being replaced**

Run: `cat tools/installer/ui_windows.go`
Expected: see the existing walk-based dialog code (~80 lines).

- [ ] **Step 2: Replace the entire file**

Overwrite `tools/installer/ui_windows.go` with:

```go
//go:build windows

package main

import (
	"context"
	"embed"
	"fmt"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var frontendAssets embed.FS

// InstallerApp is the Wails-bound singleton exposed to JS. JS calls into
// these methods via the auto-generated bindings at
// window.go.main.InstallerApp.*. Method names must start with an uppercase
// letter to be bindable.
type InstallerApp struct {
	ctx  context.Context
	opts *options // CLI-parsed defaults (install dir, allow-downgrade, etc.)
}

// OnStartup is wired into options.App.OnStartup; Wails calls it once after
// the WebView2 control is initialized. We capture the context so later
// methods can emit events and call runtime.Quit.
func (a *InstallerApp) OnStartup(ctx context.Context) {
	a.ctx = ctx
}

// InitialPath returns the install directory the dialog should pre-fill —
// the CLI --dir flag's value (or the default `C:\Program Files\SerialHop`).
func (a *InstallerApp) InitialPath() string {
	return a.opts.InstallDir
}

// BrowseFolder opens the system folder picker rooted at `current` (or the
// default if empty) and returns the chosen path. Returns "" if the
// operator cancelled the picker.
func (a *InstallerApp) BrowseFolder(current string) string {
	dlg, err := wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title:                "Choose install directory",
		DefaultDirectory:     current,
		CanCreateDirectories: true,
	})
	if err != nil {
		return ""
	}
	return dlg
}

// Install runs the install flow with the operator's chosen install
// directory, then (on success) schedules a 1500ms auto-close so the
// installer window vanishes after the panel is launched. Returns the
// Result so JS can render the final status before the close timer fires.
func (a *InstallerApp) Install(installDir string) Result {
	runOpts := *a.opts
	runOpts.InstallDir = installDir
	r := newProductionRunner()
	res := r.Run(runOpts)

	// Auto-close on success only. Failure paths leave the window open
	// so the operator can see the error and retry (or close manually).
	// Same-version "already installed" is treated as success — the
	// installer launches the existing panel and exits.
	if res.Err == nil && !runOpts.NoLaunch && !runOpts.Silent {
		go func(ctx context.Context) {
			time.Sleep(1500 * time.Millisecond)
			wailsruntime.Quit(ctx)
		}(a.ctx)
	}
	return res
}

// Cancel closes the installer window. Same effect as the system close
// button. If an install is in flight, Cancel still fires Quit — Wails
// shuts down the WebView2 control and the Go process exits. We do not
// abort the underlying install (rare for the operator to want a
// mid-rename abort, and the rollback machinery handles a SIGTERM-during-
// rename poorly).
func (a *InstallerApp) Cancel() {
	wailsruntime.Quit(a.ctx)
}

// runDialog opens the Wails-based installer window. Returns the process
// exit code (0 on user-driven close, 1 on Wails startup error).
func runDialog(opts *options) int {
	app := &InstallerApp{opts: opts}

	err := wails.Run(&options.App{
		Title:            fmt.Sprintf("SerialHop Installer"),
		Width:            480,
		Height:           300,
		MinWidth:         480,
		MinHeight:        300,
		MaxWidth:         480,
		MaxHeight:        300,
		DisableResize:    true,
		Fullscreen:       false,
		Frameless:        false, // framed window with native title bar (per spec §3)
		StartHidden:      false,
		BackgroundColour: &options.RGBA{R: 0xEC, G: 0xE9, B: 0xE0, A: 0xFF}, // --bg-page
		AssetServer: &assetserver.Options{
			Assets: frontendAssets,
		},
		Bind:      []any{app},
		OnStartup: app.OnStartup,
	})
	if err != nil {
		// We're on the windowsgui subsystem; stderr goes to nowhere unless
		// reattached. The error is also captured in the structured log file
		// at %TEMP%\SerialHop-installer-<version>.log via slog (from main.go).
		return 1
	}
	return 0
}
```

- [ ] **Step 3: Build for Windows to verify it compiles**

Need the payload stub for the embed:

```bash
printf 'stub' > tools/installer/payload/SerialHop.exe
```

Then:

```bash
GOOS=windows GOARCH=amd64 go vet ./tools/installer/
GOOS=windows GOARCH=amd64 go build -tags production -o /tmp/installer-test.exe ./tools/installer/
ls -lh /tmp/installer-test.exe
rm -f /tmp/installer-test.exe tools/installer/payload/SerialHop.exe
```

Expected: vet is clean, build succeeds, binary is ~20MB (Wails runtime adds size).

- [ ] **Step 4: Build for host OS (no embed needed since dev tag means empty payload)**

```bash
go vet ./tools/installer/
go build -o /tmp/installer-dev.exe ./tools/installer/
ls -lh /tmp/installer-dev.exe
rm -f /tmp/installer-dev.exe
```

Expected: vet clean, build succeeds.

- [ ] **Step 5: Run install_test.go to confirm dispatch tests still pass**

```bash
go test -race -count=1 ./tools/installer/
```

Expected: 10 tests pass (unchanged from before the rewrite — these tests target `Runner.Run`, not the UI shell).

- [ ] **Step 6: Commit**

```bash
git add tools/installer/ui_windows.go
git commit -m "feat(installer): rewrite UI shell as Wails window with auto-close"
```

---

## Task 5: Silent-mode console reattach

**Files:**
- Modify: `tools/installer/main.go`

When the installer is compiled with `-H windowsgui`, the binary doesn't get a console allocated by Windows. For `--silent` invocations from cmd.exe / PowerShell, the operator expects to see stdout/stderr in their terminal. Reattach the parent console via `AttachConsole(ATTACH_PARENT_PROCESS)`. If no parent console exists (e.g. double-clicked from Explorer with `--silent`), the call is a no-op and output goes nowhere — same as today.

Pattern is copied from the main binary's `attachParentConsole()` helper in `main.go`.

- [ ] **Step 1: Read the current main.go to locate the silent-path entry**

Run: `grep -n "flagSilent\|runSilent" tools/installer/main.go`
Expected: see the line where `*flagSilent` branches into `runSilent`.

- [ ] **Step 2: Add the console-attach helper**

Create `tools/installer/console_windows.go`:

```go
//go:build windows

package main

import "golang.org/x/sys/windows"

// attachParentConsole hooks the process to its parent's console so that
// stdout/stderr writes show up in the cmd.exe / PowerShell window that
// launched the installer. The binary is compiled with -H windowsgui so
// no console is allocated by default. If there is no parent console
// (e.g. double-clicked from Explorer with --silent), the call fails
// silently and output goes nowhere — same as before.
func attachParentConsole() {
	modKernel32 := windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole := modKernel32.NewProc("AttachConsole")
	// ATTACH_PARENT_PROCESS = (DWORD)-1
	procAttachConsole.Call(uintptr(^uint32(0)))
}
```

Create `tools/installer/console_other.go`:

```go
//go:build !windows

package main

// attachParentConsole is a no-op on non-Windows (the installer's silent
// path is exercised cross-platform during tests).
func attachParentConsole() {}
```

- [ ] **Step 3: Call attachParentConsole from the silent path**

Edit `tools/installer/main.go`. Find the existing dispatch logic in `main()`:

```go
if *flagSilent {
    os.Exit(runSilent(opts))
    return
}
os.Exit(runDialog(opts))
```

Change to:

```go
if *flagSilent {
    attachParentConsole()
    os.Exit(runSilent(opts))
    return
}
os.Exit(runDialog(opts))
```

- [ ] **Step 4: Verify cross-platform compile**

```bash
go build ./tools/installer/...
GOOS=windows GOARCH=amd64 go vet ./tools/installer/
```

(For the Windows build proper, the stub payload is needed; skip the actual `go build` for windows here — it's already covered by Task 4.)

Expected: host build succeeds, vet is clean on both targets.

- [ ] **Step 5: Commit**

```bash
git add tools/installer/console_windows.go tools/installer/console_other.go tools/installer/main.go
git commit -m "feat(installer): reattach parent console in --silent mode (windowsgui)"
```

---

## Task 6: Remove `lxn/walk` from go.mod

**Files:**
- Modify: `go.mod`, `go.sum`

`tools/installer/ui_windows.go` was the last importer. After Task 4 deleted those imports, `go mod tidy` should drop walk and win from go.mod.

- [ ] **Step 1: Verify no remaining walk imports**

Run: `grep -rn "lxn/walk\|lxn/win" --include="*.go" . 2>&1 | head`
Expected: no results.

If anything shows up, STOP — there's a stale import to clean up first.

- [ ] **Step 2: Run go mod tidy**

```bash
go mod tidy
```

- [ ] **Step 3: Verify go.mod no longer references walk or win**

Run: `grep -E "lxn/walk|lxn/win" go.mod go.sum 2>&1 | head`
Expected: no results.

If anything still appears in go.sum, that's a transitive that something else pulls in — leave it alone (we only care about removing walk/win as deps of this module).

- [ ] **Step 4: Verify nothing broke**

```bash
go build ./tools/installer/...
go test -race -count=1 ./...
```

Expected: build succeeds, all 425 tests pass.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "build: drop github.com/lxn/walk and lxn/win after Wails rewrite"
```

---

## Task 7: Final verification

**Files:** none (verification only)

- [ ] **Step 1: Full check suite**

```bash
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

Expected: gofmt prints nothing; vet is clean; all tests pass.

- [ ] **Step 2: Cross-platform compile**

```bash
printf 'stub' > tools/installer/payload/SerialHop.exe
GOOS=windows GOARCH=amd64 go build ./... 2>&1 | tail -5
GOOS=linux  GOARCH=amd64 go build ./... 2>&1 | tail -5
GOOS=darwin GOARCH=arm64 go build ./... 2>&1 | tail -5
rm -f tools/installer/payload/SerialHop.exe
```

Expected: Windows + Linux + Darwin all build. Errors specifically about `main.go` (the SerialHop service) being unbuildable on linux/darwin are pre-existing (build tags exclude it).

- [ ] **Step 3: Check final git state**

```bash
git log --oneline d218e40..HEAD
git status
```

Expected: 6-7 commits on top of the prior installer PR's merged state; clean working tree.

- [ ] **Step 4: Push and open PR**

```bash
git push -u origin worktree-installer-ui-redesign
```

Then create the PR:

```bash
gh pr create --title "feat(installer): replace walk dialog with Wails-styled window; auto-close on launch" --body "$(cat <<'EOF'
## Summary

- Replaces the installer's `lxn/walk` dialog with a small Wails v2 window matching the panel's visual style.
- Compiles the installer with `-H windowsgui` to drop the console window that appeared alongside the previous dialog.
- Auto-closes the installer 1500ms after a successful panel launch so the operator sees the success message briefly, then the handoff to SerialHop is clean.
- Removes `github.com/lxn/walk` and `github.com/lxn/win` from `go.mod`.

The frontend is one hand-written `tools/installer/frontend/dist/index.html` reusing panel design tokens — no npm, no React, no Vite. All install dispatch logic (`Runner.Run`, version check, payload extract, SCM rename swap) is untouched; only the UI shell changes.

Spec: `docs/superpowers/specs/2026-05-16-installer-ui-redesign-design.md`
Plan: `docs/superpowers/plans/2026-05-16-installer-ui-redesign.md`

## Test plan

- [ ] Manual: double-click installer on a Windows VM → window matches panel style; no console window.
- [ ] Manual: complete an install → success message visible briefly, installer auto-closes, panel is up.
- [ ] Manual: re-run same installer → "already installed" path, auto-close still fires after launch.
- [ ] Manual: install older over newer (no `--allow-downgrade`) → error visible, window stays open, no auto-close.
- [ ] Manual: `--silent --dir D:\SerialHop` from PowerShell → no window, install proceeds, stdout/stderr visible in PowerShell.
- [ ] CI: `go test -race -count=1 ./...` passes (425 tests).
- [ ] CI: Windows build succeeds.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review

**Spec coverage:**

| Spec section | Implementing task |
|---|---|
| §1 Purpose & scope | Plan-wide |
| §2 Architecture | Tasks 3, 4 |
| §3 Window properties | Task 4 (wails.Run options) |
| §4 Visual layout & tokens | Task 3 (CSS + HTML) |
| §5 Bindings | Task 4 (InstallerApp methods) |
| §6 Auto-close | Task 4 (1500ms timer in Install) |
| §7 Subsystem flags | Tasks 1, 2 (buildcmd + Taskfile); Task 5 (silent console reattach) |
| §8 Remove walk | Task 6 |
| §9 Frontend file layout | Task 3 |
| §10 Internal package layout | Tasks 3, 4, 5 |
| §11 Testing | Task 7 (install_test.go untouched; manual UI test in PR plan) |
| §12 Build pipeline changes | Tasks 1, 2 |
| §13 Compatibility & risk | Task 7 verification |

**Placeholder scan:** No `TBD`, `TODO`, `implement later`, or vague references. Every step contains the exact code or command.

**Type consistency:** `InstallerApp`, `Result`, `options`, `Runner`, `newProductionRunner` are consistent across Task 4. Frontend method names (`InitialPath`, `BrowseFolder`, `Install`, `Cancel`) match between the HTML in Task 3 and the Go bindings in Task 4. Build-tag conventions (`//go:build windows` for ui/peversion/shortcut/elevation/console; `//go:build !windows` for stubs) consistent throughout.

**Scope:** One PR, one feature (UI redesign). Doesn't touch dispatch logic, SCM, version comparison, CI workflows, README, or any release machinery.
