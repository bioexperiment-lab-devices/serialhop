//go:build windows

package streamer

import (
	"os/exec"
	"strconv"

	"golang.org/x/sys/windows"
)

// applyPlatformAttrs ensures the child gets its own process group so we
// can deliver CTRL_BREAK_EVENT to it (and only it).
func applyPlatformAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
	}
}

func signalGraceful(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))
}

// hardKill uses `taskkill /pid <pid> /T /F` to take down the child plus
// any descendants. cmd.Process.Kill() would only kill the top process,
// which is fine for ffmpeg today but `taskkill /T` is the documented
// future-proof approach.
func hardKill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	k := exec.Command("taskkill", "/pid", pid, "/T", "/F")
	return k.Run()
}
