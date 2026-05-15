# Installer — Design

**Date:** 2026-05-15
**Status:** Approved (brainstorming complete; pending spec review before plan)

## 1. Purpose & scope

Ship a single-file Windows installer (`SerialHop-Setup-vX.Y.Z.exe`) that performs
a first-time install of SerialHop to `C:\Program Files\SerialHop\` (or an
operator-chosen path), drops an unversioned `SerialHop.lnk` on the all-users
desktop, and launches the panel so the operator lands on the existing
credentials dialog. Re-running the installer on an already-installed machine
performs an in-place upgrade (or a same-version no-op, or a refused downgrade)
using the same rename-with-rollback logic the panel's auto-update uses.

The unversioned desktop shortcut is the load-bearing property: subsequent
in-place updates (whether via the panel's existing auto-update or via a manual
re-run of a newer installer) rename the binary in place under the same
`SerialHop.exe` filename, so the shortcut keeps working indefinitely. All
update churn — `.old` files, staged versioned binaries, the rename swap —
stays inside the install directory.

In scope:

- A new `tools/installer/` Go package that builds to `dist/SerialHop-Setup.exe`.
  Bespoke Go installer (no Inno Setup / NSIS / WiX), consistent with the repo's
  "Go programs, not shell" rule and with the existing build pipeline.
- The installer embeds the just-built `SerialHop.exe` payload via `//go:embed`.
  Each installer is a **snapshot** of a specific release; there is no
  network bootstrap. An operator who installs from an old `SerialHop-Setup-v1.2.3.exe`
  gets v1.2.3, then the panel's auto-update offers the latest on first launch.
- One walk-based dialog (path field, Browse, Install, Cancel, status label) for
  interactive installs. Flags (`--dir`, `--silent`, `--no-launch`, `--no-shortcut`,
  `--allow-downgrade`) for unattended / scripted deploys.
- Install flow that handles all three states: fresh install, upgrade,
  same-version re-run; refuses downgrade unless explicitly opted in.
- The desktop shortcut at `C:\Users\Public\Desktop\SerialHop.lnk` targeting
  `<install_dir>\SerialHop.exe` (unversioned, by design).
- CI changes: `release-build` builds the installer alongside the bare exe;
  GitHub Release ships **both** assets; the VPS upload step now sends the
  installer instead of the bare exe.
- A new exported `winsvc.InstallOrUpgrade(...)` wrapper around the existing
  `updateBinary` logic, so the installer reuses the same SCM stop / rename /
  start / rollback sequence as the panel auto-update.

Out of scope (deliberately YAGNI):

- Per-user installs (`%LOCALAPPDATA%`). The service runs as LocalSystem and may
  fail to read its binary out of a user profile under default ACLs.
- Start Menu shortcuts.
- Add/Remove Programs (`HKLM\…\Uninstall\SerialHop`) registry entry. The
  panel's existing **Uninstall** button + manual install-dir deletion remains
  the documented removal path. Promote later if it becomes painful.
- Modifying the panel auto-update flow. It continues to download the bare
  `SerialHop-vX.Y.Z.exe` from the GitHub release and perform its existing
  rename swap. The installer is for first-time installs and manual /
  offline upgrades, not the in-app update path.
- Network bootstrap at install time. Installer is a snapshot, not a fetcher.
- Code signing the installer. Defender SmartScreen reputation will be zero
  per release on both the installer and the bare exe until an EV cert is
  procured. That's tracked separately in `SECURITY.md`.
