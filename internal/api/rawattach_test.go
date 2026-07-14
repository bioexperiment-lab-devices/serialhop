package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/power"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// buildServer mirrors the construction the existing api tests use
// (see v1_test.go: ka, _ := power.New(); New(reg, disc, opener, nil, false, ka)),
// with the two new raw-serial args threaded through.
func buildServer(t *testing.T, reg *registry.Registry, op *labserial.FakeOpener, enabled bool, idle time.Duration) *Server {
	t.Helper()
	ka, err := power.New()
	if err != nil {
		t.Fatalf("power.New: %v", err)
	}
	t.Cleanup(func() { _ = ka.Close() })
	disc := func(_ context.Context) ([]*device.Session, error) { return nil, nil }
	return New(reg, disc, op, nil, false, ka, enabled, idle)
}

func newAttachServer(t *testing.T, enabled bool, ports ...string) (*httptest.Server, *labserial.FakeOpener, *registry.Registry) {
	t.Helper()
	op := labserial.NewFakeOpener()
	for _, p := range ports {
		op.Add(labserial.NewFakePort(p))
	}
	reg := registry.New()
	ts := httptest.NewServer(buildServer(t, reg, op, enabled, 0).Handler())
	t.Cleanup(ts.Close)
	return ts, op, reg
}

func newServerWithIdle(t *testing.T, reg *registry.Registry, op *labserial.FakeOpener, idle time.Duration) *Server {
	return buildServer(t, reg, op, true, idle)
}

// newSessionOwningPort builds a started device.Session that owns the named
// port, so reg.HasPort(port) reports true — mirrors v1_test.go's
// newFakeSession but with a caller-chosen port name.
func newSessionOwningPort(t *testing.T, port string) *device.Session {
	t.Helper()
	fp := labserial.NewFakePort(port)
	opener := labserial.NewFakeOpener()
	opener.Add(fp)
	conn, err := opener.Open(port)
	if err != nil {
		t.Fatal(err)
	}
	s := device.NewSession(device.SessionConfig{
		ID: "owner", Type: "fake", TypeCode: 240, PortName: port,
		Conn: conn, Opener: opener, StateDir: t.TempDir(),
		Factory: func(sess *device.Session) device.Driver { return &fakeDriver{s: sess} },
		Reprobe: func(labserial.Port) ([]byte, error) { return nil, errors.New("no reprobe in tests") },
	})
	s.Start(context.Background())
	s.WaitFirstAttach(context.Background())
	t.Cleanup(s.Close)
	return s
}

func TestAttachDisabledReturns403(t *testing.T) {
	ts, _, _ := newAttachServer(t, false, "COM3")
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/serial/ports/COM3/attach")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestAttachUnknownPortReturns404(t *testing.T) {
	ts, _, _ := newAttachServer(t, true, "COM3")
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/serial/ports/COM9/attach")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAttachAlreadyLeasedReturns409(t *testing.T) {
	ts, _, reg := newAttachServer(t, true, "COM3")
	defer ts.Close()
	if !reg.TryAcquireRaw("COM3") {
		t.Fatal("pre-acquire failed")
	}
	resp, err := http.Get(ts.URL + "/serial/ports/COM3/attach")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestAttachByteRoundTrip(t *testing.T) {
	ts, op, reg := newAttachServer(t, true, "COM3")
	defer ts.Close()
	fp, _ := op.Open("COM3") // grab the shared FakePort to Feed/Written
	fake := fp.(*labserial.FakePort)

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/serial/ports/COM3/attach?baud=115200"
	ws, dialResp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = dialResp.Body.Close() }()
	defer func() { _ = ws.Close() }()

	// expect a ready control frame first
	mt, msg, err := ws.ReadMessage()
	if err != nil || mt != websocket.TextMessage || !strings.Contains(string(msg), `"ready"`) {
		t.Fatalf("first frame mt=%d msg=%s err=%v; want ready text", mt, msg, err)
	}

	// client -> serial
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatal(err)
	}
	// serial -> client
	fake.Feed([]byte{0xAA, 0xBB})
	mt, msg, err = ws.ReadMessage()
	if err != nil || mt != websocket.BinaryMessage {
		t.Fatalf("rx frame mt=%d err=%v", mt, err)
	}
	if string(msg) != string([]byte{0xAA, 0xBB}) {
		t.Fatalf("rx = %v, want [170 187]", msg)
	}

	// give the ws->serial pump a moment, then assert the write landed
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(fake.Written()) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := fake.Written(); string(got) != string([]byte{0x01, 0x02, 0x03}) {
		t.Fatalf("written = %v, want [1 2 3]", got)
	}

	_ = ws.Close()
	// lease released after close
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(reg.RawLeasedPorts()) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := reg.RawLeasedPorts(); len(got) != 0 {
		t.Fatalf("lease not released: %v", got)
	}
}

