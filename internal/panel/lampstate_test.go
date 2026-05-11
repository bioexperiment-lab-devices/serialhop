package panel

import (
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

func TestServerLampPresentation(t *testing.T) {
	cases := []struct {
		kind      lampKind
		wantColor StatusColor
		wantText  string
	}{
		{lampChecking, ColorGrey, "Checking…"},
		{lampOK, ColorGreen, "Up"},
		{lampChiselDown, ColorRed, "Chisel down"},
		{lampUnreachable, ColorGrey, "Unreachable"},
	}
	for _, tc := range cases {
		t.Run(tc.wantText, func(t *testing.T) {
			color, text := serverLampPresentation(netLamp{kind: tc.kind})
			if color != tc.wantColor {
				t.Errorf("color: got %v, want %v", color, tc.wantColor)
			}
			if text != tc.wantText {
				t.Errorf("text: got %q, want %q", text, tc.wantText)
			}
		})
	}
}

func TestTunnelLampPresentation(t *testing.T) {
	cases := []struct {
		kind      lampKind
		wantColor StatusColor
		wantText  string
	}{
		{lampChecking, ColorGrey, "Checking…"},
		{lampOK, ColorGreen, "Connected"},
		{lampDisconnected, ColorRed, "Disconnected"},
		{lampAuthFailed, ColorRed, "Auth failed"},
		{lampServerError, ColorYellow, "Server error"},
		{lampUnreachable, ColorGrey, "Unreachable"},
		{lampNotConfigured, ColorGrey, "Not configured"},
	}
	for _, tc := range cases {
		t.Run(tc.wantText, func(t *testing.T) {
			color, text := tunnelLampPresentation(netLamp{kind: tc.kind})
			if color != tc.wantColor {
				t.Errorf("color: got %v, want %v", color, tc.wantColor)
			}
			if text != tc.wantText {
				t.Errorf("text: got %q, want %q", text, tc.wantText)
			}
		})
	}
}

func TestLampStateSnapshotAndSetters(t *testing.T) {
	var s lampState

	// Zero-value snapshot.
	svc, srv, tun := s.snapshot()
	if svc.state != winsvc.StateNotInstalled || svc.cfgValid {
		t.Errorf("zero service: got %+v, want zero", svc)
	}
	if srv.kind != lampChecking || srv.detail != "" {
		t.Errorf("zero server: got %+v, want zero", srv)
	}
	if tun.kind != lampChecking || tun.detail != "" {
		t.Errorf("zero tunnel: got %+v, want zero", tun)
	}

	// Set each slot and verify only that slot changed.
	s.setService(serviceLamp{state: winsvc.StateRunning, cfgValid: true})
	s.setServer(netLamp{kind: lampOK, detail: "200 OK"})
	s.setTunnel(netLamp{kind: lampDisconnected, detail: "port=0"})

	svc, srv, tun = s.snapshot()
	if svc.state != winsvc.StateRunning || !svc.cfgValid {
		t.Errorf("service after set: got %+v", svc)
	}
	if srv.kind != lampOK || srv.detail != "200 OK" {
		t.Errorf("server after set: got %+v", srv)
	}
	if tun.kind != lampDisconnected || tun.detail != "port=0" {
		t.Errorf("tunnel after set: got %+v", tun)
	}
}

func TestServiceLampPresentation(t *testing.T) {
	cases := []struct {
		name      string
		state     winsvc.ServiceState
		cfgValid  bool
		wantColor StatusColor
		wantText  string
	}{
		{"running", winsvc.StateRunning, true, ColorGreen, "Running"},
		{"stopped", winsvc.StateStopped, true, ColorGrey, "Stopped"},
		{"start pending", winsvc.StateStartPending, true, ColorYellow, "Starting…"},
		{"stop pending", winsvc.StateStopPending, true, ColorYellow, "Stopping…"},
		{"not installed cfg valid", winsvc.StateNotInstalled, true, ColorGrey, "Not installed"},
		{"not installed cfg invalid", winsvc.StateNotInstalled, false, ColorRed, "Not installed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			color, text := serviceLampPresentation(serviceLamp{state: tc.state, cfgValid: tc.cfgValid})
			if color != tc.wantColor {
				t.Errorf("color: got %v, want %v", color, tc.wantColor)
			}
			if text != tc.wantText {
				t.Errorf("text: got %q, want %q", text, tc.wantText)
			}
		})
	}
}
