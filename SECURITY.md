# Security

## Reporting a vulnerability

Use GitHub's private vulnerability reporting on this repository (Security tab → "Report a vulnerability"). Please do not open public issues for suspected vulnerabilities.

Include: affected version (`SerialHop --version` output or release tag), reproduction steps, and impact. We aim to acknowledge within 5 working days.

## Supported versions

Only the latest release on `main` is supported. There are no LTS branches; fixes ship as new patch releases via the standard release-please flow.

## Threat model

This document exists because SerialHop's shape — a Go static binary, Windows service running as LocalSystem, embedded reverse tunnel, persistent outbound connection to a server — is the same shape as a Remote Access Trojan. Defender heuristics flag it for that reason (see `README.md` → "Windows Defender false positives"). The architecture is not malicious, but "trust me, it isn't" is not a threat model. This is.

### What SerialHop is

A control plane for serial-attached lab instruments (peristaltic pumps, distribution valves, densitometers) on Windows machines that sit behind NAT. The lab machine cannot accept inbound connections, so the binary dials out to an operator-controlled chisel server and exposes its local REST API over a reverse tunnel. An upstream service in front of that tunnel handles authentication and authorization for actual operators.

### What the chisel tunnel does

On startup, `internal/chisel.Run` configures **exactly two routes** (`internal/chisel/client.go:26-32`):

1. **Reverse route** `R:<remote_port>:127.0.0.1:<local_port>` — exposes the local REST listener on the chisel server's loopback at `<remote_port>`. The server's docker compose puts an authenticating proxy in front of that port; the reverse-tunnel port itself is not published outside the server's docker network.
2. **Forward route** `127.0.0.1:3100:loki:3100` — opens a local listener on the **lab machine's loopback** that forwards to the Loki container in the chisel server's docker network. The in-process log shipper POSTs to this loopback address; nothing else can reach it. Only enabled when `lab_bridge.user` is set (server allowlist is per-user).

That's the entire tunneling configuration. There is no SOCKS proxy, no dynamic port forwarding, no remote-shell feature, no file-transfer feature. chisel's binary supports SOCKS, but we don't configure it and don't import the relevant client code path.

### What SerialHop does NOT do

- **No remote command execution.** The control plane is three HTTP routes (`internal/api/handlers.go:53-55`):
  - `GET /api/v1/devices` — list cached discovery results
  - `POST /api/v1/discover` — re-enumerate COM ports and rebuild device sessions
  - `POST /api/v1/devices/{id}/command` — execute one command on a device
  Commands are JSON, validated per device protocol, and translated to fixed 5-byte frames on the serial port. The one exception is the raw serial attach endpoint (`GET /serial/ports/{port}/attach`), which is off by default, tightly scoped, and covered on its own below — see "Raw serial attach".
- **No file system access.** No endpoint reads, writes, lists, or transfers arbitrary files. The only file I/O the binary performs is reading its own config, writing its own logs, persisting per-device state JSON files under its own data dir (`devicestate/`), and (in service mode) updating SCM state.
- **No screen capture, keylogging, clipboard access, microphone, camera, browser/credential theft.** None of these subsystems are imported or invoked.
- **No arbitrary network egress.** Outbound network activity from this binary consists of (a) the chisel control connection to the configured server, and (b) HTTP POSTs from the in-process log shipper to `127.0.0.1:3100` (which is then tunneled to Loki via chisel). No DNS-over-HTTPS exfil, no beaconing to attacker infrastructure, no auto-update channel.
- **No inbound port on the lab machine's external interface.** The REST listener is bound to `127.0.0.1` (`internal/api/server.go:13-19`). Reachability from outside the lab machine is **only** through the chisel tunnel that the operator chose to configure.
- **No persistence beyond the configured service.** Install creates one Windows service named `SerialHop`. Uninstall removes it. No registry run-keys, no scheduled tasks, no other services, no DLL hijacking, no lateral-movement primitives.

If a future change adds any of the above, this section must be updated in the same PR. CI does not enforce this — review does.

### Raw serial attach

`GET /serial/ports/{port}/attach` (`internal/api/rawattach.go`) is the one endpoint that gives a caller direct byte-level access to a serial port, so it gets its own entry here rather than folding into the bullet above.