- MSI / Group Policy deployment.
- Changing the install location of writable data. `%ProgramData%\SerialHop\`
  remains the home for config and logs (correct Windows split for code in
  Program Files + data in ProgramData; matches Defender's own layout).

## 2. Install location

`C:\Program Files\SerialHop\` is the default, the operator can pick another
path via the Browse dialog or `--dir`. The pairing with `%ProgramData%\SerialHop\`
for writable data is deliberate:

| Path | Owner | Contents | Why here |
|---|---|---|---|
| `<install_dir>\SerialHop.exe` | code | the service binary | system-protected after install; auto-update rename swap stays inside this dir |
| `<install_dir>\SerialHop-vX.Y.Z.exe` | code (transient) | staged versioned binary | present briefly during auto-update and during installer-driven upgrade; cleaned up on success |
| `<install_dir>\SerialHop.exe.old` | code (transient) | prior binary | present briefly during update; cleaned up best-effort |
| `%ProgramData%\SerialHop\SerialHop_config.yaml` | data | user config | LocalSystem-writable; not touched by install/upgrade |
| `%ProgramData%\SerialHop\logs\…` | data | rotated log files | LocalSystem-writable |
| `C:\Users\Public\Desktop\SerialHop.lnk` | shortcut | desktop icon | all-users desktop; visible to every operator; unversioned target |

The installer never writes to `%ProgramData%\SerialHop\`. That tree is created
on first run of the panel by `paths.EnsureDirs()` (existing behavior). The
service binary's path stored by SCM at install time is `<install_dir>\SerialHop.exe`;
the rename swap preserves that path.

## 3. Tooling & build

### 3.1 Directory layout

```
tools/installer/
  main.go               # entry point: parses flags, dispatches to dialog or silent
  install.go            # core install/upgrade flow (version check, payload extract,
                        # delegate to winsvc.InstallOrUpgrade, shortcut, launch)
  install_test.go       # cross-platform; fakes the fs and SCM, covers fresh install,
                        # upgrade, same-version no-op, downgrade refusal, rollback
  shortcut_windows.go   # COM IShellLinkW + IPersistFileW wrapper
  shortcut_other.go     # stub (build-tag) so the package compiles on macOS/Linux
  shortcut_windows_test.go # round-trip lnk creation against t.TempDir(); resolves
                           # back and asserts target/wd/icon
  ui_windows.go         # walk dialog: path field, Browse, Install, Cancel, status
  ui_other.go           # stub (build-tag)
  version.json          # FixedFileInfo + StringFileInfo for installer's PE
  manifest.template.xml # UAC manifest with requireAdministrator
  payload/
    .gitkeep            # only this is checked in; the .exe is staged at build time
```

`tools/installer/payload/SerialHop.exe` is gitignored (added to `.gitignore`
alongside the existing generated artifacts). It is populated by the build task
described in §3.3 before `go build` runs over `tools/installer`.

### 3.2 New tooling helpers

| Tool | Purpose |
|---|---|
| `tools/render-installer-manifest` | Render `tools/installer/manifest.template.xml` → `tools/installer/manifest.xml` from `tools/installer/version.json`. Mirrors the existing `tools/render-manifest` pattern. |

Reuses the existing `tools/buildcmd` for the installer's `go build` (so
`-ldflags -X` version baking and `git describe` versioning continue to work).

### 3.3 Taskfile changes

Two new tasks plus a `clean` extension:

```yaml
installer-manifest:
  desc: Generate tools/installer/manifest.xml from template + version.json.
  sources:
    - tools/installer/manifest.template.xml
    - tools/installer/version.json
    - tools/render-installer-manifest/main.go
  generates:
    - tools/installer/manifest.xml
  cmds:
    - go run ./tools/render-installer-manifest

installer-resource:
  desc: Compile the installer's .syso (icon + UAC manifest + version metadata).
  deps: [installer-manifest]
  cmds:
    - go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest
        -64
        -o tools/installer/resource_windows.syso
        -icon=assets/icon.ico
        -manifest=tools/installer/manifest.xml
        tools/installer/version.json

installer:
  desc: Build the installer (depends on a fresh `task build`).
  deps: [build, installer-resource]
  cmds:
    - cp dist/SerialHop.exe tools/installer/payload/SerialHop.exe
    - go run ./tools/buildcmd -o dist/SerialHop-Setup.exe -goos windows -goarch amd64 ./tools/installer
```

`task clean` adds:

```
rm -f tools/installer/manifest.xml tools/installer/resource_windows.syso tools/installer/payload/SerialHop.exe
```

The installer's `payload/SerialHop.exe`, `manifest.xml`, and
`resource_windows.syso` are all generated artifacts — added to `.gitignore`
under the existing "generated artifacts" section.

### 3.4 Installer's own version metadata

`tools/installer/version.json` mirrors `assets/version.json`:

- StringFileInfo `ProductName: "SerialHop Installer"`.
- StringFileInfo `OriginalFilename: "SerialHop-Setup.exe"`.
- StringFileInfo `FileVersion` / `ProductVersion`: same string as the main
  binary's version. **The release-please string-update step that bumps
  `assets/version.json` must also bump `tools/installer/version.json`**.
- FixedFileInfo `{File,Product}Version.{Major,Minor,Patch}`: same as the main
  binary. **The CI `sync version.json integer fields` step in `release-please.yml`
  is extended to apply the same jq transform to `tools/installer/version.json`**.

Release-please config:

- `release-please-config.json` gains `tools/installer/version.json` in the
  `extra-files` list with the same `json` updater pointing at
  `$.StringFileInfo.FileVersion` and `$.StringFileInfo.ProductVersion`.
- The same "string-only" caveat from `CLAUDE.md` applies — the FixedFileInfo
  integer fields are CI-derived, not release-please-managed.

## 4. Installer binary

### 4.1 Flags

```
SerialHop-Setup-vX.Y.Z.exe [--dir <path>] [--silent] [--no-launch]
                            [--no-shortcut] [--allow-downgrade] [--version]
