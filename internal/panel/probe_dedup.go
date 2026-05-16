//go:build windows

package panel

import (
	"sync"
	"time"
)

// probeDedup suppresses repeated WARN logs for the same probe failure
// reason. The first failure in a stream logs; subsequent identical
// failures within `window` are silent. A reason change or a reset
// (recovery) re-arms logging.
type probeDedup struct {
	mu     sync.Mutex
	window time.Duration
	last   map[string]probeDedupEntry
}

type probeDedupEntry struct {
	reason string
	at     time.Time
}

func newProbeDedup(window time.Duration) *probeDedup {
	return &probeDedup{window: window, last: map[string]probeDedupEntry{}}
}

func (p *probeDedup) shouldLog(probe, reason string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	prev, ok := p.last[probe]
	if !ok || prev.reason != reason || now.Sub(prev.at) >= p.window {
		p.last[probe] = probeDedupEntry{reason: reason, at: now}
		return true
	}
	return false
}

func (p *probeDedup) reset(probe string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.last, probe)
}
