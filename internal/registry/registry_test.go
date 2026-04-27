package registry

import (
	"sync"
	"testing"

	"github.com/khamitovdr/lab_devices_client/internal/serial"
)

func newDevice(t *testing.T, id, port string, typeCode byte) *Device {
	t.Helper()
	return &Device{
		ID:        id,
		Type:      typeName(typeCode),
		TypeCode:  typeCode,
		Port:      port,
		Conn:      serial.NewFakePort(port),
		Opener:    serial.NewFakeOpener(),
	}
}

func typeName(code byte) string {
	switch code {
	case 10:
		return "pump"
	case 30:
		return "valve"
	case 70:
		return "densitometer"
	}
	return "unknown"
}

func TestRegistry_CloseAll(t *testing.T) {
	r := New()
	d1 := newDevice(t, "pump_1", "COM3", 10)
	d2 := newDevice(t, "valve_1", "COM4", 30)
	r.Replace([]*Device{d1, d2})
	originalDiscoveredAt := r.DiscoveredAt()
	if originalDiscoveredAt == nil {
		t.Fatal("setup: discoveredAt should be set after Replace")
	}

	r.CloseAll()

	if len(r.List()) != 0 {
		t.Errorf("List after CloseAll: got %d, want 0", len(r.List()))
	}
	if _, ok := r.Get("pump_1"); ok {
		t.Errorf("pump_1 should be removed by CloseAll")
	}
	if _, err := d1.Conn.Write([]byte{1}); err == nil {
		t.Errorf("d1.Conn should be closed after CloseAll")
	}
	if _, err := d2.Conn.Write([]byte{1}); err == nil {
		t.Errorf("d2.Conn should be closed after CloseAll")
	}
	if got := r.DiscoveredAt(); got == nil || !got.Equal(*originalDiscoveredAt) {
		t.Errorf("CloseAll must preserve discoveredAt; got %v, want %v", got, originalDiscoveredAt)
	}
}

func TestRegistry_ReplaceAndLookup(t *testing.T) {
	r := New()
	d := newDevice(t, "pump_1", "COM3", 10)
	r.Replace([]*Device{d})
	got, ok := r.Get("pump_1")
	if !ok || got.ID != "pump_1" {
		t.Fatalf("Get pump_1: got %v ok=%v", got, ok)
	}
	if _, ok := r.Get("pump_2"); ok {
		t.Errorf("Get pump_2: expected not-found")
	}
}

func TestRegistry_ReplaceClosesOldDevices(t *testing.T) {
	r := New()
	old := newDevice(t, "pump_1", "COM3", 10)
	r.Replace([]*Device{old})
	r.Replace(nil)
	// Old port must be closed.
	if _, err := old.Conn.Write([]byte{1}); err == nil {
		t.Errorf("old conn should be closed after Replace")
	}
}

func TestRegistry_ListSortedByTypeAndPort(t *testing.T) {
	r := New()
	r.Replace([]*Device{
		newDevice(t, "valve_1", "COM4", 30),
		newDevice(t, "pump_1", "COM3", 10),
		newDevice(t, "densitometer_1", "COM7", 70),
	})
	got := r.List()
	if len(got) != 3 {
		t.Fatalf("List len: got %d", len(got))
	}
	if got[0].TypeCode != 10 || got[1].TypeCode != 30 || got[2].TypeCode != 70 {
		t.Errorf("not sorted by TypeCode: %v %v %v", got[0].TypeCode, got[1].TypeCode, got[2].TypeCode)
	}
}

func TestRegistry_DiscoveryLockSerializes(t *testing.T) {
	r := New()
	if !r.LockDiscovery() {
		t.Fatalf("first LockDiscovery should succeed")
	}
	if r.LockDiscovery() {
		t.Errorf("second LockDiscovery should fail (already locked)")
	}
	r.UnlockDiscovery()
	if !r.LockDiscovery() {
		t.Errorf("LockDiscovery after Unlock should succeed")
	}
	r.UnlockDiscovery()
}

func TestDevice_TryLockReleasesProperly(t *testing.T) {
	d := newDevice(t, "pump_1", "COM3", 10)
	if !d.TryLock() {
		t.Fatal("first TryLock should succeed")
	}
	if d.TryLock() {
		t.Error("second TryLock should fail")
	}
	d.Unlock()
	if !d.TryLock() {
		t.Error("TryLock after Unlock should succeed")
	}
	d.Unlock()
}

func TestRegistry_RemoveByID(t *testing.T) {
	r := New()
	r.Replace([]*Device{newDevice(t, "pump_1", "COM3", 10)})
	r.Remove("pump_1")
	if _, ok := r.Get("pump_1"); ok {
		t.Errorf("Remove did not delete pump_1")
	}
}

func TestRegistry_ConcurrentGetIsSafe(t *testing.T) {
	r := New()
	r.Replace([]*Device{newDevice(t, "pump_1", "COM3", 10)})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.Get("pump_1")
		}()
	}
	wg.Wait()
}
