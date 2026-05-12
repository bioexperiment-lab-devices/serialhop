package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/discovery"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

func init() {
	// Raw-serial endpoint inherits its post-open settle default from the
	// discovery package var. Zero it for tests so existing fixtures that
	// feed responses after 250 ms remain valid without scheduling shifts.
	discovery.PostOpenSettle = 0
}

// rawSrv builds an api.Server.Handler() with the given registry, opener, and
// raw_serial.enabled flag. Used by every test in this file.
func rawSrv(t *testing.T, reg *registry.Registry, opener serial.Opener, enabled bool) http.Handler {
	t.Helper()
	return New(reg, fakeDiscoverFn(nil, nil), opener, enabled, nil, false).Handler()
}

func TestGetSerialPorts_DisabledReturns403(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	srv := rawSrv(t, reg, opener, false)

	req := httptest.NewRequest(http.MethodGet, "/serial/ports", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "raw serial disabled") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func TestGetSerialPorts_EmptyRegistry(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	opener.Add(serial.NewFakePort("COM5"))
	srv := rawSrv(t, reg, opener, true)

	req := httptest.NewRequest(http.MethodGet, "/serial/ports", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp PortsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Ports) != 2 {
		t.Fatalf("ports: got %d, want 2 (%v)", len(resp.Ports), resp.Ports)
	}
	if !sort.SliceIsSorted(resp.Ports, func(i, j int) bool { return resp.Ports[i].Name < resp.Ports[j].Name }) {
		t.Errorf("ports not sorted by name: %v", resp.Ports)
	}
	for _, p := range resp.Ports {
		if p.Discovered || p.DeviceID != "" {
			t.Errorf("port %q: got discovered=%v device_id=%q, want discovered=false device_id=\"\"", p.Name, p.Discovered, p.DeviceID)
		}
	}
}

func postRaw(t *testing.T, srv http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestPostSerialCommand_DisabledReturns403(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	srv := rawSrv(t, reg, opener, false)

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[1]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d, want 403", rec.Code)
	}
}

func TestPostSerialCommand_PortNotFound(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM99/command", `{"command":[1]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "port not found") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func TestPostSerialCommand_PortHasDiscoveredDevice(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	reg.Replace([]*registry.Device{
		{ID: "pump_1", Type: "pump", TypeCode: 10, Port: "COM3", Conn: serial.NewFakePort("COM3"), Opener: opener},
	})
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[1]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "port has discovered device") {
		t.Errorf("body: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/devices/pump_1/command") {
		t.Errorf("body should suggest /devices/pump_1/command, got: %s", rec.Body.String())
	}
}

func TestPostSerialCommand_DiscoveryInProgress(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	if !reg.LockDiscovery() {
		t.Fatal("setup: LockDiscovery should succeed")
	}
	defer reg.UnlockDiscovery()
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[1]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "discovery in progress") {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func TestPostSerialCommand_BadQueryParam(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command?timeout_ms=99999999", `{"command":[1]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
}

func TestPostSerialCommand_BadSettleQueryParam(t *testing.T) {
	cases := map[string]struct {
		url      string
		wantCode int
	}{
		"negative":    {"/serial/ports/COM3/command?post_open_settle_ms=-1&wait_for_response=false", http.StatusBadRequest},
		"too_large":   {"/serial/ports/COM3/command?post_open_settle_ms=60001&wait_for_response=false", http.StatusBadRequest},
		"not_an_int":  {"/serial/ports/COM3/command?post_open_settle_ms=abc&wait_for_response=false", http.StatusBadRequest},
		"empty_value": {"/serial/ports/COM3/command?post_open_settle_ms=&wait_for_response=false", http.StatusOK}, // empty = use default
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			reg := registry.New()
			opener := serial.NewFakeOpener()
			opener.Add(serial.NewFakePort("COM3"))
			srv := rawSrv(t, reg, opener, true)
			rec := postRaw(t, srv, c.url, `{"command":[1]}`)
			if rec.Code != c.wantCode {
				t.Errorf("%s: got %d body=%s, want %d", name, rec.Code, rec.Body.String(), c.wantCode)
			}
		})
	}
}

