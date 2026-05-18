# Agent Info Endpoint — Design

**Date:** 2026-05-18
**Status:** Draft (brainstorming complete; pending spec review before plan)

## 1. Purpose & scope

A new endpoint `GET /agent/info` on the agent's existing REST API that lets the lab-bridge server learn what version of SerialHop each connected client is running, plus a small set of host facts. The server polls this endpoint on whatever cadence it chooses; the client is purely reactive.

The four motivating goals (all in scope on the *protocol* level — the *server-side* mechanisms that satisfy them are out of scope for this spec):

- **Observability.** Admin can see, in one place, what version each connected client is running.
- **Update nudging.** Server can detect outdated clients and signal them via the existing `agent.version` field in the `FetchClient` response (already used by the auto-updater).
- **Support / debugging.** When a user reports an issue, the server-side log of pulled `/agent/info` payloads (or the User-Agent on bootstrap calls) makes it trivial to look up their version, OS, arch, and machine identity.
- **Compatibility gating.** Server continues to gate at `FetchClient` time using the existing `User-Agent` header (`SerialHop/<version> (<role>)`) — `/agent/info` is for ongoing observation, not handshake-time decisions.

Out of scope (deliberately YAGNI):

- A client-initiated heartbeat. The pull model is cleaner: the agent doesn't schedule, retry, or back off.
- Authentication on the new handler beyond what the rest of the API already inherits from the Chisel reverse tunnel.
- A second reverse-tunneled port. The endpoint sits on the existing API mux, behind the existing tunnel.
- Server-side schema, storage, polling cadence, dashboards, or "min version" enforcement policy. Those live in the lab-bridge repo.
- `machine_id` on non-Windows hosts. Dev builds (macOS/Linux) omit the field; this is fine because the production fleet is Windows.
- Including the panel-process version in the payload. The panel's version is already visible to the server in the `User-Agent` header of the panel's own HTTP calls (`SerialHop/<version> (panel)`).

## 2. Architecture

The REST API at `internal/api/handlers.go` is hosted by the long-running agent process (`internal/app/app.go::Run`), which on Windows is the service worker. It already binds to `127.0.0.1:<local-port>` and is reverse-tunneled to the lab-bridge server via `R:<RemotePort>:127.0.0.1:<LocalPort>` (see `internal/chisel/client.go:28-34`). The lab-bridge server therefore reaches the endpoint as `http://<chisel-server-host>:<RemotePort>/agent/info`, with no new tunnel, no new port, and no `ClientInfo` schema change.

A new internal package `internal/agentinfo` owns the data-gathering logic. The handler in the `api` package is a one-line wire-up to `agentinfo.Snapshot()` — this keeps `api` thin and lets `agentinfo` be unit-tested in isolation, including its platform-specific `machine_id` branch.

Failure domains stay independent of any existing component. If `agentinfo.Snapshot()` fails to gather a field (e.g., the Windows registry read errors out), the handler still returns `200 OK` with the affected field empty / omitted. The endpoint never fails — partial data is more useful than a 500 for an observability channel.

## 3. Wire contract

### 3.1 Request

```
GET /agent/info HTTP/1.1
Host: <chisel-server-host>:<RemotePort>
```

No request body, no required headers beyond what Chisel adds. The server may include arbitrary `User-Agent` / correlation headers; the handler ignores them.

### 3.2 Response

```
HTTP/1.1 200 OK
Content-Type: application/json
Cache-Control: no-store
```

Body (example, Windows production build):

```json
{
  "version": "0.27.1+abc1234",
  "build_sha": "abc1234",
  "os": "windows",
  "arch": "amd64",
  "hostname": "LAB-PC-07",
  "machine_id": "5e2f9b3a-1f0c-4a82-9d9c-2e4f0e1a3b6d",
  "uptime_seconds": 12345
}
```

Body (example, macOS dev build with no machine ID and no git-describe suffix):

