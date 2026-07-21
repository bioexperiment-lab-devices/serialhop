# Remote Admin-Pushed Updates — Design

**Date:** 2026-07-21
**Status:** Approved (brainstorming complete; pending spec review before plan)
**Target platform:** Windows (amd64). The orchestration logic is cross-platform Go; the one Windows-only piece (detached child spawn) has a macOS/Linux fake.

## 1. Purpose & scope

Let a **lab-bridge admin** push a SerialHop update to a lab PC over the existing
REST API — no lab-operator action, no UAC prompt, no panel click. A single
`POST /agent/update` downloads, verifies, and installs a new binary; a companion
`GET /agent/update/status` reports the outcome (which survives the service
restart the install causes).

The whole feature is **off by default** and gated by one config flag
(`remote_update.enabled`). When off, both endpoints return `404` and behave
exactly like the pre-feature binary.

### Why this is feasible without UAC

The SerialHop service is installed as **LocalSystem**
(`internal/winsvc/control.go` — `install()` leaves `ServiceStartName` empty →
LocalSystem). It is therefore *already elevated*: it can stop/start services and
write to `C:\Program Files\SerialHop\`. The panel's auto-update needs a UAC
click only because the panel is an unprivileged desktop process; the service is
not. The one thing the service cannot do is run the in-place swap *in-process* —
stopping the service kills the process mid-swap — so it spawns a short-lived
**detached child** that inherits LocalSystem and performs the swap. No elevation
prompt is possible or needed.

### In scope

- `POST /agent/update` — trigger. Body selects the source (§4.1).
- `GET /agent/update/status` — last-known result, restart-surviving (§4.2).
- `remote_update.enabled` config flag, default `false`, plus its schema migration.
- Reuse of the existing `updater` download/verify code and the existing
  `winsvc` `update` swap-with-rollback path (extended only to write a result
  file).
- A new `internal/remoteupdate` orchestrator package + the detached-spawn
  Windows shim and its fake.

### Out of scope (deliberately YAGNI)

- **Server-side (lab_devices_server) changes.** Admin-gating (Authelia rule for
  `group:admins`) and the dispatch route that proxies to the chisel port live in
  the server repo. This spec notes the prerequisite (§8) but does not implement
  it.
- **A SerialHop-side auth token.** Authorization is enforced server-side, exactly
  like every other endpoint on this mux (§7). The config flag is the master
  on/off, not an authenticator.
- **Panel UI changes.** The existing operator-facing auto-update row (panel,
  UAC) is untouched and continues to work independently.
- **Sigstore attestation verification in-app** (same deferral as the panel
  auto-update spec, `2026-05-11-auto-update-design.md` §1).
- **Custom-URL host allow-listing.** Discussed under §7; deferred as a future
  hardening pass.
- **Delta/differential updates; scheduled/staged fleet rollouts.** Those are
  server-side orchestration concerns.

## 2. High-level flow

```
POST /agent/update            (handler, in the service process)
  ├─ remote_update.enabled == false  → 404
  ├─ an update already in flight     → 409
  ├─ resolve target version:
  │    {}                     → updater.LatestRelease()          → tag/assets
  │    {"version":"vX.Y.Z"}   → updater.ReleaseByTag("vX.Y.Z")   → tag/assets
  │    {"url","sha256",…}     → caller-supplied artifact
  ├─ GitHub modes only: target == running version → 200 {"outcome":"noop"}
  └─ else: launch background job, return 202 {"accepted":true,"to":"…"}

background job (goroutine in the service):
  1. result := {state:"downloading", from, to, started_at}   (persist to disk)
  2. download artifact → staging (SerialHop-vX.Y.Z.exe); update pct in result
  3. result.state = "verifying"; SHA-256 verify
       fail → result = {state:"failed", error}; STOP (service keeps running)
  4. result.state = "installing"; persist
  5. spawn DETACHED child (LocalSystem, no UAC):
        SerialHop.exe --admin-action=update
          --update-src=<staging>/SerialHop-vX.Y.Z.exe
          --update-result=<result path>
          --update-from=<running> --update-to=<target>
  6. goroutine returns; service keeps running until the child stops it