```

- `--dir <path>` — override default install directory. Must be an absolute path.
- `--silent` — no dialog. Proceed with defaults (or `--dir` if given). All
  output goes to stderr; exits non-zero on any error. Implies `--no-launch`
  (scripted deploys do not want a GUI window appearing post-install).
- `--no-launch` — skip launching the panel at the end. Implies nothing else.
- `--no-shortcut` — skip the desktop shortcut creation step. Implies nothing else.
- `--allow-downgrade` — proceed even if the installed version is newer than
  the installer's bundled version. See §4.4.
- `--version` — print "SerialHop Installer vX.Y.Z (payload vX.Y.Z)" and exit 0.
  Both versions are equal by construction; printed separately so a future
  decoupling is straightforward.

Unknown flags exit with `flag.PrintDefaults()` and exit 2 (matching
`flag.ExitOnError` semantics). Positional arguments are an error.

### 4.2 UAC manifest

`tools/installer/manifest.template.xml`:

```xml
<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity
    type="win32"
    name="LabDevices.SerialHopInstaller"
    version="{{.VersionFull}}"
    processorArchitecture="amd64"/>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security>
      <requestedPrivileges>
        <requestedExecutionLevel level="requireAdministrator" uiAccess="false"/>
      </requestedPrivileges>
    </security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application>
      <supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/> <!-- Windows 10/11 -->
    </application>
  </compatibility>
</assembly>
```

`{{.VersionFull}}` is rendered as `Major.Minor.Patch.0` by
`tools/render-installer-manifest`, same shape as the main binary's manifest.

`requireAdministrator` causes Windows to show the UAC prompt at double-click
time. As defense in depth, the binary also calls `windows.IsElevated()` at
startup; if false (e.g., manifest stripped), it prints an explanatory error
and exits 1.

### 4.3 Embedded payload

```go
//go:embed payload/SerialHop.exe
var payload []byte
```

The build task copies `dist/SerialHop.exe` into `tools/installer/payload/`
before `go build` runs. `payload/SerialHop.exe` is gitignored and a
build-time artifact only. The embed will fail loudly at compile time if the
file is missing, so a `task installer` without a prior `task build` produces
a clear error rather than an installer with a stale or empty payload.

### 4.4 Version check & flow gating

On every run (silent or interactive), after the operator confirms the install
path:

```
1. installedPath = <install_dir>\SerialHop.exe
2. If installedPath does not exist:
     state = "fresh"
3. Else:
     installedVersion = readPEFileVersion(installedPath)
     bundledVersion   = version.Base()  // installer's own --ldflags-baked version
     cmp = compareSemVer(installedVersion, bundledVersion)
     - cmp == 0: state = "same"
     - cmp <  0: state = "upgrade"
     - cmp >  0: state = "downgrade"
4. Dispatch:
     fresh:     extract + InstallOrUpgrade + shortcut + launch
     upgrade:   extract + InstallOrUpgrade + shortcut(refresh) + launch
     same:      shortcut(refresh) + launch + report "Already installed."
     downgrade: if --allow-downgrade: extract + InstallOrUpgrade + shortcut + launch
                else:                  error "Installed version (vX.Y.Z) is newer
                                       than this installer (vX.Y.Z). Re-run with
                                       --allow-downgrade to proceed anyway." exit 1
