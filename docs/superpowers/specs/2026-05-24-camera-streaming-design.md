# Camera streaming — design

**Date:** 2026-05-24
**Status:** brainstorming complete; pending spec review before plan
**Target platform:** Windows (panel + service); macOS/Linux for development of the
panel UI via fakes
**Related:**
- `docs/2026-05-24-serialhop-streaming-protocol.md` — wire-level protocol contract
  (already implemented on lab-bridge side)

## 1. Purpose

Let a remote researcher watch a lab experiment live by attaching cameras
already connected to the lab machine. The operator picks which cameras are
allowed to stream from a new **Cameras** tab in the panel; lab-bridge then
drives publication on demand using the SerialHop streaming protocol.

Out of scope for v1 (called out so future contributors know they're future
work, not omissions):

- Audio.
- Live preview thumbnails in the UI.
- Per-camera resolution/bitrate overrides.
- Screen-capture or non-DirectShow sources.
- Unattended streaming with the panel closed.
- MediaFoundation fallback for cameras DirectShow can't see.

## 2. High-level architecture

```
Lab-bridge ── chisel tunnel ── Service (existing REST on chisel-tunnel port)
                                  │  /api/translations
                                  │  /api/translations/{id}/start
                                  │  /api/translations/{id}/stop
                                  │      (reverse-proxy; no camera knowledge)
                                  ▼
                              Panel (Wails)  ── owns enumeration + state + ffmpeg
                                  │
                              ffmpeg child  ── direct UDP/HTTPS WHIP ── lab-bridge
```

Two roles, both inside the existing `serialhop.exe`:

- **Service** (Windows service / `--foreground`): owns the chisel-tunnel REST
  listener. Gains three new handlers under `/api/translations*` that are
  **stateless HTTP proxies** to the panel. No camera knowledge.
