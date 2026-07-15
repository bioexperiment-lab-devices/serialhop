# serialhop-attach

A small, self-contained Python bridge that presents a serial port lab device
as a local `rfc2217://` URL, tunneling bytes and line control to the
SerialHop server's raw-attach WebSocket endpoint
(`GET /serial/ports/<port>/attach`, see
[`../docs/superpowers/specs/2026-07-14-raw-serial-attach-design.md`](../docs/superpowers/specs/2026-07-14-raw-serial-attach-design.md)).

It exists for the cases the per-device JSON protocol doesn't cover: bring-up
of a new instrument with no driver yet, firmware/bootloader work (DTR reset,
baud switching), and ad-hoc interactive pyserial scripting against a live,
undiscovered port.

This is documented reference client code, not a production library. It is a
runnable command (see "Running the bridge" below) built on pyserial's own
RFC2217 server codec (`serial.rfc2217.PortManager`); see "What's tested vs.
what isn't" for exactly which parts have automated coverage and which are
exercised only by running the bridge for real. See
[`../docs/python-client-brief.md`](../docs/python-client-brief.md) for the
underlying HTTP API this bridge sits alongside.

## Must run inside JupyterLab

`ws://chisel:<port>/...` only resolves inside the JupyterLab container on the
`labnet` Docker network — that's where the chisel reverse tunnel from the lab
PC terminates. Run `serialhop_attach.py` (and the notebook that uses it) from
inside JupyterLab, not from your laptop or any other host.

## Prerequisites

```bash
pip install websocket-client pyserial
```

## Running the bridge

```bash
python serialhop_attach.py --ws ws://chisel:9001/serial/ports/COM7/attach \
                           --listen 127.0.0.1:5555 --baud 115200
```

- `--ws` — the raw-attach WebSocket URL for the target port on the lab
  machine's SerialHop instance, reached through the chisel tunnel.
- `--listen` — local `host:port` to serve RFC2217 on (default
  `127.0.0.1:5555`).
- `--baud` — initial baud rate (default `9600`); pyserial can change it later
  over the tunnel via `ser.baudrate = ...`.

## Using it from a notebook

Once the bridge is running, point pyserial at it like any other RFC2217
device:

```python
import serial
ser = serial.serial_for_url("rfc2217://127.0.0.1:5555")
ser.baudrate = 115200
ser.dtr = False; ser.dtr = True   # bootloader reset, over the tunnel
```

Reads and writes on `ser` stream as WebSocket binary frames to and from the
serial port on the lab machine; `baudrate`, `dtr`, `rts`, and `send_break`
translate to JSON control frames (`set_baud`, `set_dtr`, `set_rts`,
`send_break`) via `rfc2217_to_control` in `serialhop_attach.py`.

## Known v1 limitations

- The port is fixed at 8N1 with no flow control, matching the SerialHop
  raw-attach wire protocol (design spec §6). `bytesize`/`parity`/`stopbits`/
  `xonxoff`/`rtscts` are accepted locally (so pyserial's RFC2217 handshake
  succeeds) but are not forwarded to the device.
- `break_condition` is boolean on the RFC2217/pyserial side, but the wire
  protocol models a break as one timed pulse (`send_break`, `ms`). Setting
  `ser.break_condition = True` fires one fixed-length pulse
  (`DEFAULT_BREAK_PULSE_MS`, 250 ms); setting it back to `False` is a no-op.
- `ser.cts`/`ser.dsr`/`ser.ri`/`ser.cd` are a best-effort cache refreshed
  roughly once a second by a background poller, not synchronous reads.
- `ser.reset_input_buffer()`/`ser.reset_output_buffer()` are no-ops — there
  is no purge op in the wire protocol.

## What's tested vs. what isn't

- `rfc2217_to_control(kind, value)` — the pure translation table
  (`baud`/`dtr`/`rts`/`break` → control-frame dict, unknown → `None`) — is
  unit-tested without a live socket.
- `ControlAdapter` — the object that presents the `serial.Serial` attribute
  surface `serial.rfc2217.PortManager` reads/writes, backed by `Bridge` — is
  unit-tested with a recording fake `Bridge`: setting `baudrate`/`dtr`/`rts`/
  `break_condition` is asserted to call `Bridge.send_control` with the right
  `(kind, value)`, and the `cts`/`dsr`/`ri`/`cd` cache is asserted to update
  from a `modem` control frame.
- `Bridge` wires WS framing (binary = serial bytes, JSON text = control) on
  top of that table; it is not exercised against a live WebSocket in this
  repo's tests (that would mean standing up the Go server from Python CI,
  out of scope for v1 per the design spec §9).
- The RFC2217 server loop (`_make_listener`, `_serve_forever`,
  `_handle_connection`, built on pyserial's `serial.rfc2217.PortManager`) is
  exercised end-to-end with a real `pyserial` RFC2217 client
  (`serial.serial_for_url("rfc2217://...")`) against the real production
  wiring, with only `Bridge` swapped for an in-memory loopback fake (no live
  WebSocket or Go server) — see `test_rfc2217_round_trip_over_fake_bridge`
  in `test_serialhop_attach.py`. This is the strongest coverage this repo's
  Python tests can give the loop without wiring a Python CI job that talks
  to a real Go server, which stays out of scope for v1 (design spec §9).

Run the tests:

```bash
cd clients && pip install pyserial websocket-client pytest
python -m pytest test_serialhop_attach.py -q
```
