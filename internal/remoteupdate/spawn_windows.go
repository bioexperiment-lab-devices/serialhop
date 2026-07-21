//go:build windows

package remoteupdate

import (
	"fmt"
	"os/exec"

	"golang.org/x/sys/windows"
)

// SpawnDetached launches exe with args as a detached LocalSystem process that
// survives this (service) process being stopped by the SCM. No window, new
// process group, no inherited handles — so when the child stops the service,
// the child keeps running to finish the swap.
func SpawnDetached(exe string, args []string) error {
	cmd := exec.Command(exe, args...) //nolint:gosec // exe is os.Executable() of the service; args are internal flags
	cmd.SysProcAttr = &windows.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn detached update child: %w", err)
	}
	// Do not Wait: the child must outlive us. Release the process handle.
	return cmd.Process.Release()
}
