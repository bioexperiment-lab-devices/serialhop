//go:build windows

package power

import (
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Constants from the Windows SDK. golang.org/x/sys/windows does not
// expose these.
const (
	powerRequestContextVersion             = 0
	powerRequestContextSimpleString        = 0x1
	powerRequestSystemRequired      uint32 = 1
)

// powerRequestContext mirrors the SDK's REASON_CONTEXT when used with
// POWER_REQUEST_CONTEXT_SIMPLE_STRING. The reason string is a *uint16
// (UTF-16, null-terminated).
type powerRequestContext struct {
	Version      uint32
	Flags        uint32
	SimpleReason *uint16
}

var (
	modKernel32            = windows.NewLazySystemDLL("kernel32.dll")
	procPowerCreateRequest = modKernel32.NewProc("PowerCreateRequest")
	procPowerSetRequest    = modKernel32.NewProc("PowerSetRequest")
	procPowerClearRequest  = modKernel32.NewProc("PowerClearRequest")
)

type winKeepAwake struct {
	mu     sync.Mutex
	handle windows.Handle // 0 until first Enable
	active atomic.Bool
}

func newPlatform() (KeepAwake, error) {
	return &winKeepAwake{}, nil
}

func (k *winKeepAwake) Enable(reason string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.active.Load() {
		return nil
	}
	if k.handle == 0 {
		h, err := createRequest(reason)
		if err != nil {
			return fmt.Errorf("PowerCreateRequest: %w", err)
		}
		k.handle = h
	}
	if err := setRequest(k.handle); err != nil {
		return fmt.Errorf("PowerSetRequest: %w", err)
	}
	k.active.Store(true)
	return nil
}

func (k *winKeepAwake) Disable() error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if !k.active.Load() {
		return nil
	}
	if k.handle == 0 {
		// Nothing was ever set; defensive.
		k.active.Store(false)
		return nil
	}
	if err := clearRequest(k.handle); err != nil {
		return fmt.Errorf("PowerClearRequest: %w", err)
	}
	k.active.Store(false)
	return nil
}

func (k *winKeepAwake) Active() bool { return k.active.Load() }

func (k *winKeepAwake) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.handle == 0 {
		k.active.Store(false)
		return nil
	}
	if k.active.Load() {
		// Best-effort clear before close; ignore the error so we still
		// release the handle.
		_ = clearRequest(k.handle)
		k.active.Store(false)
	}
	if err := windows.CloseHandle(k.handle); err != nil {
		k.handle = 0
		return fmt.Errorf("CloseHandle: %w", err)
	}
	k.handle = 0
	return nil
}

// createRequest wraps PowerCreateRequest. The returned handle owns the
// reason string for its entire lifetime; PowerCreateRequest copies it
// internally so we don't need to pin it.
func createRequest(reason string) (windows.Handle, error) {
	rPtr, err := windows.UTF16PtrFromString(reason)
	if err != nil {
		return 0, err
	}
	ctx := powerRequestContext{
		Version:      powerRequestContextVersion,
		Flags:        powerRequestContextSimpleString,
		SimpleReason: rPtr,
	}
	ret, _, callErr := procPowerCreateRequest.Call(uintptr(unsafe.Pointer(&ctx)))
	if windows.Handle(ret) == windows.InvalidHandle {
		return 0, callErr
	}
	return windows.Handle(ret), nil
}

func setRequest(h windows.Handle) error {
	ret, _, callErr := procPowerSetRequest.Call(uintptr(h), uintptr(powerRequestSystemRequired))
	if ret == 0 {
		return callErr
	}
	return nil
}

func clearRequest(h windows.Handle) error {
	ret, _, callErr := procPowerClearRequest.Call(uintptr(h), uintptr(powerRequestSystemRequired))
	if ret == 0 {
		return callErr
	}
	return nil
}
