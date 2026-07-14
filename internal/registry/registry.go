package registry

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// Registry tracks the device sessions created by the most recent
// discovery, keyed by ordinal device ID. List preserves Replace order
// (discovery's (type code, port) sort). Sessions handed to Replace must
// already be Start()ed — Close on an unstarted session blocks forever.
type Registry struct {
	mu           sync.RWMutex
	ordered      []*device.Session
	byID         map[string]*device.Session
	discoveredAt *time.Time
	discoverGate atomic.Bool
	rawLeases    map[string]bool
}

func New() *Registry {
	return &Registry{byID: map[string]*device.Session{}, rawLeases: map[string]bool{}}
}

// LockDiscovery acquires the single-discovery gate; false if held.
func (r *Registry) LockDiscovery() bool { return r.discoverGate.CompareAndSwap(false, true) }

// UnlockDiscovery releases the gate.
func (r *Registry) UnlockDiscovery() { r.discoverGate.Store(false) }

// IsDiscovering reports whether a discovery pass is in flight.
func (r *Registry) IsDiscovering() bool { return r.discoverGate.Load() }

// Replace installs a new session set and closes every session of the old
// set (graceful: Close runs Detach, which persists driver state). A non-nil
// sessions slice stamps discoveredAt; Replace(nil) closes everything but
// keeps the timestamp (shutdown path).
func (r *Registry) Replace(sessions []*device.Session) {
	r.mu.Lock()
	old := r.ordered
	r.ordered = append([]*device.Session(nil), sessions...)
	r.byID = make(map[string]*device.Session, len(sessions))
	for _, s := range sessions {
		r.byID[s.ID()] = s
	}
	if sessions != nil {
		now := time.Now().UTC()
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
func (r *Registry) CloseAll() { r.removeAll() }

// DisconnectAll closes every session and returns how many were released.
func (r *Registry) DisconnectAll() int { return r.removeAll() }

func (r *Registry) removeAll() int {
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
func (r *Registry) DisconnectByPort(port string) bool {
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
func (r *Registry) Get(id string) (*device.Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byID[id]
	return s, ok
}

// List returns the sessions in Replace order.
func (r *Registry) List() []*device.Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]*device.Session(nil), r.ordered...)
}

// HasPort returns the ID of the session holding the named port, if any.
func (r *Registry) HasPort(name string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.ordered {
		if s.PortName() == name {
			return s.ID(), true
		}
	}
	return "", false
}

// TryAcquireRaw grants an exclusive raw lease on port. It fails if a
// discovery pass is in flight, the port is owned by a discovered device,
// or another raw lease is already held. Same mutex as the session map, so
// it cannot race Replace/HasPort.
func (r *Registry) TryAcquireRaw(port string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.discoverGate.Load() {
		return false
	}
	for _, s := range r.ordered {
		if s.PortName() == port {
			return false
		}
	}
	if r.rawLeases[port] {
		return false
	}
	r.rawLeases[port] = true
	return true
}

// ReleaseRaw drops the raw lease on port (no-op if not held).
func (r *Registry) ReleaseRaw(port string) {
	r.mu.Lock()
	delete(r.rawLeases, port)
	r.mu.Unlock()
}

// RawLeasedPorts returns the ports currently under a raw lease, sorted.
// Discovery excludes these from its candidate list.
func (r *Registry) RawLeasedPorts() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.rawLeases))
	for p := range r.rawLeases {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// DiscoveredAt returns the time of the last discovery, or nil if never run.
func (r *Registry) DiscoveredAt() *time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.discoveredAt == nil {
		return nil
	}
	t := *r.discoveredAt
	return &t
}
