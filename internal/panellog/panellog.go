// Package panellog owns the panel process's slog handler and the
// on-disk rotated SerialHop_panel.log file. It is symmetric to
// internal/logship's slog tap but in-process to the panel — no shipper,
// no queue. The service-side logship.fileTail watches the file and
// ships its lines via the existing chisel tunnel.
package panellog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
)

var errMissingPath = errors.New("panellog: paths.PanelLogPath unavailable; call paths.EnsureDirs first")

// Manager owns the lumberjack writer and slog handler installed by Init.
type Manager struct {
	mu        sync.Mutex
	disk      *lumberjack.Logger
	levelVar  *slog.LevelVar
	prev      *slog.Logger
	sessionID string
	closed    bool
}

// Init installs a JSON slog handler whose writer is a 10 MiB / 3-backup
// lumberjack-rotated SerialHop_panel.log under paths.LogsDir.
// It also generates a per-process session id attached as a group attr
// "panel.session_id" + "panel.pid" on every record.
// On first run it deletes any stale paths.PanelErrorLogPath() file
// (single-shot migration).
// slog.SetDefault is called. Subsequent slog.* calls land in the panel log.
func Init(version string, level slog.Level) (*Manager, error) {
	logPath := paths.PanelLogPath()
	if logPath == "" {
		return nil, errMissingPath
	}

	sid, err := newSessionID()
	if err != nil {
		return nil, fmt.Errorf("panellog: generate session id: %w", err)
	}

	disk := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    10,
		MaxBackups: 3,
		LocalTime:  true,
	}
	levelVar := new(slog.LevelVar)
	levelVar.Set(level)

	handler := slog.NewJSONHandler(disk, &slog.HandlerOptions{Level: levelVar})
	withPanel := handler.WithAttrs([]slog.Attr{
		slog.Group("panel",
			slog.String("session_id", sid),
			slog.Int("pid", os.Getpid()),
		),
	})

	prev := slog.Default()
	slog.SetDefault(slog.New(withPanel))

	// Migration: delete the legacy breadcrumb file if present.
	// Runs after slog.SetDefault so the deferred warn lands in the panel handler.
	if legacy := paths.PanelErrorLogPath(); legacy != "" {
		if err := os.Remove(legacy); err != nil && !os.IsNotExist(err) {
			// Non-fatal — log it now that the panel handler is installed.
			defer slog.Warn("panellog: failed to remove legacy file", "path", legacy, "err", err)
		}
	}

	m := &Manager{
		disk:      disk,
		levelVar:  levelVar,
		prev:      prev,
		sessionID: sid,
	}

	cfgPath := paths.ConfigPath()
	cfgPresent := false
	if cfgPath != "" {
		if _, err := os.Stat(cfgPath); err == nil {
			cfgPresent = true
		}
	}
	slog.Info("panel session start",
		"version", version,
		"data_dir", paths.DataDir(),
		"config_present", cfgPresent,
	)
	return m, nil
}

// SetLevel changes the slog level live without re-installing the handler.
func (m *Manager) SetLevel(level slog.Level) {
	m.levelVar.Set(level)
}

// SessionID returns the stable per-process panel session id.
func (m *Manager) SessionID() string { return m.sessionID }

// Shutdown emits the session-end record and closes the lumberjack
// writer. Idempotent. The previous slog.Default is NOT restored —
// panel-process lifetime equals process lifetime in production.
func (m *Manager) Shutdown(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	slog.Info("panel session end")
	err := m.disk.Close()
	m.closed = true
	return err
}

func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	// RFC 4122 v4-ish; we don't need strict UUID — a 32-hex-char unique
	// identifier is sufficient for filtering panel sessions in Grafana.
	return hex.EncodeToString(b[:]), nil
}
