package streamer

import (
	"context"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
)

type fakeSpawner struct {
	live   atomic.Int32
	args   [][]string
	killed atomic.Int32
}

func (f *fakeSpawner) Start(_ context.Context, argv []string) (sessionHandle, error) {
	f.live.Add(1)
	f.args = append(f.args, append([]string(nil), argv...))
	return &fakeSessionHandle{spawner: f, doneCh: make(chan struct{})}, nil
}

type fakeSessionHandle struct {
	spawner *fakeSpawner
	doneCh  chan struct{}
	stopped atomic.Bool
}

func (h *fakeSessionHandle) Done() <-chan struct{} { return h.doneCh }
func (h *fakeSessionHandle) Stop(_ context.Context) error {
	if h.stopped.CompareAndSwap(false, true) {
		h.spawner.killed.Add(1)
		h.spawner.live.Add(-1)
		close(h.doneCh)
	}
	return nil
}
func (h *fakeSessionHandle) LastError() string { return "" }
func (h *fakeSessionHandle) PID() int          { return 0 }

func newTestManager(t *testing.T) (*manager, *fakeSpawner) {
	t.Helper()
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "armed-cameras.json"))
	spawner := &fakeSpawner{}
	enum := fakeEnumeratorFixed{cams: []Camera{
		{ID: "cam-A", Label: "Cam A"},
		{ID: "cam-B", Label: "Cam B"},
	}}
	m := NewManager(ManagerConfig{
		Store:       store,
		Enumerator:  enum,
		Spawner:     spawner,
		FFmpegReady: func() error { return nil },
		BearerFlag:  "-authorization",
	})
	if _, err := m.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return m.(*manager), spawner
}

type fakeEnumeratorFixed struct{ cams []Camera }

func (f fakeEnumeratorFixed) List(_ context.Context) ([]Camera, error) { return f.cams, nil }

func TestManager_SetArmed_GoesToTranslations(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.SetArmed("cam-A", true); err != nil {
		t.Fatalf("SetArmed: %v", err)
	}
	tr := m.Translations()
	if len(tr) != 1 || tr[0].ID != "cam-A" {
		t.Fatalf("want [cam-A], got %+v", tr)
	}
}

func TestManager_Start_UnknownID_404(t *testing.T) {
	m, _ := newTestManager(t)
	out := m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1"})
	if out.Status != http.StatusNotFound {
		t.Fatalf("want 404, got %d", out.Status)
	}
}

func TestManager_Start_Armed_202(t *testing.T) {
	m, sp := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	out := m.Start(context.Background(), "cam-A", StartRequest{
		SessionID: "S1", WHIPURL: "http://u", WHIPToken: "tk",
	})
	if out.Status != http.StatusAccepted {
		t.Fatalf("want 202, got %d", out.Status)
	}
	if got := sp.live.Load(); got != 1 {
		t.Fatalf("want 1 live session, got %d", got)
	}
}

func TestManager_Start_IdempotentSameSession(t *testing.T) {
	m, sp := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	_ = m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1", WHIPURL: "u", WHIPToken: "tk"})
	_ = m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1", WHIPURL: "u", WHIPToken: "tk"})
	if got := sp.live.Load(); got != 1 {
		t.Fatalf("want 1 live session (idempotent), got %d", got)
	}
}

func TestManager_Start_ReplaceOnConflict(t *testing.T) {
	m, sp := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	_ = m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1", WHIPURL: "u", WHIPToken: "tk"})
	out := m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S2", WHIPURL: "u", WHIPToken: "tk"})
	if out.Status != http.StatusAccepted {
		t.Fatalf("want 202 on replace, got %d", out.Status)
	}
	if got := sp.killed.Load(); got != 1 {
		t.Fatalf("expected one old session killed, got %d", got)
	}
	if got := sp.live.Load(); got != 1 {
		t.Fatalf("want 1 live session after replace, got %d", got)
	}
}

func TestManager_Start_FFmpegUnavailable_503(t *testing.T) {
	m, _ := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	m.ffmpegReady = func() error { return ErrFFmpegUnavailable }
	out := m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1"})
	if out.Status != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", out.Status)
	}
}

func TestManager_Stop_Match_204(t *testing.T) {
	m, sp := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	_ = m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1", WHIPURL: "u", WHIPToken: "tk"})
	out := m.Stop("cam-A", "S1")
	if out.Status != http.StatusNoContent {
		t.Fatalf("want 204, got %d", out.Status)
	}
	if got := sp.live.Load(); got != 0 {
		t.Fatalf("session not stopped, live=%d", got)
	}
}

func TestManager_Stop_Mismatch_409(t *testing.T) {
	m, sp := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	_ = m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1", WHIPURL: "u", WHIPToken: "tk"})
	out := m.Stop("cam-A", "STALE")
	if out.Status != http.StatusConflict {
		t.Fatalf("want 409, got %d", out.Status)
	}
	if got := sp.live.Load(); got != 1 {
		t.Fatalf("active session must be preserved on 409 stop, live=%d", got)
	}
}

func TestManager_Stop_NoActive_204(t *testing.T) {
	m, _ := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	out := m.Stop("cam-A", "anything")
	if out.Status != http.StatusNoContent {
		t.Fatalf("want 204 on no-active, got %d", out.Status)
	}
}

func TestManager_UnarmKillsActiveSession(t *testing.T) {
	m, sp := newTestManager(t)
	_ = m.SetArmed("cam-A", true)
	_ = m.Start(context.Background(), "cam-A", StartRequest{SessionID: "S1", WHIPURL: "u", WHIPToken: "tk"})
	if err := m.SetArmed("cam-A", false); err != nil {
		t.Fatalf("SetArmed off: %v", err)
	}
	if got := sp.live.Load(); got != 0 {
		t.Fatalf("unarm must kill active session, live=%d", got)
	}
}
