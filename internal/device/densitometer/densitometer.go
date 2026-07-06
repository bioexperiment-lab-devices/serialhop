package densitometer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// TypeCode is the densitometer's probe identify code (PROTOCOL.md §3: Name1 = 70).
const TypeCode = 70

const (
	deviceType  = "densitometer"
	model       = "TDS909A-wide"
	firmwareVer = "legacy"
	protocolVer = "1.0"
	schemaV     = 1

	thermoMinC = 20.0
	thermoMaxC = 45.0
)

// Command frames (PROTOCOL.md §4). Frames with N5=0 satisfy the firmware guard;
// pingFrame is the liveness/keepalive (71 2 3 4 0 → 70 5 T_int T_frac).
var (
	serialNumFrame = []byte{71, 0, 0, 5, 0}
	channel1Frame  = []byte{71, 0, 0, 1, 0}
	forceTubeFrame = []byte{75, 3, 0, 0, 0}
	pingFrame      = []byte{71, 2, 3, 4, 0}
	tempFrame      = []byte{76, 0, 0, 0, 0}
	thermReadFrame = []byte{76, 2, 0, 0, 0}
	stopFrame      = []byte{70, 0, 0, 0, 0}
	arrayReadFrame = []byte{79, 1, 0, 0, 0}
)

// persistState is the serial-keyed on-disk schema (spec §5, decision 4).
type persistState struct {
	SchemaVersion  int              `json:"schema_version"`
	Blank          *blankState      `json:"blank"`           // nil until measure_blank
	TubeCorrection float64          `json:"tube_correction"` // default 1.0
	Thermostat     thermostatMirror `json:"thermostat"`
}

type blankState struct {
	Slope        float64   `json:"slope"`
	TemperatureC float64   `json:"temperature_c"`
	MeasuredAt   time.Time `json:"measured_at"`
}

// thermostatMirror is the driver's belief of the device set-point. Its value is
// NEVER 10 (only 0 when disabled, or 20..45) — the reboot-canary invariant.
type thermostatMirror struct {
	Enabled bool    `json:"enabled"`
	TargetC float64 `json:"target_c"`
}

// mirrorValue is the °C the device set-point should read back as: 0 when
// disabled, else the target. (target < 20 disables on the firmware.)
func (m thermostatMirror) mirrorValue() float64 {
	if !m.Enabled {
		return 0
	}
	return m.TargetC
}

// reading is one buffered measurement. Wire-exposed fields feed get_readings;
// tubeCorrectionAt is internal, used by calibrate_tube to recover the
// uncorrected value.
type reading struct {
	seq              int64
	measuredAt       time.Time
	uptimeMs         int64
	absorbance       float64 // temperature-compensated, tube-corrected
	temperatureC     float64
	tubeCorrectionAt float64
}

// sweep carries the driver-side detail of the active sweep job (the Jobs engine
// owns lifecycle/progress; this holds what completion needs).
type sweep struct {
	gen        int
	kind       string // "blank" | "measure" | "read_raw" | "monitor"
	includeRaw bool   // measure: attach the 20-point sweep to the result
	level      int    // read_raw: 0 = full 20-level sweep, n = single-level read
}

// monitoringState tracks the periodic auto-measurement scheduler.
type monitoringState struct {
	enabled    bool
	intervalS  int
	nextTickAt time.Time
}

// Driver implements device.Driver for the TDS909A-wide densitometer. All
// fields are loop-owned: every method runs on the session goroutine.
type Driver struct {
	s      *device.Session
	serial string
	store  *device.Store

	wavelengthNm   int
	connectedSince time.Time

	// persistent (mirrored in memory; saved via persist())
	blank          *blankState
	tubeCorrection float64
	thermo         thermostatMirror

	// volatile
	busyUntil    time.Time
	sweep        *sweep
	sweepGen     int
	lastReading  *reading // newest completed measurement, for status.last_measurement
	ring         *ringBuffer
	seqCounter   int64
	monitoring   monitoringState
	nextCanaryAt time.Time

	// cached for the busy-window status path
	cachedTemp   float64
	cachedTempAt time.Time
	haveCachTemp bool
}

// Register binds the densitometer driver into the device registry. Called at
// app wiring time (PR 5); nothing calls it in this PR.
func Register() { device.Register(TypeCode, deviceType, New) }

// New is the device.Factory for densitometers.
func New(s *device.Session) device.Driver { return &Driver{s: s} }