```json
{
  "version": "dev",
  "os": "darwin",
  "arch": "arm64",
  "hostname": "khamitov-mbp",
  "uptime_seconds": 42
}
```

Field semantics:

| Field | Type | Required | Source |
|---|---|---|---|
| `version` | string | yes | `internalversion.Base()` (already baked in via `-ldflags -X` at build time; see `internal/version/version.go:7` and `tools/buildcmd/main.go:74-76`). |
| `build_sha` | string | no | The segment after `+` in `version` (the `git describe` suffix). Omitted when empty. |
| `os` | string | yes | `runtime.GOOS`. |
| `arch` | string | yes | `runtime.GOARCH`. |
| `hostname` | string | yes (but may be empty) | `os.Hostname()`. Empty string on error — do not fail the handler. |
| `machine_id` | string | no | Windows: `HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid`. Omitted (`omitempty`) on non-Windows or on registry read failure. |
| `uptime_seconds` | integer | yes | `int(time.Since(startedAt).Seconds())` where `startedAt` is captured once at package init. |

The server MUST tolerate unknown fields in future versions (forward compatibility). The client MAY add optional fields without bumping a protocol version.

### 3.3 Error responses

The endpoint is best-effort and aims to never return non-2xx. The handler catches panics, returns `200` with partial data, and logs the failure server-side. The only realistic non-2xx is `405 Method Not Allowed` for non-GET requests, which Go's `ServeMux` already produces from the `GET /agent/info` pattern.

## 4. Implementation

### 4.1 New package: `internal/agentinfo`

```
internal/agentinfo/
  agentinfo.go           # Info struct + Snapshot()
  agentinfo_test.go      # cross-platform tests
  machineid_windows.go   # //go:build windows
  machineid_other.go     # //go:build !windows
  machineid_windows_test.go  # //go:build windows
```

**`agentinfo.go`:**

- Exported `type Info struct { ... }` with JSON tags matching § 3.2.
- Unexported package-level `startedAt = time.Now()` initialized at package load. (Acceptable approximation of "process start" — the agent imports `agentinfo` from `cmd/serialhop` before any meaningful work.)
- Exported `func Snapshot() Info` that returns a fully populated struct. Each field is gathered independently; a failure in one (e.g., `os.Hostname` returning an error) sets that field to its zero value and continues.
- No goroutines, no I/O beyond the cheap calls listed above.

**`machineid_windows.go`:**

- Reads `HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid` via `golang.org/x/sys/windows/registry`.
- Returns `(string, error)`. Errors are logged at warn level in the caller and the field is omitted.

**`machineid_other.go`:**

- Returns `("", nil)`. Compiles on all non-Windows platforms so CLAUDE.md's cross-platform rule holds.

### 4.2 Handler wiring (`internal/api`)

One new line in `handlers.go:49`-block:

```go
mux.HandleFunc("GET /agent/info", s.handleGetAgentInfo)
```

New handler appended to `internal/api/handlers.go` (matching the existing pattern — all 8 current handlers live in that file):

```go
func (s *Server) handleGetAgentInfo(w http.ResponseWriter, _ *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("Cache-Control", "no-store")
    _ = json.NewEncoder(w).Encode(agentinfo.Snapshot())
}
```

The handler takes no inputs from `Server` state. It exists as a method only to match the surrounding pattern; if that feels off in review, it can become a plain function and the route registration can call it directly.

### 4.3 Dependencies

- `golang.org/x/sys/windows/registry` — verify whether already pulled in transitively before adding. If not, `go get` it in the plan phase.

## 5. Security & trust boundary

The endpoint inherits the existing API's threat model. The local listener binds to `127.0.0.1`, and the only path in from outside is the Chisel reverse tunnel, whose remote port is allocated per-user by lab-bridge and protected by the Chisel session's HTTP Basic Auth.

No additional auth on `/agent/info`. Every other route on this mux (`GET /devices`, `POST /discover`, `POST /serial/ports/{port}/command`, etc.) is similarly unauthenticated at the application layer — the trust boundary is the tunnel. Adding token auth on `/agent/info` alone would be inconsistent and would create a false sense of relative sensitivity for a strictly-less-sensitive endpoint (it reads no user data, exposes no device control).

