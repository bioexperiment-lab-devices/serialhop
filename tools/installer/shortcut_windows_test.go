//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

func TestWriteShortcut_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	// Create a stand-in target file so the shortcut's TargetPath is valid.
	targetPath := filepath.Join(dir, "SerialHop.exe")
	if err := os.WriteFile(targetPath, []byte("stub"), 0o600); err != nil {
		t.Fatalf("create stub target: %v", err)
	}
	linkPath := filepath.Join(dir, "SerialHop.lnk")

	opts := shortcutOpts{
		Path:         linkPath,
		Target:       targetPath,
		WorkingDir:   dir,
		IconLocation: targetPath + ",0",
		Description:  "Test shortcut",
	}
	if err := writeShortcut(opts); err != nil {
		t.Fatalf("writeShortcut: %v", err)
	}

	// Resolve the shortcut back via the same WScript.Shell API and assert
	// TargetPath / WorkingDirectory match what we wrote.
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		if oleErr, ok := err.(*ole.OleError); !ok || oleErr.Code() != 0x80010106 {
			t.Fatalf("CoInitializeEx: %v", err)
		}
	}
	defer ole.CoUninitialize()
	unknown, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		t.Fatalf("CreateObject: %v", err)
	}
	defer unknown.Release()
	shell, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		t.Fatalf("QueryInterface: %v", err)
	}
	defer shell.Release()
	linkVar, err := oleutil.CallMethod(shell, "CreateShortcut", linkPath)
	if err != nil {
		t.Fatalf("read back CreateShortcut: %v", err)
	}
	link := linkVar.ToIDispatch()
	defer link.Release()

	gotTarget, err := oleutil.GetProperty(link, "TargetPath")
	if err != nil {
		t.Fatalf("get TargetPath: %v", err)
	}
	defer gotTarget.Clear()
	if gotTarget.ToString() != targetPath {
		t.Errorf("TargetPath = %q; want %q", gotTarget.ToString(), targetPath)
	}

	gotWD, err := oleutil.GetProperty(link, "WorkingDirectory")
	if err != nil {
		t.Fatalf("get WorkingDirectory: %v", err)
	}
	defer gotWD.Clear()
	if gotWD.ToString() != dir {
		t.Errorf("WorkingDirectory = %q; want %q", gotWD.ToString(), dir)
	}
}
