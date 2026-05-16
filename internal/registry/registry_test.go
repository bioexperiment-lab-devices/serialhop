package registry

import (
	"log/slog"
	"sync"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

func newDevice(t *testing.T, id, port string, typeCode byte) *Device {
	t.Helper()
	return &Device{
		ID:       id,
		Type:     typeName(typeCode),
		TypeCode: typeCode,
		Port:     port,
		Conn:     serial.NewFakePort(port),
		Opener:   serial.NewFakeOpener(),
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

func TestRegistry_IsDiscovering(t *testing.T) {
	r := New()
	if r.IsDiscovering() {
		t.Errorf("IsDiscovering(): got true on fresh registry, want false")
	}
	if !r.LockDiscovery() {
		t.Fatal("LockDiscovery: setup failed")
	}
	if !r.IsDiscovering() {
		t.Errorf("IsDiscovering(): got false while locked, want true")
	}
	r.UnlockDiscovery()
	if r.IsDiscovering() {
		t.Errorf("IsDiscovering(): got true after Unlock, want false")
	}
}

func TestRegistry_HasPort(t *testing.T) {
	r := New()

	if id, ok := r.HasPort("COM3"); ok {
		t.Errorf("HasPort(COM3): got (%q, true) on empty registry, want (\"\", false)", id)
	}

	r.Replace([]*Device{
		{ID: "pump_1", Type: "pump", TypeCode: 10, Port: "COM3"},
		{ID: "valve_1", Type: "valve", TypeCode: 30, Port: "COM4"},
	})

	id, ok := r.HasPort("COM3")
	if !ok || id != "pump_1" {
		t.Errorf("HasPort(COM3): got (%q, %v), want (\"pump_1\", true)", id, ok)
	}
	id, ok = r.HasPort("COM99")
	if ok || id != "" {
		t.Errorf("HasPort(COM99): got (%q, %v), want (\"\", false)", id, ok)
	}
}

func TestDisconnectAll_EmptyRegistry(t *testing.T) {
	r := New()
	got := r.DisconnectAll()
	if got != 0 {
		t.Errorf("DisconnectAll on empty: got %d, want 0", got)
	}
	if len(r.List()) != 0 {
		t.Errorf("registry not empty after DisconnectAll")
	}
}

func TestRegistry_Replace_LogsInfoRecord(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	r := New()
	r.Replace([]*Device{
		newDevice(t, "pump_1", "COM3", 10),
		newDevice(t, "valve_1", "COM4", 30),
	})

	rec.AssertRecord(t, slog.LevelInfo, "registry replace", map[string]any{
		"count":    2,
		"previous": 0,
	})
}

func TestDisconnectAll_PopulatedRegistry(t *testing.T) {
	r := New()
	devs := []*Device{
		{ID: "a", Type: "pump", TypeCode: 10, Port: "COM3", Conn: serial.NewFakePort("COM3")},
		{ID: "b", Type: "valve", TypeCode: 30, Port: "COM4", Conn: serial.NewFakePort("COM4")},
		{ID: "c", Type: "densitometer", TypeCode: 70, Port: "COM5", Conn: serial.NewFakePort("COM5")},
	}
	r.Replace(devs)

	got := r.DisconnectAll()
	if got != 3 {
		t.Errorf("DisconnectAll: got %d, want 3", got)
	}
	if len(r.List()) != 0 {
		t.Errorf("registry not empty after DisconnectAll")
	}
}
