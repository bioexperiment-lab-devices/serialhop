//go:build windows

package main

import "golang.org/x/sys/windows"

// attachParentConsole hooks the process to its parent's console so that
// stdout/stderr writes show up in the cmd.exe / PowerShell window that
// launched the installer. The binary is compiled with -H windowsgui so
// no console is allocated by default. If there is no parent console
// (e.g. double-clicked from Explorer with --silent), the call fails
// silently and output goes nowhere — same as before.
func attachParentConsole() {
	modKernel32 := windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole := modKernel32.NewProc("AttachConsole")
	// ATTACH_PARENT_PROCESS = (DWORD)-1
	procAttachConsole.Call(uintptr(^uint32(0)))
}
