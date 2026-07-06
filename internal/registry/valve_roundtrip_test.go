package registry_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device/valve"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// buildValveSession mirrors the app's discover wiring: factory via
// LookupDriver, probe reply from the fake device, shared state dir.
func buildValveSession(t *testing.T, opener *serial.FakeOpener, portName, stateDir string) *device.Session {
	t.Helper()
	name, factory, ok := device.LookupDriver(valve.TypeCode)
	if !ok {
		t.Fatal("valve driver not registered")
	}
	conn, err := opener.Open(portName)
	if err != nil {
		t.Fatal(err)
	}
	s := device.NewSession(device.SessionConfig{
		ID: "valve_1", Type: name, TypeCode: valve.TypeCode, PortName: portName,
		Conn: conn, Opener: opener, Clock: device.NewFakeClock(time.Unix(1000, 0)),
		StateDir: stateDir, Factory: factory,
		ProbeReply: []byte{30, 1, 1, 6}, // radial-6 build
		Reprobe:    func(p serial.Port) ([]byte, error) { return []byte{30, 1, 1, 6}, nil },
	})
	s.Start(context.Background())
	s.WaitFirstAttach(context.Background())
	return s
}

func exec(t *testing.T, s *device.Session, cmd, params string) device.Response {
	t.Helper()
	req := device.Request{ID: "t-" + cmd, Cmd: cmd}
	if params != "" {
		req.Params = json.RawMessage(params)
	}
	return s.Execute(context.Background(), req)
}

func resultMap(t *testing.T, resp device.Response) map[string]any {
	t.Helper()
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestValveHomedStateSurvivesDiscoverRebuild drives the production
// re-discovery flow end to end: CloseAll detaches the session (the driver
// persists homed state), a fresh session attaches on the same port + state
// dir (as a re-probe of the same device would), and the recovered state is
// visible through the new session.
func TestValveHomedStateSurvivesDiscoverRebuild(t *testing.T) {
	oldPB, oldDW := device.PerByteTimeout, device.DrainWindow
	device.PerByteTimeout, device.DrainWindow = 10*time.Millisecond, 0
	t.Cleanup(func() { device.PerByteTimeout, device.DrainWindow = oldPB, oldDW })

	valve.Register()
	stateDir := t.TempDir()
	port := serial.NewFakePort("COM9")
	opener := serial.NewFakeOpener()
	opener.Add(port)
	reg := registry.New()

	// Session 1: attach (position query answers counter 0), home at 4.
	port.Feed([]byte{30, 1, 1, 0}) // Attach's position-query reply
	s1 := buildValveSession(t, opener, "COM9", stateDir)
	reg.Replace([]*device.Session{s1})
	if !s1.Connected() {
		t.Fatal("session 1 must attach")
	}
	port.Feed([]byte{30, 1, 1, 0}) // home's belief-resync reply
	if resp := exec(t, s1, "home", `{"position":4}`); resp.Status != "ok" {
		t.Fatalf("home: %+v", resp)
	}

	// Re-discovery: CloseAll persists via Detach; rebuild on same port+dir.
	reg.CloseAll()
	port.Feed([]byte{30, 1, 1, 0}) // new Attach's position query: counter unchanged
	s2 := buildValveSession(t, opener, "COM9", stateDir)
	reg.Replace([]*device.Session{s2})
	t.Cleanup(reg.CloseAll)
	if !s2.Connected() {
		t.Fatal("session 2 must attach")
	}

	port.Feed([]byte{30, 1, 1, 0}) // status's idle CHECK_BELIEF reply
	sm := resultMap(t, exec(t, s2, "status", ""))
	if sm["state"] != "idle" || sm["homed"] != true || sm["position"] != 4.0 {
		t.Fatalf("homed state must survive the rebuild: %v", fmt.Sprintf("%v", sm))
	}
}
