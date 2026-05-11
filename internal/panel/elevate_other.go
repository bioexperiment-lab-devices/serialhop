//go:build !windows

package panel

import "errors"

var ErrUserCancelled = errors.New("user cancelled")

func RunElevatedAdminAction(action string, _ ...string) (string, error) {
	return "", errors.New("RunElevatedAdminAction is only available on Windows")
}

func OpenWithDefaultApp(path string) error {
	return errors.New("OpenWithDefaultApp is only available on Windows")
}
