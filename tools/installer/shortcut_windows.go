//go:build windows

package main

import (
	"fmt"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

type shortcutOpts struct {
	Path         string // .lnk destination (e.g., C:\Users\Public\Desktop\SerialHop.lnk)
	Target       string // executable the shortcut points at
	WorkingDir   string // working directory for the launched process
	IconLocation string // "<exe>,<index>" — typically "<exe>,0"
	Description  string // tooltip / description
}

// writeShortcut creates (or overwrites) a Windows .lnk file at opts.Path
// pointing at opts.Target. Implementation uses COM IShellLinkW and
// IPersistFileW via WScript.Shell.
func writeShortcut(opts shortcutOpts) error {
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		// CoInitializeEx returns S_FALSE if already initialized on this thread;
		// go-ole maps that to error CO_E_ALREADYINITIALIZED, which is fine.
		if oleErr, ok := err.(*ole.OleError); ok && oleErr.Code() != 0x80010106 {
			return fmt.Errorf("CoInitializeEx: %w", err)
		}
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return fmt.Errorf("CreateObject WScript.Shell: %w", err)
	}
	defer unknown.Release()

	shell, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return fmt.Errorf("QueryInterface IDispatch: %w", err)
	}
	defer shell.Release()

	linkVar, err := oleutil.CallMethod(shell, "CreateShortcut", opts.Path)
	if err != nil {
		return fmt.Errorf("CreateShortcut: %w", err)
	}
	link := linkVar.ToIDispatch()
	defer link.Release()

	if _, err := oleutil.PutProperty(link, "TargetPath", opts.Target); err != nil {
		return fmt.Errorf("set TargetPath: %w", err)
	}
	if _, err := oleutil.PutProperty(link, "WorkingDirectory", opts.WorkingDir); err != nil {
		return fmt.Errorf("set WorkingDirectory: %w", err)
	}
	if opts.IconLocation != "" {
		if _, err := oleutil.PutProperty(link, "IconLocation", opts.IconLocation); err != nil {
			return fmt.Errorf("set IconLocation: %w", err)
		}
	}
	if opts.Description != "" {
		if _, err := oleutil.PutProperty(link, "Description", opts.Description); err != nil {
			return fmt.Errorf("set Description: %w", err)
		}
	}
	if _, err := oleutil.CallMethod(link, "Save"); err != nil {
		return fmt.Errorf("Save: %w", err)
	}
	return nil
}

type realShortcutWriter struct{}

func (realShortcutWriter) Write(opts shortcutOpts) error {
	return writeShortcut(opts)
}
