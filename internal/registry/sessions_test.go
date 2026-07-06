package registry_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

type nullDriver struct {
	detached atomic.Bool
}

func (d *nullDriver) Attach(ctx context.Context, probeReply []byte) (device.Info, error) {
	return device.Info{DeviceType: "stub", Model: "stub-1", FirmwareVersion: "legacy", ProtocolVersion: "1.0"}, nil
}
func (d *nullDriver) Execute(ctx context.Context, cmd string, params json.RawMessage) (any, *device.CmdError) {
	return nil, device.ErrUnknownCommand(cmd)
}
func (d *nullDriver) Tick(now time.Time) {}
func (d *nullDriver) Detach()            { d.detached.Store(true) }

// newStubSession returns a started session on the named port and its driver.
func newStubSession(t *testing.T, id, port string) (*device.Session, *nullDriver) {
	t.Helper()
	drv := &nullDriver{}
	fp := serial.NewFakePort(port)
	opener := serial.NewFakeOpener()
	opener.Add(fp)
	conn, err := opener.Open(port)
	if err != nil {
		t.Fatal(err)
	}
	s := device.NewSession(device.SessionConfig{
		ID: id, Type: "stub", TypeCode: 201, PortName: port,
		Conn: conn, Opener: opener, Clock: device.NewFakeClock(time.Unix(1000, 0)),
		StateDir: t.TempDir(),
		Factory:  func(*device.Session) device.Driver { return drv },
		Reprobe:  func(serial.Port) ([]byte, error) { return []byte{201, 0, 0, 1}, nil },
	})
	s.Start(context.Background())
	s.WaitFirstAttach(context.Background())
	t.Cleanup(s.Close)
	return s, drv
}

func TestReplaceInstallsAndStampsDiscoveredAt(t *testing.T) {
	r := registry.NewSessionRegistry()
	s1, _ := newStubSession(t, "id1", "COM3")

	r.Replace([]*device.Session{s1})

	list := r.List()
	if len(list) != 1 || list[0] != s1 {
		t.Fatalf("List() = %v, want [s1]", list)
	}
	if got, ok := r.Get("id1"); !ok || got != s1 {
		t.Fatalf("Get(id1) = %v, %v, want s1, true", got, ok)
	}
	if r.DiscoveredAt() == nil {
		t.Fatal("DiscoveredAt() = nil, want non-nil after Replace")
	}
}

func TestReplaceClosesPreviousSessions(t *testing.T) {
	r := registry.NewSessionRegistry()
	s1, drv1 := newStubSession(t, "id1", "COM3")
	s2, _ := newStubSession(t, "id2", "COM4")

	r.Replace([]*device.Session{s1})
	r.Replace([]*device.Session{s2})

	if !drv1.detached.Load() {
		t.Error("drv1.detached = false, want true after replaced")
	}
	if _, ok := r.Get("id1"); ok {
		t.Error("Get(id1) found, want gone after Replace")
	}
	list := r.List()
	if len(list) != 1 || list[0] != s2 {
		t.Fatalf("List() = %v, want [s2]", list)
	}
}

func TestReplaceNilClosesButKeepsTimestamp(t *testing.T) {
	r := registry.NewSessionRegistry()
	s1, drv1 := newStubSession(t, "id1", "COM3")

	r.Replace([]*device.Session{s1})
	firstStamp := r.DiscoveredAt()

	r.Replace(nil)

	if !drv1.detached.Load() {
		t.Error("drv1.detached = false, want true after Replace(nil)")
	}
	if list := r.List(); len(list) != 0 {
		t.Fatalf("List() = %v, want empty after Replace(nil)", list)
	}
	got := r.DiscoveredAt()
	if got == nil || !got.Equal(*firstStamp) {
		t.Fatalf("DiscoveredAt() = %v, want unchanged %v", got, firstStamp)
	}
}

