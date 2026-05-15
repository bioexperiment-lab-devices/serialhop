//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modVersion                    = windows.NewLazySystemDLL("version.dll")
	procGetFileVersionInfoExW     = modVersion.NewProc("GetFileVersionInfoExW")
	procGetFileVersionInfoSizeExW = modVersion.NewProc("GetFileVersionInfoSizeExW")
	procVerQueryValueW            = modVersion.NewProc("VerQueryValueW")
)

// readPEFileVersion reads StringFileInfo.FileVersion from path's PE version
// resource. Returns the version string as it appears in the resource (e.g.,
// "0.7.0" for the SerialHop binary). Errors if the file is missing, has no
// version resource, or the resource is malformed.
func readPEFileVersion(path string) (string, error) {
	wpath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", fmt.Errorf("convert path: %w", err)
	}

	// 1. Determine the size of the version resource.
	var handle uint32
	size, _, _ := procGetFileVersionInfoSizeExW.Call(
		uintptr(0), // FILE_VER_GET_NEUTRAL
		uintptr(unsafe.Pointer(wpath)),
		uintptr(unsafe.Pointer(&handle)),
	)
	if size == 0 {
		return "", fmt.Errorf("no version info in %s", path)
	}

	// 2. Load the resource into a buffer.
	buf := make([]byte, size)
	ret, _, callErr := procGetFileVersionInfoExW.Call(
		uintptr(0),
		uintptr(unsafe.Pointer(wpath)),
		uintptr(0),
		uintptr(size),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if ret == 0 {
		return "", fmt.Errorf("GetFileVersionInfoExW: %w", callErr)
	}

	// 3. Probe \VarFileInfo\Translation to discover the langID + codepage,
	//    then query \StringFileInfo\<lang><cp>\FileVersion.
	type langCP struct {
		Language uint16
		CodePage uint16
	}
	subBlock, err := windows.UTF16PtrFromString(`\VarFileInfo\Translation`)
	if err != nil {
		return "", fmt.Errorf("convert sub-block: %w", err)
	}
	var ptr unsafe.Pointer
	var length uint32
	ret, _, callErr = procVerQueryValueW.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(subBlock)),
		uintptr(unsafe.Pointer(&ptr)),
		uintptr(unsafe.Pointer(&length)),
	)
	if ret == 0 || length < uint32(unsafe.Sizeof(langCP{})) {
		return "", fmt.Errorf("VerQueryValue translation: %w", callErr)
	}
	tr := *(*langCP)(ptr)

	// 4. Query the FileVersion string.
	query := fmt.Sprintf(`\StringFileInfo\%04x%04x\FileVersion`, tr.Language, tr.CodePage)
	queryPtr, err := windows.UTF16PtrFromString(query)
	if err != nil {
		return "", fmt.Errorf("convert query: %w", err)
	}
	ret, _, callErr = procVerQueryValueW.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(queryPtr)),
		uintptr(unsafe.Pointer(&ptr)),
		uintptr(unsafe.Pointer(&length)),
	)
	if ret == 0 {
		return "", fmt.Errorf("VerQueryValue %s: %w", query, callErr)
	}
	if length == 0 {
		return "", fmt.Errorf("FileVersion empty in %s", path)
	}
	// `length` is a count of UTF-16 code units including the trailing NUL.
	utf16Slice := unsafe.Slice((*uint16)(ptr), length)
	// Trim the trailing NUL if present.
	if len(utf16Slice) > 0 && utf16Slice[len(utf16Slice)-1] == 0 {
		utf16Slice = utf16Slice[:len(utf16Slice)-1]
	}
	return windows.UTF16ToString(utf16Slice), nil
}
