package panel

import "github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"

type ButtonState struct {
	Install   bool
	Uninstall bool
	Restart   bool
}

type StatusColor int

const (
	ColorGrey StatusColor = iota
	ColorYellow
	ColorGreen
	ColorRed
)

// ComputeButtons returns which admin buttons should be enabled given the
// current SCM state and whether the config validates. The file-action
// buttons ("Open config file", "Open logs folder") are not gated through
// this function — they're enabled whenever paths.EnsureDirs() succeeded
// at panel startup.
func ComputeButtons(state winsvc.ServiceState, cfgValid bool) ButtonState {
	var bs ButtonState
	switch state {
	case winsvc.StateNotInstalled:
		bs.Install = cfgValid
	case winsvc.StateStopped, winsvc.StateRunning:
		bs.Uninstall = true
		bs.Restart = true
	case winsvc.StateStartPending, winsvc.StateStopPending:
		// transient states: nothing enabled
	}
	return bs
}

// StatusIndicator returns the color of the status dot for a given state.
// Red is reserved for "not installed AND config invalid".
func StatusIndicator(state winsvc.ServiceState, cfgValid bool) StatusColor {
	switch state {
	case winsvc.StateRunning:
		return ColorGreen
	case winsvc.StateStartPending, winsvc.StateStopPending:
		return ColorYellow
	case winsvc.StateStopped:
		return ColorGrey
	case winsvc.StateNotInstalled:
		if !cfgValid {
			return ColorRed
		}
		return ColorGrey
	default:
		return ColorGrey
	}
}
