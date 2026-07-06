// Package valve implements the distribution-valve (radial flow switch)
// driver for the JSON device protocol, translating
// docs/protocol_translation_docs/distribution_valve/JSON_PROTOCOL.md onto
// the unmodified legacy 5-byte firmware per TRANSLATION.md.
//
// The firmware has no homing sensor and blindly assumes position 0 at every
// boot, so homing is VIRTUAL: the driver tracks the rotor's true physical
// position and the device's belief (its internal position counter)
// separately and translates every target through the offset between them;
// all position arithmetic is modulo S = N+1 rotor slots. CHECK_BELIEF — a
// position-counter consistency check run before every move and on idle
// ticks — turns silent device reboots into either automatic recovery or an
// explicit unhomed state.
//
// The firmware keeps servicing serial while the motor runs, but replies
// sent mid-move reflect the TARGET (the counter is bumped the instant a
// move command is parsed), not the rotor — so the driver never interprets
// them as "arrived". There are no watcher goroutines: motion completion is
// purely clock-driven (s.After) with a post-motion readback that verifies
// the device processed the command. A stalled motor is undetectable (no
// encoder) — inherent hardware gap.
package valve

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// TypeCode is the valve's probe identify code (PROTOCOL.md §3).
const TypeCode = 30

const (
	typeName     = "valve"              // hub type name: registry, state files, API ids
	deviceType   = "distribution_valve" // JSON identify device_type
	firmwareVer  = "legacy"
	protocolVer  = "1.0"
	schemaV      = 1
	replyTimeout = 2 * time.Second // 4-byte replies arrive within ~80 ms
)

// CheckInterval is the idle CHECK_BELIEF cadence (TRANSLATION.md §5): how
// often Tick verifies the device's position counter while no move runs, so
// silent reboots are detected promptly rather than at the next move.
var CheckInterval = 30 * time.Second

// Command frames (PROTOCOL.md §4).
var queryPosFrame = []byte{33, 1, 0, 0, 0} // read the position counter

// pingFrame is the side-effect-free liveness probe (31): the reply's last
// byte is the position counter, fed opportunistically into CHECK_BELIEF.
var pingFrame = []byte{31, 2, 3, 4, 5}

// rotationFrame configures the rotation method (35 1 R): direct=1, wrap=2,
// shortest=3.
func rotationFrame(code byte) []byte { return []byte{35, 1, code, 0, 0} }

// holdFrame configures hold torque (35 2 H). The firmware encoding is
// INVERTED: H=0 keeps the stepper energized after a move (hold ON), H=1
// releases it (hold OFF).
func holdFrame(on bool) []byte {
	h := byte(1)
	if on {
		h = 0
	}
	return []byte{35, 2, h, 0, 0}
}

// Register binds the valve driver into the device registry under the hub
// type name "valve" (state files valve-<port>.json per spec §5, future API
// ids valve_N); the JSON identify block reports device_type
// "distribution_valve" per JSON_PROTOCOL.md. Called at app wiring time
// (PR 5); nothing calls it in this PR.
func Register() { device.Register(TypeCode, typeName, New) }

// New is the device.Factory for distribution valves.
func New(s *device.Session) device.Driver { return &Driver{s: s} }

// configBlock is the JSON config mirror (status/configure payloads). The
// firmware's config is RAM-only and write-only: this mirror is
// authoritative by construction — it is re-pushed at attach and on every
// reboot detection (TRANSLATION.md §4 configure).
type configBlock struct {
	DefaultRotation string `json:"default_rotation"`
	HoldTorque      bool   `json:"hold_torque"`
}

// persistState is the port-keyed on-disk schema (spec §5): the valve has no
// serial-number command, so state is keyed by the COM port. Persisted on
// every successful move so a SerialHop restart can recover homed state
// (TRANSLATION.md §3 step 3).
type persistState struct {
	SchemaVersion          int         `json:"schema_version"`
	PhysicalPosition       *int        `json:"physical_position"` // null while unhomed
	DeviceBeliefAtShutdown int         `json:"device_belief_at_shutdown"`
	Config                 configBlock `json:"config"`
}

// moveJob carries the driver-side details of the active move (the Jobs
// engine owns lifecycle/progress).
type moveJob struct {
	id             string
	fromPhysical   int
	targetPhysical int
	targetDevice   int
	direction      string
	estimate       time.Duration
}

// Driver implements device.Driver for the distribution valve. All fields
// are loop-owned: every method runs on the session goroutine (spec §3).
type Driver struct {
	s *device.Session

	positions int // N: outputs 1..N; the rotor has N+1 detents (0 = all closed)
	store     *device.Store

	homed        bool
	physicalPos  int // last verified true rotor position; valid only while homed
	deviceBelief int // the firmware's position counter (boot = 0 + every commanded move)

	config     configBlock
	lastPushed string // rotation mode most recently pushed to the firmware

	connectedSince time.Time
	lastCheck      time.Time // last CHECK_BELIEF, for Tick's idle cadence

	jobGen    int // bumps on job start/end/attach; guards stale After callbacks
	moveJob   *moveJob
	lastJobID string // most recent job (for status embedding)
}

