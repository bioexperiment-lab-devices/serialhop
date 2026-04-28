package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

func newOpener(t *testing.T, ports map[string][]byte) *serial.FakeOpener {
	t.Helper()
	o := serial.NewFakeOpener()
	for name, reply := range ports {
		fp := serial.NewFakePort(name)
		// Feed response after drain completes but during read timeout
		// (drain is 200ms, probe bytes add ~50ms, so 300ms total)
		go func(port *serial.FakePort, data []byte) {
			time.Sleep(300 * time.Millisecond)
			port.Feed(data)
		}(fp, reply)
		o.Add(fp)
	}
	return o
}

func TestRun_AssignsSequentialIDs(t *testing.T) {
	o := newOpener(t, map[string][]byte{
		"COM3": {10, 1, 2, 3},
		"COM4": {10, 4, 5, 6},
		"COM5": {30, 1, 1, 6},
	})
	devs, err := Run(context.Background(), o, []string{"COM3", "COM4", "COM5"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r := registry.New()
	r.Replace(devs)
	got := r.List()
	if len(got) != 3 {
		t.Fatalf("List len: got %d", len(got))
	}
	wantIDs := []string{"pump_1", "pump_2", "valve_1"}
	for i, w := range wantIDs {
		if got[i].ID != w {
			t.Errorf("got[%d].ID=%q, want %q", i, got[i].ID, w)
		}
	}
}

func TestRun_SkipsUnknownAndPartial(t *testing.T) {
	o := newOpener(t, map[string][]byte{
		"COM3": {10, 1, 2, 3},
		"COM4": {99, 1, 2, 3},        // unknown type byte
		"COM5": {30, 1, 1},           // only 3 bytes
	})
	devs, err := Run(context.Background(), o, []string{"COM3", "COM4", "COM5"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(devs) != 1 {
		t.Errorf("got %d devices, want 1 (only the pump)", len(devs))
	}
}

func TestRun_EmptyPortList(t *testing.T) {
	o := serial.NewFakeOpener()
	devs, err := Run(context.Background(), o, []string{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(devs) != 0 {
		t.Errorf("expected no devices, got %d", len(devs))
	}
}

func TestFilterPorts_Include(t *testing.T) {
	enumerated := []string{"COM1", "COM3", "COM4"}
	got := FilterPorts(enumerated, []string{"COM3", "COM5"}, nil)
	if len(got) != 1 || got[0] != "COM3" {
		t.Errorf("include: got %v, want [COM3]", got)
	}
}

func TestFilterPorts_Exclude(t *testing.T) {
	enumerated := []string{"COM1", "COM3", "COM4"}
	got := FilterPorts(enumerated, nil, []string{"COM1"})
	if len(got) != 2 || got[0] != "COM3" || got[1] != "COM4" {
		t.Errorf("exclude: got %v, want [COM3 COM4]", got)
	}
}

func TestFilterPorts_NoFilter(t *testing.T) {
	enumerated := []string{"COM1", "COM3"}
	got := FilterPorts(enumerated, nil, nil)
	if len(got) != 2 {
		t.Errorf("no filter: got %v, want 2 entries", got)
	}
}
