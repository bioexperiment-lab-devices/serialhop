//go:build !windows

package power

import (
	"sync"
	"testing"
)

func TestFake_StartsInactive(t *testing.T) {
	ka, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = ka.Close() })
	if ka.Active() {
		t.Errorf("Active before Enable = true, want false")
	}
}

func TestFake_EnableThenDisable(t *testing.T) {
	ka, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = ka.Close() })

	if err := ka.Enable("test"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !ka.Active() {
		t.Errorf("Active after Enable = false, want true")
	}
	if err := ka.Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if ka.Active() {
		t.Errorf("Active after Disable = true, want false")
	}
}

func TestFake_EnableIsIdempotent(t *testing.T) {
	ka, _ := New()
	t.Cleanup(func() { _ = ka.Close() })
	if err := ka.Enable("a"); err != nil {
		t.Fatalf("first Enable: %v", err)
	}
	if err := ka.Enable("b"); err != nil {
		t.Fatalf("second Enable: %v", err)
	}
	if !ka.Active() {
		t.Errorf("Active = false after double Enable")
	}
}

func TestFake_DisableIsIdempotent(t *testing.T) {
	ka, _ := New()
	t.Cleanup(func() { _ = ka.Close() })
	if err := ka.Disable(); err != nil {
		t.Fatalf("Disable on cold instance: %v", err)
	}
	_ = ka.Enable("test")
	if err := ka.Disable(); err != nil {
		t.Fatalf("first Disable: %v", err)
	}
	if err := ka.Disable(); err != nil {
		t.Fatalf("second Disable: %v", err)
	}
	if ka.Active() {
		t.Errorf("Active = true after double Disable")
	}
}

func TestFake_CloseClearsActive(t *testing.T) {
	ka, _ := New()
	_ = ka.Enable("test")
	if err := ka.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ka.Active() {
		t.Errorf("Active = true after Close, want false")
	}
}

func TestFake_ConcurrentEnableDoesNotRace(t *testing.T) {
	ka, _ := New()
	t.Cleanup(func() { _ = ka.Close() })

	const N = 64
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = ka.Enable("test")
		}()
	}
	wg.Wait()
	if !ka.Active() {
		t.Errorf("Active = false after %d concurrent Enables", N)
	}
}
