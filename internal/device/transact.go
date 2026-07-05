// internal/device/transact.go
package device

import (
	"errors"
	"fmt"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// Timing knobs are vars so tests can shrink them.
var (
	// PerByteTimeout is the max silence between reply bytes. Devices insert
	// 10–20 ms gaps; 500 ms per the TRANSLATION docs' serial primitives.
	PerByteTimeout = 500 * time.Millisecond
	// DrainWindow is how long to spend discarding stale RX bytes pre-write.
	DrainWindow = 50 * time.Millisecond
)

// ErrReaderHeld: a reply-expecting transaction was attempted while a watcher
// goroutine holds the port's read side (spec §3). Driver bug by definition.
var ErrReaderHeld = errors.New("device: port read side is held by a watcher")

// transact implements the shared serial discipline (TRANSLATION docs §2):
// drain RX → write the whole frame in one write → read exactly replyLen
// bytes → on any failure retry the whole transaction once.
func transact(p serial.Port, frame []byte, replyLen int, total time.Duration) ([]byte, error) {
	reply, err := transactOnce(p, frame, replyLen, total)
	if err == nil {
		return reply, nil
	}
	return transactOnce(p, frame, replyLen, total)
}

func transactOnce(p serial.Port, frame []byte, replyLen int, total time.Duration) ([]byte, error) {
	if err := p.Drain(DrainWindow); err != nil {
		return nil, fmt.Errorf("drain: %w", err)
	}
	if _, err := p.Write(frame); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	if replyLen == 0 {
		return nil, nil
	}
	if minTotal := time.Duration(replyLen) * 30 * time.Millisecond; total < minTotal {
		total = minTotal
	}
	if err := p.SetReadTimeout(PerByteTimeout); err != nil {
		return nil, fmt.Errorf("set read timeout: %w", err)
	}
	buf := make([]byte, 0, replyLen)
	deadline := time.Now().Add(total)
	for len(buf) < replyLen {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("read: total timeout after %d/%d bytes", len(buf), replyLen)
		}
		chunk := make([]byte, replyLen-len(buf))
		n, err := p.Read(chunk)
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		if n == 0 { // per-byte timeout expired with no data
			return nil, fmt.Errorf("read: silence after %d/%d bytes", len(buf), replyLen)
		}
		buf = append(buf, chunk[:n]...)
	}
	return buf, nil
}
