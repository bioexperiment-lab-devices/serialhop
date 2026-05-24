package streamer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Translation is one entry in `GET /api/translations` (matches protocol §1.1).
type Translation struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// StartRequest is the body of `POST /api/translations/{id}/start`.
type StartRequest struct {
	SessionID  string `json:"session_id"`
	WHIPURL    string `json:"whip_url"`
	WHIPToken  string `json:"whip_token"`
	IceServers []any  `json:"ice_servers"`
}

// StopRequest is the body of `POST /api/translations/{id}/stop`.
type StopRequest struct {
	SessionID string `json:"session_id"`
}

// StartOutcome / StopOutcome describe what the HTTP listener should
// respond with.
type StartOutcome struct {
	Status int
	Body   any
}
type StopOutcome struct {
	Status int
	Body   any
}

// sessionHandle is the subset of *Session the manager uses. Tests inject
// a fake that implements this interface.
type sessionHandle interface {
	Done() <-chan struct{}
	Stop(ctx context.Context) error
	LastError() string
	PID() int
}

// SessionHandleForTest is an exported alias of the unexported
// sessionHandle interface. Cross-package tests (e.g. internal/panel)
// reference this name when defining fake spawners. Internal only — not
// part of the stable API.
type SessionHandleForTest = sessionHandle

// Spawner abstracts session creation so tests can substitute fakes.
// Implementations return any handle that satisfies the sessionHandle
// contract (Done, Stop, LastError, PID). The exported alias
// SessionHandleForTest is provided for external test packages.
type Spawner interface {
	Start(ctx context.Context, argv []string) (SessionHandleForTest, error)
}

// realSpawner spawns real ffmpeg processes via StartSession.
type realSpawner struct{}

func (realSpawner) Start(ctx context.Context, argv []string) (SessionHandleForTest, error) {
	return StartSession(ctx, SessionConfig{Argv: argv, GracefulPeriod: DefaultGracefulStopGrace})
}

// Manager is the streamer subsystem's external surface.
type Manager interface {
	Refresh(ctx context.Context) ([]Camera, error)
	Cameras() []CameraView
	SetArmed(cameraID string, armed bool) error
	Translations() []Translation
	Start(ctx context.Context, cameraID string, in StartRequest) StartOutcome
	Stop(cameraID string, sessionID string) StopOutcome
	Shutdown(ctx context.Context) error
}

// ManagerConfig wires the manager.
type ManagerConfig struct {
	Store       *Store
	Enumerator  Enumerator
	Spawner     Spawner
	FFmpegPath  string
	FFmpegReady func() error
	BearerFlag  string
	OnChange    func() // fired after any state-changing op (UI re-render)
}

type manager struct {
	store       *Store
	enum        Enumerator
	spawner     Spawner
	ffmpegPath  string
	ffmpegReady func() error
	bearerFlag  string
	onChange    func()

	mu       sync.Mutex
	cameras  map[string]*managedCam // by id
	sessions map[string]*activeSess // by camera id
}

type managedCam struct {
	Camera
	Armed     bool
	Connected bool
	LastError string
}

type activeSess struct {
	cameraID  string
	sessionID string
	startedAt time.Time
	handle    sessionHandle
}

// NewManager constructs a Manager. Spawner defaults to a real ffmpeg
// spawner; tests pass fakeSpawner.
func NewManager(cfg ManagerConfig) Manager {
	if cfg.Spawner == nil {
		cfg.Spawner = realSpawner{}
	}
	if cfg.OnChange == nil {
		cfg.OnChange = func() {}
	}
	m := &manager{
		store:       cfg.Store,
		enum:        cfg.Enumerator,
		spawner:     cfg.Spawner,
		ffmpegPath:  cfg.FFmpegPath,
		ffmpegReady: cfg.FFmpegReady,
		bearerFlag:  cfg.BearerFlag,
		onChange:    cfg.OnChange,
		cameras:     map[string]*managedCam{},
		sessions:    map[string]*activeSess{},
	}
	if cfg.Store != nil {
		armed, _ := cfg.Store.Load()
		for _, a := range armed {
			m.cameras[a.ID] = &managedCam{
				Camera:    Camera(a),
				Armed:     true,
				Connected: false, // will be flipped on Refresh
			}
		}
	}
	return m
}

func (m *manager) Refresh(ctx context.Context) ([]Camera, error) {
	cams, err := m.enum.List(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Mark all currently-known cameras as disconnected.
	for _, c := range m.cameras {
		c.Connected = false
	}
	for _, c := range cams {
		if mc, ok := m.cameras[c.ID]; ok {
			mc.Label = c.Label // refresh label in case it changed
			mc.Connected = true
		} else {
			m.cameras[c.ID] = &managedCam{
				Camera:    c,
				Armed:     false,
				Connected: true,
			}
		}
	}
	m.onChange()
	return cams, nil
}

func (m *manager) Cameras() []CameraView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CameraView, 0, len(m.cameras))
	for _, c := range m.cameras {
		_, live := m.sessions[c.ID]
		out = append(out, CameraView{
			ID:           c.ID,
			Label:        c.Label,
			Armed:        c.Armed,
			Connected:    c.Connected,
			Live:         live,
			LastErrorMsg: c.LastError,
		})
	}
	return out
}

func (m *manager) Translations() []Translation {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Translation, 0, len(m.cameras))
	for _, c := range m.cameras {
		if c.Armed && c.Connected {
			out = append(out, Translation{ID: c.ID, Label: c.Label})
		}
	}
	return out
}