func TestAttachBadBaudReturns400(t *testing.T) {
	ts, _, _ := newAttachServer(t, true, "COM3")
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/serial/ports/COM3/attach?baud=abc")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAttachOwnedPortReturns409(t *testing.T) {
	ts, _, reg := newAttachServer(t, true, "COM3")
	defer ts.Close()
	reg.Replace([]*device.Session{newSessionOwningPort(t, "COM3")})
	resp, err := http.Get(ts.URL + "/serial/ports/COM3/attach")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

func TestAttachDiscoveringReturns409(t *testing.T) {
	ts, _, reg := newAttachServer(t, true, "COM3")
	defer ts.Close()
	if !reg.LockDiscovery() {
		t.Fatal("LockDiscovery failed")
	}
	defer reg.UnlockDiscovery()
	resp, err := http.Get(ts.URL + "/serial/ports/COM3/attach")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

// TestAttachSerialDeathClosesSession is the regression test for the unbounded
// lease-hold bug: if the serial port dies while the client is connected but
// idle, gorilla's default ping handler auto-pongs and keeps resetting the read
// deadline, so the ws->serial ReadMessage loop never returns and the raw lease
// is held forever. The serialDone watcher must force ReadMessage to unblock the
// moment the serial reader exits, closing the session within milliseconds.
func TestAttachSerialDeathClosesSession(t *testing.T) {
	ts, op, reg := newAttachServer(t, true, "COM3")
	defer ts.Close()
	fp, _ := op.Open("COM3")
	fake := fp.(*labserial.FakePort)

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/serial/ports/COM3/attach"
	ws, dialResp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = dialResp.Body.Close() }()
	defer func() { _ = ws.Close() }()

	// consume the ready control frame
	if _, _, err := ws.ReadMessage(); err != nil {
		t.Fatalf("ready: %v", err)
	}

	// Simulate device death: close the underlying serial port. The client
	// deliberately sends nothing after this point.
	_ = fake.Close()

	// The server must close the ws promptly. A generous client-side read
	// deadline (5s) ensures the read only returns because the SERVER closed
	// the conn, not because the client self-timed-out; the outer 2s select
	// bounds "prompt". Without the watcher the server stays blocked ~40s and
	// this select times out (RED).
	done := make(chan error, 1)
	go func() {
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _, e := ws.ReadMessage()
		done <- e
	}()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("expected ws close error on serial death, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not close ws within 2s of serial death (lease would be held ~40s)")
	}

	// Lease must drain — the definitive RED/GREEN discriminator.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(reg.RawLeasedPorts()) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := reg.RawLeasedPorts(); len(got) != 0 {
		t.Fatalf("lease not released after serial death: %v", got)
	}
}

func TestAttachControlFrames(t *testing.T) {
	ts, op, _ := newAttachServer(t, true, "COM3")
	defer ts.Close()
	fp, _ := op.Open("COM3")
	fake := fp.(*labserial.FakePort)
	fake.SetModem(labserial.ModemBits{CTS: true, DSR: true})

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/serial/ports/COM3/attach"
	ws, dialResp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = dialResp.Body.Close() }()
	defer func() { _ = ws.Close() }()
	_, _, _ = ws.ReadMessage() // consume ready

	send := func(v map[string]any) { _ = ws.WriteJSON(v) }
	send(map[string]any{"op": "set_baud", "baud": 57600})
	send(map[string]any{"op": "set_dtr", "level": false})
	send(map[string]any{"op": "set_rts", "level": true})
	send(map[string]any{"op": "send_break", "ms": 200})
	send(map[string]any{"op": "get_modem"})

	// read frames until we see the modem reply
	var modem controlMsg
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		mt, msg, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if mt != websocket.TextMessage {
			continue
		}
		_ = json.Unmarshal(msg, &modem)
		if modem.Op == "modem" {
			break
		}
	}
	if !modem.CTS || !modem.DSR {
		t.Fatalf("modem reply = %+v, want CTS+DSR true", modem)
	}

	// assert side effects on the fake, allowing the pump to catch up
	waitFor(t, func() bool {
		return len(fake.BaudSequence()) > 0 &&
			fake.BaudSequence()[len(fake.BaudSequence())-1] == 57600 &&
			contains(fake.DTRSequence(), false) &&
			contains(fake.RTSSequence(), true) &&
			len(fake.BreakSequence()) == 1 && fake.BreakSequence()[0] == 200*time.Millisecond
	})
}

func TestAttachIdleTimeoutCloses(t *testing.T) {
	op := labserial.NewFakeOpener()
	op.Add(labserial.NewFakePort("COM3"))
	reg := registry.New()
	srv := newServerWithIdle(t, reg, op, 150*time.Millisecond)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/serial/ports/COM3/attach"
	ws, dialResp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = dialResp.Body.Close() }()
	defer func() { _ = ws.Close() }()
	_, _, _ = ws.ReadMessage() // ready

	// Send nothing. The server should close within ~idle timeout.
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			break // closed as expected
		}
	}
	waitFor(t, func() bool { return len(reg.RawLeasedPorts()) == 0 })
}

func contains[T comparable](xs []T, v T) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