func TestPostSerialCommand_SettleOverrideApplied(t *testing.T) {
	// With the package-level default at 0 (set in init()) and an explicit
	// 80 ms override, the raw call must wait at least the override before
	// returning.
	reg := registry.New()
	opener := serial.NewFakeOpener()
	fp := serial.NewFakePort("COM3")
	opener.Add(fp)
	srv := rawSrv(t, reg, opener, true)

	start := time.Now()
	rec := postRaw(t, srv,
		"/serial/ports/COM3/command?post_open_settle_ms=80&wait_for_response=false",
		`{"command":[1]}`)
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	if elapsed < 80*time.Millisecond {
		t.Errorf("elapsed=%v, want >= 80ms (settle not applied)", elapsed)
	}
}

func TestPostSerialCommand_BadByte(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[300,1,2]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
}

func TestPostSerialCommand_UnknownField(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[1,2,3],"hidden":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
}

func TestPostSerialCommand_BodyTooLarge(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	srv := rawSrv(t, reg, opener, true)

	body := strings.Builder{}
	body.WriteString(`{"command":[`)
	for i := 0; i < 20000; i++ {
		if i > 0 {
			body.WriteString(",")
		}
		body.WriteString("1")
	}
	body.WriteString("]}")

	rec := postRaw(t, srv, "/serial/ports/COM3/command", body.String())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", rec.Code)
	}
}

func TestGetSerialPorts_AnnotatesDiscoveredDevices(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	opener.Add(serial.NewFakePort("COM5"))
	opener.Add(serial.NewFakePort("COM7"))

	reg.Replace([]*registry.Device{
		{ID: "pump_1", Type: "pump", TypeCode: 10, Port: "COM3", Conn: serial.NewFakePort("COM3"), Opener: opener},
		{ID: "valve_1", Type: "valve", TypeCode: 30, Port: "COM7", Conn: serial.NewFakePort("COM7"), Opener: opener},
	})

	srv := rawSrv(t, reg, opener, true)
	req := httptest.NewRequest(http.MethodGet, "/serial/ports", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp PortsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := map[string]struct {
		discovered bool
		id         string
	}{
		"COM3": {true, "pump_1"},
		"COM5": {false, ""},
		"COM7": {true, "valve_1"},
	}
	for _, p := range resp.Ports {
		w, ok := want[p.Name]
		if !ok {
			t.Errorf("unexpected port %q in response", p.Name)
			continue
		}
		if p.Discovered != w.discovered || p.DeviceID != w.id {
			t.Errorf("port %q: got discovered=%v id=%q, want discovered=%v id=%q",
				p.Name, p.Discovered, p.DeviceID, w.discovered, w.id)
		}
	}
}

func TestPostSerialCommand_HappyPath(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	fp := serial.NewFakePort("COM3")
	opener.Add(fp)
	// Feed response after drain completes (DrainDuration=200ms) but within read timeout.
	go func() {
		time.Sleep(250 * time.Millisecond)
		fp.Feed([]byte{99, 88, 77})
	}()
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[1,2,3,4,0]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp CommandResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []int{99, 88, 77}
	if len(resp.Response) != len(want) {
		t.Fatalf("response: got %v, want %v", resp.Response, want)
	}
	for i := range want {
		if resp.Response[i] != want[i] {
			t.Errorf("response[%d]: got %d, want %d", i, resp.Response[i], want[i])
		}
	}
	written := fp.Written()
	wantWritten := []byte{1, 2, 3, 4, 0}
	if string(written) != string(wantWritten) {
		t.Errorf("written: got %v, want %v", written, wantWritten)
	}
}

func TestPostSerialCommand_NoReply(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	opener.Add(serial.NewFakePort("COM3"))
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command?timeout_ms=20", `{"command":[1,2,3]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp CommandResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Response) != 0 {
		t.Errorf("response: got %v, want []", resp.Response)
	}
}

