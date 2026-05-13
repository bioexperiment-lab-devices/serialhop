//go:build windows

package panel

import (
	"errors"

	"github.com/bioexperiment-lab-devices/serialhop/internal/winsvc"
)

// queryServiceState returns the current SCM state plus an "ok" flag.
// ok=false signals a transient SCM error (Connect failure, Query failure,
// etc.) — the panel should keep displaying the last-known state in that
// case to avoid blinking the indicator. ok=true with state==StateNotInstalled
// is the legitimate "service is not registered" reading. Uses
// DialSCMReadOnly so the panel can poll without admin elevation;
// install/uninstall/restart go through the elevated subprocess.
func queryServiceState() (winsvc.ServiceState, bool) {
	scm, err := winsvc.DialSCMReadOnly()
	if err != nil {
		return winsvc.StateNotInstalled, false
	}
	defer scm.Disconnect() //nolint:errcheck
	s, err := scm.OpenService(winsvc.ServiceName)
	if err != nil {
		if errors.Is(err, winsvc.ErrServiceMissing) {
			return winsvc.StateNotInstalled, true
		}
		return winsvc.StateNotInstalled, false
	}
	defer s.Close() //nolint:errcheck
	st, err := s.Query()
	if err != nil {
		return winsvc.StateStopped, false
	}
	return st, true
}
