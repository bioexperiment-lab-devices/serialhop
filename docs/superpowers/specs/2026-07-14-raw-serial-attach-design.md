# Raw serial attach over WebSocket — design

**Status:** approved (brainstorm 2026-07-14)
**Repo:** serialhop (server side). lab-bridge needs no transport changes.
**Supersedes:** the raw-byte device API removed in #184 (`feat!: replace raw-byte device API with per-device JSON protocol`).

## 1. Problem

v2 removed the per-call raw-byte serial API (`POST /serial/ports/{port}/command` and friends) in favor of the per-device JSON protocol. That protocol is the right default, but it cannot serve four legitimate needs on ports that have no driver:

1. **Bring-up / protocol reverse-engineering** of a brand-new instrument with no driver yet.
2. **Firmware / bootloader work** — DTR-reset into the bootloader, baud change between bootloader and sketch.
3. **Running vendor / third-party serial software** — reduced in this iteration to "pyserial in JupyterLab" (see §9, out of scope).
4. **Ad-hoc pyserial scripting** — stream bytes both ways against the live port with no per-call HTTP round-trips.

The old raw endpoint was *stateless per call* (open → drain → write → optional framed read → close). That cannot do interactive bidirectional streaming or line control. We want the **most direct access possible**: a persistent, full-duplex byte pipe with real line control, reachable through the existing chisel tunnel, and usable from pyserial.

## 2. Goals / non-goals

**Goals**
- Persistent, full-duplex raw byte streaming to a single serial port.
- Real line control: baud change, DTR, RTS, break, modem-status read.
- Reachable through the *existing* chisel reverse tunnel with **no new chisel route** and **no lab-bridge change**.
- Usable from pyserial as `serial.serial_for_url("rfc2217://127.0.0.1:PORT")`.
- Off by default; operator-gated; safe against colliding with driver sessions.

**Non-goals (this spec)**
- Windows virtual COM (com0com) / unmodified Windows vendor apps. Deferred (§9).
- Detach-and-reattach of a *live* driver session to grant raw access. Raw is **undiscovered-ports-only**.
- Any second chisel route, SOCKS, or general TCP tunnel.
- Non-8N1 data formats in v1 (documented extension point, §6).

## 3. Topology (verified 2026-07-14)

The device REST API — and therefore this new endpoint — is reached by JupyterLab **directly at `http://chisel:<reverse_port>` inside the `labnet` Docker network**. It is *not* fronted by Caddy or Authelia (those front only browser routes). Authn/authz on this path is: the per-client chisel tunnel credential (lab → VPS) + Authelia-gated access to JupyterLab itself + `labnet` isolation (the reverse port is never published to the host).

chisel forwards the reverse port as **raw TCP**, so an HTTP `Upgrade: websocket` request rides straight through it. Consequences:

- **No lab-bridge transport change.** No new route, no proxy WebSocket config, no path allowlist. The WS endpoint is reachable exactly as `/discover` is today.
- The client bridge must run **inside JupyterLab** — the only place `chisel:<reverse_port>` resolves — which is exactly where the researcher's pyserial code already runs.
- The only place raw access is gated is the **SerialHop side**, where the lab operator controls it.

```
notebook (pyserial)                         lab PC (SerialHop)
 rfc2217://127.0.0.1:5555                    GET /serial/ports/{port}/attach  (WS upgrade)
        │                                        │  binary frames  ⇄  COM7 bytes
   serialhop-attach  ── ws://chisel:PORT ──►     │  text/JSON      ⇄  baud/DTR/RTS/break/modem
   (rfc2217⇄ws, in     over the EXISTING         │
    the jupyter        chisel reverse tunnel     └─ serial.Port (go.bug.st)
    container)         (raw TCP; WS passes through)
```

## 4. Server: endpoint, gating, exclusivity

### 4.1 Endpoint

`GET /serial/ports/{port}/attach` — sits beside the existing `GET /serial/ports/detailed` (infra namespace, not `/api/v1`). Registered on the same `net/http` mux in `internal/api`. `{port}` is a Windows COM name (`COM7`); production is Windows-only, so the single path segment is safe. (Tests use fake names like `COM3`.)

Optional query params, applied at open:
- `baud` (int, default 9600 — the current `Opener.Open` default). Range 1..4_000_000.
- `post_open_settle_ms` (int, 0..60000, default `discovery.PostOpenSettle`) — matches the old raw endpoint's knob for USB-serial DTR-reboot settle.

### 4.2 Pre-upgrade gate (ordinary HTTP responses, before the WS handshake)

