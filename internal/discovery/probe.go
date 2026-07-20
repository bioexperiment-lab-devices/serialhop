package discovery

import (
	"fmt"
	"time"

	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// ProbeResult is the outcome of a successful probe + classification.
// A nil *ProbeResult means "no known device on this port" (not an error).
type ProbeResult struct {
	Type     string
	TypeCode byte
}

const (
	DrainDuration    = 200 * time.Millisecond
	ProbeByteGap     = 10 * time.Millisecond
	ProbeReadTimeout = 1 * time.Second
	// ProbeInterByteSlack must absorb USB-serial latency-timer batching
	// (FTDI default 16 ms) plus OS scheduling jitter; 25 ms was measured
	// truncating real replies. A complete frame still returns the moment
	// its 4th byte lands, so the widened slack costs time only when a
	// device stalls mid-frame.
	ProbeInterByteSlack = 250 * time.Millisecond
)

// probeBytes is the universal identification frame. The trailing 181 is
// required: one pump firmware generation in the field (Arduino 2341:0043 and
// CH340 1A86:7523 boards) validates all four parameter bytes and answers only
// this exact sequence, while the older generation accepts any 01 02 03 xx yy.
// Verified on real hardware against pump (0A), valve (1E) and densitometer
// (46) — see docs/superpowers/specs/2026-07-20-real-device-support-design.md.
var probeBytes = []byte{1, 2, 3, 4, 181}

// ProbeBytes returns a copy of the probe sequence written to every port.
// Exposed so callers (e.g. discovery logging) can report what was sent.
func ProbeBytes() []byte {
	out := make([]byte, len(probeBytes))
	copy(out, probeBytes)
	return out
}

// Probe runs the universal device probe on the given open port and classifies
// the reply. A partial (1–3 byte) reply proves a device is present with a
// broken frame, so Probe drains and retries once; classification always
// requires the full 4-byte frame (drivers' Attach consumes the payload
// bytes). Returns the raw bytes received — on a failed retry, the longest
// reply observed — so callers can log what the port answered. The result is
// non-nil only when the reply could be classified to a known device type; it
// is nil when the port did not reply or returned an unknown type byte. A
// non-nil error indicates an actual I/O failure.
func Probe(p labserial.Port) ([]byte, *ProbeResult, error) {
	if err := p.Drain(DrainDuration); err != nil {
		return nil, nil, fmt.Errorf("drain: %w", err)
	}
	if err := sendProbe(p); err != nil {
		return nil, nil, err
	}
	reply, err := labserial.ReadFrame(p, ProbeReadTimeout, ProbeInterByteSlack, 4)
	if err != nil {
		return reply, nil, fmt.Errorf("read probe reply: %w", err)
	}
	if len(reply) >= 1 && len(reply) < 4 {
		// Partial reply: a device is present but the frame broke (latency-
		// timer batching, drained mid-frame, desync). Flush any straggler
		// byte so it can't misalign the next frame, then probe once more.
		if err := p.Drain(DrainDuration); err != nil {
			return reply, nil, fmt.Errorf("drain before retry: %w", err)
		}
		if err := sendProbe(p); err != nil {
			return reply, nil, err
		}
		retry, err := labserial.ReadFrame(p, ProbeReadTimeout, ProbeInterByteSlack, 4)
		if err != nil {
			return retry, nil, fmt.Errorf("read probe retry reply: %w", err)
		}
		// Keep the longest reply: an empty or shorter retry must not mask
		// the first attempt's bytes in the caller's no-match log.
		if len(retry) >= len(reply) {
			reply = retry
		}
	}
	if len(reply) < 4 {
		return reply, nil, nil
	}
	switch reply[0] {
	case 10:
		return reply, &ProbeResult{Type: "pump", TypeCode: 10}, nil
	case 30:
		return reply, &ProbeResult{Type: "valve", TypeCode: 30}, nil
	case 70:
		return reply, &ProbeResult{Type: "densitometer", TypeCode: 70}, nil
	default:
		return reply, nil, nil
	}
}

// sendProbe writes the probe sequence one byte at a time with ProbeByteGap
// pacing (the N1 firmware parser needs the gaps).
func sendProbe(p labserial.Port) error {
	for _, b := range probeBytes {
		if _, err := p.Write([]byte{b}); err != nil {
			return fmt.Errorf("write probe: %w", err)
		}
		time.Sleep(ProbeByteGap)
	}
	return nil
}
