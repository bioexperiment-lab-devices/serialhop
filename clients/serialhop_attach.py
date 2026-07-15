"""serialhop-attach: bridge a local rfc2217:// serial URL to a SerialHop raw
attach WebSocket, so pyserial can drive a remote lab COM port over the chisel
tunnel.

Run inside the JupyterLab environment (the only place `chisel:<port>` resolves):

    python serialhop_attach.py --ws ws://chisel:9001/serial/ports/COM7/attach \\
                               --listen 127.0.0.1:5555 --baud 115200

then in a notebook:

    import serial
    ser = serial.serial_for_url("rfc2217://127.0.0.1:5555")
    ser.baudrate = 115200
    ser.dtr = False; ser.dtr = True   # bootloader reset, over the tunnel

## Known v1 limitations

- The port is fixed at 8N1 with no flow control, matching the SerialHop
  raw-attach wire protocol (see the design spec §6). `bytesize`/`parity`/
  `stopbits`/`xonxoff`/`rtscts` are accepted locally (so pyserial's default
  RFC2217 handshake succeeds) but are not forwarded to the device.
- `break_condition` is boolean on the RFC2217/pyserial side but the wire
  protocol models a break as a single timed pulse (`send_break`, `ms`).
  Setting `break_condition = True` fires one `DEFAULT_BREAK_PULSE_MS`
  pulse; setting it back to `False` is a no-op (the device already released
  the line on its own).
- `cts`/`dsr`/`ri`/`cd` are a best-effort cache refreshed roughly once a
  second by a background poller, not synchronous reads.
- `reset_input_buffer`/`reset_output_buffer` (RFC2217 PURGE_DATA) are
  no-ops — there is no purge op in the wire protocol (only `drain`, which
  is not currently wired to a pyserial call).
"""
from __future__ import annotations

import argparse
import json
import socket
import threading
import time

try:
    import websocket  # websocket-client
except ImportError:  # pragma: no cover - import guard
    websocket = None

try:
    import serial.rfc2217 as rfc2217  # pyserial's RFC2217 server-side codec
except ImportError:  # pragma: no cover - import guard
    rfc2217 = None

# RFC2217 models a break as a boolean line state (SET_CONTROL_BREAK_ON /
# _OFF); the SerialHop wire protocol models it as a single timed pulse
# (`send_break`, `ms`). We fire the pulse on the True transition and treat
# the False transition as a no-op — see the module docstring.
DEFAULT_BREAK_PULSE_MS = 250

# How often the background status-line poller refreshes the cts/dsr/ri/cd
# cache. A module constant (rather than a literal) so tests can shrink it.
MODEM_POLL_INTERVAL_S = 1.0


def rfc2217_to_control(kind: str, value) -> dict | None:
    """Map one RFC2217 COM-port-control change to a SerialHop control frame.

    kind: "baud" | "dtr" | "rts" | "break"; value: int for baud/break,
    bool for dtr/rts. Returns None for unrecognized kinds.
    """
    if kind == "baud":
        return {"op": "set_baud", "baud": int(value)}
    if kind == "dtr":
        return {"op": "set_dtr", "level": bool(value)}
    if kind == "rts":
        return {"op": "set_rts", "level": bool(value)}
    if kind == "break":
        return {"op": "send_break", "ms": int(value)}
    return None


