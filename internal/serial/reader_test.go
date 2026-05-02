package serial

import (
	"testing"
	"time"
)

func TestReadFrame_InitialTimeoutNoBytes(t *testing.T) {
	p := NewFakePort("COMTEST")
	defer p.Close() //nolint:errcheck // test teardown
	got, err := ReadFrame(p, 30*time.Millisecond, 100*time.Millisecond, -1)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want []", got)
	}
}

func TestReadFrame_InterByteTerminationUnboundedMax(t *testing.T) {
	p := NewFakePort("COMTEST")
	defer p.Close() //nolint:errcheck // test teardown
	p.Feed([]byte{10, 1, 2, 3})
	got, err := ReadFrame(p, 200*time.Millisecond, 30*time.Millisecond, -1)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	want := []byte{10, 1, 2, 3}
	if string(got) != string(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReadFrame_StopAtMax(t *testing.T) {
	p := NewFakePort("COMTEST")
	defer p.Close() //nolint:errcheck // test teardown
	// More bytes available than max — should stop at max.
	p.Feed([]byte{10, 1, 2, 3, 99, 99})
	got, err := ReadFrame(p, 200*time.Millisecond, 200*time.Millisecond, 4)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("got len %d (%v), want 4", len(got), got)
	}
	for i, want := range []byte{10, 1, 2, 3} {
		if got[i] != want {
			t.Errorf("got[%d]=%d, want %d", i, got[i], want)
		}
	}
}

func TestReadFrame_PartialBeforeInterByteTimeout(t *testing.T) {
	p := NewFakePort("COMTEST")
	defer p.Close()       //nolint:errcheck // test teardown
	p.Feed([]byte{10, 1}) // only 2 of 4 bytes; rest never arrives
	got, err := ReadFrame(p, 200*time.Millisecond, 25*time.Millisecond, 4)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %v, want 2-byte partial", got)
	}
}

func TestReadFrame_ClosedPortReturnsError(t *testing.T) {
	p := NewFakePort("COMTEST")
	_ = p.Close() // intentionally closed to test error path
	_, err := ReadFrame(p, 100*time.Millisecond, 25*time.Millisecond, -1)
	if err == nil {
		t.Errorf("expected error on closed port")
	}
}
