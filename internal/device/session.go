// internal/device/session.go
package device

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// HeartbeatInterval is how often an attached driver's Tick runs.
var HeartbeatInterval = time.Second

// Reattach backoff bounds (doubling from base to max).
var (
	ReattachBase = 5 * time.Second
	ReattachMax  = 60 * time.Second
)

// SessionConfig carries everything a Session needs at construction.
type SessionConfig struct {
	ID       string
	Type     string // registered driver type name, e.g. "pump"
	TypeCode byte   // probe type code, e.g. 10
	PortName string
	Conn     serial.Port // open port handed over by discovery
	Opener   serial.Opener
	Clock    Clock  // nil → SystemClock()
	StateDir string // devicestate directory for Store
	Factory  Factory
	// ProbeReply is the 4-byte identify reply discovery consumed.
	ProbeReply []byte
	// Reprobe re-identifies the device on a freshly opened port during
	// background re-attach. Wired to discovery.Probe by the caller.
	Reprobe func(p serial.Port) ([]byte, error)
}

type mailMsg struct {
	req  Request
	resp chan Response
}

// Session is the per-device actor: it owns the serial port and the driver,
// and runs all driver code on a single goroutine (spec §3).
type Session struct {
	cfg    SessionConfig
	jobs   *Jobs
	driver Driver

	mail   chan mailMsg
	posts  chan func()
	done   chan struct{}
	cancel context.CancelFunc

	// cross-goroutine mirrors for API reads
	connected atomic.Bool
	info      atomic.Pointer[Info]

	// loop-owned state — touched only by the session goroutine
	conn       serial.Port
	readerHeld bool
	backoff    time.Duration
	loopCtx    context.Context
}

func NewSession(cfg SessionConfig) *Session {
	if cfg.Clock == nil {
		cfg.Clock = SystemClock()
	}
	return &Session{
		cfg:   cfg,
		jobs:  NewJobs(cfg.Clock),
		mail:  make(chan mailMsg),
		posts: make(chan func(), 64),
		done:  make(chan struct{}),
		conn:  cfg.Conn,
	}
}

// Start launches the session goroutine; the initial attach runs on it.
func (s *Session) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.loopCtx = ctx
	s.driver = s.cfg.Factory(s)
	go s.loop(ctx)
}

// Close stops the loop; the driver is detached and the port closed.
// Blocks until shutdown completes. Safe to call more than once.
func (s *Session) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	<-s.done
}

func (s *Session) loop(ctx context.Context) {
	defer close(s.done)
	s.attach(s.cfg.ProbeReply)
	heartbeat := s.cfg.Clock.After(HeartbeatInterval)
	for {
		select {
		case <-ctx.Done():
			if s.connected.Load() {
				s.driver.Detach()
			}
			if s.conn != nil {
				_ = s.conn.Close()
			}
			return
		case m := <-s.mail:
			m.resp <- s.handle(ctx, m.req)
		case fn := <-s.posts:
			fn()
		case <-heartbeat:
			if s.connected.Load() {
				s.driver.Tick(s.cfg.Clock.Now())
			}
			heartbeat = s.cfg.Clock.After(HeartbeatInterval)
		}
	}
}

// attach runs driver.Attach and publishes the result. Loop-only.
func (s *Session) attach(probeReply []byte) {
	info, err := s.driver.Attach(s.loopCtx, probeReply)
	if err != nil {
		slog.Warn("device attach failed", "device", s.cfg.ID, "err", err)
		s.connected.Store(false)
		s.scheduleReattach()
		return
	}
	s.info.Store(&info)
	s.connected.Store(true)
	s.backoff = 0
	slog.Info("device attached", "device", s.cfg.ID, "port", s.cfg.PortName)
}

// Execute submits one envelope command; thread-safe API entry point.
func (s *Session) Execute(ctx context.Context, req Request) Response {
	resp := make(chan Response, 1)
	select {
	case s.mail <- mailMsg{req: req, resp: resp}:
	case <-s.done:
		return Err(req.ID, &CmdError{Code: CodeDeviceUnreachable, Message: "device session closed"})
	case <-ctx.Done():
		return Err(req.ID, ErrInternal("request cancelled"))
	}
	select {
	case r := <-resp:
		return r
	case <-s.done:
		return Err(req.ID, &CmdError{Code: CodeDeviceUnreachable, Message: "device session closed"})
	case <-ctx.Done():
		return Err(req.ID, ErrInternal("request cancelled"))
	}
}

func (s *Session) handle(ctx context.Context, req Request) Response {
	if !s.connected.Load() {
		return Err(req.ID, &CmdError{Code: CodeDeviceUnreachable, Message: "device is not responding"})
	}
	switch req.Cmd {
	case "identify":
		return OK(req.ID, *s.info.Load())
	case "get_job":
		return s.handleGetJob(req)
	}
	result, cerr := s.driver.Execute(ctx, req.Cmd, req.Params)
	if cerr != nil {
		return Err(req.ID, cerr)
	}
	return OK(req.ID, result)
}

