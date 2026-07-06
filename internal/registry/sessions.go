package registry

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// SessionRegistry tracks the device sessions created by the most recent
// discovery, keyed by ordinal device ID. List preserves Replace order
// (discovery's (type code, port) sort). Sessions handed to Replace must
// already be Start()ed — Close on an unstarted session blocks forever.
type SessionRegistry struct {
	mu           sync.RWMutex
	ordered      []*device.Session
	byID         map[string]*device.Session
	discoveredAt *time.Time
	discoverGate atomic.Bool
}

func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{byID: map[string]*device.Session{}}
}

// LockDiscovery acquires the single-discovery gate; false if held.
func (r *SessionRegistry) LockDiscovery() bool { return r.discoverGate.CompareAndSwap(false, true) }

// UnlockDiscovery releases the gate.
func (r *SessionRegistry) UnlockDiscovery() { r.discoverGate.Store(false) }

// IsDiscovering reports whether a discovery pass is in flight.
func (r *SessionRegistry) IsDiscovering() bool { return r.discoverGate.Load() }

// Replace installs a new session set and closes every session of the old
// set (graceful: Close runs Detach, which persists driver state). A non-nil
// sessions slice stamps discoveredAt; Replace(nil) closes everything but
// keeps the timestamp (shutdown path).
func (r *SessionRegistry) Replace(sessions []*device.Session) {
	r.mu.Lock()
	old := r.ordered
	r.ordered = append([]*device.Session(nil), sessions...)
	r.byID = make(map[string]*device.Session, len(sessions))
	for _, s := range sessions {
		r.byID[s.ID()] = s
	}
	if sessions != nil {
		now := time.Now()
		r.discoveredAt = &now
	}
	r.mu.Unlock()
	// Close outside the lock: Close blocks on graceful detach, which may do
	// serial I/O (pump safety stop).
	for _, s := range old {
		s.Close()
	}
}

// CloseAll closes every session and empties the registry, preserving
// discoveredAt (used before a re-probe and at shutdown).
func (r *SessionRegistry) CloseAll() { r.removeAll() }

// DisconnectAll closes every session and returns how many were released.
func (r *SessionRegistry) DisconnectAll() int { return r.removeAll() }

func (r *SessionRegistry) removeAll() int {
	r.mu.Lock()
	old := r.ordered
	r.ordered = nil
	r.byID = map[string]*device.Session{}
	r.mu.Unlock()
	for _, s := range old {
		s.Close()
	}
	return len(old)
}

// DisconnectByPort closes and removes the session holding the named port.
func (r *SessionRegistry) DisconnectByPort(port string) bool {
	r.mu.Lock()
	var victim *device.Session
	for i, s := range r.ordered {
		if s.PortName() == port {
			victim = s
			r.ordered = append(r.ordered[:i:i], r.ordered[i+1:]...)
			delete(r.byID, s.ID())
			break
		}
	}
	r.mu.Unlock()
	if victim == nil {
		return false
	}
	victim.Close()
	return true
}

// Get returns the session with the given device ID.
func (r *SessionRegistry) Get(id string) (*device.Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byID[id]
	return s, ok
}

// List returns the sessions in Replace order.
func (r *SessionRegistry) List() []*device.Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]*device.Session(nil), r.ordered...)
}

// HasPort returns the ID of the session holding the named port, if any.
func (r *SessionRegistry) HasPort(name string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.ordered {
		if s.PortName() == name {
			return s.ID(), true
		}
	}
	return "", false
}

// DiscoveredAt returns the time of the last discovery, or nil if never run.
func (r *SessionRegistry) DiscoveredAt() *time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.discoveredAt == nil {
		return nil
	}
	t := *r.discoveredAt
	return &t
}
