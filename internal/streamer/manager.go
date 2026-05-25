package streamer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
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
	StderrTail() string
	PID() int
}

// SessionHandleForTest is an exported alias of the unexported
// sessionHandle interface. Cross-package tests (e.g. internal/panel)
// reference this name when defining fake spawners. Internal only — not
// part of the stable API.
type SessionHandleForTest = sessionHandle

// Spawner abstracts session creation so tests can substitute fakes.
//
// The signature splits binaryPath (server-controlled, always
// paths.FFmpegPath() in production) from args (values flowing to the
// child). This keeps the trust boundary explicit at the spawner
// interface, which is the single place where exec.CommandContext is
// invoked.
type Spawner interface {
	Start(ctx context.Context, binaryPath string, args []string) (SessionHandleForTest, error)
}

// realSpawner spawns real ffmpeg processes via StartSession.
type realSpawner struct{}

func (realSpawner) Start(ctx context.Context, binaryPath string, args []string) (SessionHandleForTest, error) {
	return StartSession(ctx, SessionConfig{
		BinaryPath:     binaryPath,
		Args:           args,
		GracefulPeriod: DefaultGracefulStopGrace,
	})
}

// Manager is the streamer subsystem's external surface.
type Manager interface {
	Refresh(ctx context.Context) ([]Camera, error)
	Cameras() []CameraView
	// LastEnumError returns the most recent enumeration error message,
	// or "" when the last Refresh succeeded. The panel surfaces this in
	// the Cameras tab so an empty list isn't ambiguous.
	LastEnumError() string
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
	// lastEnumErr is the most recent enumeration error (empty when the
	// last Refresh succeeded). Exposed to the UI via StreamingState so
	// users see *why* no cameras appear — without it the empty state
	// reads as "no devices connected" even when the real cause is a
	// missing ffmpeg.exe or DirectShow returning nothing.
	lastEnumErr string
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
		m.mu.Lock()
		m.lastEnumErr = err.Error()
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastEnumErr = ""
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

func (m *manager) LastEnumError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastEnumErr
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
		slog.Warn("streamer: persisting armed cameras failed", "err", err, "count", len(armed))
	}
}

func (m *manager) Start(ctx context.Context, cameraID string, in StartRequest) StartOutcome {
	// Input validation BEFORE any state mutation or process spawn.
	// External callers (lab-bridge over the chisel tunnel) supply
	// SessionID / WHIPURL / WHIPToken, all of which end up in the
	// ffmpeg child's argv. Reject anything outside a tight allowlist so
	// CodeQL's go/command-injection taint analysis has an explicit
	// sanitization barrier and we get defense-in-depth even though Go's
	// exec is non-shell. Camera ID is also user-supplied via the URL
	// path — reject before lookup so a hostile path can't probe map
	// internals.
	if err := validateCameraID(cameraID); err != nil {
		return StartOutcome{Status: http.StatusBadRequest, Body: map[string]string{"error": err.Error()}}
	}
	if err := validateStartRequest(in); err != nil {
		return StartOutcome{Status: http.StatusBadRequest, Body: map[string]string{"error": err.Error()}}
	}
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
		//
		// We drop the manager lock here so the blocking Stop() can complete
		// without serializing all manager operations. This intentionally
		// admits a narrow race window: a concurrent Start for the same
		// cameraID with a third session_id can slip in during the unlock-
		// Stop-relock-spawn window and leak its ffmpeg child. For a single-
		// operator panel that's acceptable; if multi-viewer coordination
		// ever needs to race at the panel, introduce a per-camera lock or
		// a single-flight guard.
		oldHandle := cur.handle
		delete(m.sessions, cameraID)
		m.mu.Unlock()
		_ = oldHandle.Stop(context.Background())
		m.mu.Lock()
	}
	label := c.Label
	cameraIDLocked := c.ID
	m.mu.Unlock()
	args := BuildWHIPArgs(WHIPArgs{
		CameraLabel: label,
		SessionID:   in.SessionID,
		WHIPURL:     in.WHIPURL,
		BearerFlag:  m.bearerFlag,
		BearerToken: in.WHIPToken,
	})
	// Loud audit log: prove we actually invoked ffmpeg, and with what
	// argv (token redacted). Operators / support tickets grep for
	// "streamer: spawning ffmpeg" to confirm /start reached this code.
	slog.Info("streamer: spawning ffmpeg",
		"binary", m.ffmpegPath,
		"camera_id", cameraIDLocked,
		"camera_label", label,
		"session_id", in.SessionID,
		"argv", RedactedArgs(args))
	spawnedAt := time.Now()
	h, err := m.spawner.Start(ctx, m.ffmpegPath, args)
	if err != nil {
		slog.Error("streamer: ffmpeg spawn failed", "err", err, "camera_id", cameraIDLocked)
		m.mu.Lock()
		c.LastError = err.Error()
		m.mu.Unlock()
		return StartOutcome{Status: http.StatusServiceUnavailable, Body: map[string]string{"error": err.Error()}}
	}
	m.mu.Lock()
	m.sessions[cameraIDLocked] = &activeSess{
		cameraID:  cameraIDLocked,
		sessionID: in.SessionID,
		startedAt: spawnedAt,
		handle:    h,
	}
	if c, ok := m.cameras[cameraIDLocked]; ok {
		c.LastError = "" // fresh successful spawn clears any previous error
	}
	m.mu.Unlock()
	go m.watchSession(cameraIDLocked, in.SessionID, h, spawnedAt)
	m.onChange()
	return StartOutcome{Status: http.StatusAccepted, Body: struct{}{}}
}

