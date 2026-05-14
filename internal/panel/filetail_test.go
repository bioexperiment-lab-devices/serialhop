package panel

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type sink struct {
	mu      sync.Mutex
	lines   []string
	rotated int
}

func (s *sink) line(line string) {
	s.mu.Lock()
	s.lines = append(s.lines, line)
	s.mu.Unlock()
}

func (s *sink) rotate() {
	s.mu.Lock()
	s.rotated++
	s.mu.Unlock()
}

func (s *sink) snapshot() ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]string(nil), s.lines...)
	return out, s.rotated
}

func TestFileTail_StartsFromEndOfFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(p, []byte("preexisting line\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s := &sink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tailer := NewFileTail(p, 10*time.Millisecond, s.line, s.rotate)
	go tailer.Run(ctx)

	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // p is t.TempDir() + literal filename
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString("new line\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.snapshot()
		if len(got) > 0 {
			if got[0] != "new line" {
				t.Errorf("got %q, want %q", got[0], "new line")
			}
			if len(got) > 1 {
				t.Errorf("expected exactly 1 line, got %v", got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("timeout — no lines emitted")
}

func TestFileTail_DetectsRotationOnInodeReset(t *testing.T) {
	// Windows refuses to rename or remove a file that another handle on
	// the same machine holds open without FILE_SHARE_DELETE (Go's
	// os.Open doesn't set it). That blocks every test mutation that
	// would simulate inode-reset rotation while the tailer is running.
	// The size-shrink branch of Run still detects rotation correctly on
	// Windows (lumberjack on Windows actually rotates by truncating, not
	// renaming), so production coverage isn't lost — only this specific
	// inode-driven simulation is unix-only.
	if runtime.GOOS == "windows" {
		t.Skip("inode-reset simulation requires renaming a file held open by the tailer; Windows blocks that without FILE_SHARE_DELETE")
	}
	p := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(p, []byte("v1 line\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := &sink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tailer := NewFileTail(p, 10*time.Millisecond, s.line, s.rotate)
	go tailer.Run(ctx)

	// Wait for the tailer to attach.
	time.Sleep(50 * time.Millisecond)

	// Simulate lumberjack rotation: remove file, create new shorter one.
	if err := os.Remove(p); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.WriteFile(p, []byte("post-rotation\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		lines, rot := s.snapshot()
		if rot > 0 && len(lines) > 0 && lines[len(lines)-1] == "post-rotation" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, rot := s.snapshot()
	t.Errorf("rotation not detected: rotated=%d lines=%v", rot, got)
}

func TestFileTail_ReadBacklogReturnsRecentLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	content := "alpha\nbravo\ncharlie\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tailer := NewFileTail(p, 10*time.Millisecond, func(string) {}, func() {})
	defer tailer.Close() //nolint:errcheck
	got := tailer.ReadBacklog(1024)
	want := []string{"alpha", "bravo", "charlie"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("backlog: got %v, want %v", got, want)
	}
}

func TestFileTail_ReadBacklogDropsPartialFirstLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	// Generate enough content that 32 bytes lands mid-line, then the
	// first complete line begins after the next '\n'.
	content := strings.Repeat("ABCDEFGHIJ\n", 10) // 110 bytes; each line is "ABCDEFGHIJ"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tailer := NewFileTail(p, 10*time.Millisecond, func(string) {}, func() {})
	defer tailer.Close()          //nolint:errcheck
	got := tailer.ReadBacklog(32) // covers last ~32 bytes including a partial leading record
	// Every line is "ABCDEFGHIJ"; we should never see a truncated prefix.
	for i, l := range got {
		if l != "ABCDEFGHIJ" {
			t.Errorf("line %d: got %q, want %q (partial-line leak)", i, l, "ABCDEFGHIJ")
		}
	}
	if len(got) == 0 {
		t.Errorf("expected at least one complete line in 32-byte tail")
	}
}

func TestFileTail_ReadBacklogThenLiveTailHasNoOverlapOrGap(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(p, []byte("preexisting\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := &sink{}
	tailer := NewFileTail(p, 10*time.Millisecond, s.line, s.rotate)
	backlog := tailer.ReadBacklog(1024)
	if !reflect.DeepEqual(backlog, []string{"preexisting"}) {
		t.Fatalf("backlog: got %v, want [preexisting]", backlog)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tailer.Run(ctx)

	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // p is t.TempDir() + literal filename
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString("new line\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = f.Close()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, _ := s.snapshot()
		if len(got) > 0 {
			if !reflect.DeepEqual(got, []string{"new line"}) {
				t.Errorf("live: got %v, want [new line] (overlap with backlog?)", got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("timeout — no live line emitted")
}

func TestFileTail_MissingFileEmitsNothing(t *testing.T) {
	s := &sink{}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	tailer := NewFileTail(filepath.Join(t.TempDir(), "nope"), 10*time.Millisecond, s.line, s.rotate)
	tailer.Run(ctx) // blocks until ctx expires
	got, _ := s.snapshot()
	if len(got) != 0 {
		t.Errorf("expected no lines, got %v", got)
	}
}