detached child (winsvc.RunAdminAction "update", existing swap + rollback):
  stop service (this process exits) → rename swap → start service
  → write result: {state:"succeeded"|"rolled_back", from, to, error?, finished_at}

new service instance:
  on startup: reconcile result file (§5.3)
  serves GET /agent/update/status → the result JSON
```

The download+verify runs entirely in the background so the trigger request
returns in milliseconds. This matters because the admin→agent request traverses
the server's auth proxy and the chisel tunnel; a multi-minute *held* POST would
risk an intermediary idle timeout. All outcomes — including download failure and
checksum mismatch — are reported through `/agent/update/status`, never through
the trigger response.

## 3. Configuration

New top-level section in `SerialHop_config.yaml`:

```yaml
remote_update:
  enabled: false   # allow lab-bridge admins to push updates via
                   # POST /agent/update (admin-gated server-side, like
                   # /flash). the update installs with no operator action.
                   # off by default.
```

- New `RemoteUpdateConfig struct { Enabled bool }`; field `RemoteUpdate` on
  `Config`; `Default()` returns `Enabled: false`.
- Validation: bool; structurally always valid. A config written by an older
  binary parses cleanly — the missing section defaults to `enabled: false`,
  which is the safe default (feature stays off until explicitly enabled).

### 3.1 Schema migration (required — CLAUDE.md "Registering config changes")

This is the repo's **first real migration** (`migrations` currently ships empty
at baseline `CurrentSchemaVersion == 1`).

1. Bump `config.CurrentSchemaVersion` `1 → 2` (in `config.go`).
2. Append exactly one entry to `internal/config/migrations.go`:

   ```go
   {
     To:   2,
     Desc: "add remote_update section (default disabled)",
     Ops: []Op{
       Add("remote_update", `remote_update:
     enabled: false   # allow lab-bridge admins to push updates via
                      # POST /agent/update (admin-gated server-side, like
                      # /flash). the update installs with no operator action.
                      # off by default.`),
     },
   }
   ```

   `Add(path, snippet)` requires the snippet's top key to equal the final path
   segment; here path == `remote_update` and the snippet's top key ==
   `remote_update`, so the whole section (with its comment) is inserted at the
   top level, creating it only if absent (respects an operator's existing value).
3. Update the first-run scaffold in `config.go`: add the identical
   `remote_update:` block and bump the scaffold's `schema_version:` line `1 → 2`.
   The comment text in the scaffold and the migration snippet **must match** —
   `TestScaffoldMatchesMigratedBaseline` fails on drift.
4. Add a before/after migration test in `internal/config/migrate_test.go` with
   fixtures under `internal/config/testdata/migrations/` (a v1 file gains
   `remote_update.enabled: false` and `schema_version: 2`; an operator who
   already set `enabled: true` keeps their value).

`EnsureMigrated` already runs at service and panel startup and backs the file up
to `SerialHop_config.v1.bak.yaml` before rewriting.

## 4. API contract

Both routes attach to the existing mux in `internal/api/handlers.go`
(`s.Handler()`), alongside `GET /agent/info`. Both consult
`remote_update.enabled` and return `404 {"error":"not found"}` when disabled, so
a disabled agent is indistinguishable from one that never had the feature.

### 4.1 `POST /agent/update`

Request body (JSON; empty body allowed):

| Body | Meaning |
|---|---|
| `{}` or empty | Install the latest GitHub release. |
| `{"version":"v2.3.0"}` | Install a specific GitHub release tag. |
| `{"url":"https://…/SerialHop-v2.3.0.exe","sha256":"<hex>"}` | Install from a custom mirror. `sha256` required in this mode. |
| `{"url":"…","sha256":"…","version":"v2.3.0"}` | Custom mirror with explicit version (used for the staged filename / `to` field when the URL basename isn't `SerialHop-v*.exe`). |

Field rules:

- `version`, when present, must parse as `X.Y.Z` (leading `v` optional).
- `url`, when present, must be `https://` (reject `http://` and non-URL).
- `sha256` is **required** whenever `url` is present (64 hex chars).
- `url` and GitHub mode are mutually exclusive: if `url` is set, `version` is
  only a label; if `url` is absent, the source is GitHub.
