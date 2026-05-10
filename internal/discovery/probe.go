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
	DrainDuration       = 200 * time.Millisecond
	ProbeByteGap        = 10 * time.Millisecond
	ProbeReadTimeout    = 1 * time.Second
	ProbeInterByteSlack = 25 * time.Millisecond
)

var probeBytes = []byte{1, 2, 3, 4, 0}

// ProbeBytes returns a copy of the probe sequence written to every port.
// Exposed so callers (e.g. discovery logging) can report what was sent.
func ProbeBytes() []byte {
	out := make([]byte, len(probeBytes))
	copy(out, probeBytes)
	return out
}

// Probe runs the universal device probe on the given open port and classifies
// the reply. Returns the raw bytes received (possibly fewer than 4 on
// timeout, possibly nil on I/O error) so callers can log what the port
// answered. The result is non-nil only when the reply could be classified to
// a known device type; it is nil when the port did not reply or returned an
// unknown type byte. A non-nil error indicates an actual I/O failure.
func Probe(p labserial.Port) ([]byte, *ProbeResult, error) {
	if err := p.Drain(DrainDuration); err != nil {
		return nil, nil, fmt.Errorf("drain: %w", err)
	}
	for _, b := range probeBytes {
		if _, err := p.Write([]byte{b}); err != nil {
			return nil, nil, fmt.Errorf("write probe: %w", err)
		}
		time.Sleep(ProbeByteGap)
	}
	reply, err := labserial.ReadFrame(p, ProbeReadTimeout, ProbeInterByteSlack, 4)
	if err != nil {
		return reply, nil, fmt.Errorf("read probe reply: %w", err)
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
