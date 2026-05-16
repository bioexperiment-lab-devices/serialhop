//go:build windows

package panel

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

func TestLogAction_OK(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	a := &App{}
	done := a.logAction("install")
	done(nil, slog.Bool("cancelled", false))

	rec.AssertRecord(t, slog.LevelInfo, "panel action start", map[string]any{"action": "install"})
	rec.AssertRecord(t, slog.LevelInfo, "panel action ok", map[string]any{"action": "install", "cancelled": false})
}

func TestLogAction_Error(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	a := &App{}
	done := a.logAction("save_config", slog.String("cfg_host", "lab1.example.com"))
	done(errors.New("write failed"))

	rec.AssertRecord(t, slog.LevelInfo, "panel action start",
		map[string]any{"action": "save_config", "cfg_host": "lab1.example.com"})
	rec.AssertRecord(t, slog.LevelError, "panel action failed",
		map[string]any{"action": "save_config", "err": "write failed"})
}