- **Off by default.** Gated by `raw_serial.enabled` in config, defaulting to `false` (`internal/config/config.go`). An operator must explicitly opt in per lab machine.
- **Undiscovered-ports-only.** The pre-upgrade gate rejects the request with `409 Conflict` ("port has discovered device") whenever the port is already owned by a discovered device. A port is always either commanded through the JSON device protocol or attached raw — never both — so raw traffic can never land inside a driver's completion window.
- **Single leased session per port.** An in-memory registry lease (`registry.TryAcquireRaw`/`ReleaseRaw`) allows at most one raw attach per port; a second attempt on the same port gets `409 Conflict` ("port already attached") until the first session closes. The same lease blocks discovery from re-probing a raw-leased port, and blocks a new raw attach while discovery is in flight (`409`, "discovery in progress").
- **Idle timeout.** `raw_serial.idle_timeout_ms` (default `900000` ms / 15 min) tears down a session that has seen no traffic for that long, so a client that disappears doesn't hold the lease indefinitely.
- **Scoped strictly to enumerated serial ports.** The endpoint reads and writes bytes, and toggles baud/DTR/RTS/break/modem-status, on the one named COM port that was passed in the path and validated against the live port list. It is not a SOCKS proxy, not a remote shell, and not a general-purpose tunnel — there is no way to reach any resource other than that port's serial device.
- **No new chisel route.** The existing reverse chisel route already forwards raw TCP, so an HTTP `Upgrade: websocket` request rides the *same* route as every other REST call; `internal/chisel/client.go`'s two routes are unchanged. Reachability is therefore identical to the rest of the device API: authenticated chisel tunnel + Authelia-gated JupyterLab + `labnet` network isolation. The reference client (a pyserial `rfc2217://` bridge meant to run inside JupyterLab) lives in `clients/`, documented in `clients/README.md`.
- **Audited.** Every session logs `raw_attach_open` and `raw_attach_close` (port, byte counts, duration, close reason) through the same structured logger as the rest of the service.

### In-scope threats and mitigations

| Threat | Mitigation |
|---|---|
| Operator chisel-server compromise | Attacker gains the same access the legitimate operator has: the three REST endpoints. Containment depends on the server's auth proxy. Not solvable by the client. |
| Stolen `lab_bridge.user`/`lab_bridge.pass` from a single lab | Per-lab credentials. Attacker can authenticate to the chisel server as that lab but cannot impersonate other labs. Server allowlist is per-user; rotate on disclosure. |
| Bug in the command REST handler exploited via the tunnel | Service runs as LocalSystem, so an exploitable bug is high-impact. Mitigations: input validation (`decodeEnvelope` rejects malformed bodies, 32 KiB body cap, per-device param validation in the driver); each device session runs on a single goroutine, serializing its commands; race tests in CI (`go test -race`); `govulncheck` and `gosec` in `pr.yml`. |
| Tampered binary distributed to operators | Releases are built on `windows-latest` from `main`, published with `SHA256SUMS.txt` and a Sigstore build-provenance attestation. Verify with `gh attestation verify` (see `README.md` → "Releases"). |
| Config-file disclosure on the lab machine | The config contains the chisel server URL and the per-lab credentials. Disclosure → rotate credentials and reissue. The file lives next to the .exe at install location; protect with normal NTFS ACLs. No secrets are logged. |
| Log exfiltration through the Loki forward tunnel | Logs are gzipped JSON of the same lines already on disk in `SerialHop.log` / `SerialHop_stderr.log`. Line bodies are not scrubbed (per design). Operators must avoid logging secrets from upstream code; reviewers should check log call sites in PRs that touch new code paths. |

### Out of scope

- **Local compromise of the lab machine.** An attacker with code execution or admin on the lab machine has already won; SerialHop offers them nothing they don't already have.
- **Physical access to the lab instruments.** The serial wire is the trust boundary; we assume it is in a controlled lab.
- **Denial of service against the chisel server.** That is a property of the server's deployment, not this client.
- **Supply-chain compromise of upstream Go modules.** Mitigated by `go.sum` pinning, Dependabot (with the `actions/checkout` v6+ caveat in `CLAUDE.md`), and `govulncheck` in CI, but a determined upstream attack is not something this binary defends against on its own.

### Operator responsibilities

The threat model assumes the operator:

- Runs the chisel server on infrastructure they control, with an authenticating proxy in front of any reverse-tunnel port.
- Provisions per-lab `lab_bridge.user`/`lab_bridge.pass` credentials and rotates them on personnel changes or suspected disclosure.
- Verifies release artifacts against `SHA256SUMS.txt` and the Sigstore attestation before deploying.
- Restricts NTFS permissions on the install directory so non-admin users can't read `SerialHop_config.yaml`.
- Treats the lab machine as a single-purpose device — no shared workstation use.

If any of those break, the model in this document does not hold.

## Verifying it yourself

Everything above can be checked from the source tree:

- Tunnel routes: `internal/chisel/client.go` (`buildRemotes`)
- HTTP surface: `internal/api/handlers.go` (`Handler`)
- Listener binding: `internal/api/server.go` (`Listen`)
- HTTP-client call sites in runtime code: `git grep -nE 'http\.Client|\.Do\(' -- 'internal/' 'cmd/'` — every hit should be inside `internal/logship/shipper.go` (POSTing to `127.0.0.1:3100`). Anything else is a regression worth a PR comment.
- Subprocess call sites in runtime code: `git grep -nE 'os/exec|exec\.Command' -- 'internal/' 'cmd/'` — should print nothing. (`tools/buildcmd` is a build-time helper and is not shipped in the binary.)
- Build provenance for a release: `gh attestation verify SerialHop-vX.Y.Z.exe --owner bioexperiment-lab-devices`

If any of those checks contradict claims in this document, that is a documentation bug — please report it.