In order; first failure wins:

1. `raw_serial.enabled == false` → **403** `{"error":"raw serial disabled","detail":"set raw_serial.enabled: true in config"}`.
2. `port` not in `opener.List()` → **404** `{"error":"port not found","detail":"<port>"}`.
3. `reg.HasPort(port)` (owned by a discovered device) → **409** `{"error":"port has discovered device","detail":"owned by <id>"}`.
4. `reg.IsDiscovering()` → **409** `{"error":"discovery in progress"}`.
5. An active raw lease already exists for `port` → **409** `{"error":"port already attached"}`.

Only after all pass do we acquire the lease, upgrade to WebSocket, and open the port. If the upgrade or open fails, the lease is released.

### 4.3 Port lease (raw ↔ discovery interlock)

A raw session must not race discovery over the OS handle. Extend `internal/registry` with a small raw-lease set guarded by the **same mutex** that guards the session map, exposing:

- `TryAcquireRaw(port string) bool` — fails if the port is owned by a device, discovery is running, or another raw lease is held.
- `ReleaseRaw(port string)`.
- Discovery's candidate enumeration **excludes** ports with an active raw lease (so a re-discover while a raw session is live simply skips that port rather than fighting for the handle).

Because acquire-raw and discovery-start take the same lock, they are mutually exclusive; no TOCTOU window in the single-operator lab context.

### 4.4 Lifecycle

- On WS close (either side), EOF, or idle timeout: close the serial handle, release the lease, log `raw_attach_close`. The port becomes discoverable again after the existing `portSettleDelay`.
- `raw_serial.idle_timeout` (default 15m; 0 disables): if no bytes flow and no control frame arrives within the window, the server closes the WS. Guards against a forgotten JupyterLab kernel pinning a port forever.
- App-level WebSocket ping/pong (server pings on an interval; missing pong → close) detects a dead peer independently of chisel's 25 s session keepalive. After the gorilla upgrade the connection is hijacked, so the shared `http.Server` `WriteTimeout` no longer applies to it.

### 4.5 Config

Reintroduce the key #184 removed (stale-key handling already tolerates it):

```yaml
raw_serial:
  enabled: false            # default off
  idle_timeout: 15m         # 0 = never
```

Add `RawSerial struct { Enabled bool; IdleTimeout time.Duration }` to `internal/config`, wire `Enabled` + `IdleTimeout` into `api.Server` at app composition.

### 4.6 Audit logging

Mirror the old `raw_serial_command` style:
- `raw_attach_open` — port, remote addr, baud.
- `raw_attach_close` — port, bytes_tx, bytes_rx, duration_ms, reason (`client_close` | `idle_timeout` | `read_error` | `server_shutdown`).
- Line-control ops at `Debug`.

## 5. WebSocket wire protocol

Deliberately **not** RFC2217-on-the-wire: RFC2217's in-band `0xFF` (IAC) escaping is a foot-gun for binary serial data. Instead split by WS frame type so data never needs escaping.

- **Binary frame** → raw serial bytes, verbatim, both directions. No header, no wrapping.
- **Text frame (JSON)** → control.

Client → server control ops:

| op | fields | serial.Port call |
|---|---|---|
| `set_baud` | `baud` int | `SetBaudRate(baud)` |
| `set_dtr` | `level` bool | `SetDTR(level)` |
| `set_rts` | `level` bool | `SetRTS(level)` |
| `send_break` | `ms` int | `SendBreak(ms·time.Millisecond)` |
| `drain` | — | `Drain(discovery.DrainDuration)` |
| `get_modem` | — | `ModemStatus()` → `modem` reply |

Server → client control ops:

| op | fields |
|---|---|
| `ready` | `port` string, `baud` int (sent once on open) |
| `modem` | `cts`, `dsr`, `ri`, `cd` bool (reply to `get_modem`) |
| `error` | `detail` string (line-control or IO error; stream may continue) |

On a fatal serial read error / port closed, the server sends a WebSocket **Close** frame (code + reason) and tears down.

Library: promote `github.com/gorilla/websocket` (already an indirect dep via chisel) to a direct dependency.

## 6. `serial.Port` interface extension

Current `serial.Port` has `SetDTR` and `SetBaudRate` but not RTS, break, or modem-status. `go.bug.st/serial` supports all three. Add:

```go
SetRTS(level bool) error
SendBreak(d time.Duration) error
ModemStatus() (ModemBits, error) // {CTS, DSR, RI, CD bool}
```