// Attach implements TRANSLATION.md §3: derive the position count from the
// probe reply, read the device's position counter, recover port-keyed
// persistent state, and push the RAM-only config mirror (the firmware
// forgets it on every reboot).
func (d *Driver) Attach(ctx context.Context, probeReply []byte) (device.Info, error) {
	if len(probeReply) != 4 || probeReply[0] != TypeCode || probeReply[3] == 0 {
		return device.Info{}, fmt.Errorf("valve: unexpected probe reply %v", probeReply)
	}
	d.positions = int(probeReply[3])

	reply, err := d.s.Transact(queryPosFrame, 4, replyTimeout)
	if err != nil {
		return device.Info{}, fmt.Errorf("valve: position query: %w", err)
	}
	if reply[0] != TypeCode || int(reply[3]) > d.positions {
		return device.Info{}, fmt.Errorf("valve: unexpected position reply %v", reply)
	}
	d.deviceBelief = int(reply[3])

	d.store = d.s.Store(d.s.PortName())
	d.homed = false
	d.physicalPos = 0
	d.config = configBlock{DefaultRotation: "shortest", HoldTorque: false}
	var ps persistState
	found, err := d.store.Load(&ps)
	if err != nil {
		slog.Warn("valve: state file unreadable, treating as absent",
			"port", d.s.PortName(), "err", err)
		found = false
	}
	if found && ps.SchemaVersion == schemaV {
		if _, ok := rotationCode(ps.Config.DefaultRotation); ok {
			d.config = ps.Config
		}
		// TRANSLATION §3 step 3: recover homed state only when the device's
		// counter still matches the belief we persisted — proof the firmware
		// kept its counter (no reboot, no foreign host) while we were away.
		if ps.PhysicalPosition != nil && *ps.PhysicalPosition >= 0 &&
			*ps.PhysicalPosition <= d.positions &&
			d.deviceBelief == ps.DeviceBeliefAtShutdown {
			d.homed = true
			d.physicalPos = *ps.PhysicalPosition
		}
	}

	if cerr := d.pushConfig(); cerr != nil {
		return device.Info{}, fmt.Errorf("valve: config push: %s", cerr.Message)
	}

	// Volatile reset — also the recovery path after an unreachable episode.
	d.connectedSince = d.s.Now()
	d.lastCheck = d.connectedSince
	d.moveJob = nil
	d.jobGen++
	return d.info(), nil
}

type capabilities struct {
	Positions          int      `json:"positions"`
	RotationModes      []string `json:"rotation_modes"`
	SecondsPerPosition float64  `json:"seconds_per_position"`
}

func (d *Driver) info() device.Info {
	// Serial stays empty: the firmware has no serial-number command, so the
	// identify block omits it (spec §2.4, §9).
	return device.Info{
		DeviceType:      deviceType,
		Model:           fmt.Sprintf("radial-%d", d.positions),
		FirmwareVersion: firmwareVer,
		ProtocolVersion: protocolVer,
		Capabilities: capabilities{
			Positions:          d.positions,
			RotationModes:      []string{"shortest", "direct", "wrap"},
			SecondsPerPosition: SlotDuration.Seconds(),
		},
	}
}

// pushConfig sends the config mirror to the firmware (TRANSLATION.md §3
// step 4). The firmware's config is RAM-only: this runs at attach and after
// every reboot detection. Write-only — there is no config readback.
func (d *Driver) pushConfig() *device.CmdError {
	code, _ := rotationCode(d.config.DefaultRotation) // validated at every entry point
	if _, err := d.s.Transact(rotationFrame(code), 0, time.Second); err != nil {
		return device.ErrHardware("rotation config frame: " + err.Error())
	}
	d.lastPushed = d.config.DefaultRotation
	if _, err := d.s.Transact(holdFrame(d.config.HoldTorque), 0, time.Second); err != nil {
		return device.ErrHardware("hold config frame: " + err.Error())
	}
	return nil
}

// persistNow snapshots the persistent fields (TRANSLATION.md §1). Written
// on every successful move, home, configure, and detach — rare, human-paced
// events (spec §5).
func (d *Driver) persistNow() error {
	ps := persistState{
		SchemaVersion:          schemaV,
		DeviceBeliefAtShutdown: d.deviceBelief,
		Config:                 d.config,
	}
	if d.homed {
		pos := d.physicalPos // copy: never persist a pointer into live driver state
		ps.PhysicalPosition = &pos
	}
	return d.store.Save(ps)
}

// Execute dispatches one JSON command (identify/get_job are session-served).
func (d *Driver) Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *device.CmdError) {
	switch cmd {
	case "ping":
		return d.ping()
	case "status":
		return d.status()
	default:
		return nil, device.ErrUnknownCommand(cmd)
	}
}

// Tick runs the idle CHECK_BELIEF (TRANSLATION.md §5): every CheckInterval
// while no move is in flight, so silent reboots surface promptly. Never
// during a move — mid-move replies reflect the target, not the rotor.
func (d *Driver) Tick(now time.Time) {
	if d.moveJob != nil || now.Sub(d.lastCheck) < CheckInterval {
		return
	}
	_ = d.checkBelief() // a serial failure trips the session's unreachable handling
}

// Detach persists the final position knowledge — deliberately NO serial
// I/O: the firmware needs no goodbye and the port may already be dead.
func (d *Driver) Detach() {
	if d.store == nil {
		return // attach never got far enough to bind the store
	}
	if d.moveJob != nil {
		// An in-flight move finishes autonomously after we disconnect: the
		// frame was already accepted (its Transact succeeded), so the rotor
		// settles at the target. Persist that outcome; if the valve instead
		// loses power mid-move, the belief check refuses recovery on the
		// next attach — fail-safe either way.
		d.physicalPos = d.moveJob.targetPhysical
		d.deviceBelief = d.moveJob.targetDevice
	}
	if err := d.persistNow(); err != nil {
		slog.Warn("valve: detach persist failed", "port", d.s.PortName(), "err", err)
	}
}
