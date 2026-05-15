//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

func enforceElevation() error {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token)
	if err != nil {
		return err
	}
	defer token.Close()
	if !token.IsElevated() {
		return errors.New(
			"this installer must be run as administrator; right-click → " +
				"Run as administrator, or re-run and approve the UAC prompt")
	}
	return nil
}
