package flasher

import (
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	ft "github.com/bioexperiment-lab-devices/serialhop/internal/flasher/testing"
	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
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

func TestSTK_LoadAddress_RoundTrip(t *testing.T) {
	f := ft.NewFakeOptiboot()
	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := c.LoadAddress(150*time.Millisecond, 0x0040); err != nil {
		t.Errorf("LoadAddress: %v", err)
	}
}

func TestSTK_ProgPage_WritesToFakeFlash(t *testing.T) {
	f := ft.NewFakeOptiboot()
	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	page := make([]byte, 128)
	for i := range page {
		page[i] = byte(i)
	}
	if err := c.LoadAddress(150*time.Millisecond, 0x0000); err != nil {
		t.Fatal(err)
	}
	if err := c.ProgPage(500*time.Millisecond, page); err != nil {
		t.Fatalf("ProgPage: %v", err)
	}
	got := f.FlashImage()[:128]
	for i, b := range got {
		if b != byte(i) {
			t.Fatalf("flash[%d]: got %02X, want %02X", i, b, byte(i))
		}
	}
}

func TestSTK_ReadPage_ReadsFakeFlash(t *testing.T) {
	f := ft.NewFakeOptiboot()
	src := make([]byte, 128)
	for i := range src {
		src[i] = byte(255 - i)
	}
	f.PreloadFlash(src)

	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := c.LoadAddress(150*time.Millisecond, 0x0000); err != nil {
		t.Fatal(err)
	}
	got, err := c.ReadPage(500*time.Millisecond, 128)
	if err != nil {
		t.Fatalf("ReadPage: %v", err)
	}
	for i, b := range got {
		if b != src[i] {
			t.Fatalf("read[%d]: got %02X, want %02X", i, b, src[i])
		}
	}
}

func TestSTK_ChipErase_ZeroesFlash(t *testing.T) {
	f := ft.NewFakeOptiboot()
	src := []byte{0xAA, 0xBB, 0xCC}
	f.PreloadFlash(src)

	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := c.ChipErase(500 * time.Millisecond); err != nil {
		t.Fatalf("ChipErase: %v", err)
	}
	got := f.FlashImage()[:3]
	for i, b := range got {
		if b != 0xFF {
			t.Errorf("flash[%d] after erase: got %02X, want FF", i, b)
		}
	}
}

func TestSTK_EnterProgMode_HappyPath(t *testing.T) {
	f := ft.NewFakeOptiboot()
	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := c.EnterProgMode(150 * time.Millisecond); err != nil {
		t.Errorf("EnterProgMode: %v", err)
	}
}

func TestSTK_LeaveProgMode(t *testing.T) {
	f := ft.NewFakeOptiboot()
	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := c.LeaveProgMode(150 * time.Millisecond); err != nil {
		t.Errorf("LeaveProgMode: %v", err)
	}
}

func TestSTK_ChipErase_FailureReportsError(t *testing.T) {
	f := ft.NewFakeOptiboot()
	f.FailNextChipErase()
	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := c.ChipErase(150 * time.Millisecond); err == nil {
		t.Fatal("expected error from ChipErase, got nil")
	}
}

// TestSTK_ReadPage_LogsResponseInfo verifies that a successful ReadPage round
// trip emits a "stk500 response" INFO record with a non-zero len attribute.
func TestSTK_ReadPage_LogsResponseInfo(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f := ft.NewFakeOptiboot()
	src := make([]byte, 128)
	for i := range src {
		src[i] = byte(i)
	}
	f.PreloadFlash(src)

	c := newSTKClient(f)
	if err := c.Sync(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := c.LoadAddress(150*time.Millisecond, 0x0000); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadPage(500*time.Millisecond, 128); err != nil {
		t.Fatalf("ReadPage: %v", err)
	}

	found := rec.Find(slog.LevelInfo, "stk500 response", nil)
	if found == nil {
		t.Fatal("expected stk500 response INFO record, got none")
	}
	lenVal, ok := found.Attrs["len"]
	if !ok {
		t.Fatal("stk500 response record missing len attribute")
	}
	if fmt.Sprint(lenVal) == "0" {
		t.Fatal("stk500 response len attribute is zero")
	}
}
