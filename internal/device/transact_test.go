package device

import (
	"bytes"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// shrinkTimeouts makes transact fail fast in tests; restores on cleanup.
func shrinkTimeouts(t *testing.T) {
	t.Helper()
	oldPB, oldDW := PerByteTimeout, DrainWindow
	PerByteTimeout, DrainWindow = 20*time.Millisecond, 0
	t.Cleanup(func() { PerByteTimeout, DrainWindow = oldPB, oldDW })
}

func TestTransactWritesFrameAndReadsReply(t *testing.T) {
	shrinkTimeouts(t)
	p := serial.NewFakePort("COM9")
	p.Feed([]byte{10, 1, 2, 3}) // DrainWindow=0 → pre-fed reply survives
	frame := []byte{1, 2, 3, 0, 0}
	got, err := transact(p, frame, 4, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{10, 1, 2, 3}) {
		t.Errorf("reply = %v", got)
	}
	if !bytes.Equal(p.Written(), frame) {
		t.Errorf("written = %v", p.Written())
	}
}

func TestTransactDrainsStaleBytesBeforeWrite(t *testing.T) {
	oldPB, oldDW := PerByteTimeout, DrainWindow
	// generous read window so the late feed lands inside the FIRST attempt
	// (a retry would re-drain and discard it)
	PerByteTimeout, DrainWindow = 50*time.Millisecond, 30*time.Millisecond
	t.Cleanup(func() { PerByteTimeout, DrainWindow = oldPB, oldDW })

	p := serial.NewFakePort("COM9")
	p.Feed([]byte{99, 99, 99}) // stale garbage from a previous exchange
	go func() {
		time.Sleep(40 * time.Millisecond) // after the 30 ms drain window
		p.Feed([]byte{30, 1, 1, 4})
	}()
	got, err := transact(p, []byte{33, 1, 0, 0, 0}, 4, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{30, 1, 1, 4}) {
		t.Errorf("stale bytes leaked into reply: %v", got)
	}
}

func TestTransactWriteOnly(t *testing.T) {
	shrinkTimeouts(t)
	p := serial.NewFakePort("COM9")
	got, err := transact(p, []byte{19, 0, 0, 0, 0}, 0, time.Second)
	if err != nil || got != nil {
		t.Fatalf("write-only: got=%v err=%v", got, err)
	}
}

func TestTransactTimeoutRetriesWholeTransactionOnce(t *testing.T) {
	shrinkTimeouts(t)
	p := serial.NewFakePort("COM9")
	frame := []byte{1, 2, 3, 0, 0}
	_, err := transact(p, frame, 4, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if want := append(frame, frame...); !bytes.Equal(p.Written(), want) {
		t.Errorf("expected exactly two write attempts, written = %v", p.Written())
	}
}

func TestTransactSecondAttemptCanSucceed(t *testing.T) {
	shrinkTimeouts(t)
	p := serial.NewFakePort("COM9")
	go func() {
		time.Sleep(30 * time.Millisecond) // first attempt (20 ms silence) already failed
		p.Feed([]byte{70, 0, 0, 2})
	}()
	got, err := transact(p, []byte{1, 2, 3, 4, 0}, 4, 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{70, 0, 0, 2}) {
		t.Errorf("reply = %v", got)
	}
}
