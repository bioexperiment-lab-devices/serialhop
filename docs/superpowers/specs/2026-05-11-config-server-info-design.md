# Config cleanup + server-info-driven bootstrap + first-run credentials dialog

**Date:** 2026-05-11
**Status:** Draft (design)

## Motivation

Three pieces of agent configuration are currently duplicated between the local YAML config and shared knowledge that lives on the lab-bridge VPS:

- `chisel.port` (the chisel server's listen port) is in YAML; the server now exposes it at `GET /api/public/server-info → chisel.listen_port`.
- `chisel.remote_port` (the reverse-tunnel port assigned to this agent) is in YAML; the server now exposes it at `GET /api/public/clients/{user} → port`.
- The Loki push URL and the `loki:3100` forward tunnel are hardcoded as constants inside the Go code (`internal/logship/logship.go`, `internal/chisel/client.go`); the server now exposes both at `GET /api/public/server-info`.

Keeping shadow copies in the binary means they drift. This spec replaces all four with values fetched from the server at boot.

The same change exposes a second problem worth fixing in the same pass: the YAML scaffold ships with `lab_bridge.user: "devices_coordinator"` as a default, so a fresh install often runs with the wrong identity until somebody notices. We remove the default and add a first-run modal dialog that collects `user` and `pass`, verifying them against the live server before saving.

Reference contracts (provided inline by the user; no separate server-side spec files exist in this repo):

- `GET /api/public/server-info` — no auth. Returns `{ chisel: { listen_port }, loki: { push_url }, forward_tunnels: [{ name, local, remote }, ...] }`. Unknown extra keys (`chisel.fingerprint`, top-level `agent`) may appear and must be ignored.
- `GET /api/public/clients/{username}` — `Authorization: Bearer <chisel_password>`. Returns `{ port, connected }`. 401 covers all four of {unknown user, wrong token, missing header, non-Bearer scheme} indistinguishably. 5xx on server-side roster failure.

## Configuration shape after cleanup

The `Config` struct loses the entire `Chisel` section. The scaffold template becomes:

```yaml
# SerialHop_config.yaml
# Auto-generated scaffold. Site values are filled in via the panel's
# first-run dialog (username + password). Other fields are optional —
# edit only if you need to change defaults.

lab_bridge:
  host: "111.88.145.138"   # lab-bridge VPS host (chisel + public HTTPS API).
                           # change only when pointing at a different deployment.
  user: ""                 # REQUIRED — chisel auth user; also Bearer-token
                           # identity for the public API. No default.
  pass: ""                 # REQUIRED — chisel password; also Bearer token
                           # for /api/public/clients/{user}. No default.

rest:
  port: 0                  # local REST port; 0 = OS picks a free one.

discovery:
  include: []              # optional: only probe these COM ports, e.g. ["COM3", "COM4"]
  exclude: []              # optional: skip these COM ports, e.g. ["COM1"]
  post_open_settle_ms: 2000  # wait after opening a port before probing. covers the
                             # Arduino auto-reset bootloader window (~1-2 s). lower
                             # if your boards don't reset on DTR; 0 to disable.

log:
  level: "info"            # debug | info | warn | error

raw_serial:
  enabled: false           # set true to allow GET /serial/ports and
                           # POST /serial/ports/{port}/command. bypasses
                           # device classification — leave off unless diagnosing.

auto_update:
  enabled: true            # check GitHub Releases for newer versions
                           # and offer to install them from the panel.
                           # set to false on air-gapped lab boxes.
```

### Validation changes

- `lab_bridge.user` must be non-empty.
- `lab_bridge.pass` must be non-empty.
- `chisel.*` validation removed entirely.
- Everything else is unchanged.

### Migration

There is no automated migration. The release notes call out that `chisel.port` and `chisel.remote_port` lines in an existing config file are now ignored (YAML decoder tolerates unknown keys by default since the struct field is gone). Operators do not need to edit existing files.

## Server-info client and disk cache

### Wire client

Extend `internal/labbridge` with:

```go
type ForwardTunnel struct {
    Name   string
    Local  string
    Remote string
}

type ServerInfo struct {
    ChiselListenPort int
    LokiPushURL      string
    ForwardTunnels   []ForwardTunnel
}

func FetchServerInfo(ctx context.Context, hc *http.Client, base, userAgent string) (ServerInfo, error)
```

- `GET <base>/api/public/server-info`, no `Authorization` header.
- Permissive JSON: unknown top-level keys (`agent`, `chisel.fingerprint`) silently ignored — relies on standard `encoding/json` behavior (do not use `DisallowUnknownFields`).
- Response body capped at 64 KB (same `maxBodyBytes` constant the existing health/client calls use).
- Wraps `ErrServerError` on HTTP 5xx; plain error for transport/parse/unexpected status.
- 5 s timeout via the caller's `ctx`.

Validation inside the parser:
- `chisel.listen_port` must be in `1..65535`; otherwise reject with a plain error.
- `loki.push_url` must be non-empty.
- `forward_tunnels` may be `null` or `[]` (both treated as zero forwards).
- Each `forward_tunnel.local` and `forward_tunnel.remote` must be non-empty.

### Disk cache

Path: `%ProgramData%\SerialHop\server-info.cache.json` (new `paths.ServerInfoCachePath()`).

Schema:

```json
{
  "version": 1,
  "fetched_at": "2026-05-11T14:32:01Z",
  "user": "alice",
  "server_info": {
    "chisel_listen_port": 7000,
    "loki_push_url": "http://127.0.0.1:3100/loki/api/v1/push",
    "forward_tunnels": [{"name": "loki", "local": "127.0.0.1:3100", "remote": "loki:3100"}]
  },
  "remote_port": 8089
}
```

- Written atomically (write to `*.tmp`, rename) by the service worker after a successful bootstrap.
- Read by both the service worker (fallback on live-fetch failure) and the panel (display only).
- The `version` field is the future-upgrade lever; mismatched-version files are treated as missing.
- The `user` field anchors the cache to a specific identity. If the cached `user` does not match `cfg.LabBridge.User` at read time, the cache is treated as missing (and overwritten on next successful bootstrap). This prevents serving a stale `remote_port` after an operator edits `user` in the YAML.
- Any I/O or parse error → log a warning, delete the file, proceed as if cache is missing. Never fatal.

### Bootstrap resolver

New package `internal/bootstrap`:

```go
type Resolved struct {
    ServerInfo labbridge.ServerInfo
    RemotePort int
}

func Resolve(ctx context.Context, hc *http.Client, cfg config.Config, cachePath, userAgent string) (Resolved, error)
```

Algorithm (all-or-nothing — we never mix one live value with one cached value):

1. Try live `FetchServerInfo(cfg.LabBridge.Host)` + `FetchClient(user, pass)` in parallel goroutines under a single 5 s context.
2. If **both** succeed → write cache, return the live values.
3. If **either** fails with `ErrUnauthorized` (only possible from `FetchClient` since `server-info` has no auth) → cache is not consulted. Go straight to step 5 (the retry loop). 401 is a hard credentials signal; using cache to mask it would just delay the failure to chisel connection time.
4. If either fails with anything else (5xx, network, parse) → try to read cache.
   - Cache valid and `cache.user == cfg.LabBridge.User` → log warning describing which live fetch failed, return the fully-cached `Resolved`, leave the cache file intact.
   - No cache, invalid cache, or user-mismatch → fall through to the retry loop.
5. Retry the live fetches in a loop with exponential backoff (initial 1 s, doubling, capped at 1 min) until both succeed or `ctx.Done()`. Each retry re-runs the parallel pair from step 1.
6. `ctx` cancellation propagates as `ctx.Err()` at any point.

No hardcoded last-resort defaults. If the server has never been reached and the cache file does not exist, the service stays in the retry loop indefinitely; the panel reflects this via the existing red status lamps.

## Wiring changes

### `internal/winsvc/worker.go`

Current sequence: `paths.EnsureDirs → logship.Init → config.Load → manager.StartShipper(cfg.User) → app.Run(ctx, cfg)`.

New sequence:

1. `paths.EnsureDirs` — unchanged.
2. `logship.Init(version, level)` — unchanged signature; does not take or set a push URL. Disk writers come up immediately.
3. `config.Load(cfgPath)` — fails fast if validation now rejects empty `user`/`pass`.
4. `bootstrap.Resolve(ctx, hc, cfg, cachePath, userAgent)` — blocking call. The SCM `r <-chan svc.ChangeRequest` channel is still pumped via the existing `Execute` loop because `ctx` is the same context the worker uses for shutdown; a `Stop`/`Shutdown` request cancels the context and `Resolve` returns `ctx.Err()`.
5. On success: `manager.SetPushURL(resolved.ServerInfo.LokiPushURL)`, then `manager.StartShipper(cfg.LabBridge.User)`.
6. `app.Run(ctx, cfg, resolved)` — see below.

If `Resolve` returns an error other than `ctx.Err()`, the worker logs it and transitions to `Stopped` (same fatality contract as `config.Load` today).

### `internal/app/app.go`

Signature change:

```go
func Run(ctx context.Context, cfg config.Config, resolved bootstrap.Resolved) error
```

Chisel config construction uses `resolved` instead of `cfg.Chisel`:

```go
chisel.Run(ctx, chisel.Config{
    Server:         net.JoinHostPort(cfg.LabBridge.Host, strconv.Itoa(resolved.ServerInfo.ChiselListenPort)),
    User:           cfg.LabBridge.User,
    Pass:           cfg.LabBridge.Pass,
    RemotePort:     resolved.RemotePort,
    LocalPort:      localPort,
    ForwardTunnels: resolved.ServerInfo.ForwardTunnels,
})
```

The startup `slog.Info` block keeps the same keys; values come from `resolved` where applicable.

### `internal/chisel/client.go`

`Config` gains `ForwardTunnels []labbridge.ForwardTunnel`. `buildRemotes` becomes:

```go
func buildRemotes(cfg Config) []string {
    out := []string{fmt.Sprintf("R:%d:127.0.0.1:%d", cfg.RemotePort, cfg.LocalPort)}
    for _, t := range cfg.ForwardTunnels {
        out = append(out, fmt.Sprintf("%s:%s", t.Local, t.Remote))
    }
    return out
}
```

The hardcoded `"127.0.0.1:3100:loki:3100"` string and the `if cfg.User != ""` branch around it are deleted. Auth is still gated on `cfg.User != ""` (unchanged). If the server returns zero forward tunnels, no forwards are configured.

### `internal/logship/logship.go`

- Delete the `defaultPushURL` constant.
- `Init` no longer pre-populates `pushURL`; the field starts empty.
- Add `SetPushURL(string)` as an exported method (replaces the test-only `setPushURLForTest`; that test shim is removed).
- `StartShipper` checks for empty `pushURL`: if empty, log a warning and noop. Defensive against the worker calling them out of order.

## First-run credentials dialog

### Trigger logic

In `panel.Run`, replacing the current `ensureScaffold` call:

```go
state := readFirstRunState(cfgPath)
// state.Exists bool, state.ParseErr error, state.Cfg config.Config (Default() if missing/unparseable)
action := decideFirstRun(state)
switch action {
case ShowDialog:
    _ = runCredsDialog(cfgPath, state.Cfg)
    // Whether the user clicked Save or Cancel, fall through to the main panel.
    // On Cancel, the panel opens with empty creds and the existing
    // validation-warning label surfaces the missing-fields error.
case OpenPanel:
    // fall through
}
```

`decideFirstRun` (pure function):

| Condition                                                       | Action       |
|-----------------------------------------------------------------|--------------|
| Config file missing                                             | `ShowDialog` |
| Config file present, parses cleanly, `user` or `pass` blank     | `ShowDialog` |
| Config file present, parses cleanly, both `user` and `pass` set | `OpenPanel`  |
| Config file present, YAML parse error                           | `OpenPanel` — the existing validation-warning label surfaces the parse error; we do not silently overwrite a file we cannot understand. |

`writeOrPatchCreds` (see below) handles the `ShowDialog` paths by either writing a fresh scaffold (file missing) or patching the existing file (file present, blank fields).

### Dialog layout

Walk Dialog (windows-only, `_windows.go` file), modal to no parent (`mw` does not exist yet).

```
┌─ SerialHop — Set credentials ───────────────────┐
│                                                 │
│  Lab-bridge server is configured to             │
│  reach <host>. Enter your credentials:          │
│                                                 │
│  Username:  [______________________________]    │
│  Password:  [______________________________]    │   ← plain text, no mask
│                                                 │
│  <error/status line — red, hidden until use>    │
│                                                 │
│                 [ Cancel ]  [ Save ]            │
└─────────────────────────────────────────────────┘
```

- Both fields are plain `LineEdit`. Password is **not** masked, per user requirement.
- Status line is a `Label` with red text color, initially invisible.
- `Save` is the default button; `Enter` submits.

### Submit handler

1. Trim whitespace from both fields. Empty → status line "Username and password are required." No network call.
2. Disable `Save` and `Cancel`. Status line "Verifying…".
3. `labbridge.FetchClient(ctx5s, hc, "https://"+cfg.LabBridge.Host, user, pass, userAgent)`.
4. Branch on result:
   - **Success (200)** → call `writeOrPatchCreds(cfgPath, user, pass)`. On success, close dialog with `Accepted`. On write failure → modal error box ("Couldn't save config: <err>"), re-enable buttons, stay in dialog.
   - **`labbridge.ErrUnauthorized` (401)** → status line "Server rejected these credentials. Check the username and password." Re-enable buttons.
   - **`labbridge.ErrServerError` (5xx) or network error** → modal confirm: "Couldn't reach <host> to verify the credentials (<short reason>). Save anyway?" Yes → `writeOrPatchCreds` + close as success. No → re-enable buttons, return to the dialog.

### Cancel handler

Close the dialog with `Cancelled`. The main panel opens with empty creds; the existing validation-warning label surfaces "lab_bridge.user must be non-empty" (or similar). No automatic re-prompt. Operator must use `Open config file` to fix manually.

### Config write helper

`writeOrPatchCreds(path, user, pass)`:

- If the file does not exist: render the scaffold template (Section 1) with two literal substitutions — `user: ""` → `user: "<value>"`, `pass: ""` → `pass: "<value>"` — and write at 0600.
- If the file exists: read it, parse via `yaml.v3`'s Node API, find the `lab_bridge` mapping node, replace the `user` and `pass` scalar values, re-encode preserving comments and field ordering, write atomically (`*.tmp` + rename).
- If the file exists but is missing the `lab_bridge:` key: append a new `lab_bridge:` block at the end with the two values. This is an edge case we tolerate rather than fight.

### Panel display

The two existing config labels showing `Chisel server: host:port` and `Remote port: N` now show values resolved from server-info / clients lookup, not from config. Source for the panel: the `server-info.cache.json` file written by the service worker.

- Cache file exists → display the cached `chisel_listen_port` and `remote_port`.
- Cache file missing (e.g., service has never successfully bootstrapped) → display `…` for both.
- Cache file read errors → display `…`, no error popup (the lamps already surface the underlying issue).

The panel's existing 1 s `refresh` tick re-reads the cache file each tick. No `fsnotify`.

## Testing

### Cross-platform constraint

Per `CLAUDE.md`, tests must pass on macOS and Windows. The dialog itself lives in a `_windows.go` file and is excluded from non-Windows builds; all helper logic (decision functions, YAML patching, parsing, bootstrap) lives in cross-platform files.

### `internal/labbridge`

Extend the existing `httptest`-based tests:

- `FetchServerInfo` happy path with the exact body from the spec.
- `FetchServerInfo` with extra unknown keys (`agent`, `chisel.fingerprint`) — must succeed and ignore them.
- 500 wrapped as `ErrServerError`.
- Malformed JSON → plain error.
- Body > 64 KB → truncated read returns a parse error.
- Network failure (dialing a closed port) → plain error.
- Validation rejects: `chisel.listen_port = 0`, `chisel.listen_port = 70000`, missing/empty `loki.push_url`, `forward_tunnel.local = ""`.
- `forward_tunnels: null` and `forward_tunnels: []` both produce zero forwards.

### `internal/bootstrap`

- Both live calls succeed → cache file written with correct schema (including `user`); returned `Resolved` matches.
- Live `server-info` fails (5xx or network), cache valid and `user` matches → returns cached values; warning logged; cache file unchanged.
- Live `clients/{user}` fails 5xx or network, cache valid and `user` matches → returns cached values; warning logged.
- Live `clients/{user}` fails 401 → cache is **not** consulted; the retry loop continues. (401 is a hard signal that the configured credentials are wrong; falling back to cache would mask the problem and start chisel with a password that will then fail server-side anyway.)
- Cache file exists but `cache.user != cfg.LabBridge.User` → cache treated as missing; retry loop runs as if first-time bootstrap.
- Live fails, no usable cache → retries with `httptest.Server` that flips to 200 after N calls; verifies backoff happens (counter or fake clock).
- Live fails, no usable cache, ctx cancelled → returns `ctx.Err()`.
- Cache file corrupt (bad JSON) → deleted, treated as missing.
- Cache file version mismatch → deleted, treated as missing.

### `internal/chisel`

- `buildRemotes` with zero forward tunnels → only `R:<remote>:127.0.0.1:<local>`.
- `buildRemotes` with N forward tunnels → reverse route + N forwards in order.

### `internal/logship`

- `StartShipper` with empty `pushURL` is a noop and logs a warning.
- `SetPushURL` then `StartShipper` produces a running shipper (existing test pattern, just renamed from `setPushURLForTest`).

### `internal/config`

- `Validate` rejects empty `user`.
- `Validate` rejects empty `pass`.
- Scaffold template snapshot test against `testdata/scaffold.golden.yaml` — a regression gate on template edits.
- `LoadPartial` returns sensible defaults when `user`/`pass` blank.

### `internal/panel`

- `decideFirstRun` table-test covering the four cases in the trigger-logic table.
- `verifyAndSave(host, user, pass)` (the dialog's submit logic extracted to a cross-platform file) table-tested against a fake `labbridge` client:
  - 200 → action = save.
  - 401 → action = inline-error.
  - 500 → action = needs-confirm.
  - Network error → action = needs-confirm.
- `patchCredentials(yamlBytes, user, pass) []byte` — table tests for:
  - File with comments and unrelated fields → only the two values change; comments and ordering preserved.
  - File missing `lab_bridge:` block → block appended at end.
  - File with `lab_bridge` present but `user`/`pass` absent → new keys added under `lab_bridge`.
  - 0600 file permissions are set when writing.
- The dialog widget itself (`credsdialog_windows.go`) has no automated test; it is thin glue around the tested helpers.

## Out of scope (explicit non-goals)

- Reconnect-triggered server-info refresh. The chisel client keeps `MaxRetryCount: -1` and its built-in exponential backoff; we do not own the reconnect lifecycle. (Decision: server-info-shape changes are rare enough that a service restart is acceptable.)
- Hardcoded last-resort defaults for `chisel.listen_port`, `loki.push_url`, or forward tunnels. The disk cache is the only resilience mechanism.
- Per-field overrides in config for server-driven values. If you need a different setup, change `lab_bridge.host`.
- Hot-reload of the cache file via `fsnotify`. Existing 1 s panel tick is enough.
- The health endpoint (`/api/public/health`) and tunnel-lamp probe (`/api/public/clients/{user}`'s `connected` field). Already wired by the status-lamps work; unchanged.
- The CLI `--foreground` developer mode. Keeps its existing scaffold-and-quit on missing config. No dialog there.
- Auto-update, SCM control protocol, elevated subprocess, REST API surface. Unchanged.

## Files touched (summary)

| Path                                                | Change                                                              |
|-----------------------------------------------------|---------------------------------------------------------------------|
| `internal/config/config.go`                         | Drop `ChiselConfig`; require user/pass; new scaffold comments.      |
| `internal/config/load.go`                           | Validate user/pass non-empty; drop chisel validation.               |
| `internal/config/config_test.go`, `load_test.go`    | New cases + scaffold golden.                                        |
| `internal/labbridge/serverinfo.go` (new)            | `FetchServerInfo`, `ServerInfo`, `ForwardTunnel`.                   |
| `internal/labbridge/serverinfo_test.go` (new)       | Table tests for the new endpoint.                                   |
| `internal/bootstrap/bootstrap.go` (new)             | `Resolve`, cache read/write, retry loop.                            |
| `internal/bootstrap/bootstrap_test.go` (new)        | All cache + retry scenarios.                                        |
| `internal/paths/paths.go`                           | `ServerInfoCachePath()`.                                            |
| `internal/winsvc/worker.go`                         | Inject bootstrap step; pass `Resolved` to `app.Run`.                |
| `internal/app/app.go`                               | New `Resolved` parameter; use it for chisel config.                 |
| `internal/chisel/client.go`                         | `ForwardTunnels` field; data-driven `buildRemotes`.                 |
| `internal/chisel/client_test.go`                    | New cases for forward-tunnels list.                                 |
| `internal/logship/logship.go`                       | Drop `defaultPushURL`; add `SetPushURL`; guard `StartShipper`.      |
| `internal/logship/logship_test.go`                  | Rename `setPushURLForTest` callers; new noop-on-empty case.         |
| `internal/panel/panel.go`                           | First-run gate; cache-file display; drop scaffold-from-default.     |
| `internal/panel/credsdialog_windows.go` (new)       | Walk dialog.                                                        |
| `internal/panel/firstrun.go` (new, cross-platform)  | `decideFirstRun`, `verifyAndSave`, `patchCredentials`.              |
| `internal/panel/firstrun_test.go` (new)             | All helper tests.                                                   |
| `README.md`                                         | Update first-launch instructions to mention the dialog.             |