```

`readPEFileVersion` is a thin wrapper around `GetFileVersionInfoExW` /
`VerQueryValueW` that pulls `StringFileInfo.FileVersion`. Implementation in
`tools/installer/peversion_windows.go` with an `_other.go` stub returning a
sentinel error for cross-platform compilation.

`compareSemVer` reuses `internal/updater/version.IsNewer` (and its helpers
that strip leading `v` and trailing `+buildmeta`) — promoted to expose a
three-way `Compare(a, b string) (int, error)` if not already in that shape.

### 4.5 Same-version no-op detail

Re-running with the same version still **refreshes the desktop shortcut**
(idempotent overwrite) and **launches the panel** (matches the operator's
expectation that "double-clicking the installer ends with SerialHop open").
It explicitly does **not** stop/restart the service, does not rename anything,
does not create `.old` files. Status reads `SerialHop vX.Y.Z is already
installed. Refreshed desktop shortcut.`

### 4.6 Downgrade refusal detail

Downgrade is refused because: (a) config schema may have evolved between
versions (an older binary may misparse a newer config), (b) the SCM service
state may include version-specific assumptions, and (c) on-disk caches /
formats in `%ProgramData%` may be one-way.

`--allow-downgrade` opts out of the safety check; it's the operator's problem
from there. No other safety mitigations are added (no automatic config
backup, no schema-compat check). YAGNI; an operator who passes
`--allow-downgrade` is opting in to figuring out the consequences.

## 5. Install flow (post-version-check, for fresh/upgrade/downgrade-with-flag)

```
1. MkdirAll(<install_dir>, 0755).
   - Inherits ACLs of the parent (Program Files = system-protected).
   - For an operator-chosen custom path, the dir is created with the same ACLs.
2. stagedPath = <install_dir>\SerialHop-vX.Y.Z.exe  (X.Y.Z = bundled version)
3. Write payload to stagedPath via:
     - os.OpenFile(stagedPath, O_WRONLY|O_CREATE|O_TRUNC, 0644)
     - io.Copy / Write the embedded bytes
     - f.Sync()
     - f.Close()
4. SHA-256 self-check:
     - hashed = sha256(stagedPath bytes-from-disk)
     - expected = sha256(payload []byte from embed)
     - mismatch: delete stagedPath, return error
5. scm, err := winsvc.DialSCM(); if err: return error
   defer scm.Disconnect()
6. err := winsvc.InstallOrUpgrade(scm, stagedPath, <install_dir>\SerialHop.exe)
   - InstallOrUpgrade is a new exported wrapper around updateBinary:
       winsvc.InstallOrUpgrade(scm, src, target) error {
         return updateBinary(scm, realFS{}, src, target,
           productionStartTimeout, productionPollInterval, 250*time.Millisecond)
       }
   - updateBinary already gracefully handles "service not installed"
     (control.go:224-226), so this same call works for both first-install
     (no service yet — just performs the two renames) and upgrade (stop /
     swap / start, with rollback on failure).
7. If !--no-shortcut: createOrRefreshShortcut(
        path: C:\Users\Public\Desktop\SerialHop.lnk,
        target: <install_dir>\SerialHop.exe,
        workingDir: <install_dir>,
        iconLocation: <install_dir>\SerialHop.exe + ",0",
        description: "SerialHop control panel",
   )
8. If !--no-launch and !--silent: start "" "<install_dir>\SerialHop.exe"
   (detached child via syscall.CreateProcess with CREATE_NEW_PROCESS_GROUP
   so the installer can exit immediately).
