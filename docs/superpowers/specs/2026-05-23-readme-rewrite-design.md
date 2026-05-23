# README rewrite — design

**Status:** Approved structure; awaiting spec review.
**Author:** khamitovdr
**Date:** 2026-05-23

## Goal

Replace the current `README.md` with a version that achieves five things:

1. Lets a first-time reader understand what SerialHop is, who needs it, and how it fits with [lab-bridge](https://github.com/bioexperiment-lab-devices/lab-bridge).
2. Walks through SerialHop's features using the control-panel tabs as the spine.
3. Documents the REST API as a route table with collapsible deep-dives where the payload is non-trivial.
4. Gives an architectural overview of the binary (three run modes, chisel routes, on-disk state, internal-package map).
5. Provides a developer guide (Task targets, pre-PR verify pipeline, branch/PR/release flow).

Sections that exist today but no longer earn their keep are removed or folded in: Windows Defender false positives (out), standalone "Run modes" table (folded into Architecture), standalone "Log streaming to Loki" (folded into Architecture), standalone "Tests" (folded into Developer guide), "Releases" verify-binary block (folded into Install and Developer guide).

## Audience

- **Primary:** operators evaluating SerialHop and lab admins installing on Windows PCs.
- **Secondary:** developers contributing to the repo.

The README serves both; longer technical content (REST payloads, design rationale) sits behind `<details>` blocks or links into `docs/superpowers/specs/` so the operator path stays short.

## Section-by-section content

### 1. Hero & intro

- Hero image (existing `assets/serialhop.webp`).
- One-line tagline.
- Paragraph 1: SerialHop is the Windows agent for [lab-bridge](https://github.com/bioexperiment-lab-devices/lab-bridge). It runs on a lab PC, discovers serial-attached instruments (peristaltic pumps, distribution valves, densitometers), and exposes a local REST API to lab-bridge over a chisel reverse tunnel — letting researchers operate those instruments from a shared JupyterLab environment without opening any inbound port on the lab network.
- Paragraph 2: who it's for — operators of Windows PCs hosting serial-attached lab instruments behind NAT.
- Mermaid system diagram:
  - Researcher → JupyterLab on lab-bridge VPS → chisel server → (reverse tunnel) → SerialHop on lab PC → instruments.
  - Side arrow: SerialHop log shipper → (forward tunnel) → Loki on the lab-bridge VPS.

### 2. Install

Short. Two sentences plus a silent-install pointer.

- "Download `SerialHop-Setup-vX.Y.Z.exe` from the [releases page](https://github.com/bioexperiment-lab-devices/serialhop/releases/latest) or the lab management UI, and run it."
- One line: "Silent install: `SerialHop-Setup.exe --silent --dir <path>` — see `docs/superpowers/specs/2026-05-15-installer-design.md` for the full flag set and behavior."

No GUI walkthrough; the installer is trivial.

### 3. Control panel — feature tour

Intro line: Wails v2 + React desktop window. Opens on double-click of `SerialHop.exe`. Drives the underlying Windows service. Frameless, responsive (720 px breakpoint), sticky titlebar.

Five subsections, one per tab:

- **Status** — three lamps (Local service, Lab-bridge server, Reverse tunnel) and the Install / Uninstall / Restart admin buttons (UAC). When a newer release is available, an update row appears here (download → SHA-256 verify → install with rollback). Prior crash reports are surfaced inline.
- **Config** — typed editor over `%ProgramData%\SerialHop\SerialHop_config.yaml`. Field-level validation (host as IPv4 / RFC 1123 hostname, integer fields clearable). Dirty-state guard on tab switch. Default save flow is "Save & restart" so config changes apply immediately.
- **Devices** — discovered devices: type, type code, port. Send raw command bytes. Per-row Disconnect (released the single port without tearing down the rest).
- **Ports** — every enumerated COM port plus USB descriptors (VID, PID, SerialNumber, Product). Filter box. Send raw bytes to ports without a discovered device (gated by `raw_serial.enabled`).
- **Logs** — tailed structured logs from `%ProgramData%\SerialHop\logs\`, newest-first, sticky filter bar, inline log-detail view, on-open backlog.

### 4. REST API

Intro: bound to `127.0.0.1` on the lab machine; reachable from outside **only** through the chisel reverse tunnel; JSON in/out; capability gates (`raw_serial.enabled`, `flashing.enabled`) called out per row.

**Route table** (with one-line purpose and a Gate column):

| Method | Path | Purpose | Gate |
| --- | --- | --- | --- |
| `POST` | `/discover` | Run a fresh discovery and return the device list | — |
| `GET` | `/devices` | Return the cached device list | — |
| `POST` | `/devices/{id}/command` | Send raw bytes to a discovered device; optionally read a reply | — |
| `POST` | `/devices/disconnect` | Release all open device handles, or just `?port=<name>` | — |
| `GET` | `/serial/ports` | List enumerated COM ports with discovery state | `raw_serial.enabled` |
| `GET` | `/serial/ports/detailed` | Same, plus USB descriptors (VID/PID/SerialNumber/Product) | — (always available) |
| `POST` | `/serial/ports/{port}/command` | Send raw bytes to a port without a discovered device | `raw_serial.enabled` |
| `POST` | `/flash/{port}` | Pre-backup → flash → byte-verify → optional test → auto-rollback | `flashing.enabled` |
| `GET` | `/agent/info` | Agent self-description for server-pulled state | — |

**Collapsible `<details>` blocks** (one per endpoint family) with shape examples:

- **Discovery and device commands** (`POST /discover`, `GET /devices`, `POST /devices/{id}/command`) — request body, query params (`timeout_ms`, `inter_byte_ms`, `wait_for_response`, `expected_response_bytes`), response shape, error codes (409 busy, 503 unreachable, 503 identity changed). Mention the discovered device types: `pump` (10), `valve` (30), `densitometer` (70).
- **Disconnect** (`POST /devices/disconnect`) — both no-query (releases everything) and `?port=<name>` (releases one) shapes, plus the `{ "released": <int> }` response.
- **Raw serial** (`GET /serial/ports`, `GET /serial/ports/detailed`, `POST /serial/ports/{port}/command`) — port DTO shape, `post_open_settle_ms` override, and the 409 "port has discovered device → use `/devices/{id}/command`" branch.
- **Flashing** (`POST /flash/{port}`) — short request example (`firmware`, `test_command`, `expected_response`, `skip_backup`), one-line description of the staged-outcome response, link to `docs/superpowers/specs/2026-05-12-remote-firmware-flashing-design.md` for full payload.
- **Agent info** (`GET /agent/info`) — snapshot example; pointer to `docs/superpowers/specs/2026-05-18-agent-info-endpoint-design.md`.

Cross-link to `docs/superpowers/specs/2026-04-26-lab-devices-client-design.md` once at the bottom of the section for the canonical reference.

### 5. Architecture

**Mermaid diagram** of the three processes that make up SerialHop on a lab PC, plus the chisel tunnel:

- Panel process: WebView2 ↔ Wails-bound Go in `internal/panel`.
- Service worker (`internal/winsvc` → `internal/app`): owns the REST listener, registry, discovery, flasher, chisel client, and log shipper.
- Admin-action child: short-lived, UAC-elevated, invoked with `--admin-action=<install|uninstall|restart|update>` by the panel.
- chisel client opens two routes only: reverse `R:<remote_port>:127.0.0.1:<api>` (exposes the REST API on the chisel server's loopback, where lab-bridge's auth proxy fronts it) and forward `127.0.0.1:3100 → loki:3100` (the log shipper pushes here).

**Three run modes of the single binary** (replaces the old top-level "Run modes" table — same content, recontextualised):

| Launched via | Mode |
| --- | --- |
| SCM | Service worker (REST + chisel + log shipper) |
| Double-click | Control panel |
| `--admin-action=<verb>` | Internal SCM operation (UAC-elevated child) |
| `--foreground` | Console developer mode (JSON logs to stdout, Ctrl-C to stop) |

**State on disk** (`%ProgramData%\SerialHop\`):
- `SerialHop_config.yaml` — typed config (host, credentials, gates, log level, etc.).
- `logs\SerialHop.log`, `logs\SerialHop_stderr.log` — slog JSON and chisel/panic output, both rotated at 10 MB × 3.
- `cache\server-info.json` — last lab-bridge bootstrap response.
- Crash journal entries (panel) for prior-run surfacing.

Install directory holds only binaries.

**Package map** (`internal/<pkg>` — one line each):

- `agentinfo` — assembles the `GET /agent/info` snapshot.
- `api` — REST handlers, route mux, log middleware, error envelope.
- `app` — top-level service composition (registry, discovery, REST listener, chisel client, log shipper).
- `bootstrap` — fetches and caches lab-bridge server-info on startup.
- `chisel` — embedded chisel client, two-route configuration.
- `config` — typed config + validation + scaffold writer.
- `discovery` — serial probe (`\x55` probe bytes, post-open settle, type decoding).
- `flasher` — Intel HEX parser + STK500v1/optiboot driver + backup/verify/rollback.
- `labbridge` — typed HTTP client for `/server-info` and friends.
- `logship` — in-memory ring buffer + gzipped batch shipper to Loki.
- `panel` — Wails app, bindings, lamp state, probes, crash journal, update controller; frontend lives under `internal/panel/frontend`.
- `panellog` — slog handler that broadcasts records to the panel UI.
- `paths` — `%ProgramData%` paths and `EnsureDirs`.
- `registry` — in-memory device registry with per-device mutex and port-keyed reverse index.
- `serial` — port opener, framing reader, USB-descriptor enumerator.
- `slogtest` — slog test helpers.
- `updater` — auto-update state machine (check, download, verify, install, rollback).
- `version` — version string assembly from ldflags.
- `winsvc` — SCM service worker, install/uninstall/restart, admin-action child.

### 6. Developer guide

**Prerequisites:** Go (see `go.mod`), Node (panel frontend), `jq` on the build host.

**Task targets** (table — name, what it does, deps):

| Task | Purpose |
| --- | --- |
| `task build` | Build `SerialHop.exe` for `GOOS/GOARCH` (defaults: `windows/amd64`). |
| `task installer` | Build `dist/SerialHop-Setup.exe` (embeds a freshly-built `SerialHop.exe`). |
| `task test` | Run all unit tests (auto-runs `lint:secrets` first). |
| `task lint:secrets` | Fail if any `slog.*` call logs a config secret. |
| `task preview` | Run the panel UI in a desktop browser (macOS / Linux) via a Wails-runtime shim. |
| `task tidy` | `go mod tidy`. |
| `task clean` | Remove build outputs and generated resources. |

**Pre-PR verify pipeline** (mirror `CLAUDE.md`; run before pushing):

```
gofmt -l .
go vet ./...
golangci-lint run
go test -race -count=1 ./...
govulncheck ./...
```

**Branch & PR flow:**
- `main` is protected; no direct pushes. Every change lands via PR, squash-merged, linear history.
- PR titles are Conventional Commits (`feat:`, `fix:`, `chore:`, etc.) — `pr.yml` blocks merge if they aren't.
- The PR title becomes the squash commit on `main` and feeds release-please. One PR = one logical change.

**Release flow:**
- Merge `feat:` / `fix:` PRs. release-please opens or updates a `chore(main): release X.Y.Z` PR.
- Squash-merge it. `release-build` on `windows-latest` publishes `SerialHop-vX.Y.Z.exe`, `SerialHop-Setup-vX.Y.Z.exe`, `SHA256SUMS.txt`, and a Sigstore build-provenance attestation.
- Verify a downloaded binary:
  ```
  gh release download vX.Y.Z -p "SerialHop-*.exe" -p "SHA256SUMS.txt"
  shasum -a 256 -c SHA256SUMS.txt
  gh attestation verify SerialHop-vX.Y.Z.exe --owner bioexperiment-lab-devices
  ```

**Where to read more:** `CLAUDE.md` for agent-friendly working notes; `docs/superpowers/specs/` for per-feature designs.

### 7. Security & License

Two short lines.

- Threat model and vulnerability reporting: `SECURITY.md`.
- License: Apache-2.0 — see `LICENSE`.

## Non-goals

- Re-architecting any subsystem or changing behavior. This is documentation only.
- Replacing `SECURITY.md` content; the README points at it.
- Documenting every config field (the panel renders them with validation; design docs cover semantics).
- Writing operator-flow installer screenshots. Section 2 stays one paragraph.

## Risks / open items

- Mermaid renders in GitHub markdown but not in some IDE previewers. Acceptable: the README's primary read context is github.com.
- The `internal/<pkg>` map is mildly redundant with code structure; if it drifts it's worse than nothing. Mitigation: short, one-liner per package, easy to keep accurate; revisit if maintenance becomes a chore.

## Plan handoff

After approval, implementation is a single editor pass on `README.md`. No multi-step plan needed; `writing-plans` is not invoked. The PR will be one `docs:` commit on `docs/readme-rewrite` branch, squash-merged.