func TestPostSerialCommand_WaitForResponseFalse(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	fp := serial.NewFakePort("COM3")
	fp.Feed([]byte{99, 88}) // would be returned, but caller opts out
	opener.Add(fp)
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command?wait_for_response=false", `{"command":[1,2,3]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp CommandResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Response) != 0 {
		t.Errorf("response: got %v, want []", resp.Response)
	}
	if string(fp.Written()) != string([]byte{1, 2, 3}) {
		t.Errorf("written: got %v, want [1 2 3]", fp.Written())
	}
}

func TestPostSerialCommand_ExpectedBytesStopsEarly(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	fp := serial.NewFakePort("COM3")
	opener.Add(fp)
	// Feed response after drain completes (DrainDuration=200ms) but within read timeout.
	go func() {
		time.Sleep(250 * time.Millisecond)
		fp.Feed([]byte{1, 2, 3, 4, 99, 99, 99}) // more than 4 — must stop at 4
	}()
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command?expected_response_bytes=4", `{"command":[1]}`)
	if rec.Code != 200 {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp CommandResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Response) != 4 {
		t.Errorf("response: got %v, want 4 bytes", resp.Response)
	}
}

func TestPostSerialCommand_WriteFails(t *testing.T) {
	reg := registry.New()
	opener := serial.NewFakeOpener()
	fp := serial.NewFakePort("COM3")
	opener.Add(fp)
	// FakeOpener.Open resets closed=false on each call, so a port closed
	// AFTER it's first opened by the handler is what we want to simulate.
	// Achieve that with a stub opener that wraps FakeOpener and returns the
	// already-closed port without resetting the flag.
	srv := rawSrv(t, reg, &alreadyClosedOpener{inner: opener, target: "COM3", port: fp}, true)
	_ = fp.Close()

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[1]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "port write failed") &&
		!strings.Contains(rec.Body.String(), "port drain failed") {
		t.Errorf("body should mention drain or write failure, got: %s", rec.Body.String())
	}
}

// alreadyClosedOpener returns a pre-closed port for `target`, bypassing
// FakeOpener.Open's auto-reopen behavior, so the handler observes I/O errors.
type alreadyClosedOpener struct {
	inner  *serial.FakeOpener
	target string
	port   *serial.FakePort
}

func (o *alreadyClosedOpener) List() ([]string, error) { return o.inner.List() }
func (o *alreadyClosedOpener) Open(name string) (serial.Port, error) {
	if name == o.target {
		return o.port, nil
	}
	return o.inner.Open(name)
}
func (o *alreadyClosedOpener) OpenWithBaud(name string, baud int) (serial.Port, error) {
	return o.inner.OpenWithBaud(name, baud)
}
func (o *alreadyClosedOpener) ListDetailed() ([]serial.DetailedPort, error) {
	return o.inner.ListDetailed()
}

// listOnlyOpener wraps FakeOpener and adds names that List returns but Open
// rejects. Used to simulate the OS-level race where a port disappears between
// enumeration and Open().
type listOnlyOpener struct {
	*serial.FakeOpener
	listOnly map[string]error
}

func (o *listOnlyOpener) List() ([]string, error) {
	base, err := o.FakeOpener.List()
	if err != nil {
		return nil, err
	}
	for n := range o.listOnly {
		base = append(base, n)
	}
	return base, nil
}

func (o *listOnlyOpener) Open(name string) (serial.Port, error) {
	if err, ok := o.listOnly[name]; ok {
		return nil, err
	}
	return o.FakeOpener.Open(name)
}

func TestPostSerialCommand_OpenFails(t *testing.T) {
	reg := registry.New()
	opener := &listOnlyOpener{
		FakeOpener: serial.NewFakeOpener(),
		listOnly:   map[string]error{"COM3": io.ErrUnexpectedEOF},
	}
	srv := rawSrv(t, reg, opener, true)

	rec := postRaw(t, srv, "/serial/ports/COM3/command", `{"command":[1]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "port open failed") {
		t.Errorf("body: %s", rec.Body.String())
	}
}
