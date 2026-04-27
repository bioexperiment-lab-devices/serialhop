//go:build !windows

package panel

import "errors"

func Run() error {
	return errors.New("panel.Run is only available on Windows")
}
