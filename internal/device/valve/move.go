package valve

import (
	"encoding/json"
	"fmt"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// home declares the rotor's current physical position (TRANSLATION.md §4
// home). No motion frame is sent — homing is purely a translator-side
// declaration; all future moves are computed relative to it. The device's
// counter is re-read first so the belief↔physical offset is anchored to
// reality, then both are persisted for restart recovery.
func (d *Driver) home(params json.RawMessage) (any, *device.CmdError) {
	var p struct {
		Position *int `json:"position"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, device.ErrInvalidParams("params", nil, "params is not valid JSON")
	}
	if j := d.s.Jobs().Active(); j != nil {
		return nil, device.ErrBusy("a move is in progress", map[string]any{"job_id": j.ID})
	}
	if p.Position == nil || *p.Position < 0 || *p.Position > d.positions {
		return nil, device.ErrInvalidParams("position", p.Position,
			fmt.Sprintf("position must be between 0 and %d", d.positions))
	}
	reply, err := d.s.Transact(queryPosFrame, 4, replyTimeout)
	if err != nil {
		return nil, device.ErrHardware("position query: " + err.Error())
	}
	if reply[0] != TypeCode {
		return nil, device.ErrHardware("position query: unexpected reply")
	}
	d.deviceBelief = int(reply[3])
	d.lastCheck = d.s.Now()
	d.physicalPos = *p.Position
	d.homed = true
	if err := d.persistNow(); err != nil {
		return nil, device.ErrInternal("persist home: " + err.Error())
	}
	return map[string]any{"homed": true, "position": *p.Position}, nil
}