// Attach implements TRANSLATION §3: read serial + wavelength, force the
// device-side tube correction to 1.0, recover persistent state, sync the
// thermostat mirror (which arms the reboot canary). probeReply is the 4-byte
// identify reply discovery consumed ([70, _, _, channels]).
func (d *Driver) Attach(ctx context.Context, probeReply []byte) (device.Info, error) {
	if len(probeReply) != 4 || probeReply[0] != TypeCode {
		return device.Info{}, fmt.Errorf("densitometer: unexpected probe reply %v", probeReply)
	}

	snReply, err := d.s.Transact(serialNumFrame, 4, replyTimeout)
	if err != nil {
		return device.Info{}, fmt.Errorf("densitometer: serial read: %w", err)
	}
	d.serial = formatSerial(snReply[2], snReply[3])

	wlReply, err := d.s.Transact(channel1Frame, 4, replyTimeout)
	if err != nil {
		return device.Info{}, fmt.Errorf("densitometer: wavelength read: %w", err)
	}
	d.wavelengthNm = int(wlReply[2])*100 + int(wlReply[3])

	// TRANSLATION §3 step 4: force the device factor to 1.0. EEPROM-persistent,
	// so it survives reboots — from here all tube correction is driver-side.
	if _, err := d.s.Transact(forceTubeFrame, 0, replyTimeout); err != nil {
		return device.Info{}, fmt.Errorf("densitometer: force tube correction: %w", err)
	}

	// Recover persistent state before the thermostat sync (which needs the mirror).
	d.store = d.s.Store(d.serial)
	d.blank, d.tubeCorrection, d.thermo = nil, 1.0, thermostatMirror{}
	var ps persistState
	found, lerr := d.store.Load(&ps)
	if lerr != nil {
		slog.Warn("densitometer: state file unreadable, treating as absent",
			"device", d.serial, "err", lerr)
		found = false
	}
	if found && ps.SchemaVersion == schemaV {
		d.blank = ps.Blank
		if ps.TubeCorrection > 0 {
			d.tubeCorrection = ps.TubeCorrection
		}
		d.thermo = ps.Thermostat
	}

	// Volatile reset (also the reboot-recovery path).
	d.connectedSince = d.s.Now()
	d.busyUntil = time.Time{}
	d.sweep, d.lastReading = nil, nil
	d.sweepGen++
	d.seqCounter = 0
	d.monitoring = monitoringState{}
	d.ring = newRingBuffer()
	d.nextCanaryAt = d.s.Now().Add(CanaryInterval)

	if err := d.syncThermostat(found && ps.SchemaVersion == schemaV); err != nil {
		return device.Info{}, err
	}
	return d.info(), nil
}

type thermostatCaps struct {
	MinC float64 `json:"min_c"`
	MaxC float64 `json:"max_c"`
}

type capabilities struct {
	WavelengthNm      int            `json:"wavelength_nm"`
	BrightnessLevels  int            `json:"brightness_levels"`
	Thermostat        thermostatCaps `json:"thermostat"`
	TemperatureSensor string         `json:"temperature_sensor"`
}

func (d *Driver) info() device.Info {
	return device.Info{
		DeviceType: deviceType, Model: model, Serial: d.serial,
		FirmwareVersion: firmwareVer, ProtocolVersion: protocolVer,
		Capabilities: capabilities{
			WavelengthNm:      d.wavelengthNm,
			BrightnessLevels:  20,
			Thermostat:        thermostatCaps{MinC: thermoMinC, MaxC: thermoMaxC},
			TemperatureSensor: "DS18B20",
		},
	}
}

// Execute dispatches one JSON command. identify/get_job are session-served.
func (d *Driver) Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *device.CmdError) {
	switch cmd {
	// handlers wired in later tasks:
	// ping, status, stop, stop_monitoring, set_thermostat, set_tube_correction,
	// calibrate_tube, set_led, measure, measure_blank, read_raw,
	// start_monitoring, get_readings
	default:
		return nil, device.ErrUnknownCommand(cmd)
	}
}

// Tick runs ~1/s while attached (filled in Task 8: idle canary + monitoring).
func (d *Driver) Tick(now time.Time) {}

// Detach persists current state; it performs no serial I/O (state is already
// saved on every mutation, so this is belt-and-suspenders and safe on a dead
// port).
func (d *Driver) Detach() {
	if d.store != nil {
		if err := d.persist(); err != nil {
			slog.Warn("densitometer: detach persist failed", "device", d.serial, "err", err)
		}
	}
}

// persist writes the serial-keyed state file (spec §5).
func (d *Driver) persist() error {
	return d.store.Save(persistState{
		SchemaVersion:  schemaV,
		Blank:          d.blank,
		TubeCorrection: d.tubeCorrection,
		Thermostat:     d.thermo,
	})
}

// serialGate rejects commands that would touch the port during a sweep's
// busy window (decision 1). status serves cached values instead of gating.
func (d *Driver) serialGate() *device.CmdError {
	if d.s.Now().Before(d.busyUntil) {
		return device.ErrBusy("device is mid-sweep",
			map[string]any{"busy_ms": d.busyUntil.Sub(d.s.Now()).Milliseconds()})
	}
	return nil
}

// busyGuard rejects a job-starting command while one is already active.
func (d *Driver) busyGuard() *device.CmdError {
	if j := d.s.Jobs().Active(); j != nil {
		return device.ErrBusy("a job is running", map[string]any{"job_id": j.ID})
	}
	return nil
}

// TEMPORARY (replaced in Task 3): first-contact disable only.
func (d *Driver) syncThermostat(hasMirror bool) error {
	if _, err := d.s.Transact(thermReadFrame, 4, replyTimeout); err != nil {
		return fmt.Errorf("densitometer: thermostat read: %w", err)
	}
	if _, err := d.s.Transact([]byte{75, 2, 0, 0, 0}, 0, replyTimeout); err != nil {
		return fmt.Errorf("densitometer: thermostat disable: %w", err)
	}
	d.thermo = thermostatMirror{Enabled: false, TargetC: 0}
	return d.persist()
}

// TEMPORARY (replaced in Task 8): ring buffer stub.
func newRingBuffer() *ringBuffer { return &ringBuffer{} }

type ringBuffer struct{}
