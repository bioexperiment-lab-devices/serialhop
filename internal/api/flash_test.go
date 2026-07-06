package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher"
	"github.com/bioexperiment-lab-devices/serialhop/internal/power"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

func newTestServerForFlash(t *testing.T) (*Server, *registry.Registry, *labserial.FakeOpener) {
	t.Helper()
	reg := registry.New()
	op := labserial.NewFakeOpener()
	ka, err := power.New()
	if err != nil {
		t.Fatalf("power.New: %v", err)
	}
	t.Cleanup(func() { _ = ka.Close() })
	s := New(reg, nil, op, nil, false, ka)
	return s, reg, op
}

func TestDisconnect_EmptyRegistry(t *testing.T) {
	s, _, _ := newTestServerForFlash(t)
	req := httptest.NewRequest(http.MethodPost, "/devices/disconnect", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"released":0`) {
		t.Errorf("body: got %q, want released:0", rr.Body.String())
	}
}

func TestDisconnect_PopulatedRegistry(t *testing.T) {
	s, reg, _ := newTestServerForFlash(t)
	reg.Replace([]*device.Session{
		newFakeSession(t, "a", &fakeDriver{}),
		newFakeSession(t, "b", &fakeDriver{}),
	})
	req := httptest.NewRequest(http.MethodPost, "/devices/disconnect", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Errorf("status: got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"released":2`) {
		t.Errorf("body: %q", rr.Body.String())
	}
	if len(reg.List()) != 0 {
		t.Errorf("registry not empty after disconnect")
	}
}

func TestDisconnectByPort_NotFound(t *testing.T) {
	s, reg, _ := newTestServerForFlash(t)
	reg.Replace([]*device.Session{newFakeSession(t, "a", &fakeDriver{})})
	req := httptest.NewRequest(http.MethodPost, "/devices/disconnect?port=COM99", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"error":"device not found"`) {
		t.Errorf("body: %q", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"detail":"COM99"`) {
		t.Errorf("body detail: %q", rr.Body.String())
	}
	if len(reg.List()) != 1 {
		t.Errorf("registry size: got %d, want 1 (404 must not mutate)", len(reg.List()))
	}
}

func TestDisconnectByPort_Found(t *testing.T) {
	s, reg, _ := newTestServerForFlash(t)
	target := newFakeSession(t, "a", &fakeDriver{})
	other := newFakeSession(t, "b", &fakeDriver{})
	reg.Replace([]*device.Session{target, other})

	req := httptest.NewRequest(http.MethodPost, "/devices/disconnect?port="+target.PortName(), nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: got %d, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"released":1`) {
		t.Errorf("body: %q", rr.Body.String())
	}
	if _, ok := reg.HasPort(target.PortName()); ok {
		t.Errorf("%s should be gone from registry", target.PortName())
	}
	if _, ok := reg.HasPort(other.PortName()); !ok {
		t.Errorf("%s must remain in registry", other.PortName())
	}
}

func TestDetailedPorts_ReturnsAnnotatedPorts(t *testing.T) {
	s, reg, op := newTestServerForFlash(t)
	sess := newFakeSession(t, "pump_1", &fakeDriver{})
	reg.Replace([]*device.Session{sess})
	// The session holds sess.PortName(); make the opener enumerate it (with
	// USB detail) plus an unclaimed COM4 so the handler must annotate exactly
	// the discovered one.
	op.Add(labserial.NewFakePort(sess.PortName()))
	op.Add(labserial.NewFakePort("COM4"))
	op.SetDetail(sess.PortName(), labserial.DetailedPort{
		Name: sess.PortName(), IsUSB: true, VID: "2341", PID: "0043", Product: "Arduino Uno",
	})

	req := httptest.NewRequest(http.MethodGet, "/serial/ports/detailed", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: %d, body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"name":"`+sess.PortName()+`"`) {
		t.Errorf("missing %s in body: %s", sess.PortName(), body)
	}
	if !strings.Contains(body, `"name":"COM4"`) {
		t.Errorf("missing COM4 in body: %s", body)
	}
	if !strings.Contains(body, `"discovered":true`) {
		t.Errorf("expected discovered:true for %s: %s", sess.PortName(), body)
	}
	if !strings.Contains(body, `"device_id":"pump_1"`) {
		t.Errorf("expected device_id pump_1: %s", body)
	}
}