9. Report success (status label or stdout) and exit 0.
```

Failure handling for steps 1-6 inherits the rollback semantics already
documented in `2026-05-11-auto-update-design.md` §5. Step 7 (shortcut)
failure is logged but does **not** roll back the binary install — the
binary is functional without a shortcut, the operator can re-run the
installer or create the shortcut manually. Step 8 (launch) failure
likewise does not fail the install; the panel can be launched manually.

## 6. Desktop shortcut

`C:\Users\Public\Desktop\SerialHop.lnk` — the all-users Desktop, visible to
every operator on the machine, appropriate for a system-wide LocalSystem
service. Created via the COM `IShellLinkW` + `IPersistFileW` pair.

**Implementation choice — to be decided during implementation:**

Two paths, both produce identical `.lnk` files:

- **(a) Raw `golang.org/x/sys/windows` COM**: hand-roll the vtable boilerplate.
  Pro: zero new dependencies. Con: ~150 LOC of fiddly COM glue.
- **(b) `github.com/go-ole/go-ole`**: a small well-bounded dependency that
  wraps COM. Pro: ~30 LOC of caller code, well-tested. Con: a new transitive
  dep in `go.mod`.

The plan will commit to one path after a 30-minute spike. Default preference
is (a) per the "no new deps" hard preference, but if the spike shows (a) is
gnarly enough that the resulting code is hard to maintain, (b) is acceptable.
Either way, the public function signature stays the same:

```go
func writeShortcut(path, target, workingDir, iconLocation, description string) error
```

Properties of the produced `.lnk`:

- `TargetPath` = `<install_dir>\SerialHop.exe` (unversioned — this is the
  load-bearing property).
- `WorkingDirectory` = `<install_dir>`.
- `IconLocation` = `<install_dir>\SerialHop.exe,0` (first icon resource in the
  exe, which is the existing `assets/icon.ico` baked in via `goversioninfo`).
- `Description` = `"SerialHop control panel"`.
- `Arguments` = `""` (no flags — bare double-click launches the panel as today).
- `ShowCmd` = `SW_SHOWNORMAL`.

The function overwrites any pre-existing `SerialHop.lnk` at the same path.
Idempotent.

## 7. CI changes

`.github/workflows/release-please.yml`, `release-build` job:

| Step | Change |
|---|---|
| `sync version.json integer fields` | Apply the same jq transform to `tools/installer/version.json`. Same VER source, same six fields. |
| `build` | After `task build`, add `task installer`. Two artifacts in `dist/`: `SerialHop.exe` and `SerialHop-Setup.exe`. |
| `rename and checksum` | Move both: `SerialHop.exe` → `SerialHop-${tag}.exe`; `SerialHop-Setup.exe` → `SerialHop-Setup-${tag}.exe`. The `Get-FileHash dist\*.exe` glob already covers both; `SHA256SUMS.txt` lines for both. |
| `attest-build-provenance` | Subject path `dist/SerialHop-*.exe` already covers both files. |
| `upload to release` | `gh release upload $tag dist/SerialHop-$tag.exe dist/SerialHop-Setup-$tag.exe dist/SHA256SUMS.txt --clobber` |
| `upload agent build to VPS` | Replace `-F "binary=@dist/SerialHop-${TAG}.exe"` with `-F "binary=@dist/SerialHop-Setup-${TAG}.exe"`. The `version=` form field is unchanged. |

`.github/workflows/pr.yml`, `verify` job: no changes. The installer package
compiles cross-platform thanks to the `_windows.go` / `_other.go` build-tag
split. `go test ./...` covers `tools/installer`'s cross-platform tests on
both macOS and Windows runners; the shortcut round-trip test is Windows-only.

`release-please-config.json`:

- Extend `extra-files` to include `tools/installer/version.json` with the
  `json` updater targeting `$.StringFileInfo.FileVersion` and
  `$.StringFileInfo.ProductVersion`. (Per `CLAUDE.md`: don't add the
  FixedFileInfo integer fields — those are CI-derived.)

`CHANGELOG.md` / release-please: this lands as a single
`feat: ship installer that creates desktop shortcut and supports in-place
upgrades` PR. release-please will bump the minor on the next release.

## 8. VPS interaction

Today's curl from `release-build`:

```bash
curl --fail-with-body -sSL -X POST "https://${VPS_HOST}/api/agent/upload" \
  -H "Authorization: Bearer ${AGENT_UPLOAD_TOKEN}" \
  -F "version=${VERSION}" \
  -F "binary=@dist/SerialHop-${TAG}.exe" \
  -w '\nHTTP %{http_code}\n'
```

Becomes:

```bash
curl --fail-with-body -sSL -X POST "https://${VPS_HOST}/api/agent/upload" \
  -H "Authorization: Bearer ${AGENT_UPLOAD_TOKEN}" \
  -F "version=${VERSION}" \
  -F "binary=@dist/SerialHop-Setup-${TAG}.exe" \
  -w '\nHTTP %{http_code}\n'