Implement on `realPort` (thin delegates to `SetRTS`, `Break`, `GetModemStatusBits`) and on the `fake` (`internal/serial/fake.go`) so the whole WS protocol is testable on macOS/Linux per the CLAUDE.md cross-platform rule.

**v1 data-format limitation:** the port stays 8N1 (data bits / parity / stop bits are fixed, matching `Opener.Open`/`OpenWithBaud`). The lab instruments are all 8N1, and the primary interactive need is baud + DTR. Full `set_mode` (databits/parity/stop) is a clean future extension (add `SetMode` to the interface + a `set_mode` control op); called out here so it can be vetoed.

## 7. Client bridge (`serialhop-attach`)

A small, self-contained **Python** module shipped with the client docs (the consumer is already a pyserial user; pyserial hands us the RFC2217 server-side telnet negotiation almost for free — a Go reimplementation would be far more code for no benefit). It:

- Serves RFC2217 on a loopback port (default `127.0.0.1:5555`).
- Dials `ws://chisel:<reverse_port>/serial/ports/<port>/attach?baud=<n>`.
- Pumps data as WS binary frames, and maps RFC2217 COM-port-control → JSON control frames:
  - `SET_BAUDRATE` → `set_baud`
  - `SET_CONTROL` DTR on/off → `set_dtr`; RTS on/off → `set_rts`; break on/off → `send_break` (timed; break-on/off is best-effort)
  - modem/line-state poll → `get_modem` → RFC2217 modem-state notification
- Ships with the three-line pyserial usage example and lands as a companion to `docs/python-client-brief.md`.

```python
ser = serial.serial_for_url("rfc2217://127.0.0.1:5555")
ser.baudrate = 115200
ser.dtr = False; ser.dtr = True   # bootloader reset, over the tunnel
```

Unit tests cover the RFC2217→JSON translation table. The bridge is documented reference client code; wiring a Python CI job into this Go repo is out of scope for v1 (noted as a follow-up).

## 8. Security / threat model

Add a `SECURITY.md` section stating precisely what this is and isn't:

- **Off by default** (`raw_serial.enabled: false`); the lab operator opts in per lab PC.
- **Undiscovered ports only**; a port owned by a driver session returns 409 — raw traffic can never enter a driver's completion window.
- **Single session per port**; leased, with idle timeout.
- Scoped strictly to **enumerated serial ports** — it is not a SOCKS proxy, remote shell, or general TCP tunnel, and adds **no new chisel route**. Reachability is unchanged from the existing device API (authenticated chisel tunnel + Authelia-gated JupyterLab + labnet isolation).

This is the honest counterweight to the platform's "no SOCKS/shell/file-transfer" posture: raw serial is now *possible* but narrowly bounded and disabled unless an operator turns it on.

## 9. Scope boundaries

**In:** WS attach endpoint + protocol; pre-upgrade gate; port lease + discovery interlock; `raw_serial` config; `serial.Port` extension (`SetRTS`/`SendBreak`/`ModemStatus`) + fake; Python rfc2217⇄ws bridge with translation tests; SECURITY.md + python-client-brief docs.

**Out (deferred, own spec later):** com0com / Windows virtual COM / vendor-app path; detach-and-reattach of live driver sessions; non-8N1 data formats; Python CI wiring; lab-bridge doc updates.

## 10. Testing

- Handler/protocol tests against the serial fake: full gate matrix (403/404/409×3), byte round-trip both directions, each control op → fake side effect, `ready` on open, idle-timeout close, single-session enforcement.
- Registry: raw-lease acquire/release; discovery excludes leased ports; acquire-vs-discovery mutual exclusion.
- Loopback integration: a real `gorilla/websocket` client ↔ handler ↔ fake port, asserting DTR/baud/break/modem reach the fake.
- Client bridge: unit-test the RFC2217→JSON translation table.
- Cross-platform: all Go tests pass on macOS/Linux (fake) and Windows, per CLAUDE.md.

## 11. Pre-flight (repo conventions)

Run before PR (`pr.yml` verify job): `gofmt -l .`, `go vet ./...`, `golangci-lint run`, `go test -race -count=1 ./...`, `govulncheck ./...`. PR title `feat: raw serial port attach over websocket` → minor bump via release-please. One PR = this logical change (spec + server + bridge + docs).

## 12. Open extension points (not built now)

- `set_mode` for non-8N1 (databits/parity/stop).
- com0com virtual COM bridge for Windows vendor software (the deferred deliverable).
- Optional lab-bridge public-docs page describing raw access from a notebook.
