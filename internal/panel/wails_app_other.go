//go:build !windows

package panel

import "errors"

// Run is a non-Windows stub so the package builds on macOS/Linux CI.
// The panel only ships on Windows; on other platforms invoking it is
// a programming error.
func Run() error {
	return errors.New("panel.Run is only available on Windows")
}