func (s *Session) handleGetJob(req Request) Response {
	var p struct {
		JobID string `json:"job_id"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return Err(req.ID, ErrInvalidParams("params", nil, "params is not valid JSON"))
		}
	}
	if p.JobID == "" {
		return Err(req.ID, ErrInvalidParams("job_id", p.JobID, "job_id is required"))
	}
	job := s.jobs.Get(p.JobID)
	if job == nil {
		return Err(req.ID, ErrInvalidParams("job_id", p.JobID, "unknown job"))
	}
	return OK(req.ID, *job)
}

// --- thread-safe accessors (API/DTO reads) ---

func (s *Session) ID() string       { return s.cfg.ID }
func (s *Session) TypeName() string { return s.cfg.Type }
func (s *Session) PortName() string { return s.cfg.PortName }
func (s *Session) Connected() bool  { return s.connected.Load() }

// CachedInfo returns the identify block from the last successful attach.
func (s *Session) CachedInfo() (Info, bool) {
	p := s.info.Load()
	if p == nil {
		return Info{}, false
	}
	return *p, true
}

// --- driver services ---

// Jobs, Now, Store, SetInfo, Conn, HoldReader, ReleaseReader: session-goroutine only.
func (s *Session) Jobs() *Jobs    { return s.jobs }
func (s *Session) Now() time.Time { return s.cfg.Clock.Now() }

// Store returns the persistent store for this device; drivers call it with
// their state key (serial number, or port name for serial-less devices).
func (s *Session) Store(key string) *Store {
	return NewStore(s.cfg.StateDir, s.cfg.Type+"-"+key)
}

// SetInfo refreshes the cached identify block (e.g. capabilities derived
// from a new calibration).
func (s *Session) SetInfo(info Info) { s.info.Store(&info) }

// Conn exposes the raw port for watcher-goroutine reads (pump opcode-18).
func (s *Session) Conn() serial.Port { return s.conn }

// HoldReader marks the port's read side as owned by a watcher goroutine;
// ReleaseReader clears it. Reply-expecting Transact calls fail with
// ErrReaderHeld while held.
func (s *Session) HoldReader()    { s.readerHeld = true }
func (s *Session) ReleaseReader() { s.readerHeld = false }

// Post schedules fn on the session goroutine. Thread-safe.
func (s *Session) Post(fn func()) {
	select {
	case s.posts <- fn:
	case <-s.done:
	}
}

// After runs fn on the session goroutine after d (via the injectable clock).
func (s *Session) After(d time.Duration, fn func()) {
	ch := s.cfg.Clock.After(d)
	go func() {
		select {
		case <-ch:
			s.Post(fn)
		case <-s.done:
		}
	}()
}

// Go runs fn on a watcher goroutine (blocking port reads). fn reports back
// to the loop via Post.
func (s *Session) Go(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("device watcher panic", "device", s.cfg.ID, "panic", r)
			}
		}()
		fn()
	}()
}

// Transact runs one serial transaction with the shared discipline. A
// double failure flips the session to unreachable, fails the active job,
// and schedules a backoff reattach. Session-goroutine only.
func (s *Session) Transact(frame []byte, replyLen int, timeout time.Duration) ([]byte, error) {
	if replyLen > 0 && s.readerHeld {
		return nil, ErrReaderHeld
	}
	reply, err := transact(s.conn, frame, replyLen, timeout)
	if err != nil {
		s.markUnreachable(err)
	}
	return reply, err
}

// markUnreachable transitions ready → unreachable. No-op when already
// unreachable or still attaching: those paths own their own retries.
func (s *Session) markUnreachable(cause error) {
	if !s.connected.Load() {
		return
	}
	slog.Warn("device unreachable", "device", s.cfg.ID, "port", s.cfg.PortName, "err", cause)
	s.connected.Store(false)
	s.readerHeld = false
	if s.jobs.Active() != nil {
		s.jobs.Fail(ErrHardware("device became unreachable mid-job"))
	}
	s.scheduleReattach()
}

func (s *Session) scheduleReattach() {
	if s.backoff == 0 {
		s.backoff = ReattachBase
	} else {
		s.backoff *= 2
		if s.backoff > ReattachMax {
			s.backoff = ReattachMax
		}
	}
	s.After(s.backoff, s.tryReattach)
}

// tryReattach reopens the port, re-verifies device identity, and re-runs
// driver.Attach. Loop-only (scheduled via After).
func (s *Session) tryReattach() {
	if s.connected.Load() {
		return
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	conn, err := s.cfg.Opener.Open(s.cfg.PortName)
	if err != nil {
		slog.Warn("device reattach: open failed", "device", s.cfg.ID, "err", err)
		s.scheduleReattach()
		return
	}
	s.conn = conn
	reply, err := s.cfg.Reprobe(conn)
	if err != nil {
		slog.Warn("device reattach: probe failed", "device", s.cfg.ID, "err", err)
		s.scheduleReattach()
		return
	}
	if len(reply) == 0 || reply[0] != s.cfg.TypeCode {
		slog.Warn("device reattach: identity changed on port",
			"device", s.cfg.ID, "port", s.cfg.PortName, "reply", reply)
		s.scheduleReattach()
		return
	}
	s.attach(reply)
}