```

The `version=` field is the source of truth for what gets stored on the VPS;
the filename of the `binary=@…` part is metadata the server is presumed not
to depend on. If the VPS does care about the filename (e.g., for the
operator-facing download URL), the server-side change is one-line and lives
outside this repo — flagged here for the deployment review.

The VPS will now serve the installer instead of the bare binary to operators
who download from the lab management UI. Operators who download directly
from GitHub Releases still see both `SerialHop-vX.Y.Z.exe` and
`SerialHop-Setup-vX.Y.Z.exe` and pick. The installer is the
recommended path; the bare exe stays available for the panel's in-app
auto-update flow (which is what consumes it programmatically) and for
operators who want to inspect/manage the binary manually.

## 9. Internal package & code changes

| File | Change |
|---|---|
| `tools/installer/main.go` (new) | Entry point. Parse flags, branch into silent vs dialog path, dispatch to `install.Run(opts)`. |
| `tools/installer/install.go` (new) | `Run(opts)`: version check (§4.4), payload extract, `winsvc.InstallOrUpgrade`, shortcut, launch. |
| `tools/installer/install_test.go` (new) | Cross-platform unit tests with fake fs, fake SCM, fake shortcut writer, fake launcher. Covers fresh, upgrade, same, downgrade-refused, downgrade-with-flag, shortcut step failure (non-fatal), SHA-256 mismatch. |
| `tools/installer/peversion_windows.go` (new) | `readPEFileVersion(path) (string, error)` via `GetFileVersionInfoExW` + `VerQueryValueW`. |
| `tools/installer/peversion_other.go` (new) | Build-tag stub: returns `("", errors.New("PE version read only supported on Windows"))`. Tests that exercise this codepath skip on non-Windows. |
| `tools/installer/shortcut_windows.go` (new) | `writeShortcut(...)`. COM `IShellLinkW`/`IPersistFileW`. |
| `tools/installer/shortcut_other.go` (new) | Build-tag stub: returns `errors.New("shortcut creation only supported on Windows")`. |
| `tools/installer/shortcut_windows_test.go` (new) | Round-trip lnk creation in `t.TempDir()`. Resolves via `IShellLink::GetPath` / `GetWorkingDirectory` / `GetIconLocation` and asserts equality. |
| `tools/installer/ui_windows.go` (new) | walk dialog. Path field, Browse (`walk.FileDialog`), Install, Cancel, status label. Drives `install.Run(opts)` on a goroutine; marshals status updates via `mw.Synchronize`. |
| `tools/installer/ui_other.go` (new) | Build-tag stub: panics if invoked. Not invoked from cross-platform tests. |
| `tools/installer/version.json` (new) | Same shape as `assets/version.json` (see §3.4). |
| `tools/installer/manifest.template.xml` (new) | UAC manifest (§4.2). |
| `tools/installer/manifest.xml` | gitignored (generated). |
| `tools/installer/resource_windows.syso` | gitignored (generated). |
| `tools/installer/payload/SerialHop.exe` | gitignored (generated at build time by Taskfile). |
| `tools/installer/payload/.gitkeep` (new) | Keeps the `payload/` dir tracked. |
| `tools/render-installer-manifest/main.go` (new) | Parallel of `tools/render-manifest`. |
| `internal/winsvc/control.go` | Export `InstallOrUpgrade(scm SCMConn, src, target string) error` as a thin wrapper around `updateBinary` with production timeouts. Documented as the entry point for both the panel auto-update path (called via `RunAdminAction("update", …)`) and the installer's direct call. |
| `internal/winsvc/control_test.go` | Add a coverage test that `InstallOrUpgrade` (a) dispatches to `updateBinary` correctly and (b) succeeds when the service is not installed (the installer's fresh-install case). The existing `updateBinary` tests already cover the rest. |
| `internal/updater/version.go` | If `IsNewer(remote, local)` doesn't already have a three-way `Compare(a, b) (int, error)` alongside, add it. Used by the installer's downgrade detection. |
| `internal/updater/version_test.go` | Cover `Compare` table-driven, same cases as `IsNewer`. |
| `Taskfile.yaml` | Add `installer-manifest`, `installer-resource`, `installer` tasks; extend `clean`. |
| `.github/workflows/release-please.yml` | Apply changes from §7. |
| `release-please-config.json` | Add `tools/installer/version.json` to `extra-files`. |
| `.gitignore` | Add `tools/installer/manifest.xml`, `tools/installer/resource_windows.syso`, `tools/installer/payload/SerialHop.exe`. |
| `README.md` | New "Install on a Windows lab machine" subsection: "Download `SerialHop-Setup-vX.Y.Z.exe` from the release page or the lab management UI. Double-click; approve UAC; the installer opens, defaults to `C:\Program Files\SerialHop`, click Install. SerialHop opens automatically; click Install to register the service and enter credentials." Old "copy the .exe to a folder" instructions move to an "Advanced / manual install" subsection. |

No changes to `internal/api/`, `internal/app/`, `internal/bootstrap/`,
`internal/chisel/`, `internal/config/`, `internal/discovery/`,
`internal/flasher/`, `internal/labbridge/`, `internal/logship/`,
`internal/panel/`, `internal/paths/`, `internal/registry/`,
`internal/serial/`. The installer is additive.

## 10. Testing

`tools/installer/install_test.go` runs cross-platform. The test uses
interface fakes (mirroring `internal/winsvc/control_test.go`'s `fakeSCM`
pattern) for:

- `fsOps`: `Rename`, `Remove`, plus `WriteFile`, `Stat` for the install flow.
- `versionReader`: `ReadFileVersion(path) (string, error)` — substitutes the
  PE-resource lookup with a fake that returns a configured version per path.
- `scm`: reuses `internal/winsvc.SCMConn` / fakeSCM.
- `shortcutWriter`: `Write(opts) error`.
- `launcher`: `Launch(path) error`.

Test cases:

| Case | Initial state | Expected |
|---|---|---|
| Fresh install | `<install_dir>` doesn't exist | dir created, payload written + verified, no SCM stop (service missing → updateBinary no-ops the stop), rename to `SerialHop.exe`, shortcut written, panel launched, exit 0 |
| Upgrade, service running | installed v0.6.1, service running | stop → swap → start → shortcut refresh → launch → exit 0 |
| Upgrade, service stopped | installed v0.6.1, service stopped | swap → no start → shortcut refresh → launch → exit 0 |
| Upgrade, service uninstalled | installed v0.6.1, no service | swap (no SCM calls) → shortcut refresh → launch → exit 0 |
| Same version | installed v0.7.0, installer v0.7.0 | no payload extract, no rename, no SCM ops, shortcut refresh, launch, status "Already installed", exit 0 |
| Downgrade refused | installed v0.7.0, installer v0.6.1, no `--allow-downgrade` | no payload extract, no rename, no SCM ops, no shortcut, no launch, status error, exit 1 |
| Downgrade with flag | installed v0.7.0, installer v0.6.1, `--allow-downgrade` | swap proceeds same as upgrade |
| Payload SHA mismatch | (simulated by tampering with the read-back bytes) | stagedPath deleted, no rename, no SCM ops, error, exit 1 |
| Rename swap failure → rollback | fs.Rename returns error on src → target | .old rolled back, service restored if it was running, error |
| Service start fails after swap | fs ok, scm.Start returns error | both renames rolled back, new exe preserved under versioned name for diagnostics, error |
| Shortcut writer fails | other steps ok | binary install succeeds, shortcut error logged, exit 0 (non-fatal) |
| Launcher fails | other steps ok | binary install + shortcut succeed, launch error logged, exit 0 (non-fatal) |
| `--no-shortcut` | other steps ok | shortcut writer never called, no error |
| `--no-launch` | other steps ok | launcher never called, no error |
| `--silent` | other steps ok | launcher never called (silent implies no-launch), no error |

`tools/installer/shortcut_windows_test.go` runs only on Windows (build tag).
Creates a shortcut to a fake target path in `t.TempDir()`, resolves it via
the same COM interface, asserts target/working-dir/icon match.

`internal/winsvc/control_test.go` extension: a one-line test that
`InstallOrUpgrade` dispatches successfully to `updateBinary`. The existing
suite already covers the failure modes; we don't duplicate.

`internal/updater/version_test.go` extension: `Compare` table-driven cases
(equal, newer, older, malformed inputs).

No live-network tests. No tests against actual `Program Files`. No tests that
spawn the real installer binary (the package-level tests against the fakes
are sufficient).

## 11. Logging

The installer is short-lived. In both `--silent` and dialog modes, full
detail is written to a one-shot log file at
`%TEMP%\SerialHop-installer-<timestamp>.log` so a failed install leaves
diagnostics behind. The path is printed in the final error message. In
`--silent` mode, the same lines also stream to stderr; in dialog mode the
status label is the operator-facing surface and the log file is the
diagnostic record.

Log lines use the same `slog.Info`/`slog.Warn` shape as the rest of the
codebase:

- `slog.Info("installer_started", "bundled_version", ..., "target_dir", ...)`
- `slog.Info("version_check", "installed", ..., "bundled", ..., "decision", "fresh|upgrade|same|downgrade")`
- `slog.Info("payload_extracted", "path", ..., "size", ..., "sha256", ...)`
- `slog.Info("install_or_upgrade_completed", "version", ...)`
- `slog.Warn("shortcut_failed", "path", ..., "err", ...)`
- `slog.Warn("launch_failed", "path", ..., "err", ...)`
- `slog.Info("installer_finished", "status", "success|already_installed|downgrade_refused|error", "duration_ms", ...)`

## 12. Error response surface (operator-visible)

| Trigger | Where surfaced | Message |
|---|---|---|
| Not elevated (manifest stripped) | dialog status / stderr | "This installer must be run as administrator. Right-click → Run as administrator, or rerun and approve the UAC prompt." exit 1 |
| MkdirAll failed (path not writable) | status / stderr | "Cannot create install directory `<path>`: <err>. Pick a different location." exit 1 |
| Payload SHA mismatch | status / stderr | "Bundled payload integrity check failed; the installer may be corrupted. Re-download from the source and try again." exit 1 |
| Service stop timed out | status / stderr | "Service did not stop within 15s. Manually stop SerialHop in Services.msc and re-run." exit 1 |
| Service start failed after swap | status / stderr | "Service failed to restart after update; rolled back to v<X.Y.Z>. See `%TEMP%\SerialHop-installer-<timestamp>.log`." exit 1 |
| Downgrade refused | status / stderr | "Installed version (v<X.Y.Z>) is newer than this installer (v<A.B.C>). Re-run with `--allow-downgrade` to proceed anyway." exit 1 |
| Cancelled in dialog | dialog closes | (no error; exit 0) |
| Shortcut creation failed | status / stderr (non-fatal) | "Install succeeded but desktop shortcut creation failed: <err>. You can launch SerialHop from `<install_dir>\SerialHop.exe`." exit 0 |
| Panel launch failed | status / stderr (non-fatal) | "Install succeeded but launching SerialHop failed: <err>. Double-click the desktop shortcut to start it." exit 0 |
| Same version (info, not error) | status / stderr | "SerialHop v<X.Y.Z> is already installed. Refreshed desktop shortcut." exit 0 |

## 13. Compatibility

- No breaking changes to existing endpoints, request bodies, response shapes,
  config fields, or persistent state.
- A box with SerialHop pre-installed via the legacy "copy the exe" path is
  recognized by the installer as an existing install at the same `<install_dir>\SerialHop.exe`
  and gets the upgrade flow on next installer run, regardless of how that
  exe got there originally. The desktop shortcut is created on the first
  installer run.
- `%ProgramData%\SerialHop\` layout is untouched.
- The GitHub release artifact list grows by one (`SerialHop-Setup-vX.Y.Z.exe`).
  Existing scripts that download `SerialHop-vX.Y.Z.exe` keep working.
- The VPS-served artifact changes from the bare exe to the installer.
  Operator-facing download URLs may need a server-side filename rename if the
  server exposes the original upload filename (out of scope for this repo;
  flag at deployment review).

## 14. Build / release

- One new third-party dependency *possibly* (`github.com/go-ole/go-ole`,
  conditional on the §6 spike). Otherwise zero new deps. `tools/installer`
  uses `embed`, `flag`, `crypto/sha256`, `os`, `path/filepath`,
  `os/exec`, `log/slog`, the local `internal/winsvc` and
  `internal/updater/version`, and `golang.org/x/sys/windows` (already in
  `go.sum`).
- Two new Taskfile targets (`installer-manifest`, `installer-resource`,
  `installer`) plus an extension to `clean`.
- One new tool under `tools/` (`render-installer-manifest`) mirroring an
  existing pattern.
- Conventional Commits: lands as a single `feat: ship installer that creates
  desktop shortcut and supports in-place upgrades` PR. release-please bumps
  minor on the next release.

## 15. Security posture

- The installer requires admin to run (UAC manifest). It writes to
  `C:\Program Files\SerialHop\` (system-protected), starts/stops the
  `SerialHop` SCM service, and writes `C:\Users\Public\Desktop\SerialHop.lnk`.
  No network, no registry mutations outside the SCM database, no scheduled
  tasks, no user-profile writes.
- Trust anchor for the embedded payload is *the installer's own PE signature*
  — i.e., once code signing is in place. Until then, the operator trusts the
  installer the same way they trust the bare exe today (TLS to the GitHub /
  VPS download path, optional manual `shasum -a 256 -c SHA256SUMS.txt`, +
  Sigstore attestation verification via `gh attestation verify`). The
  installer's bundled payload SHA is verified against the embedded bytes
  *only* — that catches local corruption but not adversarial substitution
  before the user ran the installer.
- The installer never executes the embedded payload directly. It only writes
  the bytes to disk and registers them via SCM (after `winsvc.InstallOrUpgrade`).
  Indistinguishable from an operator hand-copying the file and running the
  panel's Install button.
- Downgrade refusal is a safety mechanism, not a security mechanism — an
  attacker with admin can `--allow-downgrade` just as easily as a friendly
  operator can. It exists to make "I double-clicked the wrong installer"
  recoverable.
- Sigstore attestation continues to cover both artifacts via the existing
  wildcard.