- **Panel** (Wails app in the operator's user session): owns camera
  enumeration, the armed-cameras list, the `session_id ↔ camera` mapping,
  and all ffmpeg child processes.

The service is the chisel-tunnel ingress because the protocol pins the
control plane to that port. The panel is the streaming worker because
DirectShow / MediaFoundation camera access is unreliable from a LocalSystem
Session 0 process; the panel already runs in the operator's interactive
session with full device-access rights.

**Failure mode when the panel is closed.** `panel_endpoint.json` is missing
or its HTTP probe fails, so the service responds:

- `GET /api/translations` → `200 {"translations":[]}` (lab card appears
  inactive on the viewer picker; per spec §1.1).
- `POST /api/translations/{id}/start` → `503 {"error":"panel not running"}`.
- `POST /api/translations/{id}/stop` → `204` (idempotent — nothing to stop).

This matches the protocol's documented behavior for the no-translations
case, so lab-bridge needs no special handling.

## 3. Package layout

```
internal/streamer/                  # NEW — panel-side; all WHIP session logic
  enumerator.go                     # Enumerator interface + Camera type
  enumerator_windows.go             # ffmpeg -list_devices parser
  enumerator_other.go               # fake (returns nothing real)
  session.go                        # one WHIP session ↔ one ffmpeg child
  manager.go                        # armed list + active sessions
  store.go                          # persist armed_cameras.json
  ffmpeg.go                         # argv builder + exec wrapper
  ffmpeg_build.go                   # pinned ffmpeg version + SHA256
  testbin/                          # stub ffmpeg used by session tests

internal/panel/streaming_bindings.go    # Wails bindings: ListCameras / SetArmed / GetStreamingState
internal/panel/streaming_http.go        # localhost HTTP listener for service-proxied calls

internal/api/translations.go        # service-side proxy handlers
internal/bootstrap/                 # add WritePanelEndpoint / ReadPanelEndpoint helpers
internal/paths/                     # add FFmpeg() resolver

internal/panel/frontend/src/tabs/CamerasTab.tsx
internal/panel/frontend/src/tabs/CamerasTab.test.tsx
internal/panel/frontend/src/components/TabBar.tsx   # add "cameras" id
internal/panel/frontend/src/App.tsx                  # mount tab
```

Cross-platform requirement (per `CLAUDE.md`): every Windows-only file has a
sibling `_other.go` fake so tests compile and run on macOS/Linux.

## 4. Persistence (two new files alongside the bootstrap cache)

### 4.1 `armed_cameras.json` — owned by panel

```json
{
  "version": 1,
  "cameras": [
    {
      "id": "@device:pnp:\\\\?\\usb#vid_046d&pid_0825#abcdef#{65e8773d-...}\\global",
      "label": "Logitech C270"
    }
  ]
}
```

- Written atomically (write-to-temp + rename).
- Reread on panel startup.
- Cameras whose `id` is no longer present in the live enumeration are kept
  in the file but flagged as "disconnected" in the UI; they do **not**
  appear in `GET /api/translations` until the device returns.

### 4.2 `panel_endpoint.json` — owned by panel

```json
{
  "version": 1,
  "host": "127.0.0.1",
  "port": 49217,
  "pid": 12345,
  "started_at": "2026-05-24T13:45:00Z"
}
```

- Written on panel startup once the listener is bound.
- Deleted on graceful exit (best-effort).
- Service detects stale entries via the HTTP probe failing; no need to
  trust the file alone.

Both files live in the same directory as the existing bootstrap cache,
resolved by `paths.ServerInfoCachePath()`'s parent.

## 5. Camera identity

- `id` = the DirectShow "Alternative name" reported by
  `ffmpeg -list_devices`. This is the Windows device instance path
  (`@device:pnp:\\?\usb#vid_xxxx&pid_yyyy#serial#{guid}\global`) and is
  stable across reboots and replugs of the same physical device into the
  same USB port.
- `label` = the friendly DirectShow name. If multiple cameras report the
  same friendly name (two of the same model), the panel appends
  ` #2`, ` #3`, … in enumeration order for display only — the `id` stays
  the disambiguator on the wire.

The protocol's server-wide key is `(chisel_username, id)`, so as long as
`id` is stable per device the viewer side reconnects cleanly.

## 6. Lifecycle

### 6.1 Arming (operator-driven, inside the panel)

1. Operator opens the **Cameras** tab.
2. Panel calls Wails binding `ListCameras()`, which delegates to the
   platform `Enumerator`. On Windows that runs
   `ffmpeg -list_devices true -f dshow -i dummy` and parses stderr.
3. The tab renders one card per camera: friendly name, id chip,
   **Allow streaming** toggle, state badge.
4. Toggle ON → panel atomically rewrites `armed_cameras.json` and emits a
   Wails event `streaming:armed-changed` so any other open tab re-fetches.
5. Toggle OFF → if a session is currently publishing for that id, it is
   torn down (see 6.4) before the file is rewritten.

The toggle is live (no Save button), consistent with the Status tab's
keep-awake control.

### 6.2 Stream start (lab-bridge-driven)

1. Lab-bridge → service: `POST /api/translations/{id}/start` with body
   `{ session_id, whip_url, whip_token, ice_servers }`.
2. Service reads `panel_endpoint.json`, builds a request to
   `http://127.0.0.1:<panel_port>/api/translations/{id}/start`, copies
   the body, awaits the response, and returns the panel's status code +
   body verbatim. Timeout: 5 s.
3. Panel's `streaming_http` handler dispatches to `streamer.Manager`:
   - **404** if `{id}` is not in the armed list (or is armed but the
     device is currently disconnected).
   - **202** with empty body if `{id}` is already publishing under the
     *same* `session_id` (idempotent retry; protocol §1.2).
   - **Replace-on-conflict**: if `{id}` is publishing under a *different*
     `session_id`, the manager tears down the old session (see "graceful
     termination" below) and starts the new one. Returns 202.
   - **503** with `{"error":"ffmpeg unavailable"}` if `paths.FFmpeg()`
     fails its `ffmpeg -version` probe.
   - Otherwise: spawn ffmpeg, register the session in the manager, return
     202.
4. Spawn command (one invocation per session):

   ```
   ffmpeg
     -hide_banner -loglevel error
     -f dshow -rtbufsize 256M
     -framerate 24 -video_size 1280x720
     -i video="<friendly>"
     -c:v libx264 -preset veryfast -tune zerolatency
     -profile:v baseline -level 3.1 -pix_fmt yuv420p
     -b:v 1500k -maxrate 1500k -bufsize 3000k
     -g 48 -keyint_min 48
     -metadata serialhop_session=<sid>
     -f whip <bearer-flag>=<whip_token>
     "<whip_url>"
   ```

   The exact ffmpeg flag for the WHIP bearer token (`-authorization` vs
   `-bearer_token`) depends on the pinned ffmpeg build's WHIP muxer
   options. The implementation plan pins this against the SHA256'd
   binary; the spec deliberately leaves it as `<bearer-flag>` to avoid
   locking in a value that depends on a build we haven't picked yet.

   The `-metadata serialhop_session=<sid>` is the orphan-recovery marker
   (see 6.5).

5. Panel watches the child. ffmpeg's WHIP output handles SDP + ICE +
   media. The 10-second-from-202 deadline (protocol §1.2) and
   5-seconds-from-201 first-frame deadline (§2.6) are met by ffmpeg's
   default behavior; we do not need a separate timer.

### 6.3 Stream stop (lab-bridge-driven)

1. Lab-bridge → service: `POST /api/translations/{id}/stop` with
   `{ session_id }`.
2. Service proxies to panel.
3. Manager:
   - Match → graceful termination (see below), drop session, return 204.
   - Mismatch (active session has a different `session_id` than the body)
     → return **409** with `{"active_session_id":"<current>"}`. The
     active session is **not** touched (protocol §1.3 stale-stop guard).
   - Unknown id / no active session → 204 (idempotent).

**Graceful termination of an ffmpeg child** (used by replace-on-conflict,
stop, and unarm-while-live):

- **Windows:** ffmpeg is spawned in a new process group (via
  `CREATE_NEW_PROCESS_GROUP` in `SysProcAttr`). On termination, send
  `CTRL_BREAK_EVENT` via `GenerateConsoleCtrlEvent`. ffmpeg responds by
  finishing its current WHIP DELETE and exiting cleanly. If the child
  hasn't exited within 2 s, fall back to `taskkill /pid <pid> /T /F`.
- **macOS / Linux** (developer hosts only — never in production):
  `os.Process.Signal(syscall.SIGTERM)`; 2 s grace; `syscall.SIGKILL`.

The graceful-then-force pattern keeps the WHIP DELETE happy path
(protocol §2.4) while guaranteeing we don't leak processes.

### 6.4 Operator unarms while publishing

Same internal path as stop: graceful termination of the ffmpeg child,
drop the session, then rewrite `armed_cameras.json` without that camera.
The lab-bridge side sees the publisher disappear via ICE failure on the
media plane and tears down subscribers; no out-of-band notification is
needed (protocol §3 "operator disarms while publishing").

### 6.5 Crash recovery

When the panel starts up:

1. Read `armed_cameras.json` → populate the armed list.
2. Walk the live process list. For every `ffmpeg.exe` whose command line
   contains `-metadata serialhop_session=`, kill it with `taskkill /T /F`.
   This drops orphans from a previous panel instance that didn't clean up.
3. Bind the localhost listener, write `panel_endpoint.json`.

The `-metadata` marker is the only way we know a given ffmpeg is ours
without a parent-pid match (the parent pid is stale after our crash).

### 6.6 Panel shutdown

- Graceful: terminate all active ffmpeg children (per 6.3), delete
  `panel_endpoint.json`, then exit Wails.
- Hard crash: orphans remain; the next panel start cleans them up (6.5).
- Service shutdown is independent. Service exit while panel is up does
  not interrupt active media plane streams — they continue flowing
  directly to lab-bridge until the WHIP session ends. New `start` calls
  become impossible until the service is back (chisel tunnel down). No
  special handling on our side; lab-bridge's existing behavior covers it.

## 7. Defaults (no per-camera config in v1)

Constants in `internal/streamer/defaults.go`:

| Setting | Value |
|---|---|
| Resolution | 1280×720 |
| Framerate | 24 fps |
| Bitrate target | 1500 kbps |
| Codec | H.264 Constrained Baseline (profile 42e01f) |
| Keyframe interval | 48 frames (~2 s @ 24 fps) |
| Service→panel proxy timeout | 5 s |
| Ffmpeg version probe TTL | once per process |

These are good enough for "watch the experiment" usage. Per-camera
overrides are a v2 problem.

## 8. Failure handling

| Failure | Surface | Response |
|---|---|---|
| `ffmpeg.exe` missing or wrong version | Red banner on Cameras tab; service→503 with `{"error":"ffmpeg unavailable"}` on start | Operator runs the installer to repair |
| Camera disconnected after arming | Card shows "Disconnected" badge; excluded from `GET /api/translations` | Operator reconnects; reappears |
| Camera in use by another process | ffmpeg exits within ~1 s; manager records last stderr line on card and drops session | Operator closes the other app and retries (lab-bridge will retry on next viewer) |
| Panel HTTP listener bound but unresponsive | Service treats as panel-down: empty list / 503 start / 204 stop | Panel restart |
| WHIP POST returns 401/410/404 | ffmpeg exits non-zero; manager records error, drops session | Lab-bridge issues a fresh `start` on next viewer (per protocol §2.3) |
| WHIP 5xx | ffmpeg exits non-zero; manager records error | Same as above |
| Disk full / IO error on `armed_cameras.json` write | Toggle change rejected; toast in UI; previous state preserved | Operator frees disk |

All failures funnel through the manager → Wails event
`streaming:state-changed` → tab re-render.

## 9. UI

### 9.1 Tab placement

New tab **Cameras** inserted between Ports and Logs.

Final order: `Status | Config | Devices | Ports | Cameras | Logs`.

Implementation touches:
- `internal/panel/frontend/src/components/TabBar.tsx` — add the id.
- `internal/panel/frontend/src/App.tsx` — mount `<CamerasTab/>` under the
  existing `ErrorBoundary scope="tab:cameras"` pattern.

### 9.2 Layout

- **Header row:** "Cameras" title, refresh button (re-enumerates), and a
  small count `"<armed>/<total> armed"`.
- **Empty state** (no devices found): `"No cameras detected. Connect a
  camera or check whether another application is using it."`
- **Card per camera:**
  - Friendly name (large), id chip (small, monospaced, truncated).
  - **Allow streaming** toggle (live, no Save button).
  - State badge: `idle` (armed, no viewer), `live` (publishing now),
    `disconnected` (armed but device gone), `error` (last attempt
    failed; click to view stderr line; X dismisses).
  - On `live`: small "Stop" link that triggers an operator-initiated
    stop without unarming.

### 9.3 Live state updates

Panel emits a Wails event `streaming:state` whenever:

- A session is started, replaced, stopped, or errored.
- A camera's armed bit flips.
- An enumeration discovers / loses a device.

The tab subscribes via the existing `wailsEvents.ts` helper and
re-renders. No polling.

### 9.4 Privacy disclosure

When the operator flips a camera from disarmed → armed, the card briefly
shows a yellow "Now allowing remote viewers" note (auto-dismisses after
5 s). No modal, no second-step confirmation. Rationale: the toggle is
explicit, the label `Allow streaming` is explicit, and the existing
panel UX favors live controls over modals.

## 10. Bundling ffmpeg

- The installer (`tools/installer`) copies a pinned `ffmpeg.exe` build
  next to `serialhop.exe` in the install dir.
- New helper `paths.FFmpeg()` returns the absolute path
  (`filepath.Join(paths.InstallDir(), "ffmpeg.exe")`).
- Pinned build identity (vendor, version, build flags, SHA256) lives in
  `internal/streamer/ffmpeg_build.go`. The panel verifies once per
  process by running `ffmpeg -version` and checking the version string.
- Choice of build: **gyan.dev "essentials"** (~80 MB unpacked) — small
  footprint, ships H.264/VP8 and the dshow input + whip muxer. Pin to a
  specific release tag with SHA256.
- Installer size goes from ~25 MB to ~105 MB. Documented in the release
  notes.

If the SHA256 check fails or the binary is missing, the Cameras tab
shows a single red banner ("ffmpeg.exe missing or modified; reinstall
SerialHop") and the service returns 503 on all `start` calls. Existing
device/serial functionality is unaffected.

## 11. Testing strategy

All tests must pass on macOS + Windows (per `CLAUDE.md`).

### 11.1 Enumerator

- `enumerator_other.go` is a fake that returns a static 1-camera list.
  Used in macOS/Linux test runs and in `wails dev` on a developer Mac.
- Windows code is tested by injecting a fake `exec` function and feeding
  it a captured `ffmpeg -list_devices` stderr blob.
- Parser handles: empty list, single camera, multiple cameras, the
  `Alternative name` line appearing on a separate line (the dshow output
  format).

### 11.2 Manager

Pure-Go logic tests. No subprocess. Cases:
- Arm → start → stop → unarm.
- Idempotent start: same session_id → 202 no-op.
- Replace-on-conflict: different session_id → old killed, new running.
- 409 stale-stop guard: stop with a non-active session_id → 409, active
  preserved.
- Operator unarm while live → session killed, file rewritten.
- 404 on unarmed id, 503 when ffmpeg unavailable.

### 11.3 Session

Integration test against `internal/streamer/testbin/fake_ffmpeg`, a tiny
Go binary that:
- Prints `whip published` to stderr.
- Sleeps until SIGTERM.
- Exits 0 on SIGTERM, exits 137 on SIGKILL.

Cases:
- Spawn → stderr captured → SIGTERM → clean exit.
- Spawn → SIGTERM ignored → SIGKILL after 2 s grace.
- Spawn → child exits 1 quickly → last stderr line surfaced as error.

### 11.4 Service proxy

`httptest.Server` stands in for the panel. Cases:
- GET: panel returns translations array → service passes through.
- GET: panel-endpoint file missing → service returns
  `{"translations":[]}`.
- GET: file present but HTTP probe fails → same.
- POST start: panel returns 202 → service returns 202 with same body.
- POST start: panel-endpoint missing → service returns 503.
- POST stop: panel-endpoint missing → service returns 204.

### 11.5 Panel HTTP listener

Standard `net/http` handler tests. Cases:
- Path routing (only the three documented endpoints; everything else 404).
- Method enforcement (only the documented method per path; everything
  else 405).
- Body decoding errors → 400.

### 11.6 Frontend

`CamerasTab.test.tsx` against the existing Vitest setup. Cases:
- Renders empty state when no cameras.
- Renders one card per camera with correct badges.
- Toggling fires the Wails binding and updates badge optimistically.
- Receives `streaming:state` event and re-renders.

## 12. Trust model

- **Control plane** (service `/api/translations*` on the chisel port):
  unauthenticated; the chisel tunnel is the auth boundary, identical to
  the existing devices REST endpoints. The service-to-panel hop on
  127.0.0.1 is implicitly trusted (loopback).
- **WHIP** outbound: `Authorization: Bearer <whip_token>` per protocol;
  ffmpeg handles this. The token is one-shot.
- **Logging**: `whip_token` is **never logged** — neither in the panel
  Wails bindings nor in the service proxy. Test that the token doesn't
  appear in panel logs even at debug level.

## 13. Migration / compatibility

- Adds new files; touches no existing serialized formats.
- Bumps the installer's payload by ~80 MB (ffmpeg.exe). Release notes
  call this out.
- Existing config (`lab_devices_client_config.yaml`) is untouched.
- Service `/api/translations*` endpoints are additive; old SerialHop
  installs already return 404, which lab-bridge interprets as "no
  translations" (protocol §6) — so a partial rollout (newer panel,
  older service) is a non-event for the viewer side.

## 14. Versioning

This is **protocol v1** on the SerialHop side. Per protocol §6:

- We tolerate unknown fields in `start` request bodies.
- We respond 404 to any path not in our handler set.

No `X-Lab-Bridge-Protocol-Version` header is emitted in v1; we fall back
to v1 if one ever arrives.

## 15. Conformance checklist (mirrors protocol §7)

- [ ] `GET /api/translations` returns 200 with `{"translations":[]}` when
      no cameras armed; otherwise the documented array shape.
- [ ] `POST .../start` returns 202 for valid armed translations, 404 for
      unknown id, 503 for hardware unavailable.
- [ ] Same `session_id` on `start` → 202 with empty body (idempotent).
- [ ] Different `session_id` while publishing → replace, return 202.
- [ ] WHIP publish begins within 10 s of `start` 202 (ffmpeg default).
- [ ] First video frame within 5 s of WHIP 201 (ffmpeg default).
- [ ] `POST .../stop` returns 204 for matching session_id, 409 with
      `active_session_id` for mismatched.
- [ ] On 204, ffmpeg terminated and camera released.
- [ ] Operator disarm proactively tears down + DELETEs WHIP (ffmpeg
      handles DELETE on its side at clean exit).
- [ ] On panel restart, all in-flight `session_id` state is dropped;
      subsequent stops for unknown sessions return 204.
