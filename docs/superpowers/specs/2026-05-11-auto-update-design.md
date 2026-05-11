# Auto-Update — Design

**Date:** 2026-05-11
**Status:** Approved (brainstorming complete; pending spec review before plan)

## 1. Purpose & scope

Surface a "new release is available" prompt in the SerialHop control panel and let the operator install the update with a single UAC-gated click. Detection runs panel-side only (no service-side network code). Verification is SHA-256 against the release's `SHA256SUMS.txt`. The install is performed by the existing elevated admin-action mechanism, extended with one new action.

In scope:

- Panel polls `https://api.github.com/repos/bioexperiment-lab-devices/serialhop/releases/latest` on open and every 6 h while it stays open.
- If `tag_name` parses as a SemVer strictly greater than the running binary's version, show "Update available: vX.Y.Z" with **[Download]** and **[Release notes]** controls.
- **[Download]** fetches `SerialHop-vX.Y.Z.exe` plus `SHA256SUMS.txt` into the install directory **under the asset's versioned name from the GitHub API** (e.g., `SerialHop-v0.7.0.exe`), verifies SHA-256, then morphs into **[Install update]**.
- **[Install update]** invokes the existing UAC flow with `--admin-action=update --update-src=<path>`. The elevated child stops the service, renames `SerialHop.exe` → `SerialHop.exe.old`, renames the staged file (e.g., `SerialHop-v0.7.0.exe`) → `SerialHop.exe`, starts the service, and rolls back on failure.

### Naming convention

| File | Where it lives | When it exists |
|---|---|---|
| `SerialHop-v<X.Y.Z>.exe` | GitHub release asset *and* install dir while staged | Between **[Download]** completing and **[Install update]** succeeding. On success it becomes `SerialHop.exe` via rename. |
| `SerialHop.exe` | Install dir | Always (the SCM-registered service binary path). The on-disk name is version-less; the version is recoverable from the PE `VS_FIXEDFILEINFO` resource (and matches `internal/version.Version` at build time). |
| `SerialHop.exe.old` | Install dir | Briefly during install; lingers if the panel was running. Cleaned up on next panel launch (§4.4). |

The **installed** filename stays `SerialHop.exe` for two reasons: (a) SCM stores the absolute binary path at install time, so changing the filename on every update would require re-registering the service; (b) the README and existing install instructions all reference `SerialHop.exe`. The version travels in the resource, not the path.
- A config flag (`auto_update.enabled`, default `true`) lets operators turn the whole feature off.

Out of scope (deliberately YAGNI):

- Service-side detection / Windows toast notifications.
- Pre-release / channel selection (the GitHub `/releases/latest` endpoint already excludes pre-releases, which matches today's release process).
- Sigstore attestation verification in-app. The release already ships an attestation; verifying it requires pulling in a substantial dependency tree (`sigstore-go` plus its transitive deps for cosign / rekor / TUF). Defer until there's a concrete reason — for now, TLS to `api.github.com` plus SHA-256 against `SHA256SUMS.txt` is the trust boundary.
- Code signing the `.exe` (separate, orthogonal initiative; would also fix Defender false positives).
- Auto-restart of the panel process after install (operator closes / reopens; see §4.4).
- "Skip this version" / "Remind me later" UI affordances.
- Delta / differential updates.
- Downgrade.

## 2. Release-side prerequisite: SHA256SUMS.txt format

The current `release-build` job produces `SHA256SUMS.txt` via:

```powershell
Get-FileHash -Algorithm SHA256 dist\*.exe | Format-List | Out-File -Encoding ascii dist\SHA256SUMS.txt
```

`Format-List` output is multi-line `Algorithm : SHA256 / Hash : ... / Path : ...` blocks — not parseable by `shasum -a 256 -c` (which the README in §Releases tells operators to run for manual verification). The format is also awkward for our in-app parser.

Change the workflow step to emit the standard one-line-per-file `<lowercase-hex>  <filename>` format used by `sha256sum` / `shasum`:

```powershell
Get-FileHash -Algorithm SHA256 dist\*.exe | ForEach-Object {
  "$($_.Hash.ToLower())  $([System.IO.Path]::GetFileName($_.Path))"
} | Out-File -Encoding ascii dist\SHA256SUMS.txt
```

Two-space separator, lowercase hex, filename only (no directory). This:

- Makes `shasum -a 256 -c SHA256SUMS.txt` from the README actually work.
- Lets the in-app parser be three lines (`bufio.Scanner` + split on the two-space separator).
- Is the format every Linux/macOS operator already expects.

This change ships in the same PR as the in-app feature so the first auto-update release produces a parseable file. Old releases keep their non-standard `SHA256SUMS.txt`, but auto-update only ever compares against the *new* release's checksum file, so the old format never enters the parsing path. (Manual verification of historical releases per the README still misled operators — that's a pre-existing bug being incidentally fixed.)

