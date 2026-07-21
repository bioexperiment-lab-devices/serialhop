// Package remoteupdate orchestrates admin-pushed updates: resolve source,
// download + SHA-verify, then spawn the detached elevated swap child. Reachable
// via POST /agent/update, gated by remote_update.enabled (default off) and, in
// production, by server-side admin auth. See
// docs/superpowers/specs/2026-07-21-remote-admin-update-design.md.
package remoteupdate

import (
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/bioexperiment-lab-devices/serialhop/internal/updateresult"
)

// Sentinel errors mapped to HTTP status by the api handler.
var (
	ErrDisabled   = errors.New("remote update disabled")
	ErrInProgress = errors.New("update in progress")
)

// Config constructs a Manager. Zero optional fields get production defaults in
// New; tests override HTTPClient/ReleasesURL/TagURL/Spawn/RunBackground.
type Config struct {
	Enabled    bool
	HTTPClient *http.Client
	StagingDir string
	ResultPath string
	CurVersion string // version.Base()
	UserAgent  string
	ExePath    string // service exe to re-launch as the swap child

	// Optional test seams.
	ReleasesURL   string                             // default updater.DefaultReleasesURL
	TagURL        func(tag string) string            // default updater.ReleasesByTagURL
	Spawn         func(exe string, a []string) error // default SpawnDetached
	RunBackground func(func())                       // default: go f()
}

// Manager owns the in-flight guard and the resolved config.
type Manager struct {
	cfg Config

	mu       sync.Mutex
	inFlight bool
}

// New fills defaults and returns a Manager. Safe to call with Enabled=false.
func New(c Config) *Manager {
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{}
	}
	if c.ReleasesURL == "" {
		c.ReleasesURL = defaultReleasesURL
	}
	if c.TagURL == nil {
		c.TagURL = defaultTagURL
	}
	if c.Spawn == nil {
		c.Spawn = SpawnDetached
	}
	if c.RunBackground == nil {
		c.RunBackground = func(f func()) { go f() }
	}
	return &Manager{cfg: c}
}

// Enabled reports whether remote update is turned on.
func (m *Manager) Enabled() bool { return m != nil && m.cfg.Enabled }

// Status returns the last-known result (or {State:"none"}). A malformed result
// file surfaces as a failed record rather than an error to the caller.
func (m *Manager) Status() updateresult.Result {
	r, err := updateresult.Read(m.cfg.ResultPath)
	if err != nil {
		slog.Warn("remote_update status read failed", "err", err.Error())
		return updateresult.Result{State: updateresult.StateFailed, Error: err.Error()}
	}
	return r
}

// Reconcile fixes a result stuck at "installing" (child died before writing a
// terminal state) by comparing the running version to the recorded to/from.
// Called once at service startup.
func (m *Manager) Reconcile() {
	r, err := updateresult.Read(m.cfg.ResultPath)
	if err != nil || r.State != updateresult.StateInstalling {
		return
	}
	switch m.cfg.CurVersion {
	case r.To:
		r.State = updateresult.StateSucceeded
	case r.From:
		r.State = updateresult.StateFailed
		r.Error = "install did not complete (reconciled at startup)"
	default:
		return
	}
	if err := updateresult.Write(m.cfg.ResultPath, r); err != nil {
		slog.Warn("remote_update reconcile write failed", "err", err.Error())
		return
	}
	slog.Info("remote_update reconciled", "state", r.State, "version", m.cfg.CurVersion)
}

// tryAcquire sets inFlight if it is not already; returns false if a job is
// already running.
func (m *Manager) tryAcquire() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inFlight {
		return false
	}
	m.inFlight = true
	return true
}

func (m *Manager) release() {
	m.mu.Lock()
	m.inFlight = false
	m.mu.Unlock()
}
