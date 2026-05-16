# Cache the running lab-bridge identity for status-badge probes

**Status:** Approved 2026-05-16
**Branch:** `fix/actual_config_values_for_status_queries`

## Problem

When an operator changes `lab_bridge.host` (or `.user` / `.pass`) in the YAML config and clicks **Save** without restarting the Windows service:

- The service keeps running with the *previous* credentials — chisel tunnel, REST API, everything still works.
- But the panel's status-lamp probe goroutines (`internal/panel/wails_app.go:77-94`) call `config.LoadPartial(paths.ConfigPath())` on every tick, immediately picking up the *new* YAML values.
- The new host may be unreachable, mistyped, or simply not yet provisioned. The Server and Tunnel lamps flip to **Unreachable**.
- Net result: the operator sees red/grey lamps even though the service is happily connected. The lamps are reporting the *intended* config, not the *running* config.

The Service lamp does not suffer the same bug because SCM state isn't derived from YAML.

## Goal

Status badges should reflect what the **running service** is using, not what the YAML currently says. They should only re-track YAML values after a service restart applies them.

## Non-goals

- Changing the YAML schema, the cache schema's `version`, the installer, or release-please configuration.
- Reworking the `VerifyCredentials` flow (the "try the new creds before saving" path). It already probes the new YAML host directly and is correct as-is.
- Auto-restarting the service when the YAML is saved. Save-then-restart-when-ready remains the explicit user gesture.

## Approach

Extend the existing on-disk bootstrap cache (`server-info.cache.json`, written by the service at startup) so it stores the lab-bridge **identity triple** (`host`, `user`, `pass`) the running service is using. The panel reads identity from the cache instead of the YAML.

Two read regimes:

- **Service installed:** trust the cache exclusively. Edits to the YAML do not affect lamps until the service restarts.
- **Service not installed** (e.g. fresh install, before the operator clicks **Install**): fall back to the YAML so the operator gets lamp feedback while configuring.

The cache is **eager-written** at the top of service startup, *before* `bootstrap.Resolve` runs. This avoids a misleading edge case where a service stuck in bootstrap retries (because the new credentials are wrong) would otherwise leave the cache showing the previously-working host and the lamps would falsely show "Up" against a stale endpoint.

## Architecture

```
SERVICE START                                   PANEL PROBE TICK
─────────────                                   ────────────────
LoadConfig(YAML) → cfg                          svc lamp state?
        │                                       ├─ NotInstalled → LoadPartial(YAML)
        ▼                                       └─ otherwise    → ReadCacheRaw
SeedCache(path, host, user, pass)                                       │
   reads raw cache (if any)                                             │
   overwrites Host/User/Pass                                            ▼
   preserves ServerInfo/RemotePort/ActualRestPort               {host,user,pass}
        │                                                       (empty triple if
        ▼                                                        cache missing
bootstrap.Resolve                                                & service installed)
   on success: WriteCache (full struct)                                 │
        │                                                               ▼
        ▼                                              runServerProbe / runTunnelProbe
app.Run → writeActualRestPort
   updates only ActualRestPort
```

Between the eager-write at service start and any subsequent YAML edit, the cache is **frozen** with the running identity. Only a service restart updates it. Panel probes use that frozen snapshot.

## Components & changes

### `internal/bootstrap/cache.go`

- Add `Host string` (`json:"host"`) and `Pass string` (`json:"pass"`) to the `Cache` struct, alongside the existing `User`.
- Do **not** bump `cacheCurrentVersion`. A pre-existing v1 cache reads with empty `Host`/`Pass` and no error; the next service start rewrites it.
- Add `ReadCacheRaw(path string) (Cache, error)` — identical to `ReadCache` except it omits the user-anchor check. Same error contract (`ErrCacheMissing` on missing / corrupt / version-mismatched files) and same side-effect of deleting corrupt or version-mismatched files.
- `ReadCache(path, user)` stays. Remaining callers: `bootstrap.Resolve`'s own fallback path and `app.writeActualRestPort`. Both run inside the service, after `SeedCache` has aligned the cache's `user` to the current YAML, so the anchor check is effectively a no-op but harmless.

