package api

import (
	"context"
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

//nolint:unused // fixture for the Task 6 idle-timeout test; not yet exercised in Task 4
func newServerWithIdle(t *testing.T, reg *registry.Registry, op *labserial.FakeOpener, idle time.Duration) *Server {
	return buildServer(t, reg, op, true, idle)
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