- Custom mode needs a determinable version for the staged filename
  (`SerialHop-v<ver>.exe`, which the child's existing filename check requires)
  and the `to` field: use `version` if given, else parse it from the URL
  basename if it matches `SerialHop-v*.exe`, else `400`.

Responses:

| Status | Body | When |
|---|---|---|
| `202 Accepted` | `{"accepted":true,"to":"2.3.0"}` | Job started. |
| `200 OK` | `{"outcome":"noop","reason":"already at 2.3.0"}` | GitHub mode, target == running version. |
| `400 Bad Request` | `{"error":"…","detail":"…"}` | Malformed body; bad `version`/`url`/`sha256`; custom mode with undeterminable version. |
| `404 Not Found` | `{"error":"not found"}` | `remote_update.enabled: false`. |
| `409 Conflict` | `{"error":"update in progress"}` | A job is already running (in-process guard, §7). |
| `502 Bad Gateway` | `{"error":"release lookup failed","detail":"…"}` | GitHub release/tag lookup failed *synchronously* during resolution (before the job is accepted). Download failures happen in the background and surface via status, not here. |

Version policy: downgrade and same-version reinstall are **allowed** (the admin
is authoritative — rolling back a bad fleet release is a first-class use). GitHub
modes short-circuit to `noop` only when the resolved target exactly equals the
running version — compared with `updater.Compare(target, version.Base()) == 0`,
which strips any leading `v` and `+buildmeta` suffix so a dev build compares by
its base release. Custom-URL mode always installs what the admin points at (no
cheap pre-download version to compare).

### 4.2 `GET /agent/update/status`

Returns the last-known update result (or `{"state":"none"}` if none). Survives
the service restart because it is read from disk (§5.2).

```json
{
  "state": "succeeded",
  "from": "2.2.0",
  "to": "2.3.0",
  "started_at": "2026-07-21T10:00:00Z",
  "finished_at": "2026-07-21T10:01:12Z"
}
```

`state` ∈ `none | downloading | verifying | installing | succeeded | rolled_back | failed`.
`downloading` may include `"pct": <0-100>`. `failed` and `rolled_back` include
`"error": "<detail>"`. `Cache-Control: no-store`.

| state | Meaning |
|---|---|
| `none` | No update has ever been triggered on this agent. |
| `downloading` / `verifying` / `installing` | In progress (see §5.3 reconciliation for a stuck `installing`). |
| `succeeded` | New binary is running; running version == `to`. |
| `rolled_back` | Swap failed after the service stopped; the child restored the previous binary and restarted the service on `from`. |
| `failed` | Download or verification failed; the service was never touched (still on `from`). |

## 5. Result file & staging

### 5.1 Staging directory

The service runs as LocalSystem. Stage under **ProgramData**, not
`%LOCALAPPDATA%` (whose LocalSystem expansion is the awkward
`systemprofile\AppData\Local`). Add to `internal/paths`:

- `ServiceUpdateStagingDir()` → `<DataDir>/updates` (i.e.
  `C:\ProgramData\SerialHop\updates`), created 0o750 via a new
  `EnsureServiceUpdateStagingDir()`.
- `UpdateResultPath()` → `<DataDir>/update_result.json`.

Both honor the existing `SERIALHOP_DATA_DIR` test override that `paths` already
supports.

The staged file is named `SerialHop-v<X.Y.Z>.exe`. `runUpdateWithDeps` already
handles the cross-directory (and cross-volume) copy from the staging dir into the
install dir before the same-volume rename swap, so staging outside the install
dir is fine and already covered.

### 5.2 Result file writers

Two processes write the same JSON file, handing off at the process boundary:

- **Service (background goroutine):** writes `downloading` → `verifying` →
  (`failed` | `installing`). On `failed`, it is the terminal writer (no child is
  spawned).
- **Detached child (`winsvc` update action):** on entry rewrites `installing`,
  then terminal `succeeded` | `rolled_back`. The child only writes the result
  file when `--update-result=<path>` is passed, so the panel-driven update path
  (which passes no such flag) is completely unchanged.

Writes are atomic (write to `<path>.partial`, fsync, rename) so a status read
never sees a torn file.

### 5.3 Startup reconciliation

If the child dies hard (power loss, kill) after the swap but before writing a
terminal state, the file is stuck at `installing`. On service startup the
`remoteupdate` package reconciles once:

- result.state == `installing` and running version == `to` → rewrite
  `succeeded`.
- result.state == `installing` and running version == `from` → rewrite `failed`
  with `error: "install did not complete (reconciled at startup)"`.
- otherwise leave as-is.

This makes `/agent/update/status` trustworthy even across an abnormal child exit.

## 6. Internal package layout

| File | Change |
|---|---|
| `internal/config/config.go` | Add `RemoteUpdateConfig`, `Config.RemoteUpdate`, `Default()` value, scaffold section; bump `CurrentSchemaVersion` → 2. |
| `internal/config/migrations.go` | Append the `{To: 2, …}` migration (§3.1). |
| `internal/config/migrate_test.go` + `testdata/migrations/` | v1→v2 before/after case; operator-value-preserved case. |
| `internal/config/config_test.go` | `Default().RemoteUpdate.Enabled == false`; scaffold round-trips; `TestScaffoldMatchesMigratedBaseline` stays green. |
| `internal/updater/release.go` | Add `ReleasesByTagURL(tag)` + `ReleaseByTag(ctx, hc, url, ua)` hitting `/releases/tags/{tag}`. Reuse the existing `Release`/`Asset` types, `Download`, `Verify`. |
| `internal/updater/release_test.go` | Tag-lookup happy path + 404 (unknown tag) → error. |
| `internal/remoteupdate/remoteupdate.go` (**new**) | Orchestrator: `Manager` holding config-enabled flag, `*http.Client`, staging/result paths, spawner, and the in-flight guard. Methods: `Trigger(ctx, Request) (Accepted, error)`, `Status() Result`, `Reconcile()`. Pure logic; no direct Windows calls. |
| `internal/remoteupdate/result.go` (**new**) | `Result` struct + atomic read/write helpers (JSON, `.partial`+rename). |
| `internal/remoteupdate/spawn_windows.go` (**new**, `//go:build windows`) | `spawnDetached(exe string, args []string) error` using `os/exec` + `SysProcAttr{HideWindow:true, CreationFlags: DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP}`, no handle inheritance, no `Wait`. |
| `internal/remoteupdate/spawn_other.go` (**new**, `//go:build !windows`) | Fake `spawnDetached` that records the invocation (or runs a test hook) so the orchestrator compiles and is testable on macOS/Linux. |
| `internal/remoteupdate/*_test.go` (**new**) | See §9. |
| `internal/api/handlers.go` | Add two routes + handlers; add a `remoteUpdate *remoteupdate.Manager` field to `Server` and a param to `New`. Handlers 404 when the manager is disabled. |
| `internal/api/types.go` | Request/response DTOs for the two endpoints. |
| `internal/app/app.go` | Construct the `remoteupdate.Manager` from `cfg.RemoteUpdate.Enabled` (+ paths, http client, current version), call `Reconcile()` at startup, pass it to `api.New`. |
| `internal/winsvc/control.go` | Extend the `update` action to write the result file when a result path is supplied; add `runUpdate` params for `resultPath/fromVersion/toVersion`. Panel path (empty result path) unchanged. |
| `main.go` | Add `--update-result`, `--update-from`, `--update-to` flags; pass through to `winsvc.RunAdminAction`. |
| `internal/paths/paths.go` | Add `ServiceUpdateStagingDir`, `EnsureServiceUpdateStagingDir`, `UpdateResultPath`. |
| `README.md` / `docs/configuration.md` | "Remote updates (admin push)" subsection: what it does, the config flag, that admin-gating is server-side. |

No changes to `internal/discovery`, `internal/serial`, `internal/chisel`,
`internal/registry`, `internal/device`, or `internal/logship`.

### 6.1 `winsvc` signature change

`RunAdminAction(action, errorFile, updateSrc string) int` grows to
`RunAdminAction(action, errorFile, updateSrc, resultPath, fromVersion, toVersion string) int`.
Only the `update` action consumes the three new params; `install`/`uninstall`/
`restart` ignore them. `runUpdate`/`runUpdateWithDeps`/`updateBinary` thread the
result-writing through: on success write `succeeded`, on any rollback path write
`rolled_back` with the wrapped error. When `resultPath == ""`, no result file is
written (preserves the panel path exactly). The result-write is best-effort — a
failed result write must not change the update's own success/failure.

## 7. Security & concurrency

- **Authorization is server-side only.** SerialHop's REST API has no app-layer
  auth; the trust boundary is the chisel tunnel plus the server's forward-auth.
  The server (`lab_devices_server`) already gates `^/flash.*` to `group:admins`
  via Authelia; the update path gets the same treatment (§8). SerialHop's config
  flag is the master on/off (404 when off), not an authenticator. This is
  consistent with `/agent/info`, `/flash/{port}`, `/serial/…/attach`, and every
  device route.
- **Trust anchors.** GitHub modes keep the established anchor: TLS to
  `api.github.com` + SHA-256 against the release's `SHA256SUMS.txt`. Custom-URL
  mode's caller-supplied `sha256` guards **transfer integrity only, not
  provenance** (the caller provides both the URL and its hash). Its real anchor
  is the admin-only server gate plus the operator's explicit opt-in. This is
  documented, accepted, and the reason the feature is off by default.
- **Deferred hardening (noted, not built):** a config `allowed_hosts` list for
  custom-URL mode, and re-verifying SHA-256 inside the elevated child. Both are
  future passes; neither blocks this feature.
- **Concurrency.** An in-process guard in the `Manager` (a mutex + `inFlight`
  bool) rejects a second `POST /agent/update` with `409` while a job runs. This
  covers the realistic case (double admin-push). The rare cross-process race with
  a *panel-driven* UAC update is bounded by `updateBinary`'s own resilience (it
  tolerates a service that is already stopped and rolls back on any failure); it
  is documented as low-risk and not separately locked.
