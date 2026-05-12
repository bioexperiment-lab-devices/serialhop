package registry

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// Device is one classified, port-open serial device.
type Device struct {
	ID       string
	Type     string // "pump" | "valve" | "densitometer"
	TypeCode byte   // 10 | 30 | 70
	Port     string
	Conn     serial.Port
	Opener   serial.Opener // used to re-open the port on reconnect

	busy atomic.Bool
}

// TryLock attempts to claim exclusive access to this device.
// Returns false if the device is already locked.
func (d *Device) TryLock() bool { return d.busy.CompareAndSwap(false, true) }

// Unlock releases the lock acquired by TryLock.
func (d *Device) Unlock() { d.busy.Store(false) }

// Registry holds the live device set. All methods are safe for concurrent use.
type Registry struct {
	mu           sync.RWMutex
	devices      map[string]*Device
	discoveredAt *time.Time

	discoverGate atomic.Bool
}

func New() *Registry {
	return &Registry{devices: map[string]*Device{}}
}

// LockDiscovery returns true if no other discovery is in progress; the
// caller must call UnlockDiscovery when finished.
func (r *Registry) LockDiscovery() bool {
	return r.discoverGate.CompareAndSwap(false, true)
}

func (r *Registry) UnlockDiscovery() { r.discoverGate.Store(false) }

// IsDiscovering reports whether a discovery is currently in progress.
// Non-acquiring read; callers must NOT use it as a lock.
func (r *Registry) IsDiscovering() bool {
	return r.discoverGate.Load()
}

// Replace closes every device currently in the registry and installs the new set.
// If devs is nil (e.g. shutdown path), the existing devices are closed but
// discoveredAt is NOT updated so a racing GET /devices sees the original timestamp.
func (r *Registry) Replace(devs []*Device) {
	r.mu.Lock()
	old := r.devices
	r.devices = make(map[string]*Device, len(devs))
	for _, d := range devs {
		r.devices[d.ID] = d
	}
	if devs != nil {
		now := time.Now().UTC()
		r.discoveredAt = &now
	}
	r.mu.Unlock()

	for _, d := range old {
		if d.Conn != nil {
			_ = d.Conn.Close()
		}
	}
}

// CloseAll closes every device port currently in the registry and empties the
// device map. discoveredAt is preserved so a racing GET /devices during
// re-discovery still sees the original timestamp until Replace installs the
// new set.
func (r *Registry) CloseAll() {
	r.mu.Lock()
	old := r.devices
	r.devices = map[string]*Device{}
	r.mu.Unlock()

	for _, d := range old {
		if d.Conn != nil {
			_ = d.Conn.Close()
		}
	}
}

// DisconnectAll closes every device port in the registry, empties the map,
// and returns the count of devices that were removed. Safe on an empty
// registry. Used by POST /devices/disconnect before a flash operation.
func (r *Registry) DisconnectAll() int {
	r.mu.Lock()
	n := len(r.devices)
	old := r.devices
	r.devices = map[string]*Device{}
	r.mu.Unlock()

	for _, d := range old {
		if d.Conn != nil {
			_ = d.Conn.Close()
		}
	}
	return n
}

// Get looks up a device by ID.
func (r *Registry) Get(id string) (*Device, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.devices[id]
	return d, ok
}

// Remove deletes a device from the registry and closes its port.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	d, ok := r.devices[id]
	if ok {
		delete(r.devices, id)
	}
	r.mu.Unlock()
	if ok && d.Conn != nil {
		_ = d.Conn.Close()
	}
}

// List returns devices sorted by (TypeCode, Port).
func (r *Registry) List() []*Device {
	r.mu.RLock()
	out := make([]*Device, 0, len(r.devices))
	for _, d := range r.devices {
		out = append(out, d)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].TypeCode != out[j].TypeCode {
			return out[i].TypeCode < out[j].TypeCode
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// HasPort reports whether any device in the registry currently uses the named
// serial port. If a match exists, returns its device ID and true; otherwise
// "", false. Linear scan — registry size is bounded by the number of attached
// devices (typically <10).
func (r *Registry) HasPort(name string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, d := range r.devices {
		if d.Port == name {
			return d.ID, true
		}
	}
	return "", false
}

// DiscoveredAt returns the timestamp of the most recent successful discovery
// (UTC), or nil if discovery has never run.
func (r *Registry) DiscoveredAt() *time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.discoveredAt == nil {
		return nil
	}
	t := *r.discoveredAt
	return &t
}
