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
