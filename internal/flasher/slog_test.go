package flasher

import (
	"context"
	"log/slog"
	"testing"
	"time"

	ft "github.com/bioexperiment-lab-devices/serialhop/internal/flasher/testing"
	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

// TestFlash_SlogBoundaries verifies that stage-boundary slog records and the
// top-level "flasher start" / "flasher complete" records are emitted on the
// happy path.
func TestFlash_SlogBoundaries(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	op := &fakeOpenerForFlasher{port: "COM3", fake: ft.NewFakeOptiboot()}
	prev2 := make([]byte, 128)
	for i := range prev2 {
		prev2[i] = byte(i)
	}
	op.fake.PreloadFlash(prev2)

	fl, err := New(op, t.TempDir(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	newImg := make([]byte, 128)
	for i := range newImg {
		newImg[i] = byte(255 - i)
	}

	res, err := fl.Flash(context.Background(), "COM3", Request{
		Firmware:  newImg,
		Timeout:   500 * time.Millisecond,
		InterByte: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Flash: %v", err)
	}
	if res.Outcome != OutcomeSuccess {
		t.Fatalf("Outcome: got %s, want success", res.Outcome)
	}

	// Verify "flasher start" is emitted.
	rec.AssertRecord(t, slog.LevelInfo, "flasher start", map[string]any{"port": "COM3"})

	// Verify "flasher complete" is emitted.
	rec.AssertRecord(t, slog.LevelInfo, "flasher complete", map[string]any{"port": "COM3"})

	// Verify stage start records for each of the 6 stages that run.
	for _, stage := range []string{"preflight", "backup", "erase", "program", "verify", "test"} {
		rec.AssertRecord(t, slog.LevelInfo, "flasher stage start", map[string]any{"stage": stage})
	}

	// Verify stage ok records (test is skipped on happy path without TestCommand).
	for _, stage := range []string{"preflight", "backup", "erase", "program", "verify"} {
		rec.AssertRecord(t, slog.LevelInfo, "flasher stage ok", map[string]any{"stage": stage})
	}
	// test stage is skipped but still logged as ok.
	rec.AssertRecord(t, slog.LevelInfo, "flasher stage ok", map[string]any{"stage": "test", "skipped": true})
}