// stubFlasher records the latest Flash call and returns a canned Result.
type stubFlasher struct {
	res  *flasher.Result
	err  error
	last struct {
		Port string
		Req  flasher.Request
	}
}

func (s *stubFlasher) Flash(_ context.Context, port string, req flasher.Request) (*flasher.Result, error) {
	s.last.Port = port
	s.last.Req = req
	return s.res, s.err
}

func newTestServerWithFlash(t *testing.T, fl flasher.Flasher, enabled bool) (*Server, *registry.Registry, *labserial.FakeOpener) {
	t.Helper()
	reg := registry.New()
	op := labserial.NewFakeOpener()
	ka, err := power.New()
	if err != nil {
		t.Fatalf("power.New: %v", err)
	}
	t.Cleanup(func() { _ = ka.Close() })
	s := New(reg, nil, op, fl, enabled, ka)
	return s, reg, op
}

func TestFlash_403_FlashingDisabled(t *testing.T) {
	s, _, op := newTestServerWithFlash(t, &stubFlasher{}, false)
	op.Add(labserial.NewFakePort("COM3"))
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(`{"firmware":":00000001FF"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Errorf("status: got %d, want 403", rr.Code)
	}
}

func TestFlash_404_UnknownPort(t *testing.T) {
	s, _, _ := newTestServerWithFlash(t, &stubFlasher{}, true)
	req := httptest.NewRequest(http.MethodPost, "/flash/COMNOPE", strings.NewReader(`{"firmware":":00000001FF"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 404 {
		t.Errorf("status: got %d", rr.Code)
	}
}

func TestFlash_409_RegistryNotEmpty(t *testing.T) {
	s, reg, op := newTestServerWithFlash(t, &stubFlasher{}, true)
	op.Add(labserial.NewFakePort("COM3"))
	reg.Replace([]*device.Session{newFakeSession(t, "fake_1", &fakeDriver{})})
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(`{"firmware":":00000001FF"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 409 {
		t.Errorf("status: got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "/devices/disconnect") {
		t.Errorf("expected hint about /devices/disconnect in body: %s", rr.Body.String())
	}
}

func TestFlash_409_DiscoveryInProgress(t *testing.T) {
	s, reg, op := newTestServerWithFlash(t, &stubFlasher{}, true)
	op.Add(labserial.NewFakePort("COM3"))
	if !reg.LockDiscovery() {
		t.Fatal("could not acquire discovery gate")
	}
	defer reg.UnlockDiscovery()

	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(`{"firmware":":00000001FF"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 409 {
		t.Errorf("status: got %d", rr.Code)
	}
}

func TestFlash_400_BadJSON(t *testing.T) {
	s, _, op := newTestServerWithFlash(t, &stubFlasher{}, true)
	op.Add(labserial.NewFakePort("COM3"))
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(`not json`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d", rr.Code)
	}
}

func TestFlash_400_TestPairAsymmetric(t *testing.T) {
	s, _, op := newTestServerWithFlash(t, &stubFlasher{}, true)
	op.Add(labserial.NewFakePort("COM3"))
	body := `{"firmware":":00000001FF","test_command":"010203"}`
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "both or neither") {
		t.Errorf("expected 'both or neither' in body: %s", rr.Body.String())
	}
}

func TestFlash_400_BadHex(t *testing.T) {
	s, _, op := newTestServerWithFlash(t, &stubFlasher{}, true)
	op.Add(labserial.NewFakePort("COM3"))
	body := `{"firmware":":00000001FF","test_command":"GGGG","expected_response":"AABB"}`
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("status: got %d, body=%s", rr.Code, rr.Body.String())
	}
}

func TestFlash_409_FlashInFlight(t *testing.T) {
	stub := &stubFlasher{err: flasher.ErrBusy}
	s, _, op := newTestServerWithFlash(t, stub, true)
	op.Add(labserial.NewFakePort("COM3"))
	body := `{"firmware":":00000001FF"}`
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 409 {
		t.Errorf("status: got %d", rr.Code)
	}
}

func TestFlash_200_SuccessShape(t *testing.T) {
	stub := &stubFlasher{
		res: &flasher.Result{
			Outcome: flasher.OutcomeSuccess,
			Port:    "COM3",
			Stages: map[string]flasher.StageResult{
				"preflight": {Status: "ok", Duration: 12 * time.Millisecond},
				"backup":    {Status: "ok", Duration: 8000 * time.Millisecond},
				"erase":     {Status: "ok", Duration: 90 * time.Millisecond},
				"program":   {Status: "ok", Duration: 7900 * time.Millisecond},
				"verify":    {Status: "ok", Duration: 3100 * time.Millisecond},
				"test":      {Status: "skipped"},
				"rollback":  {Status: "n/a"},
			},
			Backup:    flasher.BackupInfo{Path: "/tmp/x.hex", SHA256: "abc", SizeBytes: 32},
			BackupHex: ":00000001FF\n",
		},
	}
	s, _, op := newTestServerWithFlash(t, stub, true)
	op.Add(labserial.NewFakePort("COM3"))
	body := `{"firmware":":00000001FF"}`
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: %d body=%s", rr.Code, rr.Body.String())
	}
	body2 := rr.Body.String()
	for _, want := range []string{
		`"outcome":"success"`,
		`"port":"COM3"`,
		`"hex":":00000001FF\n"`,
		`"sha256":"abc"`,
		`"scope":"flash_only"`,
		`"status":"ok"`,
		`"status":"skipped"`,
		`"status":"n/a"`,
	} {
		if !strings.Contains(body2, want) {
			t.Errorf("body missing %q\nbody: %s", want, body2)
		}
	}
	if strings.Contains(body2, `"test_result"`) {
		t.Errorf("test_result must be omitted when nil: %s", body2)
	}
}

func TestFlash_200_RolledBackShape_WithTestResult(t *testing.T) {
	stub := &stubFlasher{
		res: &flasher.Result{
			Outcome: flasher.OutcomeRolledBackTestFailed,
			Port:    "COM3",
			Stages: map[string]flasher.StageResult{
				"preflight": {Status: "ok"},
				"backup":    {Status: "ok"},
				"erase":     {Status: "ok"},
				"program":   {Status: "ok"},
				"verify":    {Status: "ok"},
				"test":      {Status: "failed", Error: "mismatch"},
				"rollback":  {Status: "ok", VerifyStatus: "ok"},
			},
			Backup:    flasher.BackupInfo{Path: "/tmp/x.hex"},
			BackupHex: ":00000001FF\n",
			TestResult: &flasher.TestResult{
				Sent: []byte{0x01}, Expected: []byte{0xAA}, Received: []byte{0xBB}, Match: false,
			},
		},
	}
	s, _, op := newTestServerWithFlash(t, stub, true)
	op.Add(labserial.NewFakePort("COM3"))
	body := `{"firmware":":00000001FF","test_command":"01","expected_response":"AA"}`
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: %d", rr.Code)
	}
	body2 := rr.Body.String()
	for _, want := range []string{
		`"outcome":"rolled_back_test_failed"`,
		`"sent":"01"`,
		`"expected":"aa"`,
		`"received":"bb"`,
		`"match":false`,
		`"verify_status":"ok"`,
	} {
		if !strings.Contains(body2, want) {
			t.Errorf("body missing %q\nbody: %s", want, body2)
		}
	}
}