- **No new secrets in logs.** The result file and log lines contain versions,
  URLs, and hashes only — never credentials. (`tools/forbidsecretlog` continues
  to guard the log surface.)

## 8. Cross-repo prerequisite (lab_devices_server — out of scope here)

For the feature to be *reachable* by an admin, the server repo must, mirroring
its `/flash.*` handling:

1. Add an Authelia access rule gating the update path to `group:admins`.
2. Add a dispatch route that proxies `POST /agent/update` and
   `GET /agent/update/status` to the target client's chisel reverse-tunnel port.

This spec implements the SerialHop (agent) side only. The server work is tracked
separately, exactly as the `2026-05-18-agent-info-endpoint-design.md` spec left
server-side polling to the server repo.

## 9. Testing

All tests run on macOS/Linux CI and Windows CI (CLAUDE.md cross-platform rule).

**`internal/remoteupdate` (all platforms, fake spawner):**

- `Trigger` GitHub-latest: fake GitHub server → job runs → fake spawner receives
  `--update-src`/`--update-from`/`--update-to`; result transitions
  downloading→verifying→installing.
- `Trigger` GitHub-tag: `/releases/tags/v2.3.0` path exercised.
- `Trigger` noop: target == running → `200`/noop, no spawn.
- `Trigger` custom-URL: url+sha256 honored; version parsed from `version` field
  and from `SerialHop-v*.exe` basename; missing/underivable version → error.
