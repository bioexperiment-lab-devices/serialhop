package pump

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// TypeCode is the pump's probe identify code (PROTOCOL.md §3).
const TypeCode = 10

const (
	deviceType   = "pump"
	model        = "peristaltic-1ch"
	firmwareVer  = "legacy"
	protocolVer  = "1.0"
	schemaV      = 1
	replyTimeout = 2 * time.Second // 4-byte replies arrive within ~50 ms
)

// Command frames (PROTOCOL.md §4). identifyFrame is the only frame safe for
// polling — it writes nothing to EEPROM. serialFrame IS stored as the
// device's "last command", which the end-of-job panel disarm exploits.
var (
	identifyFrame = []byte{1, 2, 3, 0, 0}
	serialFrame   = []byte{11, 2, 3, 4, 5}
	pauseFrame    = []byte{19, 0, 0, 0, 0}
	stopFrame     = []byte{10, 0, 0, 0, 0}
)

// Register binds the pump driver into the device registry. Called at app
// wiring time (PR 5); nothing calls it in this PR.
func Register() { device.Register(TypeCode, deviceType, New) }

// New is the device.Factory for pumps.
func New(s *device.Session) device.Driver { return &Driver{s: s} }

type pumpState string

// JSON status.state values (JSON_PROTOCOL.md §3 status).
const (
	stateIdle        pumpState = "idle"
	stateRotating    pumpState = "rotating"
	stateDispensing  pumpState = "dispensing"
	stateCalibrating pumpState = "calibrating"
	statePaused      pumpState = "paused"
)

// persistState is the serial-keyed on-disk schema (spec §5).
type persistState struct {
	SchemaVersion int       `json:"schema_version"`
	MlPerStep     float64   `json:"ml_per_step"`
	SetAt         time.Time `json:"set_at"`
	Serial        string    `json:"serial"`
}

// motionJob carries the driver-side details of the active job (the Jobs
// engine owns lifecycle/progress; this holds what the pump needs to build
// results and completion handling).
type motionJob struct {
	id         string
	kind       string // "dispense" | "calibration"
	direction  string
	volumeML   float64
	steps      int64 // commanded count, includes suckback inflation
	delTimeUs  float64
	speedML    float64 // actual quantized ml/min; 0 in gradient/raw mode
	suckbackML float64 // actual quantized echo value
	gradient   bool
	estimate   time.Duration
}

// watchHandle wires the loop to one opcode-18 watcher goroutine.
// stop: loop → watcher abandon signal. done: watcher → loop exit signal
// (closed before the watcher's final Post so the loop may safely block on
// it). timedOut is loop-owned bookkeeping set by the watchdog.
type watchHandle struct {
	stop     chan struct{}
	done     chan struct{}
	timedOut bool
}

// Driver implements device.Driver for the peristaltic pump. All fields are
// loop-owned: every method runs on the session goroutine (spec §3).
type Driver struct {
	s *device.Session

	serial     string
	store      *device.Store
	mlPerStep  float64 // 0 = not calibrated
	calSetAt   time.Time
	unverified bool // recovered from the device's EEPROM mirror, unconfirmed

	connectedSince time.Time
	state          pumpState
	pausedFrom     pumpState // state resume returns to
	pauseAssumed   bool      // belief about the firmware's blind cmd-19 toggle

	rotDirection string
	rotSpeedML   float64 // actual quantized; 0 when unknown (raw/uncalibrated)
	rotSpeedPct  int     // last rotate_raw percentage, 0 otherwise

	jobGen       int // bumps on job start/stop/attach; guards stale timers+watchers
	job          *motionJob
	lastJobID    string // most recent job (for status embedding)
	lastJobKind  string
	lastVolumeML float64 // volume of the most recent dispense job
	watch        *watchHandle
}