### `internal/bootstrap/seed.go` (new file)

```go
// SeedCache writes the running lab-bridge identity into the cache, preserving
// any server_info / remote_port / actual_rest_port from a previous run.
// Called at service startup (worker and runForeground) BEFORE bootstrap.Resolve
// so the cache always reflects the credentials the service is actually using —
// even if bootstrap is stuck in a retry loop against bad new credentials.
// Idempotent.
func SeedCache(path, host, user, pass string) error {
    c, err := ReadCacheRaw(path)
    if err != nil {
        c = Cache{Version: cacheCurrentVersion}
    }
    c.Version = cacheCurrentVersion
    c.Host = host
    c.User = user
    c.Pass = pass
    c.FetchedAt = time.Now().UTC().Format(time.RFC3339)
    return WriteCache(path, c)
}
```

### `internal/winsvc/worker.go` & `main.go::runForeground`

Insert one call before `bootstrap.Resolve`:

```go
if err := bootstrap.SeedCache(paths.ServerInfoCachePath(),
    cfg.LabBridge.Host, cfg.LabBridge.User, cfg.LabBridge.Pass); err != nil {
    slog.Warn("seed cache failed", "err", err)
}
```

Worker site: inside the goroutine launched at `worker.go:72`, before the `bootstrap.Resolve` call at line 75.

Foreground site: in `runForeground`, before the `bootstrap.Resolve` call at `main.go:146`.

### `internal/panel/servicecli.go`

- Drop the `user` field from `ServiceCli`. `NewServiceCli(cachePath string) *ServiceCli`.
- `baseURL()` switches to `ReadCacheRaw` — the local REST port belongs to whoever the running service is, regardless of YAML edits.

### `internal/panel/wails_app.go`

- Add a `probeCreds()` method on `*App`:

  ```go
  func (a *App) probeCreds() (host, user, pass string) {
      svc, _, _ := a.lamps.snapshot()
      if svc.state == winsvc.StateNotInstalled {
          c, _ := config.LoadPartial(paths.ConfigPath())
          return c.LabBridge.Host, c.LabBridge.User, c.LabBridge.Pass
      }
      c, err := bootstrap.ReadCacheRaw(paths.ServerInfoCachePath())
      if err != nil {
          return "", "", ""
      }
      if c.Host == "" {
          // Upgrade path: a v1 cache from before this fix has no Host yet,
          // and the service hasn't restarted. Fall back to YAML one time so
          // the lamps don't show Unreachable purely because of the upgrade.
          y, _ := config.LoadPartial(paths.ConfigPath())
          return y.LabBridge.Host, y.LabBridge.User, y.LabBridge.Pass
      }
      return c.Host, c.User, c.Pass
  }
  ```

- Replace the `config.LoadPartial(...)` blocks at lines 78 and 87 (inside the probe-loop closures) with a single call to `probeCreds()` each.
- Update the `NewServiceCli` call at line 65 to drop the second argument.

### `internal/panel/bindings.go`

- The diag-info section near line 437 (`d.CacheActualRestPort = c.ActualRestPort`) currently reads via `ReadCache(path, user)`. Switch to `ReadCacheRaw` — diag wants to surface "what is actually in the cache", not "what would the cache look like to my user".

## Edge cases

