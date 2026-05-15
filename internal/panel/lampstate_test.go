package panel

import (
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

func TestServerLampPresentation(t *testing.T) {
	cases := []struct {
		name      string
		v         netLamp
		wantColor StatusColor
		wantText  string
		wantSub   string
	}{
		{"checking", netLamp{kind: lampChecking}, ColorGrey, "Checking…", ""},
		{"ok with host", netLamp{kind: lampOK, sub: "lab.example.com"}, ColorGreen, "Up", "lab.example.com"},
		{"chisel down", netLamp{kind: lampChiselDown, sub: "lab.example.com"}, ColorRed, "Chisel down", "lab.example.com"},
		{"unreachable", netLamp{kind: lampUnreachable}, ColorGrey, "Unreachable", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			color, text, sub := serverLampPresentation(tc.v)
			if color != tc.wantColor {
				t.Errorf("color: got %v, want %v", color, tc.wantColor)
			}
			if text != tc.wantText {
				t.Errorf("text: got %q, want %q", text, tc.wantText)
			}
			if sub != tc.wantSub {
				t.Errorf("sub: got %q, want %q", sub, tc.wantSub)
			}
		})
	}
}

func TestTunnelLampPresentation(t *testing.T) {
	cases := []struct {
		name      string
		v         netLamp
		wantColor StatusColor
		wantText  string
		wantSub   string
	}{
		{"checking", netLamp{kind: lampChecking}, ColorGrey, "Checking…", ""},
		{"connected with port", netLamp{kind: lampOK, sub: "remote port 29017"}, ColorGreen, "Connected", "remote port 29017"},
		{"disconnected", netLamp{kind: lampDisconnected}, ColorRed, "Disconnected", ""},
		{"auth failed", netLamp{kind: lampAuthFailed}, ColorRed, "Auth failed", ""},
		{"server error", netLamp{kind: lampServerError}, ColorYellow, "Server error", ""},
		{"unreachable", netLamp{kind: lampUnreachable}, ColorGrey, "Unreachable", ""},
		{"not configured", netLamp{kind: lampNotConfigured}, ColorGrey, "Not configured", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			color, text, sub := tunnelLampPresentation(tc.v)
			if color != tc.wantColor {
				t.Errorf("color: got %v, want %v", color, tc.wantColor)
			}
			if text != tc.wantText {
				t.Errorf("text: got %q, want %q", text, tc.wantText)
			}
			if sub != tc.wantSub {
				t.Errorf("sub: got %q, want %q", sub, tc.wantSub)
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
	s.setServer(netLamp{kind: lampOK, detail: "200 OK", sub: "lab.example.com"})
	s.setTunnel(netLamp{kind: lampDisconnected, detail: "port=0"})

	svc, srv, tun = s.snapshot()
	if svc.state != winsvc.StateRunning || !svc.cfgValid {
		t.Errorf("service after set: got %+v", svc)
	}
	if srv.kind != lampOK || srv.detail != "200 OK" || srv.sub != "lab.example.com" {
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
		wantSub   string
	}{
		{"running", winsvc.StateRunning, true, ColorGreen, "Running", ""},
		{"stopped", winsvc.StateStopped, true, ColorGrey, "Stopped", ""},
		{"start pending", winsvc.StateStartPending, true, ColorYellow, "Starting…", ""},
		{"stop pending", winsvc.StateStopPending, true, ColorYellow, "Stopping…", ""},
		{"not installed cfg valid", winsvc.StateNotInstalled, true, ColorGrey, "Not installed", ""},
		{"not installed cfg invalid", winsvc.StateNotInstalled, false, ColorRed, "Not installed", "config invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			color, text, sub := serviceLampPresentation(serviceLamp{state: tc.state, cfgValid: tc.cfgValid})
			if color != tc.wantColor {
				t.Errorf("color: got %v, want %v", color, tc.wantColor)
			}
			if text != tc.wantText {
				t.Errorf("text: got %q, want %q", text, tc.wantText)
			}
			if sub != tc.wantSub {
				t.Errorf("sub: got %q, want %q", sub, tc.wantSub)
			}
		})
	}
}
