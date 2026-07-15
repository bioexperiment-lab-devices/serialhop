import queue
import threading
import time

import pytest

import serialhop_attach as sa


def test_baud_maps_to_set_baud():
    assert sa.rfc2217_to_control("baud", 115200) == {"op": "set_baud", "baud": 115200}


def test_dtr_maps_to_set_dtr():
    assert sa.rfc2217_to_control("dtr", True) == {"op": "set_dtr", "level": True}
    assert sa.rfc2217_to_control("dtr", False) == {"op": "set_dtr", "level": False}


def test_rts_maps_to_set_rts():
    assert sa.rfc2217_to_control("rts", True) == {"op": "set_rts", "level": True}


def test_break_maps_to_send_break():
    assert sa.rfc2217_to_control("break", 250) == {"op": "send_break", "ms": 250}


def test_unknown_returns_none():
    assert sa.rfc2217_to_control("bogus", 1) is None


# --- ControlAdapter: the RFC2217 <-> Bridge.send_control wiring ------------
#
# These use a bare recording fake, not a real Bridge/WebSocket: the point is
# to prove the adapter's property setters call Bridge.send_control with the
# right (kind, value) pair, independent of any live transport.


class _RecordingBridge:
    def __init__(self):
        self.calls = []

    def send_control(self, kind, value):
        self.calls.append((kind, value))


def test_adapter_baudrate_forwards_to_send_control():
    bridge = _RecordingBridge()
    adapter = sa.ControlAdapter(bridge, baudrate=9600)
    adapter.baudrate = 115200
    assert adapter.baudrate == 115200
    assert bridge.calls == [("baud", 115200)]


def test_adapter_dtr_forwards_to_send_control():
    bridge = _RecordingBridge()
    adapter = sa.ControlAdapter(bridge)
    adapter.dtr = False
    assert bridge.calls == [("dtr", False)]


def test_adapter_rts_forwards_to_send_control():
    bridge = _RecordingBridge()
    adapter = sa.ControlAdapter(bridge)
    adapter.rts = True
    assert bridge.calls == [("rts", True)]


def test_adapter_break_condition_true_fires_pulse():
    bridge = _RecordingBridge()
    adapter = sa.ControlAdapter(bridge)
    adapter.break_condition = True
    assert bridge.calls == [("break", sa.DEFAULT_BREAK_PULSE_MS)]


def test_adapter_break_condition_false_without_prior_true_is_noop():
    bridge = _RecordingBridge()
    adapter = sa.ControlAdapter(bridge)
    adapter.break_condition = False
    assert bridge.calls == []


def test_adapter_break_condition_refires_only_on_new_true_transition():
    bridge = _RecordingBridge()
    adapter = sa.ControlAdapter(bridge)
    adapter.break_condition = True
    adapter.break_condition = True  # still True: no second pulse
    assert bridge.calls == [("break", sa.DEFAULT_BREAK_PULSE_MS)]
    adapter.break_condition = False
    adapter.break_condition = True  # new True transition: new pulse
    assert bridge.calls == [
        ("break", sa.DEFAULT_BREAK_PULSE_MS),
        ("break", sa.DEFAULT_BREAK_PULSE_MS),
    ]


def test_adapter_modem_cache_updates_from_control_frame_and_defaults_false():
    bridge = _RecordingBridge()
    adapter = sa.ControlAdapter(bridge)
    assert (adapter.cts, adapter.dsr, adapter.ri, adapter.cd) == (False, False, False, False)
    # omitempty on the wire: a missing key means False, not "unchanged".
    adapter.handle_control({"op": "modem", "cts": True, "dsr": True})
    assert (adapter.cts, adapter.dsr, adapter.ri, adapter.cd) == (True, True, False, False)
    adapter.handle_control({"op": "error", "detail": "boom"})  # ignored, not a modem frame
    assert (adapter.cts, adapter.dsr, adapter.ri, adapter.cd) == (True, True, False, False)


# --- Server loop: startup-without-crash + a real pyserial round trip -------
#
# `_LoopbackBridge` is a full stand-in for Bridge (send_bytes/send_control/
# request_modem_status/recv/close) that loops sent bytes back as if a
# device echoed them, and answers modem polls with a canned reply. No real
# WebSocket or Go server is involved; per the task brief this is
# intentional ("do NOT stand up the real Go server; a fake Bridge is
# fine").


class _LoopbackBridge:
    def __init__(self, *_args, **_kwargs):
        self.sent_control = []
        self._queue: "queue.Queue" = queue.Queue()

    def send_bytes(self, data):
        self._queue.put(data)

    def send_control(self, kind, value):
        self.sent_control.append((kind, value))

    def request_modem_status(self):
        self._queue.put({"op": "modem", "cts": True, "dsr": True, "ri": False, "cd": False})

    def recv(self, on_control=None):
        while True:
            item = self._queue.get()
            if item is None:
                return
            if isinstance(item, dict):
                if on_control is not None:
                    on_control(item)
                continue
            yield item

    def close(self):
        self._queue.put(None)


def test_serve_forever_starts_and_stops_without_crash(monkeypatch):
    """Smoke-verify startup: the listener + per-connection PortManager
    wiring construct and tear down cleanly with no client ever connecting."""
    monkeypatch.setattr(sa, "Bridge", _LoopbackBridge)
    srv = sa._make_listener("127.0.0.1", 0)
    t = threading.Thread(
        target=sa._serve_forever, args=(srv, "ws://fake", 9600), daemon=True
    )
    t.start()
    time.sleep(0.05)
    srv.close()
    t.join(timeout=2)
    assert not t.is_alive()


def test_rfc2217_round_trip_over_fake_bridge(monkeypatch):
    """A real pyserial RFC2217 client talks to our real PortManager +
    ControlAdapter wiring end to end; only the Bridge (WS transport) is
    faked, via monkeypatching the module-level `Bridge` name that
    `_handle_connection` constructs."""
    serial = pytest.importorskip("serial")

    monkeypatch.setattr(sa, "Bridge", _LoopbackBridge)
    monkeypatch.setattr(sa, "MODEM_POLL_INTERVAL_S", 0.02)

    srv = sa._make_listener("127.0.0.1", 0)
    port = srv.getsockname()[1]
    t = threading.Thread(
        target=sa._serve_forever, args=(srv, "ws://fake", 9600), daemon=True
    )
    t.start()
    try:
        ser = serial.serial_for_url(f"rfc2217://127.0.0.1:{port}", baudrate=9600, timeout=2)
        try:
            ser.baudrate = 19200  # exercises SET_BAUDRATE negotiation
            ser.dtr = False
            ser.rts = True
            ser.write(b"hello")
            assert ser.read(5) == b"hello"  # round-trips through the fake loopback
        finally:
            ser.close()
    finally:
        srv.close()
        t.join(timeout=2)
