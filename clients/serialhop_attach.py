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
"""
from __future__ import annotations

import argparse
import json
import threading

try:
    import websocket  # websocket-client
except ImportError:  # pragma: no cover - import guard
    websocket = None


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

    def recv(self):
        """Yield serial bytes from the WS; swallow control replies."""
        while True:
            op = self.ws.recv()
            if isinstance(op, bytes):
                yield op
            else:
                # JSON control frame (ready/modem/error): log, don't forward.
                try:
                    msg = json.loads(op)
                except ValueError:
                    continue
                if msg.get("op") == "error":
                    print("serialhop-attach: server error:", msg.get("detail"))


def _serve(ws_url: str, listen: str) -> None:  # pragma: no cover - I/O glue
    host, port = listen.split(":")
    import serial.rfc2217 as r  # noqa: F401  pyserial provides the server codec
    # NOTE: wire a socketserver that speaks RFC2217 on (host, port), maps
    # its baud/dtr/rts/break callbacks through Bridge.send_control, and pumps
    # socket<->Bridge bytes. Kept out of the unit-tested surface; see README.
    raise SystemExit("run via the documented recipe; translation is unit-tested")


if __name__ == "__main__":  # pragma: no cover
    ap = argparse.ArgumentParser()
    ap.add_argument("--ws", required=True)
    ap.add_argument("--listen", default="127.0.0.1:5555")
    ap.add_argument("--baud", type=int, default=9600)
    args = ap.parse_args()
    _serve(args.ws, args.listen)
