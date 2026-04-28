package logship

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

func TestInstallSlogTapWritesToDiskAndQueue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slog.log")
	q := newQueue(64)
	levelVar := new(slog.LevelVar)
	levelVar.Set(slog.LevelInfo)

	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	lj := &lumberjack.Logger{Filename: path, MaxSize: 1, MaxBackups: 1}
	t.Cleanup(func() { _ = lj.Close() })

	if err := installSlogTap(lj, levelVar, q); err != nil {
		t.Fatalf("installSlogTap: %v", err)
	}

	slog.Info("hello", "k", "v")

	// Disk: file should contain the message.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), "hello") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), `"hello"`) {
		t.Fatalf("log file missing message:\n%s", data)
	}

	// Queue: one record with stream=stdout.
	got := q.drainUpTo(10)
	if len(got) != 1 {
		t.Fatalf("queue drain returned %d records, want 1", len(got))
	}
	if got[0].stream != "stdout" {
		t.Errorf("stream=%q, want stdout", got[0].stream)
	}
	if !strings.Contains(got[0].line, `"hello"`) {
		t.Errorf("line=%q does not contain message", got[0].line)
	}
}