// Attach implements TRANSLATION §3: read the serial number, recover
// persistent calibration (store first, EEPROM mirror as unverified
// fallback), reset volatile state.
func (d *Driver) Attach(ctx context.Context, probeReply []byte) (device.Info, error) {
	if len(probeReply) != 4 || probeReply[0] != TypeCode {
		return device.Info{}, fmt.Errorf("pump: unexpected probe reply %v", probeReply)
	}
	calMirror := uint32(probeReply[1])<<16 | uint32(probeReply[2])<<8 | uint32(probeReply[3])

	reply, err := d.s.Transact(serialFrame, 4, replyTimeout)
	if err != nil {
		return device.Info{}, fmt.Errorf("pump: serial number read: %w", err)
	}
	if reply[0] != TypeCode {
		return device.Info{}, fmt.Errorf("pump: unexpected serial reply %v", reply)
	}
	d.serial = fmt.Sprintf("%d-%03d", reply[1], reply[2])

	d.store = d.s.Store(d.serial)
	d.mlPerStep, d.calSetAt, d.unverified = 0, time.Time{}, false
	var ps persistState
	found, err := d.store.Load(&ps)
	if err != nil {
		slog.Warn("pump: state file unreadable, treating as absent", "device", d.serial, "err", err)
		found = false
	}
	switch {
	case found && ps.SchemaVersion == schemaV && ps.MlPerStep > 0:
		d.mlPerStep, d.calSetAt = ps.MlPerStep, ps.SetAt
	case calMirror > 0:
		// TRANSLATION §3 step 3: propose the EEPROM mirror, but devices
		// calibrated under the legacy host may hold bytes with different
		// semantics — require confirmation before metered dispensing.
		d.mlPerStep, d.unverified = float64(calMirror)/1e8, true
	}

	// Volatile reset — also the reboot-recovery path (TRANSLATION §5):
	// after a re-probe, state = idle and the pause toggle boots "running".
	d.connectedSince = d.s.Now()
	d.state, d.pausedFrom, d.pauseAssumed = stateIdle, "", false
	d.rotDirection, d.rotSpeedML, d.rotSpeedPct = "", 0, 0
	d.job, d.watch = nil, nil
	d.jobGen++
	return d.info(), nil
}

type speedRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

type capabilities struct {
	Channels             int         `json:"channels"`
	SpeedMlMin           *speedRange `json:"speed_ml_min"`
	SupportsGradient     bool        `json:"supports_gradient"`
	SupportsDropSuckback bool        `json:"supports_drop_suckback"`
	// CalibrationUnverified flags an ml_per_step recovered from the device
	// mirror that has not been confirmed (TRANSLATION §3 step 3).
	CalibrationUnverified bool `json:"calibration_unverified,omitempty"`
}

func (d *Driver) info() device.Info {
	caps := capabilities{
		Channels: 1, SupportsGradient: true, SupportsDropSuckback: true,
		CalibrationUnverified: d.unverified,
	}
	if d.mlPerStep > 0 && !d.unverified {
		caps.SpeedMlMin = &speedRange{
			Min: actualSpeedMlMin(d.mlPerStep, maxDelTimeUs),
			Max: actualSpeedMlMin(d.mlPerStep, MinDelTimeUs),
		}
	}
	return device.Info{
		DeviceType: deviceType, Model: model, Serial: d.serial,
		FirmwareVersion: firmwareVer, ProtocolVersion: protocolVer,
		Capabilities: caps,
	}
}

// requireCalibration gates metered (ml-denominated) commands.
func (d *Driver) requireCalibration() *device.CmdError {
	if d.mlPerStep <= 0 {
		return device.ErrNotCalibrated("no volume calibration stored")
	}
	if d.unverified {
		e := device.ErrNotCalibrated(
			"device calibration mirror is unverified — confirm with set_calibration or run start_calibration")
		e.Details = map[string]any{
			"reason": "unverified_mirror", "proposed_ml_per_step": d.mlPerStep,
		}
		return e
	}
	return nil
}

// Execute dispatches one JSON command (identify/get_job are session-served).
func (d *Driver) Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *device.CmdError) {
	switch cmd {
	default:
		return nil, device.ErrUnknownCommand(cmd)
	}
}

// Tick is a no-op: the pump has no canaries or monitoring schedule, and the
// EEPROM-wear rules forbid periodic traffic (TRANSLATION §5).
func (d *Driver) Tick(now time.Time) {}

// Detach drops the watcher and leaves the motor stopped if motion was
// active. Write-only; tolerates a dead port (the session publishes
// connected=false before calling Detach, so a failed write cannot trigger
// the unreachable machinery mid-shutdown).
func (d *Driver) Detach() {
	if d.watch != nil {
		close(d.watch.stop)
		d.watch = nil
	}
	switch d.state {
	case stateRotating, stateDispensing, stateCalibrating, statePaused:
		_, _ = d.s.Transact(stopFrame, 0, time.Second)
	}
}