class Bridge:
    """Pumps bytes between a local RFC2217 server socket and the WS. The
    RFC2217 telnet negotiation is delegated to pyserial's rfc2217 server
    building blocks; this class wires data + control across the WS."""

    def __init__(self, ws_url: str):
        if websocket is None:
            raise RuntimeError("pip install websocket-client")
        self.ws = websocket.create_connection(ws_url, enable_multithread=True)
        self._lock = threading.Lock()

    def send_bytes(self, data: bytes) -> None:
        with self._lock:
            self.ws.send_binary(data)

    def send_control(self, kind: str, value) -> None:
        frame = rfc2217_to_control(kind, value)
        if frame is not None:
            with self._lock:
                self.ws.send(json.dumps(frame))

    def request_modem_status(self) -> None:
        """Ask the server for a `modem` reply (CTS/DSR/RI/CD). The reply
        arrives asynchronously through `recv()`'s `on_control` callback,
        same as every other control frame."""
        with self._lock:
            self.ws.send(json.dumps({"op": "get_modem"}))

    def recv(self, on_control=None):
        """Yield serial bytes from the WS. JSON control frames (ready/
        modem/error) are not yielded as data; if `on_control` is given, it
        is called with the parsed dict for every one of them."""
        while True:
            op = self.ws.recv()
            if isinstance(op, bytes):
                yield op
            else:
                try:
                    msg = json.loads(op)
                except ValueError:
                    continue
                if msg.get("op") == "error":
                    print("serialhop-attach: server error:", msg.get("detail"))
                if on_control is not None:
                    on_control(msg)

    def close(self) -> None:
        self.ws.close()


class ControlAdapter:
    """Presents the `serial.Serial` attribute surface that
    `serial.rfc2217.PortManager` reads and writes, backed by a `Bridge`.

    `baudrate`, `dtr`, `rts`, and `break_condition` forward to the SerialHop
    device over `bridge`. `bytesize`/`parity`/`stopbits`/`xonxoff`/`rtscts`
    are accepted and stored locally but not forwarded — see the module
    docstring's "Known v1 limitations". `cts`/`dsr`/`ri`/`cd` reflect the
    last `modem` reply seen by `handle_control`.
    """

    def __init__(self, bridge: Bridge, baudrate: int = 9600):
        self._bridge = bridge
        self._baudrate = baudrate
        self.bytesize = 8
        self.parity = "N"
        self.stopbits = 1
        self.xonxoff = False
        self.rtscts = False
        self._dtr = False
        self._rts = False
        self._break_condition = False
        self._cts = False
        self._dsr = False
        self._ri = False
        self._cd = False

    @property
    def baudrate(self):
        return self._baudrate

    @baudrate.setter
    def baudrate(self, value):
        self._baudrate = value
        self._bridge.send_control("baud", value)

    @property
    def dtr(self):
        return self._dtr

    @dtr.setter
    def dtr(self, value):
        self._dtr = bool(value)
        self._bridge.send_control("dtr", value)

    @property
    def rts(self):
        return self._rts

    @rts.setter
    def rts(self, value):
        self._rts = bool(value)
        self._bridge.send_control("rts", value)

    @property
    def break_condition(self):
        return self._break_condition

    @break_condition.setter
    def break_condition(self, value):
        value = bool(value)
        if value and not self._break_condition:
            self._bridge.send_control("break", DEFAULT_BREAK_PULSE_MS)
        self._break_condition = value

    @property
    def cts(self):
        return self._cts

    @property
    def dsr(self):
        return self._dsr

    @property
    def ri(self):
        return self._ri

    @property
    def cd(self):
        return self._cd

    def handle_control(self, msg: dict) -> None:
        """`Bridge.recv(on_control=...)` hook: refresh the modem-status
        cache from a `modem` control frame; ignore everything else.
        `modem`'s boolean fields use `omitempty` on the wire, so a missing
        key means False (see docs/python-client-brief.md)."""
        if msg.get("op") == "modem":
            self._cts = bool(msg.get("cts", False))
            self._dsr = bool(msg.get("dsr", False))
            self._ri = bool(msg.get("ri", False))
            self._cd = bool(msg.get("cd", False))

    def reset_input_buffer(self) -> None:
        pass  # no purge op on the wire in v1 — see module docstring

    def reset_output_buffer(self) -> None:
        pass  # no purge op on the wire in v1 — see module docstring