- `Trigger` disabled: manager built disabled → handler-visible 404 path (tested
  at the api layer) / `Trigger` returns a disabled sentinel.
- `Trigger` in-flight: second call while first job runs → `409` sentinel.
- Download failure (fake server 500) → result `failed`, no spawn.
- Checksum mismatch → result `failed`, no spawn.
- `Reconcile`: installing+version==to → succeeded; installing+version==from →
  failed; terminal states untouched.
- `result.go`: atomic write/read round-trip; torn `.partial` never surfaces.

**`internal/api` (httptest):**

- `POST /agent/update` disabled → 404; enabled + `{}` → 202 shape; enabled +
  in-flight → 409; malformed body → 400; `http://` url → 400; url without
  sha256 → 400.
- `GET /agent/update/status` disabled → 404; enabled → 200 with the manager's
  current result; `none` when nothing ran.

**`internal/updater/release_test.go`:** `ReleaseByTag` happy path + unknown-tag
404 → error.

**`internal/winsvc/control_test.go` (extend existing `updateBinary`/`runUpdate`
tests):** with a result path set — success writes `succeeded`; rename-step
failure writes `rolled_back` with the error; start-timeout writes `rolled_back`.
With empty result path — no file written (panel-path regression guard). Uses the
existing `FS` fake; add a tiny fake/temp-file for the result write.

