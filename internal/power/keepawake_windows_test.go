//go:build windows

package power

import "testing"

func TestWindows_EnableDisableSmoke(t *testing.T) {
	ka, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = ka.Close() })

	if ka.Active() {
		t.Fatalf("Active before Enable = true, want false")
	}
	if err := ka.Enable("unit test: TestWindows_EnableDisableSmoke"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !ka.Active() {
		t.Errorf("Active after Enable = false, want true")
	}
	if err := ka.Enable("idempotent second call"); err != nil {
		t.Fatalf("second Enable: %v", err)
	}
	if err := ka.Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if ka.Active() {
		t.Errorf("Active after Disable = true, want false")
	}
	if err := ka.Disable(); err != nil {
		t.Fatalf("second Disable: %v", err)
	}
}

func TestWindows_CloseAfterEnableReleasesHandle(t *testing.T) {
	ka, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := ka.Enable("test"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := ka.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After Close the request is cleared and the handle freed; Active
	// must report false.
	if ka.Active() {
		t.Errorf("Active = true after Close")
	}
}
