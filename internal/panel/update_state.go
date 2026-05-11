package panel

// UpdateState is the state of the update row in the control panel.
// See docs/superpowers/specs/2026-05-11-auto-update-design.md §4.1.
type UpdateState int

const (
	UpdateIdle UpdateState = iota
	UpdateAvailable
	UpdateDownloading
	UpdateDownloadFailed
	UpdateReady
	UpdateInstalling
	UpdateInstalled
	UpdateInstallFailed
)

// UpdateEvent is an input to the state machine.
type UpdateEvent int

const (
	EvUpdateAvailable UpdateEvent = iota
	EvDownloadStart
	EvDownloadOK
	EvDownloadFail
	EvInstallStart
	EvInstallOK
	EvInstallFail
	EvRetry
	EvHide
	EvCancel
)

// nextUpdateState returns the new state given the current state and an
// event. Unrecognized transitions leave the state unchanged so the panel
// goroutine doesn't have to know every "this can't happen" combination.
func nextUpdateState(cur UpdateState, ev UpdateEvent) UpdateState {
	switch cur {
	case UpdateIdle:
		if ev == EvUpdateAvailable {
			return UpdateAvailable
		}
	case UpdateAvailable:
		switch ev {
		case EvDownloadStart:
			return UpdateDownloading
		case EvHide:
			return UpdateIdle
		}
	case UpdateDownloading:
		switch ev {
		case EvDownloadOK:
			return UpdateReady
		case EvDownloadFail:
			return UpdateDownloadFailed
		case EvCancel:
			return UpdateAvailable
		}
	case UpdateDownloadFailed:
		if ev == EvRetry {
			return UpdateAvailable
		}
	case UpdateReady:
		if ev == EvInstallStart {
			return UpdateInstalling
		}
	case UpdateInstalling:
		switch ev {
		case EvInstallOK:
			return UpdateInstalled
		case EvInstallFail:
			return UpdateInstallFailed
		case EvCancel:
			return UpdateReady
		}
	case UpdateInstallFailed:
		if ev == EvRetry {
			return UpdateReady
		}
	case UpdateInstalled:
		if ev == EvHide {
			return UpdateIdle
		}
	}
	return cur
}
