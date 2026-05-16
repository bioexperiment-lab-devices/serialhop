package slogtest_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

func TestRecorder_CapturesRecords(t *testing.T) {
	rec := slogtest.NewRecorder()
	logger := slog.New(rec)
	logger.Info("hello", "k", 1)
	logger.LogAttrs(context.Background(), slog.LevelWarn, "warn msg", slog.String("err", "boom"))

	if got := len(rec.Records()); got != 2 {
		t.Fatalf("got %d records, want 2", got)
	}
	if rec.Records()[0].Message != "hello" {
		t.Errorf("rec[0].Message = %q, want hello", rec.Records()[0].Message)
	}
	if rec.Records()[1].Level != slog.LevelWarn {
		t.Errorf("rec[1].Level = %v, want WARN", rec.Records()[1].Level)
	}
}

func TestRecorder_FindByMessageAttr(t *testing.T) {
	rec := slogtest.NewRecorder()
	logger := slog.New(rec)
	logger.Info("panel action start", "action", "install")
	logger.Info("panel action ok", "action", "install", "dur", "12ms")

	got := rec.Find(slog.LevelInfo, "panel action ok", map[string]any{"action": "install"})
	if got == nil {
		t.Fatal("expected to find panel action ok with action=install")
	}
	miss := rec.Find(slog.LevelInfo, "panel action ok", map[string]any{"action": "nope"})
	if miss != nil {
		t.Fatal("did not expect to find action=nope")
	}
}
