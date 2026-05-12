package flasher

import (
	"errors"
	"testing"
	"time"

	ft "github.com/bioexperiment-lab-devices/serialhop/internal/flasher/testing"
)

func TestSTK_Sync_HappyPath(t *testing.T) {
	f := ft.NewFakeOptiboot()
	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Errorf("Sync: %v", err)
	}
}

func TestSTK_Sync_RetriesThenSucceeds(t *testing.T) {
	f := ft.NewFakeOptiboot()
	f.FailSyncTimes(2)
	c := newSTKClient(f)
	if err := c.Sync(2 * time.Second); err != nil {
		t.Errorf("Sync after 2 ignored attempts: %v", err)
	}
}

func TestSTK_Sync_ExhaustsRetries(t *testing.T) {
	f := ft.NewFakeOptiboot()
	f.FailSyncTimes(100)
	c := newSTKClient(f)
	err := c.Sync(800 * time.Millisecond)
	if err == nil {
		t.Fatal("expected sync exhaustion error, got nil")
	}
	if !errors.Is(err, errBootloaderUnresponsive) {
		t.Errorf("error: got %v, want %v", err, errBootloaderUnresponsive)
	}
}

func TestSTK_GetSignOn_ReturnsVendorString(t *testing.T) {
	f := ft.NewFakeOptiboot()
	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	signOn, err := c.GetSignOn(150 * time.Millisecond)
	if err != nil {
		t.Fatalf("GetSignOn: %v", err)
	}
	if signOn == "" {
		t.Error("expected non-empty sign-on")
	}
}
