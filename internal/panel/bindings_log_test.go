//go:build windows

package panel

import (
	"log/slog"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

// These tests exercise the logAction integration shape used by Install/
// Uninstall/Restart. The bindings themselves are wrapped with logAction
// and their underlying svc calls happen via *ServiceCli which is hard
// to stub without elevation. So we assert the helper's contract directly
// — the binding bodies just route through it.
func TestInstall_LogsStartAndOk(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	a := &App{}
	done := a.logAction("install")
	done(nil, slog.Bool("cancelled", false))

	rec.AssertRecord(t, slog.LevelInfo, "panel action start", map[string]any{"action": "install"})
	rec.AssertRecord(t, slog.LevelInfo, "panel action ok",
		map[string]any{"action": "install", "cancelled": false})
}

func TestUninstall_LogsStartAndOk(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	a := &App{}
	done := a.logAction("uninstall")
	done(nil, slog.Bool("cancelled", true))

	rec.AssertRecord(t, slog.LevelInfo, "panel action ok",
		map[string]any{"action": "uninstall", "cancelled": true})
}

func TestRestart_LogsStartAndOk(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	a := &App{}
	done := a.logAction("restart")
	done(nil)

	rec.AssertRecord(t, slog.LevelInfo, "panel action start", map[string]any{"action": "restart"})
	rec.AssertRecord(t, slog.LevelInfo, "panel action ok", map[string]any{"action": "restart"})
}
