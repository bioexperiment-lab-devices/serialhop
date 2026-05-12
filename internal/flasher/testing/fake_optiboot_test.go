package testing_test

import (
	"bytes"
	"testing"
	"time"

	ft "github.com/bioexperiment-lab-devices/serialhop/internal/flasher/testing"
)

const (
	stkGetSync byte = 0x30
	stkCrcEop  byte = 0x20
	stkInSync  byte = 0x14
	stkOK      byte = 0x10
)

func TestFakeOptiboot_RespondsToSync(t *testing.T) {
	f := ft.NewFakeOptiboot()
	if err := f.SetReadTimeout(50 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{stkGetSync, stkCrcEop}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := readN(t, f, 2, 200*time.Millisecond)
	want := []byte{stkInSync, stkOK}
	if !bytes.Equal(got, want) {
		t.Errorf("sync reply: got % X, want % X", got, want)
	}
}

func TestFakeOptiboot_FailSyncTimes(t *testing.T) {
	f := ft.NewFakeOptiboot()
	f.FailSyncTimes(2)
	_ = f.SetReadTimeout(20 * time.Millisecond)

	for i := 0; i < 2; i++ {
		_, _ = f.Write([]byte{stkGetSync, stkCrcEop})
		buf := make([]byte, 2)
		n, _ := f.Read(buf)
		if n != 0 {
			t.Errorf("attempt %d: expected silence, got % X", i+1, buf[:n])
		}
	}
	_, _ = f.Write([]byte{stkGetSync, stkCrcEop})
	got := readN(t, f, 2, 200*time.Millisecond)
	if !bytes.Equal(got, []byte{stkInSync, stkOK}) {
		t.Errorf("third sync reply: got % X", got)
	}
}

func readN(t *testing.T, f *ft.FakeOptiboot, n int, total time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(total)
	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timeout after %v; got % X so far", total, out)
		}
		_ = f.SetReadTimeout(remaining)
		got, err := f.Read(buf)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		out = append(out, buf[:got]...)
	}
	return out
}
