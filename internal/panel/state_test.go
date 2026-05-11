package panel

import (
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

func TestComputeButtons(t *testing.T) {
	cases := []struct {
		name     string
		state    winsvc.ServiceState
		cfgValid bool
		want     ButtonState
	}{
		{
			name:     "not installed, valid config",
			state:    winsvc.StateNotInstalled,
			cfgValid: true,
			want:     ButtonState{Install: true},
		},
		{
			name:     "not installed, invalid config",
			state:    winsvc.StateNotInstalled,
			cfgValid: false,
			want:     ButtonState{},
		},
		{
			name:     "running",
			state:    winsvc.StateRunning,
			cfgValid: true,
			want:     ButtonState{Uninstall: true, Restart: true},
		},
		{
			name:     "stopped",
			state:    winsvc.StateStopped,
			cfgValid: true,
			want:     ButtonState{Uninstall: true, Restart: true},
		},
		{
			name:  "starting (transient, all disabled)",
			state: winsvc.StateStartPending,
			want:  ButtonState{},
		},
		{
			name:  "stopping (transient, all disabled)",
			state: winsvc.StateStopPending,
			want:  ButtonState{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeButtons(tc.state, tc.cfgValid)
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestStatusColor(t *testing.T) {
	cases := []struct {
		state    winsvc.ServiceState
		cfgValid bool
		want     StatusColor
	}{
		{winsvc.StateRunning, true, ColorGreen},
		{winsvc.StateStartPending, true, ColorYellow},
		{winsvc.StateStopPending, true, ColorYellow},
		{winsvc.StateStopped, true, ColorGrey},
		{winsvc.StateNotInstalled, true, ColorGrey},
		{winsvc.StateNotInstalled, false, ColorRed},
	}
	for _, tc := range cases {
		got := StatusIndicator(tc.state, tc.cfgValid)
		if got != tc.want {
			t.Errorf("state=%v cfgValid=%v: got %v, want %v", tc.state, tc.cfgValid, got, tc.want)
		}
	}
}