`machine_id` is the most identifying field. The Windows MachineGuid is already known to numerous Microsoft and third-party telemetry channels on the same machine and is regenerated on a clean OS reinstall, so its disclosure to the lab-bridge server (which already authenticates and routes that user's traffic) is consistent with the existing trust model.

## 6. Testing

All tests run on every CI runner per CLAUDE.md's "cross-platform" rule.

**`internal/agentinfo/agentinfo_test.go` (all platforms):**

- `TestSnapshot_PopulatesRequiredFields` — `Snapshot()` returns non-empty `version`, `os`, `arch`. `os` matches `runtime.GOOS`; `arch` matches `runtime.GOARCH`.
- `TestSnapshot_UptimeMonotonic` — call `Snapshot()` twice, sleep between, assert the second `uptime_seconds >=` the first.
- `TestSnapshot_BuildSHAFromVersion` — temporarily set `internalversion.Version = "0.27.1+abc1234"`, assert `build_sha == "abc1234"`. With `Version = "dev"`, assert `build_sha == ""`.
- `TestInfoJSON_OmitsMachineIDWhenEmpty` — construct an `Info{}` with `MachineID == ""` directly, `json.Marshal`, assert the resulting object has no `machine_id` key. Platform-independent (does not rely on what `Snapshot()` returns under different OSes).
- `TestInfoJSON_OmitsBuildSHAWhenEmpty` — same shape for `build_sha`.

**`internal/api/handlers_test.go` (all platforms — match wherever existing handler tests live; if there is no such file yet, create `internal/api/handlers_test.go`):**

- `TestHandleGetAgentInfo_200JSON` — `httptest.NewRequest("GET", "/agent/info", nil)` + `httptest.NewRecorder`; assert status 200, `Content-Type: application/json`, body unmarshals into a struct matching § 3.2, required fields present.
- `TestHandleGetAgentInfo_RejectsNonGET` — POST/PUT/DELETE return 405 (this comes for free from `mux.HandleFunc("GET /agent/info", ...)` but pin the behavior).

**`internal/agentinfo/machineid_windows_test.go` (`//go:build windows`):**

- `TestReadMachineGuid_NonEmpty` — read the live registry value, assert non-empty and looks like a GUID (regex). Use `t.Skip` if the registry read errors (CI hardening — Wine, locked-down runners).

## 7. What is explicitly NOT changing

- `internal/labbridge/client.go` — `FetchClient`/`FetchHealth`/`FetchServerInfo` unchanged. The `agent.version` response field already exists and continues to ride that channel for update-nudge.
- `internal/chisel/client.go` — `buildRemotes` unchanged. One reverse route, same as today.
- `assets/version.json`, `release-please-config.json`, `tools/buildcmd/main.go` — version-injection plumbing unchanged. We consume `internalversion.Base()` only.
- `internal/winsvc/*` — service worker startup unchanged. The new HTTP route is attached to the existing mux that `app.Run` already serves.
- The `User-Agent` header on bootstrap calls. It remains the channel for compat-gating at `FetchClient` time.

## 8. Open questions for the lab-bridge (server) repo

Listed here for cross-referencing only; they are NOT in scope for the client-side spec.

- **Polling cadence.** Server-side decision. A first cut of 5 minutes seems reasonable; tighter when actively triaging a fleet.
- **Storage model.** "Last-known info per user" is enough for the goals above; a time series is overkill until there's a need.
- **Surfacing stale clients.** The existing `agent.version` field on the `FetchClient` response is the nudge channel; the server populates it based on observed-version vs. recommended-version policy.
- **Compat-gating policy.** Server reads `User-Agent` on `FetchClient`, parses `SerialHop/<version>`, and applies whatever min-version rules exist. Returning 401 / 403 / 426 is a server-side decision.
