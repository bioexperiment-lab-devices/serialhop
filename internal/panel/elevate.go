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
func RunElevatedAdminAction(action string) (errMsg string, err error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	errFile := filepath.Join(os.TempDir(), fmt.Sprintf("lab_devices_client_admin_%d.err", os.Getpid()))
	defer os.Remove(errFile)

	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exePath)
	// We compose the args directly. The error-file path is built from
	// os.TempDir() + a numeric PID, neither of which contain spaces or
	// quotes, so plain string concatenation is safe here.
	params, _ := windows.UTF16PtrFromString(fmt.Sprintf("--admin-action=%s --error-file=%s", action, errFile))

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
	defer windows.CloseHandle(hProc)
	if _, err := windows.WaitForSingleObject(hProc, windows.INFINITE); err != nil {
		return "", fmt.Errorf("WaitForSingleObject: %w", err)
	}

	data, readErr := os.ReadFile(errFile)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", fmt.Errorf("read error file: %w", readErr)
	}
	return strings.TrimSpace(string(data)), nil
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
