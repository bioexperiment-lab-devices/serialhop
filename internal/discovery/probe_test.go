package discovery

import (
	"bytes"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

func TestProbe_Pump(t *testing.T) {
	p := serial.NewFakePort("COM3")
	defer p.Close() //nolint:errcheck // test teardown

	// Feed response after drain completes but during read timeout
	go func() {
		time.Sleep(300 * time.Millisecond) // drain is 200ms, probe bytes add ~50ms, so 300ms total
		p.Feed([]byte{10, 99, 88, 77})
	}()

	reply, got, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got == nil {
		t.Fatal("Probe returned nil result for pump reply")
	}
	if got.TypeCode != 10 || got.Type != "pump" {
		t.Errorf("got type=%q code=%d, want pump/10", got.Type, got.TypeCode)
	}
	if string(reply) != string([]byte{10, 99, 88, 77}) {
		t.Errorf("Probe returned reply=%v, want [10 99 88 77]", reply)
	}
	written := p.Written()
	want := []byte{1, 2, 3, 4, 181}
	if string(written) != string(want) {
		t.Errorf("probe sent %v, want %v", written, want)
	}
}

func TestProbe_Valve(t *testing.T) {
	p := serial.NewFakePort("COM4")
	defer p.Close() //nolint:errcheck // test teardown

	go func() {
		time.Sleep(300 * time.Millisecond)
		p.Feed([]byte{30, 1, 1, 6})
	}()

	_, got, err := Probe(p)
	if err != nil || got == nil || got.TypeCode != 30 || got.Type != "valve" {
		t.Errorf("Probe valve: got=%v err=%v", got, err)
	}
}

func TestProbe_Densitometer(t *testing.T) {
	p := serial.NewFakePort("COM7")
	defer p.Close() //nolint:errcheck // test teardown

	go func() {
		time.Sleep(300 * time.Millisecond)
		p.Feed([]byte{70, 0, 0, 2})
	}()

	_, got, err := Probe(p)
	if err != nil || got == nil || got.TypeCode != 70 || got.Type != "densitometer" {
		t.Errorf("Probe densitometer: got=%v err=%v", got, err)
	}
}

func TestProbe_UnknownTypeByte(t *testing.T) {
	p := serial.NewFakePort("COM5")
	defer p.Close() //nolint:errcheck // test teardown
	// Feed AFTER drain finishes (200ms) so the bytes survive into the read
	// phase — otherwise drain wipes them and we exercise the no-reply path.
	go func() {
		time.Sleep(300 * time.Millisecond)
		p.Feed([]byte{99, 1, 2, 3})
	}()
	reply, got, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil result for unknown type byte 99, got %v", got)
	}
	// Reply must still be returned so callers can log it.
	if string(reply) != string([]byte{99, 1, 2, 3}) {
		t.Errorf("expected reply=[99 1 2 3] for unknown type, got %v", reply)
	}
}

// A partial reply proves a device is present, so Probe retries exactly once.
// Here the retry gets silence: the result is still nil, the first attempt's
// bytes are returned for logging, and the probe was sent twice.
func TestProbe_FewerThan4Bytes(t *testing.T) {
	p := serial.NewFakePort("COM6")
	defer p.Close() //nolint:errcheck // test teardown
	go func() {
		time.Sleep(300 * time.Millisecond)
		p.Feed([]byte{10, 1}) // only 2 bytes, then silence forever
	}()
	reply, got, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for partial reply, got %v", got)
	}
	// The partial bytes are the strongest evidence of a device — they must
	// survive the empty retry so callers can log them.
	if string(reply) != string([]byte{10, 1}) {
		t.Errorf("expected partial reply=[10 1], got %v", reply)
	}
	if want := 2 * len(probeBytes); len(p.Written()) != want {
		t.Errorf("probe written %d bytes, want %d (two attempts)", len(p.Written()), want)
	}
}

// A silent port is genuinely deviceless: no retry, single probe write.
func TestProbe_NoReply(t *testing.T) {
	p := serial.NewFakePort("COM8")
	defer p.Close() //nolint:errcheck // test teardown
	reply, got, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for no reply, got %v", got)
	}
	if len(reply) != 0 {
		t.Errorf("expected empty reply on timeout, got %v", reply)
	}
	if want := len(probeBytes); len(p.Written()) != want {
		t.Errorf("probe written %d bytes, want %d (no retry on silence)", len(p.Written()), want)
	}
}

// Partial first attempt, complete frame on the retry: classified normally.
func TestProbe_PartialThenCompleteOnRetry(t *testing.T) {
	p := serial.NewFakePort("COM3")
	defer p.Close() //nolint:errcheck // test teardown
	go func() {
		time.Sleep(300 * time.Millisecond)
		p.Feed([]byte{10, 1}) // truncated frame → triggers retry
		// Wait for the second probe sequence to finish (drain during the
		// retry would wipe anything fed earlier), then answer properly.
		deadline := time.Now().Add(3 * time.Second)
		for len(p.Written()) < 2*len(probeBytes) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		p.Feed([]byte{10, 99, 88, 77})
	}()
	reply, got, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got == nil || got.TypeCode != 10 || got.Type != "pump" {
		t.Fatalf("expected pump classification after retry, got %v", got)
	}
	if string(reply) != string([]byte{10, 99, 88, 77}) {
		t.Errorf("expected retry reply=[10 99 88 77], got %v", reply)
	}
}

// Regression for the original field bug: USB latency timers batch reply
// bytes with gaps far beyond the old 25 ms slack. 100 ms inter-byte gaps
// must classify on the first attempt.
func TestProbe_SlowInterByteArrival(t *testing.T) {
	p := serial.NewFakePort("COM4")
	defer p.Close() //nolint:errcheck // test teardown
	go func() {
		time.Sleep(300 * time.Millisecond)
		for _, b := range []byte{10, 99, 88, 77} {
			p.Feed([]byte{b})
			time.Sleep(100 * time.Millisecond)
		}
	}()
	reply, got, err := Probe(p)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got == nil || got.Type != "pump" {
		t.Fatalf("expected pump despite slow byte arrival, got %v", got)
	}
	if string(reply) != string([]byte{10, 99, 88, 77}) {
		t.Errorf("reply=%v, want [10 99 88 77]", reply)
	}
	if want := len(probeBytes); len(p.Written()) != want {
		t.Errorf("probe written %d bytes, want %d (no retry needed)", len(p.Written()), want)
	}
}

func TestProbeBytes_ReturnsCopy(t *testing.T) {
	a := ProbeBytes()
	if string(a) != string([]byte{1, 2, 3, 4, 181}) {
		t.Fatalf("ProbeBytes: got %v, want [1 2 3 4 181]", a)
	}
	a[0] = 99
	b := ProbeBytes()
	if b[0] != 1 {
		t.Errorf("ProbeBytes returned a slice that aliases internal state: %v", b)
	}
}

// TestProbeBytesUsesStrictIdentifyFrame pins the frame that strict pump
// firmware requires. COM6/COM7 answer only this exact sequence; valve and
// densitometer were verified to answer it identically to the old frame.
func TestProbeBytesUsesStrictIdentifyFrame(t *testing.T) {
	got := ProbeBytes()
	want := []byte{1, 2, 3, 4, 181}
	if !bytes.Equal(got, want) {
		t.Errorf("ProbeBytes() = %v, want %v", got, want)
	}
}