## 3. Configuration

New top-level section in `SerialHop_config.yaml`:

```yaml
auto_update:
  enabled: true    # check GitHub Releases for newer versions and offer
                   # to install them from the control panel.
                   # set to false on air-gapped lab boxes or sites where
                   # the install machine cannot reach api.github.com.
```

- `Default()` returns `AutoUpdate.Enabled = true`.
- Validation: bool field; structurally always valid.
- Scaffold (`config.WriteScaffold`) gets the new section with the comment above.
- When `enabled: false`: panel skips all update-check work entirely (no network request, no UI row). The behavior is indistinguishable from the pre-auto-update binary.

A config file written by an older binary parses cleanly — the missing section defaults to `enabled: true`, which is the right default for the typical lab box that does have internet.

## 4. Panel UX

### 4.1 New UI row

A single row inserted above the existing button rows in `panel.go`, hidden by default:

```
Update:           v0.7.0 available  [Download]  [Release notes]
```

States:

| State | Row text | Buttons |
|---|---|---|
| Hidden (no update or check pending) | — | — |
| Update available, not downloaded | `Update: v0.7.0 available` | `[Download]` `[Release notes]` |
| Downloading | `Update: v0.7.0 — downloading…` | `[Cancel]` |
| Verification failed | `Update: v0.7.0 — checksum mismatch` (red) | `[Retry]` |
| Downloaded, verified, ready | `Update: v0.7.0 — ready to install` | `[Install update]` `[Release notes]` |
| Install in progress | `Update: installing…` | (disabled) |
| Install succeeded | `Updated to v0.7.0. Close and reopen this window to load the new panel.` | — |
| Install failed (rolled back) | `Update failed — service restored to v0.6.1.` (red) | `[Retry]` `[View error]` |
| Network error during check | (no row; logged to `SerialHop_panel_error.log` at debug) | — |

Layout details:

- The row uses the same `Composite{Layout: HBox{}}` pattern as the existing button rows.
- `[Release notes]` opens the release's `html_url` (returned by the GitHub API) via `OpenWithDefaultApp` (which already wraps `ShellExecute "open"` — it works for URLs as well as files).
- Progress for `[Download]` is rendered into the existing `statusBar` label (e.g., `Downloading 23% (8.4 MB / 36 MB)`), refreshed at ≤ 5 Hz to keep the UI responsive without flooding repaints. No separate progress widget — keeps the panel layout unchanged.

### 4.2 Update-check timing

- One check fires from a goroutine `~500 ms` after `panel.Run()` finishes its first `refresh()` (so the panel paints first, then the network call happens).
- A `walk.Timer`-driven re-check every 6 h while the panel stays open. Lab operators rarely keep the panel open that long, so this mostly serves the rare long-running session.
- Network failures (DNS, TCP, TLS, non-200 HTTP) are logged to `SerialHop_panel_error.log` at one line per failure with `time.Now().Format(time.RFC3339)` prefix, and the update row stays hidden. No popup, no status-bar noise — a flaky upstream shouldn't badger the operator.
- HTTP timeout: 10 s for the JSON metadata call; no retry on this layer (it'll retry naturally at the next 6 h tick or the next panel open).

### 4.3 Download

- Destination: `<install_dir>/<asset.Name>` — the asset's filename verbatim from the GitHub API (e.g., `SerialHop-v0.7.0.exe`). Same volume as `SerialHop.exe`, which is required for the rename trick in §5. If `<install_dir>` is not writable (read-only network share, ACL'd `Program Files` install), the download fails and surfaces "permission denied — install dir not writable" in red. We do not fall back to `%TEMP%` because the rename step needs same-volume.
- Stream from the asset's `browser_download_url` (also from the API JSON — never hand-constructed) with a 5 min overall timeout. Show `Cancel` while in flight; cancelling closes the connection, deletes the partial file, and reverts the row to the "available" state.
- After the body completes, fetch `SHA256SUMS.txt` from the same release (separate request, 10 s timeout). Parse `<hex>  <filename>` lines, find the row whose filename equals `<asset.Name>`, compare against the SHA-256 of the downloaded file. On mismatch: delete the file, set the row to the red "checksum mismatch" state, log full detail to `SerialHop_panel_error.log`.
- The verified file is left on disk under its versioned name. If the operator closes the panel before clicking `Install update`, the file persists; on next panel open, the panel looks for `<install_dir>/<latest_asset.Name>` (using the asset name returned by the current release check). If it exists *and* its SHA-256 still matches the release's `SHA256SUMS.txt`, the row jumps straight to "ready to install" without re-downloading. The re-checksum is cheap (one local SHA-256 over ~40 MB) and forecloses the cheap attack where someone with write access to the install dir swaps the staged file between download and install.
- Any other `SerialHop-v*.exe` file in the install directory whose name does **not** match the current latest-release asset name is treated as a stale leftover from a previous staging attempt and deleted on launch (§4.4). The currently-installed `SerialHop.exe` (no version in the name) is of course not in scope of this glob.

