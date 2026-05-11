package panel

import (
	"sync"

	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

// lampKind enumerates the abstract states the two network lamps can be in.
// Per-lamp text (e.g. "Up" vs "Connected" for lampOK) is resolved by the
// per-lamp presentation functions below.
type lampKind int

const (
	lampChecking lampKind = iota
	lampOK
	lampDisconnected
	lampAuthFailed
	lampServerError
	lampUnreachable
	lampNotConfigured
	lampChiselDown
)

// netLamp is the state of a network-probed lamp (Server or Tunnel).
type netLamp struct {
	kind   lampKind
	detail string // optional human-readable extra info (currently unused; reserved for tooltip)
}

// serviceLamp is the state of the local-service lamp.
type serviceLamp struct {
	state    winsvc.ServiceState
	cfgValid bool
}

// lampState is the panel's shared mutable view of all three lamps.
// All access goes through mu.
type lampState struct {
	mu      sync.Mutex
	service serviceLamp
	server  netLamp
	tunnel  netLamp
}

// snapshot returns a copy of the current lamp triple under the mutex.
// Used by the GUI paint loop.
func (s *lampState) snapshot() (serviceLamp, netLamp, netLamp) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.service, s.server, s.tunnel
}

// setService writes only the service-lamp slot.
func (s *lampState) setService(v serviceLamp) {
	s.mu.Lock()
	s.service = v
	s.mu.Unlock()
}

// setServer writes only the server-lamp slot.
func (s *lampState) setServer(v netLamp) {
	s.mu.Lock()
	s.server = v
	s.mu.Unlock()
}

// setTunnel writes only the tunnel-lamp slot.
func (s *lampState) setTunnel(v netLamp) {
	s.mu.Lock()
	s.tunnel = v
	s.mu.Unlock()
}

// serverLampPresentation maps a netLamp to the color and label text shown
// in the Server row.
func serverLampPresentation(v netLamp) (StatusColor, string) {
	switch v.kind {
	case lampChecking:
		return ColorGrey, "Checking…"
	case lampOK:
		return ColorGreen, "Up"
	case lampChiselDown:
		return ColorRed, "Chisel down"
	case lampUnreachable:
		return ColorGrey, "Unreachable"
	default:
		// lampDisconnected / lampAuthFailed / lampServerError / lampNotConfigured
		// are tunnel-only states; fall back to "Unreachable" if we somehow get
		// them in a server context.
		return ColorGrey, "Unreachable"
	}
}

// tunnelLampPresentation maps a netLamp to the color and label text shown
// in the Tunnel row.
func tunnelLampPresentation(v netLamp) (StatusColor, string) {
	switch v.kind {
	case lampChecking:
		return ColorGrey, "Checking…"
	case lampOK:
		return ColorGreen, "Connected"
	case lampDisconnected:
		return ColorRed, "Disconnected"
	case lampAuthFailed:
		return ColorRed, "Auth failed"
	case lampServerError:
		return ColorYellow, "Server error"
	case lampUnreachable:
		return ColorGrey, "Unreachable"
	case lampNotConfigured:
		return ColorGrey, "Not configured"
	default:
		return ColorGrey, "Unreachable"
	}
}

// serviceLampPresentation maps a serviceLamp to the color and label text
// shown in the Service row. Reuses StatusIndicator() for the color
// (which already encodes "red iff not-installed-and-config-invalid").
func serviceLampPresentation(v serviceLamp) (StatusColor, string) {
	color := StatusIndicator(v.state, v.cfgValid)
	var text string
	switch v.state {
	case winsvc.StateRunning:
		text = "Running"
	case winsvc.StateStartPending:
		text = "Starting…"
	case winsvc.StateStopPending:
		text = "Stopping…"
	case winsvc.StateStopped:
		text = "Stopped"
	case winsvc.StateNotInstalled:
		text = "Not installed"
	default:
		text = v.state.String()
	}
	return color, text
}