func TestCloseAllPreservesDiscoveredAt(t *testing.T) {
	r := registry.NewSessionRegistry()
	s1, drv1 := newStubSession(t, "id1", "COM3")

	r.Replace([]*device.Session{s1})
	firstStamp := r.DiscoveredAt()

	r.CloseAll()

	if !drv1.detached.Load() {
		t.Error("drv1.detached = false, want true after CloseAll")
	}
	if list := r.List(); len(list) != 0 {
		t.Fatalf("List() = %v, want empty after CloseAll", list)
	}
	got := r.DiscoveredAt()
	if got == nil || !got.Equal(*firstStamp) {
		t.Fatalf("DiscoveredAt() = %v, want unchanged %v", got, firstStamp)
	}
}

func TestDisconnectAllReturnsCount(t *testing.T) {
	r := registry.NewSessionRegistry()
	s1, drv1 := newStubSession(t, "id1", "COM3")
	s2, drv2 := newStubSession(t, "id2", "COM4")
	r.Replace([]*device.Session{s1, s2})

	n := r.DisconnectAll()

	if n != 2 {
		t.Fatalf("DisconnectAll() = %d, want 2", n)
	}
	if !drv1.detached.Load() || !drv2.detached.Load() {
		t.Error("both drivers should be detached after DisconnectAll")
	}
	if list := r.List(); len(list) != 0 {
		t.Fatalf("List() = %v, want empty after DisconnectAll", list)
	}
}

func TestDisconnectByPort(t *testing.T) {
	r := registry.NewSessionRegistry()
	s1, drv1 := newStubSession(t, "id1", "COM3")
	s2, drv2 := newStubSession(t, "id2", "COM4")
	r.Replace([]*device.Session{s1, s2})

	if ok := r.DisconnectByPort("COM3"); !ok {
		t.Fatal("DisconnectByPort(COM3) = false, want true")
	}
	if !drv1.detached.Load() {
		t.Error("drv1.detached = false, want true after DisconnectByPort")
	}
	if drv2.detached.Load() {
		t.Error("drv2.detached = true, want false (untouched session)")
	}
	if _, ok := r.Get("id1"); ok {
		t.Error("Get(id1) found, want gone after DisconnectByPort")
	}

	if ok := r.DisconnectByPort("COM9"); ok {
		t.Error("DisconnectByPort(COM9) = true, want false for unknown port")
	}
}

func TestHasPort(t *testing.T) {
	r := registry.NewSessionRegistry()
	s1, _ := newStubSession(t, "id1", "COM3")
	r.Replace([]*device.Session{s1})

	if id, ok := r.HasPort("COM3"); !ok || id != "id1" {
		t.Fatalf("HasPort(COM3) = %q, %v, want id1, true", id, ok)
	}
	if _, ok := r.HasPort("COM9"); ok {
		t.Error("HasPort(COM9) = true, want false")
	}
}

func TestListPreservesReplaceOrder(t *testing.T) {
	r := registry.NewSessionRegistry()
	s1, _ := newStubSession(t, "id1", "COM3")
	s2, _ := newStubSession(t, "id2", "COM4")

	r.Replace([]*device.Session{s2, s1})

	list := r.List()
	if len(list) != 2 || list[0] != s2 || list[1] != s1 {
		t.Fatalf("List() = %v, want [s2, s1]", list)
	}
}

func TestDiscoveryGate(t *testing.T) {
	r := registry.NewSessionRegistry()

	if !r.LockDiscovery() {
		t.Fatal("LockDiscovery() = false, want true on first acquire")
	}
	if !r.IsDiscovering() {
		t.Error("IsDiscovering() = false, want true while held")
	}
	if r.LockDiscovery() {
		t.Error("LockDiscovery() = true, want false while already held")
	}

	r.UnlockDiscovery()

	if r.IsDiscovering() {
		t.Error("IsDiscovering() = true, want false after unlock")
	}
	if !r.LockDiscovery() {
		t.Error("LockDiscovery() = false, want true after unlock")
	}
}