**`internal/config` (extend):** migration v1→v2 before/after; operator value
preserved; `Default().RemoteUpdate.Enabled == false`;
`TestScaffoldMatchesMigratedBaseline` green.

Coverage target: ≥80% on `remoteupdate`, `config`, `api`, `updater` (unchanged
policy). The `spawn_windows.go` shim is thin and Windows-only; its logic is
covered by the fake on non-Windows and is manual-verified on the lab machine.

## 10. Logging

- `remoteupdate`: `slog.Info("remote_update_triggered", "mode", …, "to", …)`,
  `..._download_started/…_completed/…_failed`, `..._verified`,
  `..._verify_failed`, `..._spawn_child`, `..._reconciled`. Debug for per-pct
  progress.
- `winsvc` update action (already has update logging per
  `2026-05-11-auto-update-design.md` §9): add the result-file write outcome at
  info/warn.

## 11. Compatibility

- No breaking changes to existing endpoints, request/response shapes, or
  persistent state. Two new routes; one new config section (defaults off); one
  schema bump with an append-only migration.
- A config written by an older binary parses cleanly and is migrated to v2 on
  next startup (backed up first). `remote_update` defaults to `enabled: false`.
- A binary with this change against a config with `remote_update.enabled: false`
  (or absent) behaves exactly like the pre-feature binary.
- New on-disk artifacts under `C:\ProgramData\SerialHop\`:
  `updates/SerialHop-v*.exe` (staged) and `update_result.json`. Both live in
  ProgramData, not the source tree — no `.gitignore` change.
- The `winsvc.RunAdminAction` signature change is internal (called only from
  `main.go`); no external contract.

## 12. Build / release

- No new third-party dependencies. `remoteupdate` uses `net/http`,
  `encoding/json`, `os/exec`, `context`, `log/slog`, and (Windows shim)
  `golang.org/x/sys/windows` — already in the module (used by `agentinfo`
  machine-id and `winsvc`).
- No `Taskfile.yaml` or release-please changes.
- Conventional Commits: ships as a single `feat: remote admin-pushed updates`
  PR. Release-please bumps the minor on the next release.
</content>
</invoke>
