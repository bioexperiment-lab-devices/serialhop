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
	Reply    []byte // the full 4-byte reply
}

const (
	DrainDuration       = 200 * time.Millisecond
	ProbeByteGap        = 10 * time.Millisecond
	ProbeReadTimeout    = 1 * time.Second
	ProbeInterByteSlack = 25 * time.Millisecond
)

var probeBytes = []byte{1, 2, 3, 4, 0}

// Probe runs the universal device probe on the given open port and classifies
// the reply. Returns nil ProbeResult if the device did not reply within the
// expected window or returned an unknown type byte. Returns a non-nil error
// only on actual I/O failures.
func Probe(p labserial.Port) (*ProbeResult, error) {
	if err := p.Drain(DrainDuration); err != nil {
		return nil, fmt.Errorf("drain: %w", err)
	}
	for _, b := range probeBytes {
		if _, err := p.Write([]byte{b}); err != nil {
			return nil, fmt.Errorf("write probe: %w", err)
		}
		time.Sleep(ProbeByteGap)
	}
	reply, err := labserial.ReadFrame(p, ProbeReadTimeout, ProbeInterByteSlack, 4)
	if err != nil {
		return nil, fmt.Errorf("read probe reply: %w", err)
	}
	if len(reply) < 4 {
		return nil, nil
	}
	switch reply[0] {
	case 10:
		return &ProbeResult{Type: "pump", TypeCode: 10, Reply: reply}, nil
	case 30:
		return &ProbeResult{Type: "valve", TypeCode: 30, Reply: reply}, nil
	case 70:
		return &ProbeResult{Type: "densitometer", TypeCode: 70, Reply: reply}, nil
	default:
		return nil, nil
	}
}
