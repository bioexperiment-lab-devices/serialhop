//go:build !windows

package winsvc

import "errors"

func RunWorker() error {
	return errors.New("RunWorker is only available on Windows")
}