// initFailureWindow is the threshold below which we treat a session
// exit as "ffmpeg never made it past initialization". A WHIP publish
// that actually opens a TCP socket to the lab-bridge edge would
// normally take a few seconds for ICE + DTLS even on the local LAN;
// anything shorter is overwhelmingly likely to be an argv parse
// error, codec init failure, or dshow open failure.
const initFailureWindow = 5 * time.Second

// sessionHandleWithExit lets us read the child's exit code when the
// concrete *Session is in use; tests inject fakes that don't implement
// this and we just record -1.
type sessionHandleWithExit interface {
	ExitCode() int
}

func (m *manager) watchSession(cameraID, sessionID string, h sessionHandle, spawnedAt time.Time) {
	<-h.Done()
	tail := h.StderrTail()
	uptime := time.Since(spawnedAt)
	exitCode := -1
	if ec, ok := h.(sessionHandleWithExit); ok {
		exitCode = ec.ExitCode()
	}
	m.mu.Lock()
	if cur, ok := m.sessions[cameraID]; ok && cur.sessionID == sessionID {
		delete(m.sessions, cameraID)
		// Prefer the full tail when populated — the last line alone is
		// often "Conversion failed!" with the actual diagnostic 5-10
		// lines earlier (codec mismatch, dshow open failure, WHIP
		// muxer rejection, etc.). Truncate at ~2 KB to fit in the
		// Wails event payload without bloating it.
		errMsg := tail
		if errMsg == "" {
			errMsg = h.LastError()
		}
		if len(errMsg) > 2048 {
			errMsg = errMsg[:2048] + "\n... (truncated)"
		}
		if errMsg != "" {
			if c, ok := m.cameras[cameraID]; ok {
				c.LastError = errMsg
			}
		}
	}
	m.mu.Unlock()
	// Permanent record in the panel log. Use slog.Error (not Warn)
	// when the session dies inside initFailureWindow — that's the
	// signature of ffmpeg never reaching the network code, which is
	// the failure mode that prompted lab-bridge to ask us to log
	// loudly.
	if uptime < initFailureWindow {
		slog.Error("streamer: session exited (init failure suspected)",
			"camera_id", cameraID,
			"session_id", sessionID,
			"uptime", uptime.String(),
			"exit_code", exitCode,
			"stderr_tail", tail)
	} else {
		slog.Warn("streamer: session exited",
			"camera_id", cameraID,
			"session_id", sessionID,
			"uptime", uptime.String(),
			"exit_code", exitCode,
			"stderr_tail", tail)
	}
	m.onChange()
}

func (m *manager) Stop(cameraID, sessionID string) StopOutcome {
	// Mirror the validation Start runs (allowlist before map lookup) so
	// a malformed Stop lands as 400 rather than silently 204-ing on an
	// unknown id. Keeps both endpoints symmetric for the protocol's
	// session_id stale-stop guard to behave predictably.
	if err := validateCameraID(cameraID); err != nil {
		return StopOutcome{Status: http.StatusBadRequest, Body: map[string]string{"error": err.Error()}}
	}
	if !sessionIDPattern.MatchString(sessionID) {
		return StopOutcome{Status: http.StatusBadRequest, Body: map[string]string{"error": "session_id contains invalid characters or is empty"}}
	}
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

// Input validation regexes used by Manager.Start.
//
// These exist as defense-in-depth against the go/command-injection
// concern raised by CodeQL on internal/streamer/session.go: external
// callers supply SessionID / WHIPURL / WHIPToken via the lab-bridge
// `/start` request body, and those values end up in the ffmpeg child's
// argv. Go's exec is non-shell, so quoting attacks can't escape into
// other commands, but a bogus value can still be passed through to
// ffmpeg verbatim. We reject anything outside a tight allowlist before
// the values reach BuildWHIPArgs.
//
// The patterns are intentionally permissive (within reason):
//   - sessionIDPattern matches ULIDs (Crockford Base32, 26 chars) AND
//     any other A-Z/a-z/0-9/-/_ string up to 128 chars, so a minor
//     protocol-versioning change in lab-bridge doesn't require a panel
//     update.
//   - tokenPattern matches base64url plus `.~+/=` to admit JWTs and a
//     few other common bearer encodings.
//   - URL validation uses net/url + an explicit `https` scheme check
//     (HTTPS is the protocol's stated transport for WHIP) and refuses
//     URLs whose first char is `-` so ffmpeg can't reinterpret the
//     output positional as a flag.
var (
	cameraIDPattern  = regexp.MustCompile(`^[\x20-\x7e]{1,256}$`)
	sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	tokenPattern     = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]{1,512}$`)
)

func validateCameraID(id string) error {
	if id == "" {
		return errors.New("camera id is required")
	}
	if !cameraIDPattern.MatchString(id) {
		return errors.New("camera id contains invalid characters")
	}
	return nil
}

func validateStartRequest(in StartRequest) error {
	if !sessionIDPattern.MatchString(in.SessionID) {
		return errors.New("session_id contains invalid characters or is empty")
	}
	if !tokenPattern.MatchString(in.WHIPToken) {
		return errors.New("whip_token contains invalid characters or is empty")
	}
	if err := validateWHIPURL(in.WHIPURL); err != nil {
		return err
	}
	return nil
}

func validateWHIPURL(raw string) error {
	if raw == "" {
		return errors.New("whip_url is required")
	}
	if len(raw) > 2048 {
		return errors.New("whip_url is too long")
	}
	// Refuse a leading '-' so ffmpeg can't reinterpret the final
	// positional as a flag.
	if raw[0] == '-' {
		return errors.New("whip_url may not start with '-'")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("whip_url is not a valid URL: %w", err)
	}
	if u.Scheme != "https" {
		return errors.New("whip_url must use https scheme")
	}
	if u.Host == "" {
		return errors.New("whip_url is missing host")
	}
	return nil
}
