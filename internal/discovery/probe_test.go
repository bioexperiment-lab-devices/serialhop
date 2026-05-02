package discovery

import (
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

func TestProbe_Pump(t *testing.T) {
	p := serial.NewFakePort("COM3")
	defer p.Close() //nolint:errcheck // test teardown //nolint:errcheck // test teardown

	// Feed response after drain completes but during read timeout
	go func() {
		time.Sleep(300 * time.Millisecond) // drain is 200ms, probe bytes add ~50ms, so 300ms total
		p.Feed([]byte{10, 99, 88, 77})
	}()

	got, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got == nil {
		t.Fatal("Probe returned nil result for pump reply")
	}
	if got.TypeCode != 10 || got.Type != "pump" {
		t.Errorf("got type=%q code=%d, want pump/10", got.Type, got.TypeCode)
	}
	written := p.Written()
	want := []byte{1, 2, 3, 4, 0}
	if string(written) != string(want) {
		t.Errorf("probe sent %v, want %v", written, want)
	}
}

func TestProbe_Valve(t *testing.T) {
	p := serial.NewFakePort("COM4")
	defer p.Close() //nolint:errcheck // test teardown //nolint:errcheck // test teardown

	go func() {
		time.Sleep(300 * time.Millisecond)
		p.Feed([]byte{30, 1, 1, 6})
	}()

	got, err := Probe(p)
	if err != nil || got == nil || got.TypeCode != 30 || got.Type != "valve" {
		t.Errorf("Probe valve: got=%v err=%v", got, err)
	}
}

func TestProbe_Densitometer(t *testing.T) {
	p := serial.NewFakePort("COM7")
	defer p.Close() //nolint:errcheck // test teardown //nolint:errcheck // test teardown

	go func() {
		time.Sleep(300 * time.Millisecond)
		p.Feed([]byte{70, 0, 0, 2})
	}()

	got, err := Probe(p)
	if err != nil || got == nil || got.TypeCode != 70 || got.Type != "densitometer" {
		t.Errorf("Probe densitometer: got=%v err=%v", got, err)
	}
}

func TestProbe_UnknownTypeByte(t *testing.T) {
	p := serial.NewFakePort("COM5")
	defer p.Close() //nolint:errcheck // test teardown
	p.Feed([]byte{99, 1, 2, 3})
	got, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil result for unknown type byte 99, got %v", got)
	}
}

func TestProbe_FewerThan4Bytes(t *testing.T) {
	p := serial.NewFakePort("COM6")
	defer p.Close() //nolint:errcheck // test teardown
	p.Feed([]byte{10, 1}) // only 2 bytes
	got, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for partial reply, got %v", got)
	}
}

func TestProbe_NoReply(t *testing.T) {
	p := serial.NewFakePort("COM8")
	defer p.Close() //nolint:errcheck // test teardown
	got, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for no reply, got %v", got)
	}
}
