//go:build windows

package panel

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

// relaunchPanelExe spawns a detached copy of the panel binary. After a
// successful in-place update the file at os.Executable() is the new
// version, so re-invoking it with no args reopens the panel under the
// new build. CREATE_NEW_PROCESS_GROUP + DETACHED_PROCESS lets the child
// outlive the about-to-quit parent.
func relaunchPanelExe() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	cmd := exec.Command(exe) //nolint:gosec // exe path comes from os.Executable, not user input
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn panel: %w", err)
	}
	// Release the OS process handle so the child can exit independently
	// once the current process goes away.
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	return nil
}

// requestQuit asks Wails to quit the current panel after a short pause.
// The pause gives the freshly-spawned child a moment to come up so the
// taskbar handoff looks continuous to the operator.
func (a *App) requestQuit() {
	ctx := a.ctx
	if ctx == nil {
		return
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		runtime.Quit(ctx)
	}()
}
