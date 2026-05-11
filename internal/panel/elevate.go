//go:build windows

package panel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const seMaskNoCloseProcess = 0x00000040

type shellExecuteInfoW struct {
	cbSize         uint32
	fMask          uint32
	hwnd           uintptr
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       uintptr
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      uintptr
	dwHotKey       uint32
	hIconOrMonitor uintptr
	hProcess       uintptr
}

var (
	modShell32          = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = modShell32.NewProc("ShellExecuteExW")
)

// RunElevatedAdminAction relaunches the current executable elevated, asking
// it to perform an admin action. Returns the contents of the temp error
// file on failure (or an empty string on success). Returns ErrUserCancelled
// if the user dismissed the UAC prompt.
//
// `extraArgs` are appended to the elevated child's command line. Each
// entry should be a single `--flag=value` token; values containing spaces
// are automatically double-quoted so a path like `C:\Program Files\...`
// arrives as one argument. Used by the update action to pass
// `--update-src=<path>`; ignored by install/uninstall/restart.
func RunElevatedAdminAction(action string, extraArgs ...string) (errMsg string, err error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	errFile := filepath.Join(os.TempDir(), fmt.Sprintf("SerialHop_admin_%d.err", os.Getpid()))
	defer os.Remove(errFile) //nolint:errcheck

	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exePath)

	// Compose the command line. errFile and action are already controlled
	// inputs (action is a literal constant from panel.go, errFile is built
	// from os.TempDir + numeric PID). extraArgs are caller-supplied — the
	// only current caller passes a path produced by filepath.Join inside
	// the same dir as os.Executable(), which on Windows can contain spaces
	// in 'Program Files'-style installs. Quote the value half of each
	// extraArg's `--flag=value` token to handle spaces.
	args := fmt.Sprintf("--admin-action=%s --error-file=%s", action, errFile)
	for _, a := range extraArgs {
		args += " " + quoteFlagValue(a)
	}
	params, _ := windows.UTF16PtrFromString(args)

	info := shellExecuteInfoW{
		cbSize:       uint32(unsafe.Sizeof(shellExecuteInfoW{})),
		fMask:        seMaskNoCloseProcess,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        1, // SW_SHOWNORMAL
	}
	r1, _, lastErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		if lastErr == syscall.Errno(windows.ERROR_CANCELLED) {
			return "", ErrUserCancelled
		}
		return "", fmt.Errorf("ShellExecuteExW: %w", lastErr)
	}
	if info.hProcess == 0 {
		return "", errors.New("ShellExecuteExW returned no process handle")
	}

	hProc := windows.Handle(info.hProcess)
	defer windows.CloseHandle(hProc) //nolint:errcheck
	if _, err := windows.WaitForSingleObject(hProc, windows.INFINITE); err != nil {
		return "", fmt.Errorf("WaitForSingleObject: %w", err)
	}

	data, readErr := os.ReadFile(errFile) //nolint:gosec // errFile is a temp path constructed in this function
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", fmt.Errorf("read error file: %w", readErr)
	}
	return strings.TrimSpace(string(data)), nil
}

// quoteFlagValue takes a "--flag=value" token and double-quotes the value
// half if it contains a space. Windows command-line parsing splits on
// unquoted spaces, so an install path under "C:\Program Files\..." would
// otherwise arrive as multiple args. Tokens without '=' or without spaces
// pass through unchanged.
func quoteFlagValue(token string) string {
	eq := strings.IndexByte(token, '=')
	if eq < 0 {
		return token
	}
	flag := token[:eq]
	val := token[eq+1:]
	if !strings.ContainsAny(val, " \t") {
		return token
	}
	// We don't expect literal quotes inside the value (install paths are
	// not quoted by the OS), so no escaping beyond the wrapping is needed.
	return flag + `="` + val + `"`
}

// ErrUserCancelled is returned when the user dismisses the UAC prompt.
var ErrUserCancelled = errors.New("user cancelled UAC prompt")

// OpenWithDefaultApp invokes ShellExecute with verb "open" on the given
// path. Used by the panel's "Open config file" / "Open log file" buttons.
func OpenWithDefaultApp(path string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(path)
	r1, _, lastErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&shellExecuteInfoW{
		cbSize: uint32(unsafe.Sizeof(shellExecuteInfoW{})),
		lpVerb: verb,
		lpFile: file,
		nShow:  1,
	})))
	if r1 == 0 {
		return fmt.Errorf("ShellExecuteExW: %w", lastErr)
	}
	return nil
}