### 4.4 Install-dir cleanup

Two cleanup actions, running at different points in the panel's lifecycle:

**On startup (no network required):** the panel attempts `os.Remove("<install_dir>/SerialHop.exe.old")` once, best-effort. `.old` lingers if the panel was running during the last update install (the prior panel held the file open under its renamed name, so the elevated child's best-effort delete in §5 step 7 failed). The new panel is running from `SerialHop.exe` (the post-update binary), so it does not hold `.old` open — the delete succeeds. A failure is logged at debug and otherwise ignored; the file will be removed on the next launch instead.

**After the first successful update-check returns (requires the latest-release asset name):** the panel globs `<install_dir>/SerialHop-v*.exe` and deletes any entry whose name does not match the current latest-release asset name. This catches (a) a stale stage from an older latest-release that has since been superseded, (b) a partial download that was renamed-into-place but never installed, (c) any `*.exe` an operator manually parked there with a `SerialHop-v` prefix. False positives are bounded: anything matching the glob is either ours to manage or the operator's responsibility to not name like our staging files. If the update-check keeps failing (offline lab, GitHub outage), this cleanup never runs and stale staged files accumulate — but in practice the cardinality is one per Download-without-Install cycle, so the disk-bloat ceiling is very low.

### 4.5 Install

`[Install update]` calls `RunElevatedAdminAction` (unchanged from today) with action `update`, passing `--update-src=<install_dir>/<asset.Name>` (e.g., `--update-src=C:\Tools\SerialHop\SerialHop-v0.7.0.exe`) as an extra parameter. The existing helper already supports `--admin-action=<name>` and `--error-file=<path>`; we extend the parameter composition to also pass `--update-src=<path>` for this one action.

On success, the panel's status-bar shows `Updated to vX.Y.Z. Close and reopen this window to load the new panel.` and the update row is hidden until the next check tick.

On failure, the row shows the rolled-back state with `[Retry]` (re-runs the elevated install action with the same source path) and `[View error]` (opens the error file in Notepad via `OpenWithDefaultApp`).

The panel process itself is **not** restarted. Its open exe handle is bound to the renamed `SerialHop.exe.old`, so it continues to run; the operator must close and reopen for the panel UI to reflect the new code. This is called out in the success message so they aren't surprised.

## 5. Install flow (elevated child)

New action `update` in `internal/winsvc/control.go`. Sequence:

```
1. Parse --update-src; require:
   - file exists,
   - is on the same volume as the elevated child's own exe (`os.Executable()`),
   - is in the same directory as the elevated child's own exe,
   - filename matches "SerialHop-v*.exe" (sanity check; the elevated child only ever processes files the panel staged under the GitHub asset name).
2. Determine target = <install_dir>/SerialHop.exe (from os.Executable() of the elevated child — which is the same install location as the panel).
3. Connect to SCM. If the service is installed and running:
     a. Stop it. Wait up to 15 s for StateStopped (existing helper).
        Record `serviceWasRunning = true`.
   If the service is not installed, or already stopped, record `serviceWasRunning = false` and skip to step 4.
4. Rename target → <install_dir>/SerialHop.exe.old.
     - If a stale .old exists from a prior aborted update, delete it first; if delete fails (in-use), abort with a clear error.
     - Retry the rename up to 5× with 250 ms backoff (covers AV transient handles).
     - If rename ultimately fails: try to restart the service (so the operator isn't left with a stopped service), then return the rename error.
5. Rename --update-src (e.g., SerialHop-v0.7.0.exe) → target (SerialHop.exe).
     - If this fails: rename .old → target (best-effort rollback), restart the service if it was running, return error.
6. If serviceWasRunning, Start service. Wait up to 15 s for StateRunning.
     - If start fails / times out: rename target → <original --update-src name> (preserve the new binary under its versioned name for diagnostics), rename .old → target, Start, return the original start error wrapped as "update rolled back: <err>".
7. Delete .old (best-effort; if it's still held by something — typically the panel process — leave it. §4.4 handles cleanup on the next panel launch.)
8. Return success.
```

Key properties:

- **Idempotent retry**: if step 4 succeeds but step 5 fails, the rollback in step 5 returns the system to its pre-update state. If steps 4-5 succeed but step 6 fails, the rollback in step 6 likewise returns the system to its pre-update state. The new exe is preserved under its original versioned name (e.g., `SerialHop-v0.7.0.exe`) for the operator to inspect.
- **Rename, not copy**: same-volume `MoveFile` is the operation Windows itself uses for in-place updates. The kernel binds processes to NTFS file records, not paths, so the running service (when we briefly skip the stop, e.g., if it was already stopped) and the panel keep working under the renamed name.
- **No SMB / cross-volume case**: we deliberately don't handle install on a UNC path or a different volume from the download. The download is staged in the install directory, so the volumes always match. If the install directory is somehow on a different volume from itself, that's a bug elsewhere.
- **Timeouts match existing `restart` action**: 15 s stop, 15 s start. Same as today's restart admin action.
- **Service-not-installed case**: covered. Some operators run the panel without ever clicking Install (e.g., during initial setup). In that case the flow degenerates to "rename, rename" with no SCM calls.

The elevated child writes any error to `errorFile` exactly as the existing admin actions do; the panel reads it and either logs the rollback success line or surfaces the error to the user.

## 6. Internal package layout

| File | Change |
|---|---|
| `internal/config/config.go` | Add `AutoUpdateConfig` struct, field on `Config`, `Default()` value (`enabled: true`), scaffold section. |
| `internal/config/load_test.go` | Coverage: parses `auto_update.enabled: false`; `Default()` stays `true`; scaffold round-trip preserves the section; config file without the section still loads with `enabled: true`. |
| `internal/updater/check.go` (new) | `LatestRelease(ctx, httpClient) (Release, error)`. Hits the GitHub API, returns `{TagName, HTMLURL, Assets []{Name, BrowserDownloadURL, Size}}`. Pure data fetch; no version comparison. |
| `internal/updater/version.go` (new) | `IsNewer(remote, local string) (bool, error)`. SemVer comparison. Strips a leading `v` and any `+buildmeta` suffix from both sides. Handles the `0.6.1+v0.6.1-7-gabc1234-dirty` shape that the dev-build `-ldflags` produces — falls back to the base when build-meta is present. |
| `internal/updater/download.go` (new) | `Download(ctx, asset, destPath, progress func(received, total int64)) error`. Streams the asset to a `.partial` file, fsyncs, renames into place. Cancelable via ctx. |
| `internal/updater/verify.go` (new) | `Verify(filePath, sumsBody, filename string) error`. Parses the standard `sha256sum` format and compares. |
| `internal/updater/*_test.go` | See §8. |
| `internal/panel/panel.go` | Add the update row (§4.1), wire goroutine for periodic checks (§4.2), buttons for Download / Install / Retry / Release notes / Cancel. Use `mw.Synchronize()` to marshal goroutine-driven UI updates onto the GUI thread. |
| `internal/panel/update_state.go` (new) | Pure state machine: `UpdateState` enum + transitions. Same isolation pattern as `state.go` for the status indicator — keeps the testable logic out of the Windows-only `panel.go`. |
| `internal/panel/update_state_test.go` (new) | Coverage for the state machine. |
| `internal/winsvc/control.go` | (1) `RunAdminAction` signature grows a `updateSrc string` parameter — ignored by existing actions, consumed by `update`. (2) Add `update` case dispatching to a new `updateBinary(scm, srcPath, exePath)` function implementing §5. Threads through the existing `productionStopTimeout` / `productionStartTimeout` / `productionPollInterval` constants. |
| `internal/winsvc/control_test.go` | Coverage for `updateBinary` against the existing SCM fakes: serviceWasRunning, service stopped, service missing; success, rename-step-5 failure → rollback, start-step-6 failure → rollback. |
| `cmd/serialhop/main.go` | Add `flagUpdateSrc` so the elevated child can receive `--update-src=<path>`. Pass through to `winsvc.RunAdminAction`. |
| `internal/panel/elevate.go` | Extend `RunElevatedAdminAction(action string)` → `RunElevatedAdminAction(action string, extraArgs ...string)` so the panel can pass `--update-src=<path>` for the `update` action only. Existing callers compile unchanged. |
| `.github/workflows/release-please.yml` | Replace the `Get-FileHash | Format-List` line with the `ForEach-Object` form from §2 so `SHA256SUMS.txt` is in standard format. |
| `README.md` | Add a short "Auto-update" subsection under "Install on a Windows lab machine" explaining what the operator will see and how to opt out via config. |

No changes to `internal/api/`, `internal/discovery/`, `internal/serial/`, `internal/chisel/`, `internal/registry/`, or `internal/logship/`.

## 7. Network / HTTP behavior

- One shared `*http.Client` per `updater` instance, with `Timeout: 0` on the client and per-request `context.WithTimeout` (10 s for JSON, 5 min for asset download). This is the standard idiom that lets the download timeout differ from the check timeout.
- `User-Agent: SerialHop/<version> (auto-update; +https://github.com/bioexperiment-lab-devices/serialhop)`. GitHub's API requires a UA; the version string aids any future log analysis on the GitHub side.
- No GitHub token. The release API endpoint is anonymous-readable. Anonymous rate limit is 60 req/h per source IP — six-hour polling means ≤ 4 req/day per panel, well below the cap even with many panels open behind a shared NAT.
- TLS via stdlib defaults. No custom CA pool.
- The asset's `browser_download_url` from the API JSON is the canonical download URL — we don't hand-construct one from the tag. This survives any future change in GitHub's URL scheme.

## 8. Testing

`internal/updater/` is pure Go and runs on macOS/Linux:

- `check_test.go`: HTTP fake (`httptest.NewServer`) returns canned `/releases/latest` JSON. Verifies tag, html_url, asset name + URL extraction. Network error → returns wrapped error.
- `version_test.go`: table-driven SemVer cases:
  - `0.6.1` vs `0.7.0` → newer.
  - `0.7.0` vs `0.6.1` → not newer.
  - `0.7.0` vs `0.7.0` → not newer (strict greater).
  - `0.6.1+v0.6.1-7-gabc1234-dirty` (dev build) vs `0.7.0` (release) → newer.
  - `0.6.1+v0.6.1-7-gabc1234-dirty` vs `0.6.1` → not newer (base matches).
  - `v0.7.0` (leading `v` from tag) parsed correctly.
  - Malformed input → returns `(false, error)`.
- `download_test.go`: `httptest` server serves a small payload with a configurable slow body; verify (a) full download succeeds, (b) context cancellation aborts mid-stream and removes the `.partial` file, (c) HTTP 404 returns error, (d) progress callback is invoked with monotonically non-decreasing `received`.
- `verify_test.go`: standard-format sums file with two entries; correct filename → ok; mismatched hex → error; filename not in sums file → error; malformed line → error.

`internal/panel/update_state_test.go`: state machine transitions. Pure logic, no Windows deps.

`internal/winsvc/control_test.go` (extension): `updateBinary` against `FakeSCM`:

- Happy path: service running → stop → swap → start → cleanup.
- Service already stopped → swap → no start, no error.
- Service not installed → swap → no SCM calls, no error.
- Rename step (target → .old) fails → service restored if it was running.
- Rename step (src → target) fails → .old rolled back, service restored.
- Start fails / times out → new exe preserved under its versioned name (e.g., `SerialHop-v0.7.0.exe`), .old rolled back, error wraps the start failure.
- The actual `MoveFile` calls go through a small `fsOps` interface so the test substitutes a fake without writing real files. Wire the production `os.Rename` / `os.Stat` / `os.Remove` impl in the obvious one-method-per-call wrapper.

Coverage targets unchanged (≥80% on changed packages). No live-network tests.

## 9. Logging

- `internal/updater/check.go`: `slog.Debug("update_check", "current", ..., "latest", ..., "newer", bool)` on every check (success or no-op). `slog.Debug("update_check_failed", "err", ...)` on network error. Debug-level so the noise stays out of production info logs.
- `internal/updater/download.go`: `slog.Info("update_download_started", "asset", ..., "size", ...)`, `slog.Info("update_download_completed", "asset", ..., "bytes", ..., "duration_ms", ...)`, `slog.Info("update_download_failed", "asset", ..., "err", ...)`.
- `internal/updater/verify.go`: `slog.Info("update_verified", "file", ..., "sha256", ...)`, `slog.Warn("update_verify_failed", "file", ..., "expected", ..., "got", ...)`.
- `internal/winsvc/control.go` (update action): `slog.Info("update_install_started", "src", ..., "target", ...)`, `slog.Info("update_install_succeeded", "version", ...)`, `slog.Warn("update_install_rolled_back", "stage", ..., "err", ...)`.

All these log lines go through the panel process's logging path, which today is bare `slog` writing to stderr (which is `/dev/null` under `windowsgui`). For the elevated child the lines go to `errorFile` via the existing error-string mechanism. Adding panel-side log file rotation is out of scope; the panel's startup-failure mechanism (`writePanelStartupError`) is the durable record for anything the operator needs to see post-mortem.

## 10. Error response surface (user-visible)

| Trigger | Where surfaced | Message |
|---|---|---|
| Update check network failure | Silent in panel; `SerialHop_panel_error.log` | (logged only) |
| Tag name unparseable | Silent | (logged only) |
| GitHub API rate limited (403) | Silent | (logged only; next 6 h tick retries) |
| Install dir not writable for download | Update row, red | "Update v0.7.0 — install dir is not writable" |
| Download interrupted | Update row reverts to "available" | (status bar: "Download cancelled") |
| Download HTTP 4xx/5xx | Update row, red | "Update v0.7.0 — download failed (HTTP NNN)" |
| SHA-256 mismatch | Update row, red | "Update v0.7.0 — checksum mismatch (release artifact may have been re-uploaded; retry)" |
| Elevated update action failed | Update row, red + status bar | "Update failed — service restored to v0.6.1." `[View error]` opens the error file. |
| UAC dismissed | Status bar only | "Cancelled." (same as existing admin-action UAC dismiss path) |

## 11. Compatibility

- No breaking changes to existing endpoints, request bodies, response shapes, config fields, or persistent state.
- A config file written by an older binary parses cleanly. The missing `auto_update` section defaults to `enabled: true`.
- A binary with this change run against a config file that explicitly sets `auto_update.enabled: false` behaves like the pre-feature binary.
- `SerialHop.exe.old` and `SerialHop-v*.exe` (the staged versioned binary) are new artifacts the install directory may contain after the first auto-update. Both are best-effort cleaned up on subsequent panel launches. They live alongside the deployed binary, not in the source tree, so no `.gitignore` change is needed.

## 12. Build / release

- No new third-party dependencies. `internal/updater` uses only `net/http`, `encoding/json`, `crypto/sha256`, `bufio`, `context`, and `log/slog`.
- No changes to `Taskfile.yaml`.
- Release-please config unchanged.
- `release-please.yml` changes only the `SHA256SUMS.txt` generation step (§2).
- Conventional Commits: this lands as a single `feat: in-app auto-update with SHA-256 verification` PR. Release-please will bump the minor on the next release.

## 13. Security posture

- Trust anchors: TLS to `api.github.com` (system root CA store) + SHA-256 against the release's `SHA256SUMS.txt`.
- Threat model gap (acknowledged): a compromise of the GitHub release publishing path could publish a malicious `.exe` *and* a matching `SHA256SUMS.txt`, and our verification would accept it. Mitigations: (a) GitHub Actions OIDC + the existing Sigstore attestation provides forensic provenance for incident response; (b) release publishing is gated on a release-please PR that requires a maintainer squash-merge; (c) the public release surface is the same surface operators download from manually today, so auto-update doesn't *expand* the attack surface — it only automates fetching from it.
- If the threat model later requires defending against a compromised publishing path, the lift is to add Sigstore attestation verification (§1 out-of-scope). Spec'd but deferred.
- No code is executed from the downloaded `.exe` before the operator clicks `Install update` — we only hash it. The elevated child runs the new `.exe` only by registering it as the service binary path; it is not exec'd directly from the updater.