class _TelnetWriter:
    """Adapts a thread-safe `write(bytes)` callable to the `.write()`
    method `PortManager` expects on its `connection` argument (used for
    raw, unescaped telnet negotiation bytes)."""

    def __init__(self, write_fn):
        self._write_fn = write_fn

    def write(self, data: bytes) -> None:
        self._write_fn(data)


def _make_listener(host: str, port: int) -> socket.socket:
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind((host, port))
    srv.listen(1)
    return srv


def _handle_connection(conn: socket.socket, ws_url: str, baud: int) -> None:
    """Bridge one accepted RFC2217 client connection to one SerialHop
    raw-attach WebSocket session, until either side closes."""
    if rfc2217 is None:
        raise RuntimeError("pip install pyserial")
    conn.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)

    write_lock = threading.Lock()

    def conn_write(data: bytes) -> None:
        with write_lock:
            conn.sendall(data)

    bridge = Bridge(ws_url)
    adapter = ControlAdapter(bridge, baudrate=baud)
    pm = rfc2217.PortManager(adapter, _TelnetWriter(conn_write))

    alive = threading.Event()
    alive.set()

    def reader() -> None:
        """WS -> socket: pump device bytes to the RFC2217 client."""
        try:
            for data in bridge.recv(on_control=adapter.handle_control):
                conn_write(b"".join(pm.escape(data)))
        except Exception:  # noqa: BLE001 - end this session, not the process
            pass
        finally:
            alive.clear()
            # If the WS side died first and the client is otherwise idle,
            # the main loop below is blocked in conn.recv() and would never
            # notice `alive` clearing. Force it to unblock so the session
            # tears down promptly instead of lingering until the client
            # next writes (or times out on its own).
            try:
                conn.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass

    def poller() -> None:
        """Best-effort modem-status refresh, mirroring pyserial's own
        examples/rfc2217_server.py status_line_poller."""
        while alive.is_set():
            try:
                bridge.request_modem_status()
            except Exception:  # noqa: BLE001 - end this session, not the process
                break
            time.sleep(MODEM_POLL_INTERVAL_S)
            pm.check_modem_lines()

    t_reader = threading.Thread(target=reader, daemon=True, name="serialhop-attach-reader")
    t_reader.start()
    t_poller = threading.Thread(target=poller, daemon=True, name="serialhop-attach-poller")
    t_poller.start()

    try:
        while alive.is_set():
            data = conn.recv(4096)
            if not data:
                break
            bridge.send_bytes(b"".join(pm.filter(data)))
    except OSError:
        pass
    finally:
        alive.clear()
        try:
            bridge.close()
        except Exception:  # noqa: BLE001 - best-effort teardown
            pass
        t_reader.join(timeout=2)
        t_poller.join(timeout=2)


def _serve_forever(srv: socket.socket, ws_url: str, baud: int) -> None:
    while True:
        try:
            conn, addr = srv.accept()
        except OSError:
            return  # listener closed
        print(f"serialhop-attach: client connected from {addr}")
        try:
            _handle_connection(conn, ws_url, baud)
        except Exception as exc:  # noqa: BLE001 - keep serving other connections
            print("serialhop-attach: session error:", exc)
        finally:
            conn.close()
            print("serialhop-attach: client disconnected")


def _serve(ws_url: str, listen: str, baud: int) -> None:
    host, port_s = listen.rsplit(":", 1)
    srv = _make_listener(host, int(port_s))
    print(f"serialhop-attach: serving rfc2217 on {listen}, bridging to {ws_url}")
    try:
        _serve_forever(srv, ws_url, baud)
    finally:
        srv.close()


if __name__ == "__main__":  # pragma: no cover
    ap = argparse.ArgumentParser()
    ap.add_argument("--ws", required=True)
    ap.add_argument("--listen", default="127.0.0.1:5555")
    ap.add_argument("--baud", type=int, default=9600)
    args = ap.parse_args()
    _serve(args.ws, args.listen, args.baud)