func (m *manager) SetArmed(cameraID string, armed bool) error {
	m.mu.Lock()
	c, ok := m.cameras[cameraID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("streamer: unknown camera %q", cameraID)
	}
	c.Armed = armed
	// If unarming while a session is live, tear it down here under the
	// same lock to avoid races with concurrent Start.
	var toStop *activeSess
	if !armed {
		if s, ok := m.sessions[cameraID]; ok {
			toStop = s
			delete(m.sessions, cameraID)
		}
	}
	m.persistLocked()
	m.mu.Unlock()
	if toStop != nil {
		_ = toStop.handle.Stop(context.Background())
	}
	m.onChange()
	return nil
}

func (m *manager) persistLocked() {
	if m.store == nil {
		return
	}
	armed := make([]ArmedCamera, 0)
	for _, c := range m.cameras {
		if c.Armed {
			armed = append(armed, ArmedCamera{ID: c.ID, Label: c.Label})
		}
	}
	if err := m.store.Save(armed); err != nil {
		// Don't fail the in-memory update; surface a UI-visible error elsewhere.
		// Persistence error logged by callers if needed.
		_ = err
	}
}

func (m *manager) Start(ctx context.Context, cameraID string, in StartRequest) StartOutcome {
	if m.ffmpegReady != nil {
		if err := m.ffmpegReady(); err != nil {
			return StartOutcome{
				Status: http.StatusServiceUnavailable,
				Body:   map[string]string{"error": "ffmpeg unavailable"},
			}
		}
	}
	m.mu.Lock()
	c, ok := m.cameras[cameraID]
	if !ok || !c.Armed || !c.Connected {
		m.mu.Unlock()
		return StartOutcome{Status: http.StatusNotFound, Body: map[string]string{"error": "unknown translation"}}
	}
	if cur, ok := m.sessions[cameraID]; ok {
		if cur.sessionID == in.SessionID {
			m.mu.Unlock()
			return StartOutcome{Status: http.StatusAccepted, Body: struct{}{}}
		}
		// Replace-on-conflict: kill old below the lock.
		oldHandle := cur.handle
		delete(m.sessions, cameraID)
		m.mu.Unlock()
		_ = oldHandle.Stop(context.Background())
		m.mu.Lock()
	}
	label := c.Label
	cameraIDLocked := c.ID
	m.mu.Unlock()
	argv := BuildWHIPArgs(WHIPArgs{
		BinaryPath:  m.ffmpegPath,
		CameraLabel: label,
		SessionID:   in.SessionID,
		WHIPURL:     in.WHIPURL,
		BearerFlag:  m.bearerFlag,
		BearerToken: in.WHIPToken,
	})
	h, err := m.spawner.Start(ctx, argv)
	if err != nil {
		m.mu.Lock()
		c.LastError = err.Error()
		m.mu.Unlock()
		return StartOutcome{Status: http.StatusServiceUnavailable, Body: map[string]string{"error": err.Error()}}
	}
	m.mu.Lock()
	m.sessions[cameraIDLocked] = &activeSess{
		cameraID:  cameraIDLocked,
		sessionID: in.SessionID,
		startedAt: time.Now(),
		handle:    h,
	}
	m.mu.Unlock()
	go m.watchSession(cameraIDLocked, in.SessionID, h)
	m.onChange()
	return StartOutcome{Status: http.StatusAccepted, Body: struct{}{}}
}

func (m *manager) watchSession(cameraID, sessionID string, h sessionHandle) {
	<-h.Done()
	m.mu.Lock()
	if cur, ok := m.sessions[cameraID]; ok && cur.sessionID == sessionID {
		delete(m.sessions, cameraID)
		if errMsg := h.LastError(); errMsg != "" {
			if c, ok := m.cameras[cameraID]; ok {
				c.LastError = errMsg
			}
		}
	}
	m.mu.Unlock()
	m.onChange()
}

func (m *manager) Stop(cameraID, sessionID string) StopOutcome {
	m.mu.Lock()
	cur, ok := m.sessions[cameraID]
	if !ok {
		m.mu.Unlock()
		return StopOutcome{Status: http.StatusNoContent, Body: struct{}{}}
	}
	if cur.sessionID != sessionID {
		body := map[string]string{"active_session_id": cur.sessionID}
		m.mu.Unlock()
		return StopOutcome{Status: http.StatusConflict, Body: body}
	}
	handle := cur.handle
	delete(m.sessions, cameraID)
	m.mu.Unlock()
	_ = handle.Stop(context.Background())
	m.onChange()
	return StopOutcome{Status: http.StatusNoContent, Body: struct{}{}}
}

func (m *manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	handles := make([]sessionHandle, 0, len(m.sessions))
	for _, s := range m.sessions {
		handles = append(handles, s.handle)
	}
	m.sessions = map[string]*activeSess{}
	m.mu.Unlock()
	var err error
	for _, h := range handles {
		if e := h.Stop(ctx); e != nil && err == nil {
			err = e
		}
	}
	if err != nil {
		return fmt.Errorf("streamer: shutdown: %w", err)
	}
	return nil
}

// ErrUnknownCamera is a sentinel for callers that want to distinguish
// "armed-but-unknown" from truly-unknown.
var ErrUnknownCamera = errors.New("streamer: unknown camera")