func TestFlash_200_FailedNoRecoveryShape(t *testing.T) {
	stub := &stubFlasher{
		res: &flasher.Result{
			Outcome:      flasher.OutcomeFailedNoRecovery,
			Port:         "COM3",
			RecoveryHint: "ISP recovery required",
			Stages: map[string]flasher.StageResult{
				"preflight": {Status: "ok"},
				"backup":    {Status: "ok"},
				"erase":     {Status: "ok"},
				"program":   {Status: "ok"},
				"verify":    {Status: "failed"},
				"test":      {Status: "skipped"},
				"rollback":  {Status: "failed", Error: "chip erase: NOSYNC"},
			},
		},
	}
	s, _, op := newTestServerWithFlash(t, stub, true)
	op.Add(labserial.NewFakePort("COM3"))
	body := `{"firmware":":00000001FF"}`
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: %d", rr.Code)
	}
	body2 := rr.Body.String()
	for _, want := range []string{
		`"outcome":"failed_no_recovery"`,
		`"recovery_hint":"ISP recovery required"`,
	} {
		if !strings.Contains(body2, want) {
			t.Errorf("body missing %q\nbody: %s", want, body2)
		}
	}
}

func TestFlash_200_SkipBackup_PassesThrough(t *testing.T) {
	stub := &stubFlasher{
		res: &flasher.Result{
			Outcome: flasher.OutcomeSuccess,
			Port:    "COM3",
			Stages: map[string]flasher.StageResult{
				"preflight": {Status: "ok"},
				"backup":    {Status: "skipped"},
				"erase":     {Status: "ok"},
				"program":   {Status: "ok"},
				"verify":    {Status: "ok"},
				"test":      {Status: "skipped"},
				"rollback":  {Status: "n/a"},
			},
		},
	}
	s, _, op := newTestServerWithFlash(t, stub, true)
	op.Add(labserial.NewFakePort("COM3"))
	body := `{"firmware":":00000001FF","skip_backup":true}`
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: %d body=%s", rr.Code, rr.Body.String())
	}
	if !stub.last.Req.SkipBackup {
		t.Errorf("SkipBackup did not reach the flasher: got %+v", stub.last.Req)
	}
	body2 := rr.Body.String()
	for _, want := range []string{
		`"outcome":"success"`,
		`"scope":"skipped"`,
		`"hex":""`,
		`"saved_path":""`,
		`"sha256":""`,
		`"size_bytes":0`,
	} {
		if !strings.Contains(body2, want) {
			t.Errorf("body missing %q\nbody: %s", want, body2)
		}
	}
}

