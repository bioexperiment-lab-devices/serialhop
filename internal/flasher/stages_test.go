package flasher

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher/avr"
	ft "github.com/bioexperiment-lab-devices/serialhop/internal/flasher/testing"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// fakeOpenerForFlasher wires a single FakeOptiboot into a serial.Opener for tests.
type fakeOpenerForFlasher struct {
	port    string
	fake    *ft.FakeOptiboot
	openErr error
}

func (f *fakeOpenerForFlasher) Open(name string) (labserial.Port, error) {
	if name != f.port {
		return nil, errors.New("unknown port")
	}
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.fake, nil
}
func (f *fakeOpenerForFlasher) OpenWithBaud(name string, _ int) (labserial.Port, error) {
	return f.Open(name)
}
func (f *fakeOpenerForFlasher) List() ([]string, error) { return []string{f.port}, nil }
func (f *fakeOpenerForFlasher) ListDetailed() ([]labserial.DetailedPort, error) {
	return []labserial.DetailedPort{{Name: f.port, IsUSB: true}}, nil
}

func TestFlash_FailedPreflight_FirmwareTooLarge(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	fl, err := New(op, t.TempDir(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{
		Firmware:       make([]byte, avr.FlashSize-avr.BootloaderSize+1),
		Timeout:        100 * time.Millisecond,
		InterByte:      25 * time.Millisecond,
		PostOpenSettle: 0,
	}
	res, err := fl.Flash(context.Background(), "COM3", req)
	if err != nil {
		t.Fatalf("Flash: %v", err)
	}
	if res.Outcome != OutcomeFailedPreflight {
		t.Errorf("Outcome: got %s, want failed_preflight", res.Outcome)
	}
	st := res.Stages["preflight"]
	if st.Status != "failed" {
		t.Errorf("preflight stage status: got %q, want failed", st.Status)
	}
}

func TestFlash_SingleFlight_SecondCallReturnsErrBusy(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	fl, err := New(op, t.TempDir(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Slow the first call by holding the fake's read forever.
	op.fake.FailSyncTimes(1000)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = fl.Flash(context.Background(), "COM3", Request{
			Firmware:  []byte{0x00, 0x01},
			Timeout:   50 * time.Millisecond,
			InterByte: 10 * time.Millisecond,
		})
	}()

	// Give the first call a moment to take the lock.
	time.Sleep(20 * time.Millisecond)
	_, err = fl.Flash(context.Background(), "COM3", Request{
		Firmware:  []byte{0x00, 0x01},
		Timeout:   50 * time.Millisecond,
		InterByte: 10 * time.Millisecond,
	})
	if !errors.Is(err, ErrBusy) {
		t.Errorf("second concurrent Flash: got err=%v, want ErrBusy", err)
	}
	wg.Wait()
}

func TestFlash_Success_HappyPath(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	// Pre-seed a previous-firmware image so backup is non-trivial.
	prev := make([]byte, 128)
	for i := range prev {
		prev[i] = byte(i)
	}
	op.fake.PreloadFlash(prev)

	fl, err := New(op, t.TempDir(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	// New firmware: 128 bytes of byte(255-i).
	newImg := make([]byte, 128)
	for i := range newImg {
		newImg[i] = byte(255 - i)
	}

	res, err := fl.Flash(context.Background(), "COM3", Request{
		Firmware:  newImg,
		Timeout:   500 * time.Millisecond,
		InterByte: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Flash: %v", err)
	}
	if res.Outcome != OutcomeSuccess {
		t.Fatalf("Outcome: got %s, want success", res.Outcome)
	}
	for _, name := range []string{"preflight", "backup", "erase", "program", "verify"} {
		if got := res.Stages[name].Status; got != "ok" {
			t.Errorf("stage %s: got %q, want ok", name, got)
		}
	}
	if got := res.Stages["test"].Status; got != "skipped" {
		t.Errorf("stage test: got %q, want skipped", got)
	}
	if got := res.Stages["rollback"].Status; got != "n/a" {
		t.Errorf("stage rollback: got %q, want n/a", got)
	}
	img := op.fake.FlashImage()
	for i := 0; i < 128; i++ {
		if img[i] != newImg[i] {
			t.Fatalf("flash[%d]: got %02X, want %02X", i, img[i], newImg[i])
		}
	}
	if res.BackupHex == "" {
		t.Error("BackupHex empty")
	}
	if res.Backup.Path == "" {
		t.Error("Backup.Path empty")
	}
}

func TestFlash_Success_WithTestPair(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	op.fake.SetSketchResponse([]byte{0xAA, 0xBB, 0xCC})

	fl, _ := New(op, t.TempDir(), 10, 0)
	res, err := fl.Flash(context.Background(), "COM3", Request{
		Firmware:         []byte{0x00, 0x01, 0x02},
		TestCommand:      []byte{0x10, 0x20},
		ExpectedResponse: []byte{0xAA, 0xBB, 0xCC},
		Timeout:          200 * time.Millisecond,
		InterByte:        20 * time.Millisecond,
		PostOpenSettle:   0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeSuccess {
		t.Errorf("Outcome: got %s, want success", res.Outcome)
	}
	if res.TestResult == nil {
		t.Fatal("TestResult nil")
	}
	if !res.TestResult.Match {
		t.Errorf("TestResult.Match: got false, want true; received=% X", res.TestResult.Received)
	}
	if res.Stages["test"].Status != "ok" {
		t.Errorf("stage test: %q", res.Stages["test"].Status)
	}
}

func TestFlash_RolledBackTestFailed_WhenMismatch(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	op.fake.SetSketchResponse([]byte{0x99})

	fl, _ := New(op, t.TempDir(), 10, 0)
	res, err := fl.Flash(context.Background(), "COM3", Request{
		Firmware:         []byte{0x00, 0x01, 0x02},
		TestCommand:      []byte{0x10},
		ExpectedResponse: []byte{0xAA},
		Timeout:          100 * time.Millisecond,
		InterByte:        20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Note: rollback impl is still the stub in this task; accept either failed_no_recovery (stub)
	// or rolled_back_test_failed (after Task 16).
	if res.Outcome != OutcomeRolledBackTestFailed && res.Outcome != OutcomeFailedNoRecovery {
		t.Errorf("Outcome: got %s, want rolled_back_test_failed or failed_no_recovery (stub)", res.Outcome)
	}
	if res.TestResult == nil {
		t.Fatal("TestResult nil after test_failed")
	}
	if res.TestResult.Match {
		t.Errorf("Match: got true, want false")
	}
	if len(res.TestResult.Received) == 0 || res.TestResult.Received[0] != 0x99 {
		t.Errorf("Received: got % X, want [99]", res.TestResult.Received)
	}
}

func TestFlash_RolledBackVerifyFailed(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	prev := make([]byte, avr.PageSize)
	for i := range prev {
		prev[i] = 0xA5
	}
	op.fake.PreloadFlash(prev)
	op.fake.AckButDontPersistNextProgPage()

	fl, _ := New(op, t.TempDir(), 10, 0)
	res, err := fl.Flash(context.Background(), "COM3", Request{
		Firmware:  make([]byte, avr.PageSize),
		Timeout:   500 * time.Millisecond,
		InterByte: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeRolledBackVerifyFailed {
		t.Fatalf("Outcome: got %s, want rolled_back_verify_failed", res.Outcome)
	}
	if res.Stages["rollback"].Status != "ok" {
		t.Errorf("rollback stage: %q", res.Stages["rollback"].Status)
	}
	if res.Stages["rollback"].VerifyStatus != "ok" {
		t.Errorf("rollback.verify_status: %q", res.Stages["rollback"].VerifyStatus)
	}
}

func TestFlash_RolledBackTestFailed_RealRollback(t *testing.T) {
	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	prev := make([]byte, avr.PageSize)
	for i := range prev {
		prev[i] = 0x5A
	}
	op.fake.PreloadFlash(prev)
	op.fake.SetSketchResponse([]byte{0x99})

	fl, _ := New(op, t.TempDir(), 10, 0)
	res, err := fl.Flash(context.Background(), "COM3", Request{
		Firmware:         make([]byte, avr.PageSize),
		TestCommand:      []byte{0x10},
		ExpectedResponse: []byte{0xAA},
		Timeout:          200 * time.Millisecond,
		InterByte:        20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeRolledBackTestFailed {
		t.Fatalf("Outcome: got %s, want rolled_back_test_failed", res.Outcome)
	}
	if res.Stages["rollback"].Status != "ok" {
		t.Errorf("rollback stage: %q", res.Stages["rollback"].Status)
	}
}
