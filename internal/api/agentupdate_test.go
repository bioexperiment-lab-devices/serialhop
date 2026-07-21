package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/remoteupdate"
	"github.com/bioexperiment-lab-devices/serialhop/internal/updateresult"
)

func enabledMgr(t *testing.T, cur string) *remoteupdate.Manager {
	t.Helper()
	dir := t.TempDir()
	return remoteupdate.New(remoteupdate.Config{
		Enabled: true, StagingDir: dir,
		ResultPath: filepath.Join(dir, "update_result.json"),
		CurVersion: cur, ExePath: filepath.Join(dir, "SerialHop.exe"),
		Spawn:         func(string, []string) error { return nil },
		RunBackground: func(f func()) { f() },
	})
}

// serverWith builds a Server exposing only the remote-update wiring under test.
func serverWith(mgr *remoteupdate.Manager) *Server {
	return &Server{remoteUpdate: mgr}
}

func TestPostAgentUpdate_DisabledIs404(t *testing.T) {
	s := serverWith(nil) // no manager => disabled
	rr := httptest.NewRecorder()
	s.handlePostAgentUpdate(rr, httptest.NewRequest(http.MethodPost, "/agent/update", strings.NewReader("{}")))
	if rr.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rr.Code)
	}
}

func TestPostAgentUpdate_BadURLIs400(t *testing.T) {
	s := serverWith(enabledMgr(t, "2.2.0"))
	rr := httptest.NewRecorder()
	body := `{"url":"http://x/SerialHop-v2.3.0.exe","sha256":"` + strings.Repeat("a", 64) + `"}`
	s.handlePostAgentUpdate(rr, httptest.NewRequest(http.MethodPost, "/agent/update", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rr.Code)
	}
}

func TestPostAgentUpdate_MalformedBodyIs400(t *testing.T) {
	s := serverWith(enabledMgr(t, "2.2.0"))
	rr := httptest.NewRecorder()
	s.handlePostAgentUpdate(rr, httptest.NewRequest(http.MethodPost, "/agent/update", strings.NewReader("{not json")))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rr.Code)
	}
}

func TestGetAgentUpdateStatus_DisabledIs404(t *testing.T) {
	s := serverWith(nil)
	rr := httptest.NewRecorder()
	s.handleGetAgentUpdateStatus(rr, httptest.NewRequest(http.MethodGet, "/agent/update/status", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rr.Code)
	}
}

func TestGetAgentUpdateStatus_Enabled200(t *testing.T) {
	s := serverWith(enabledMgr(t, "2.2.0"))
	rr := httptest.NewRecorder()
	s.handleGetAgentUpdateStatus(rr, httptest.NewRequest(http.MethodGet, "/agent/update/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), updateresult.StateNone) {
		t.Errorf("status body = %s, want state none", rr.Body.String())
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", rr.Header().Get("Cache-Control"))
	}
}

func TestStatusForTriggerErr_Mapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{remoteupdate.ErrDisabled, http.StatusNotFound},
		{remoteupdate.ErrInProgress, http.StatusConflict},
		{&remoteupdate.BadRequestError{Msg: "x"}, http.StatusBadRequest},
		{&remoteupdate.UpstreamError{Err: errors.New("x")}, http.StatusBadGateway},
		{errors.New("other"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		if got := statusForTriggerErr(c.err); got != c.want {
			t.Errorf("statusForTriggerErr(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}
