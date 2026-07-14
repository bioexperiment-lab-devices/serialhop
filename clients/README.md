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

This is documented reference client code, not a production library — it is
unit-tested for its pure translation logic (see below) but the socket/telnet
I/O glue is intentionally out of scope for automated coverage. See
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

## What's unit-tested vs. what isn't

- `rfc2217_to_control(kind, value)` — the pure translation table
  (`baud`/`dtr`/`rts`/`break` → control-frame dict, unknown → `None`) — is
  unit-tested in `test_serialhop_attach.py` without a live socket.
- `Bridge` wires WS framing (binary = serial bytes, JSON text = control) on
  top of that table.
- `_serve` (the RFC2217 telnet socket server built on pyserial's
  `serial.rfc2217` server codec) is documented I/O glue, not unit-tested —
  wiring a Python CI job for this Go repo is out of scope for v1.

Run the tests:

```bash
cd clients && python -m pytest test_serialhop_attach.py -q
```
