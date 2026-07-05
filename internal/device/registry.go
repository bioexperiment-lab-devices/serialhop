package device

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Driver is the per-device-type contract (spec §2.4). One instance per
// attached device; every method runs on the session goroutine.
type Driver interface {
	// Attach performs post-probe setup per the device's TRANSLATION.md §3:
	// read the serial number, push config mirrors, recover persistent state.
	// probeReply is the 4-byte identify reply discovery consumed (pump:
	// calibration bytes; valve: position count; densitometer: channels).
	// The returned Info is cached and served for `identify` and GET /devices.
	Attach(ctx context.Context, probeReply []byte) (Info, error)
	// Execute handles one JSON command. `identify` and `get_job` are served
	// by the session before reaching the driver.
	Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *CmdError)
	// Tick runs ~1/s while attached: canaries, monitoring schedulers.
	Tick(now time.Time)
	// Detach persists state and drops watchers; the session closes the port.
	Detach()
}

// Factory builds the driver bound to its session.
type Factory func(s *Session) Driver

// Info is the cached identify block (JSON_PROTOCOL.md §3 `identify`).
type Info struct {
	DeviceType      string `json:"device_type"`
	Model           string `json:"model"`
	Serial          string `json:"serial,omitempty"`
	FirmwareVersion string `json:"firmware_version"`
	ProtocolVersion string `json:"protocol_version"`
	Capabilities    any    `json:"capabilities"`
}

type driverEntry struct {
	name    string
	factory Factory
}

var (
	regMu   sync.RWMutex
	drivers = map[byte]driverEntry{}
)

// Register binds a probe type code to a driver factory. Called at app wiring
// time (not package init), so tests may register fakes under unused codes.
func Register(code byte, name string, f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	drivers[code] = driverEntry{name: name, factory: f}
}

// LookupDriver resolves a probe type code to its registered driver.
func LookupDriver(code byte) (string, Factory, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	e, ok := drivers[code]
	return e.name, e.factory, ok
}
