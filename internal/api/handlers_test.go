package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/power"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// fakeDiscoverFn returns a closure suitable for Server.discover.
func fakeDiscoverFn(devs []*registry.Device, err error) DiscoverFn {
	return func(ctx context.Context) ([]*registry.Device, error) {
		return devs, err
	}
}

func newTestServer(t *testing.T, reg *registry.Registry, disc DiscoverFn) http.Handler {
	t.Helper()
	if disc == nil {
		disc = fakeDiscoverFn(nil, nil)
	}
	ka, err := power.New()
	if err != nil {
		t.Fatalf("power.New: %v", err)
	}
	t.Cleanup(func() { _ = ka.Close() })
	return New(reg, disc, serial.NewFakeOpener(), false, nil, false, ka).Handler()
}

func decode(t *testing.T, body io.Reader, into any) {
	t.Helper()
	if err := json.NewDecoder(body).Decode(into); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestGetDevices_Empty(t *testing.T) {
	reg := registry.New()
	srv := newTestServer(t, reg, nil)
	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp DevicesResponse
	decode(t, rec.Body, &resp)
	if len(resp.Devices) != 0 {
		t.Errorf("devices: got %v, want []", resp.Devices)
	}
	if resp.DiscoveredAt != nil {
		t.Errorf("discovered_at: got %v, want nil", resp.DiscoveredAt)
	}
}

func TestGetDevices_AfterDiscovery(t *testing.T) {
	reg := registry.New()
	d := &registry.Device{
		ID: "pump_1", Type: "pump", TypeCode: 10, Port: "COM3",
		Conn:   serial.NewFakePort("COM3"),
		Opener: serial.NewFakeOpener(),
	}
	reg.Replace([]*registry.Device{d})

	srv := newTestServer(t, reg, nil)
	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp DevicesResponse
	decode(t, rec.Body, &resp)
	if len(resp.Devices) != 1 || resp.Devices[0].ID != "pump_1" {
		t.Errorf("devices: %v", resp.Devices)
	}
	if resp.DiscoveredAt == nil {
		t.Errorf("discovered_at: got nil, want timestamp")
	}
}

func TestPostDiscover_Success(t *testing.T) {
	reg := registry.New()
	dev := &registry.Device{
		ID: "valve_1", Type: "valve", TypeCode: 30, Port: "COM4",
		Conn:   serial.NewFakePort("COM4"),
		Opener: serial.NewFakeOpener(),
	}
	srv := newTestServer(t, reg, fakeDiscoverFn([]*registry.Device{dev}, nil))

	req := httptest.NewRequest(http.MethodPost, "/discover", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp DevicesResponse
	decode(t, rec.Body, &resp)
	if len(resp.Devices) != 1 || resp.Devices[0].ID != "valve_1" {
		t.Errorf("devices: %v", resp.Devices)
	}
	// Registry must reflect the discovery output.
	if got, ok := reg.Get("valve_1"); !ok || got.Port != "COM4" {
		t.Errorf("registry not updated: got=%v ok=%v", got, ok)
	}
}

func TestPostDiscover_ClosesOldPortsBeforeProbing(t *testing.T) {
	// Regression: the second /discover call returned an empty list because
	// the old device handles were still held during probing — Open() found
	// the COM ports locked and silently skipped them.
	reg := registry.New()
	oldPort := serial.NewFakePort("COM3")
	oldDev := &registry.Device{
		ID: "pump_1", Type: "pump", TypeCode: 10, Port: "COM3",
		Conn: oldPort, Opener: serial.NewFakeOpener(),
	}
	reg.Replace([]*registry.Device{oldDev})

	var oldPortClosedAtProbeTime bool
	discoverFn := func(ctx context.Context) ([]*registry.Device, error) {
		// At the moment discovery probes ports, the old port must already be
		// closed. Writing to it should fail with ErrClosed.
		_, err := oldPort.Write([]byte{1})
		oldPortClosedAtProbeTime = err != nil
		return []*registry.Device{}, nil
	}

	ka2, err := power.New()
	if err != nil {
		t.Fatalf("power.New: %v", err)
	}
	t.Cleanup(func() { _ = ka2.Close() })
	srv := New(reg, discoverFn, serial.NewFakeOpener(), false, nil, false, ka2).Handler()
	req := httptest.NewRequest(http.MethodPost, "/discover", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d", rec.Code)
	}
	if !oldPortClosedAtProbeTime {
		t.Errorf("old device port was still open when discoverFn was called — Open() would lock against it")
	}
}

func TestPostDiscover_AlreadyRunning(t *testing.T) {
	reg := registry.New()
	if !reg.LockDiscovery() {
		t.Fatal("setup: LockDiscovery should succeed")
	}
	defer reg.UnlockDiscovery()
	srv := newTestServer(t, reg, nil)

	req := httptest.NewRequest(http.MethodPost, "/discover", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "discovery in progress") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func makeFakeDevice(t *testing.T, id, port string, typeCode byte, fp *serial.FakePort, opener *serial.FakeOpener) *registry.Device {
	t.Helper()
	if opener == nil {
		opener = serial.NewFakeOpener()
	}
	if fp == nil {
		fp = serial.NewFakePort(port)
	}
	opener.Add(fp)
	typeName := map[byte]string{10: "pump", 30: "valve", 70: "densitometer"}[typeCode]
	return &registry.Device{
		ID: id, Type: typeName, TypeCode: typeCode, Port: port,
		Conn: fp, Opener: opener,
	}
}

func postCmd(t *testing.T, srv http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestPostCommand_DeviceNotFound(t *testing.T) {
	reg := registry.New()
	srv := newTestServer(t, reg, nil)
	rec := postCmd(t, srv, "/devices/pump_1/command", `{"command":[1,2,3,4,0]}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rec.Code)
	}
}

func TestPostCommand_HappyPathWithReply(t *testing.T) {
	reg := registry.New()
	fp := serial.NewFakePort("COM3")
	fp.Feed([]byte{10, 1, 2, 3})
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, fp, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command", `{"command":[1,2,3,4,0]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp CommandResponse
	decode(t, rec.Body, &resp)
	want := []int{10, 1, 2, 3}
	if len(resp.Response) != len(want) {
		t.Fatalf("response: got %v, want %v", resp.Response, want)
	}
	for i := range want {
		if resp.Response[i] != want[i] {
			t.Errorf("response[%d]: got %d, want %d", i, resp.Response[i], want[i])
		}
	}
	// Verify the device received the command bytes.
	written := fp.Written()
	wantWritten := []byte{1, 2, 3, 4, 0}
	if string(written) != string(wantWritten) {
		t.Errorf("written: got %v, want %v", written, wantWritten)
	}
}

func TestPostCommand_DebugLogsBytesAsIntArrays(t *testing.T) {
	// Regression: the Debug "command bytes" line used to render []byte via
	// slog's default JSON encoder, which base64-encodes them ("AQIDBAA=").
	// Operators want raw integer arrays so the bytes are readable in logs.
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	reg := registry.New()
	fp := serial.NewFakePort("COM3")
	fp.Feed([]byte{10, 1, 0, 181})
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, fp, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command", `{"command":[1,2,3,4,0]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}

	var line struct {
		Msg  string `json:"msg"`
		Cmd  any    `json:"cmd"`
		Resp any    `json:"resp"`
	}
	var found bool
	for _, raw := range bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n")) {
		var probe struct {
			Msg string `json:"msg"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("log line is not JSON: %s", raw)
		}
		if probe.Msg != "command bytes" {
			continue
		}
		if err := json.Unmarshal(raw, &line); err != nil {
			t.Fatalf("decode command-bytes line: %v", err)
		}
		found = true
		break
	}
	if !found {
		t.Fatalf("did not find 'command bytes' debug line in:\n%s", buf.String())
	}

	wantCmd := []any{1.0, 2.0, 3.0, 4.0, 0.0}
	wantResp := []any{10.0, 1.0, 0.0, 181.0}
	gotCmd, ok := line.Cmd.([]any)
	if !ok {
		t.Fatalf("cmd: got %T (%v), want []any (JSON number array)", line.Cmd, line.Cmd)
	}
	if !equalJSONNums(gotCmd, wantCmd) {
		t.Errorf("cmd: got %v, want %v", gotCmd, wantCmd)
	}
	gotResp, ok := line.Resp.([]any)
	if !ok {
		t.Fatalf("resp: got %T (%v), want []any (JSON number array)", line.Resp, line.Resp)
	}
	if !equalJSONNums(gotResp, wantResp) {
		t.Errorf("resp: got %v, want %v", gotResp, wantResp)
	}
}

func equalJSONNums(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		af, aok := a[i].(float64)
		bf, bok := b[i].(float64)
		if !aok || !bok || af != bf {
			return false
		}
	}
	return true
}

func TestPostCommand_NoReplyReturnsEmpty(t *testing.T) {
	reg := registry.New()
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, nil, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command?timeout_ms=20", `{"command":[1,2,3]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp CommandResponse
	decode(t, rec.Body, &resp)
	if len(resp.Response) != 0 {
		t.Errorf("response: got %v, want []", resp.Response)
	}
}

func TestPostCommand_WaitForResponseFalse(t *testing.T) {
	reg := registry.New()
	fp := serial.NewFakePort("COM3")
	fp.Feed([]byte{10, 1, 2, 3}) // would be returned, but caller opts out
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, fp, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command?wait_for_response=false", `{"command":[1,2,3]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp CommandResponse
	decode(t, rec.Body, &resp)
	if len(resp.Response) != 0 {
		t.Errorf("response: got %v, want []", resp.Response)
	}
}

func TestPostCommand_ExpectedBytesStopsEarly(t *testing.T) {
	reg := registry.New()
	fp := serial.NewFakePort("COM3")
	fp.Feed([]byte{10, 1, 2, 3, 99, 99, 99}) // more than 4 — should stop at 4
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, fp, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command?expected_response_bytes=4", `{"command":[1,2,3]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp CommandResponse
	decode(t, rec.Body, &resp)
	if len(resp.Response) != 4 {
		t.Errorf("response: got %v, want 4 bytes", resp.Response)
	}
}

func TestPostCommand_DeviceBusy(t *testing.T) {
	reg := registry.New()
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, nil, nil)
	reg.Replace([]*registry.Device{dev})
	if !dev.TryLock() {
		t.Fatal("setup: TryLock should succeed")
	}
	defer dev.Unlock()
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command", `{"command":[1,2,3]}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "device busy") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func TestPostCommand_BadByte(t *testing.T) {
	reg := registry.New()
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, nil, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command", `{"command":[300,1,2]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
}

func TestPostCommand_BodyTooLarge(t *testing.T) {
	reg := registry.New()
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, nil, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	body := strings.Builder{}
	body.WriteString(`{"command":[`)
	for i := 0; i < 20000; i++ {
		if i > 0 {
			body.WriteString(",")
		}
		body.WriteString("1")
	}
	body.WriteString("]}")

	rec := postCmd(t, srv, "/devices/pump_1/command", body.String())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (body too large)", rec.Code)
	}
}

func TestPostCommand_CommandTooLong(t *testing.T) {
	reg := registry.New()
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, nil, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	body := strings.Builder{}
	body.WriteString(`{"command":[`)
	for i := 0; i < 1100; i++ {
		if i > 0 {
			body.WriteString(",")
		}
		body.WriteString("0")
	}
	body.WriteString("]}")

	rec := postCmd(t, srv, "/devices/pump_1/command", body.String())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (command too long)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "exceeds max") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func TestPostCommand_UnknownField(t *testing.T) {
	reg := registry.New()
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, nil, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command", `{"command":[1,2,3],"hidden":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (unknown field)", rec.Code)
	}
}

func TestPostCommand_BadQueryParam(t *testing.T) {
	reg := registry.New()
	dev := makeFakeDevice(t, "pump_1", "COM3", 10, nil, nil)
	reg.Replace([]*registry.Device{dev})
	srv := newTestServer(t, reg, nil)

	rec := postCmd(t, srv, "/devices/pump_1/command?timeout_ms=99999999", `{"command":[1]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
}

func TestPostCommand_ReconnectThenSuccess(t *testing.T) {
	// First Write fails (port closed). Reconnect-reprobe: re-open succeeds,
	// probe returns same type, retry write+read succeeds.
	reg := registry.New()
	fp := serial.NewFakePort("COM3")
	_ = fp.Close() // simulate port already closed → next Write returns ErrClosed
	opener := serial.NewFakeOpener()
	opener.Add(fp)
	dev := &registry.Device{
		ID: "pump_1", Type: "pump", TypeCode: 10, Port: "COM3",
		Conn: fp, Opener: opener,
	}
	reg.Replace([]*registry.Device{dev})

	// Pre-feed: Probe.Drain takes 200ms and clears the buffer repeatedly.
	// Feed the probe reply AFTER the drain completes, then feed the command reply.
	go func() {
		// Wait for Probe's Drain (200ms) + some buffer
		time.Sleep(220 * time.Millisecond)
		fp.Feed([]byte{10, 1, 2, 3}) // probe reply
		time.Sleep(100 * time.Millisecond)
		fp.Feed([]byte{42, 43}) // actual command reply
	}()

	srv := newTestServer(t, reg, nil)
	rec := postCmd(t, srv, "/devices/pump_1/command?timeout_ms=500", `{"command":[7,8]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp CommandResponse
	decode(t, rec.Body, &resp)
	if len(resp.Response) != 2 || resp.Response[0] != 42 || resp.Response[1] != 43 {
		t.Errorf("response: got %v, want [42 43]", resp.Response)
	}
}

func TestPostCommand_ReconnectIdentityChanged(t *testing.T) {
	reg := registry.New()
	fp := serial.NewFakePort("COM3")
	_ = fp.Close() // simulate port already closed → next Write returns ErrClosed
	opener := serial.NewFakeOpener()
	opener.Add(fp)
	dev := &registry.Device{
		ID: "pump_1", Type: "pump", TypeCode: 10, Port: "COM3",
		Conn: fp, Opener: opener,
	}
	reg.Replace([]*registry.Device{dev})

	go func() {
		// Wait for Probe's Drain (200ms) + some buffer
		time.Sleep(220 * time.Millisecond)
		fp.Feed([]byte{30, 1, 1, 6}) // valve, not pump
	}()

	srv := newTestServer(t, reg, nil)
	rec := postCmd(t, srv, "/devices/pump_1/command?timeout_ms=500", `{"command":[1]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "device identity changed") {
		t.Errorf("body: %s", rec.Body.String())
	}
	if _, ok := reg.Get("pump_1"); ok {
		t.Errorf("device should have been removed from registry")
	}
}

func TestPostCommand_ReconnectFailsToOpen(t *testing.T) {
	reg := registry.New()
	fp := serial.NewFakePort("COM3")
	_ = fp.Close() // simulate port already closed → next Write returns ErrClosed
	opener := serial.NewFakeOpener()
	// Do NOT register fp with opener — Open will fail.
	dev := &registry.Device{
		ID: "pump_1", Type: "pump", TypeCode: 10, Port: "COM3",
		Conn: fp, Opener: opener,
	}
	reg.Replace([]*registry.Device{dev})

	srv := newTestServer(t, reg, nil)
	rec := postCmd(t, srv, "/devices/pump_1/command", `{"command":[1]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "device unreachable") {
		t.Errorf("body: %s", rec.Body.String())
	}
	// Device stays in the registry per spec (next call will retry).
	if _, ok := reg.Get("pump_1"); !ok {
		t.Errorf("device should NOT have been removed (only identity-change does that)")
	}
}

func TestGetAgentInfo_200JSON(t *testing.T) {
	reg := registry.New()
	srv := newTestServer(t, reg, nil)
	req := httptest.NewRequest(http.MethodGet, "/agent/info", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control: got %q, want no-store", cc)
	}

	var got map[string]any
	decode(t, rec.Body, &got)
	for _, key := range []string{"version", "os", "arch", "hostname", "uptime_seconds"} {
		if _, ok := got[key]; !ok {
			t.Errorf("required key %q missing from response: %v", key, got)
		}
	}
}

func TestGetAgentInfo_RejectsNonGET(t *testing.T) {
	reg := registry.New()
	srv := newTestServer(t, reg, nil)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/agent/info", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /agent/info: got %d, want 405", method, rec.Code)
		}
	}
}
