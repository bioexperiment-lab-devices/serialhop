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
	detail string // raw error / chisel detail (internal; not shown to operators)
	sub    string // operator-visible sub line (host, "remote port 29017", etc.)
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

// serverLampPresentation maps a netLamp to the color, label, and operator-
// visible sub line shown in the Server row.
func serverLampPresentation(v netLamp) (StatusColor, string, string) {
	switch v.kind {
	case lampChecking:
		return ColorGrey, "Checking…", ""
	case lampOK:
		return ColorGreen, "Up", v.sub
	case lampChiselDown:
		return ColorRed, "Chisel down", v.sub
	case lampUnreachable:
		return ColorGrey, "Unreachable", v.sub
	default:
		// lampDisconnected / lampAuthFailed / lampServerError / lampNotConfigured
		// are tunnel-only states; fall back to "Unreachable" if we somehow get
		// them in a server context.
		return ColorGrey, "Unreachable", ""
	}
}

// tunnelLampPresentation maps a netLamp to the color, label, and sub line
// shown in the Tunnel row.
func tunnelLampPresentation(v netLamp) (StatusColor, string, string) {
	switch v.kind {
	case lampChecking:
		return ColorGrey, "Checking…", ""
	case lampOK:
		return ColorGreen, "Connected", v.sub
	case lampDisconnected:
		return ColorRed, "Disconnected", ""
	case lampAuthFailed:
		return ColorRed, "Auth failed", ""
	case lampServerError:
		return ColorYellow, "Server error", ""
	case lampUnreachable:
		return ColorGrey, "Unreachable", ""
	case lampNotConfigured:
		return ColorGrey, "Not configured", ""
	default:
		return ColorGrey, "Unreachable", ""
	}
}

// serviceLampPresentation maps a serviceLamp to the color, label, and sub
// line shown in the Service row. Reuses StatusIndicator() for the color
// (which already encodes "red iff not-installed-and-config-invalid").
func serviceLampPresentation(v serviceLamp) (StatusColor, string, string) {
	color := StatusIndicator(v.state, v.cfgValid)
	var text, sub string
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
		if !v.cfgValid {
			sub = "config invalid"
		}
	default:
		text = v.state.String()
	}
	return color, text, sub
}