| Scenario | Behavior |
|---|---|
| Cache missing while service installed | `probeCreds` returns empty triple → lamps go Unreachable. Correct: this is an anomalous state worth surfacing. |
| Cache anchored to different user (legacy) | Ignored by panel reads (raw path). `SeedCache` overwrites on next service start. |
| `SeedCache` write fails | `slog.Warn`, service continues. `bootstrap.Resolve` will rewrite on success. |
| Eager write preserves a stale `ActualRestPort` from previous run | Brief window (~ms) until `writeActualRestPort` fires. HTTP to the dead port returns refused → `StatusServiceDown` — correct UX during startup. |
| YAML edited mid-tick | Probe loop is one-shot; `probeCreds()` is called inside each tick's closure. No torn reads. |
| Operator changes `lab_bridge.user` in YAML, Save without Restart | Cache retains OLD user → panel probes OLD-user tunnel endpoint. Desired behavior. After Save & Restart, `SeedCache` updates cache. |
| Tunnel pass blank in cache | `runTunnelProbe` already short-circuits to `lampNotConfigured` when pass is empty. No new code. |
| Corrupt cache | `ReadCacheRaw` deletes it (inherited behavior). Panel falls back per the empty-cache rule. |
| Concurrent writes | `WriteCache` is atomic (temp + rename). `SeedCache` and `writeActualRestPort` are sequential on the worker goroutine. |
| v1 cache after binary upgrade, before service restart | Cache has empty `Host` → `probeCreds` falls back to YAML one-time (see implementation). Brief: until the next service start, lamps behave like the pre-fix world. |

## Test plan

### `internal/bootstrap/cache_test.go` (extend)
- Round-trip `Host` / `Pass` through `WriteCache` → `ReadCache`.
- Read a synthetic v1 file (JSON without `host` / `pass` keys) → empty `Host` / `Pass`, no error.
- `ReadCacheRaw` returns the cache regardless of on-disk `user`.
- `ReadCacheRaw` rejects version-mismatched files and deletes them (parity with `ReadCache`).
- `ReadCacheRaw` returns `ErrCacheMissing` when the file is absent or corrupt.

### `internal/bootstrap/seed_test.go` (new)
- `SeedCache` against a missing path writes a fresh cache with `Version=1` and the supplied triple.
- `SeedCache` against an existing cache overwrites `Host` / `User` / `Pass` and preserves `ServerInfo`, `RemotePort`, `ActualRestPort`.
- `SeedCache` against a corrupt cache writes a fresh one (recoverable).
- `SeedCache` is semantically idempotent.

### `internal/panel/probe_creds_test.go` (new)
- Service lamp = `StateNotInstalled` → `probeCreds` returns YAML.
- Service lamp = `StateRunning`, cache present with non-empty `Host` → returns cache.
- Service lamp = `StateRunning`, cache present with empty `Host` (legacy v1) → falls back to YAML.
- Service lamp = `StateRunning`, cache missing → empty triple.
- Service lamp = `StateStopped`, cache present → still returns cache (no YAML fallback for "stopped").

### `internal/panel/servicecli_test.go` (extend)
- `baseURL` returns the cached port regardless of the cache's on-disk `user`.
- Compiles with the new `NewServiceCli(cachePath)` signature.

### `internal/app/app_test.go`
- Existing `writeActualRestPort` tests pass unchanged (anchored path is unmodified).

All new tests live in non-`_windows.go` files so they compile and run on macOS / Linux runners.

## Migration & release

- Cache `Version` stays at `1`. Pre-existing caches read with empty `Host` / `Pass`; the one-time YAML fallback in `probeCreds` covers the upgrade window until the service is next restarted.
- No YAML schema change. No installer change. No release-please manifest change.
- Conventional Commit: `fix:` → patch bump.
- Single PR. No `BREAKING CHANGE:` line.

## What this PR explicitly does not touch

- `SaveConfig`'s `kickNetProbes()` call stays. Probes will now hit cache values, but the kick is still useful right after Save & Restart so lamps refresh promptly once `SeedCache` runs.
- `VerifyCredentials` (the inline credential test before save) — separate code path, unaffected.
- `cacheCurrentVersion` — stays at 1.
- The 30 s probe interval, lamp colors, sub-line formats — all unchanged.