func TestFlash_200_NoSkipBackup_ScopeIsFlashOnly(t *testing.T) {
	stub := &stubFlasher{
		res: &flasher.Result{
			Outcome: flasher.OutcomeSuccess,
			Port:    "COM3",
			Stages: map[string]flasher.StageResult{
				"preflight": {Status: "ok"},
				"backup":    {Status: "ok"},
				"erase":     {Status: "ok"},
				"program":   {Status: "ok"},
				"verify":    {Status: "ok"},
				"test":      {Status: "skipped"},
				"rollback":  {Status: "n/a"},
			},
			BackupHex: ":00000001FF\n",
			Backup:    flasher.BackupInfo{Path: "/tmp/x.hex", SHA256: "abc", SizeBytes: 12},
		},
	}
	s, _, op := newTestServerWithFlash(t, stub, true)
	op.Add(labserial.NewFakePort("COM3"))
	body := `{"firmware":":00000001FF"}`
	req := httptest.NewRequest(http.MethodPost, "/flash/COM3", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: %d body=%s", rr.Code, rr.Body.String())
	}
	if stub.last.Req.SkipBackup {
		t.Errorf("SkipBackup should default to false when omitted")
	}
	if !strings.Contains(rr.Body.String(), `"scope":"flash_only"`) {
		t.Errorf("body should carry scope:flash_only, got: %s", rr.Body.String())
	}
}
