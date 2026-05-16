package logship

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close() //nolint:errcheck
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func drainQueue(q *queue, n int, timeout time.Duration) []record {
	deadline := time.Now().Add(timeout)
	var got []record
	for len(got) < n && time.Now().Before(deadline) {
		got = append(got, q.drainUpTo(n-len(got))...)
		if len(got) < n {
			time.Sleep(10 * time.Millisecond)
		}
	}
	return got
}

// TestFileTail_ReadsNewLines verifies that lines appended after the tailer
// has anchored its cold-start offset are shipped to the queue.
//
// The plan's original test seeded the file before starting the tailer, which
// conflicts with the cold-start-at-EOF anchoring spec (§6.3). We create an
// empty file first so the anchor lands at offset 0, then append the lines.
func TestFileTail_ReadsNewLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.log")
	offsetPath := filepath.Join(dir, "panel-log.offset")

	// Create the file empty FIRST so the tailer cold-starts at EOF=0.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("touch: %v", err)
	}

	q := newQueue(100)
	ft := &fileTail{q: q, path: path, offsetPath: offsetPath, stream: "panel", poll: 20 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go ft.run(ctx)

	// Give the tailer one tick to anchor at EOF=0.
	time.Sleep(50 * time.Millisecond)

	// Now append lines — these are AFTER the cold-start anchor.
	writeLines(t, path, `{"msg":"one"}`, `{"msg":"two"}`)

	got := drainQueue(q, 2, 400*time.Millisecond)
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(got), got)
	}
	if !strings.Contains(got[0].line, "one") || got[0].stream != "panel" {
		t.Errorf("got[0] = %+v", got[0])
	}
}

func TestFileTail_ResumesFromOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.log")
	offsetPath := filepath.Join(dir, "panel-log.offset")

	writeLines(t, path, `{"msg":"one"}`, `{"msg":"two"}`)
	q := newQueue(100)
	ft := &fileTail{q: q, path: path, offsetPath: offsetPath, stream: "panel", poll: 20 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	go ft.run(ctx)
	_ = drainQueue(q, 2, 250*time.Millisecond)
	cancel()
	<-time.After(50 * time.Millisecond)

	writeLines(t, path, `{"msg":"three"}`)
	q2 := newQueue(100)
	ft2 := &fileTail{q: q2, path: path, offsetPath: offsetPath, stream: "panel", poll: 20 * time.Millisecond}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	go ft2.run(ctx2)
	got := drainQueue(q2, 1, 250*time.Millisecond)
	if len(got) != 1 || !strings.Contains(got[0].line, "three") {
		t.Fatalf("want only 'three' replayed; got %+v", got)
	}
}

func TestFileTail_HandlesRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.log")
	offsetPath := filepath.Join(dir, "panel-log.offset")

	writeLines(t, path, `{"msg":"old1"}`, `{"msg":"old2"}`)
	q := newQueue(100)
	ft := &fileTail{q: q, path: path, offsetPath: offsetPath, stream: "panel", poll: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ft.run(ctx)
	_ = drainQueue(q, 2, 300*time.Millisecond)

	// Simulate lumberjack rotation: rename to .1 and create new file.
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	writeLines(t, path, `{"msg":"new1"}`)

	got := drainQueue(q, 1, 500*time.Millisecond)
	if len(got) != 1 || !strings.Contains(got[0].line, "new1") {
		t.Fatalf("want new1 after rotation; got %+v", got)
	}
}

// TestFileTail_MissingFile verifies that the tailer waits gracefully when the
// log file doesn't exist and picks up lines once the file appears.
//
// The plan's original test created the file with content before starting the
// second tailer run, which conflicts with cold-start-at-EOF anchoring. We
// create an empty file first so the tailer anchors at offset 0, then append.
// We also use separate fileTail instances for the two runs to avoid data races
// on loggedMissing between goroutines.
func TestFileTail_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "absent.log")
	offsetPath := filepath.Join(dir, "panel-log.offset")
	q := newQueue(100)

	// First run: file doesn't exist — tailer should log once and produce nothing.
	ft1 := &fileTail{q: q, path: path, offsetPath: offsetPath, stream: "panel", poll: 20 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go ft1.run(ctx)
	<-ctx.Done()
	if recs := q.drainUpTo(10); len(recs) != 0 {
		t.Errorf("queue has records, want none: %+v", recs)
	}

	// Create an empty file so the second tailer cold-starts at EOF=0, then append.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create empty file: %v", err)
	}
	ft2 := &fileTail{q: q, path: path, offsetPath: offsetPath, stream: "panel", poll: 20 * time.Millisecond}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel2()
	go ft2.run(ctx2)
	// Give the tailer one tick to anchor at EOF=0.
	time.Sleep(50 * time.Millisecond)
	writeLines(t, path, `{"msg":"hello"}`)
	got := drainQueue(q, 1, 300*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("file appeared but tailer missed it: %+v", got)
	}
}

func TestFileTail_CorruptOffsetFallsBackToEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.log")
	offsetPath := filepath.Join(dir, "panel-log.offset")
	writeLines(t, path, `{"msg":"pre"}`)
	if err := os.WriteFile(offsetPath, []byte("not-json"), 0o600); err != nil {
		t.Fatalf("seed offset: %v", err)
	}
	q := newQueue(100)
	ft := &fileTail{q: q, path: path, offsetPath: offsetPath, stream: "panel", poll: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ft.run(ctx)
	// Allow one poll cycle so the tailer rewrites the offset.
	time.Sleep(80 * time.Millisecond)
	writeLines(t, path, `{"msg":"post"}`)
	got := drainQueue(q, 1, 300*time.Millisecond)
	if len(got) != 1 || !strings.Contains(got[0].line, "post") {
		t.Fatalf("want only post after corrupt-offset reset; got %+v", got)
	}
}

func TestFileTail_LineTooLong(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.log")
	offsetPath := filepath.Join(dir, "panel-log.offset")

	// Cold-start: create empty file so the tailer anchors at 0.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("touch: %v", err)
	}

	q := newQueue(100)
	ft := &fileTail{q: q, path: path, offsetPath: offsetPath, stream: "panel", poll: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ft.run(ctx)
	time.Sleep(80 * time.Millisecond) // anchor at EOF

	// Write a line larger than the 1 MiB scanner buffer.
	huge := make([]byte, (1<<20)+10)
	for i := range huge {
		huge[i] = 'x'
	}
	huge[len(huge)-1] = '\n'
	if err := os.WriteFile(path, huge, 0o600); err != nil {
		t.Fatalf("write huge: %v", err)
	}

	// Wait for the tailer to encounter ErrTooLong and advance.
	time.Sleep(150 * time.Millisecond)

	// Append a normal line — should ship.
	writeLines(t, path, `{"msg":"after-huge"}`)

	got := drainQueue(q, 1, 400*time.Millisecond)
	if len(got) != 1 || !strings.Contains(got[0].line, "after-huge") {
		t.Fatalf("want only after-huge to ship; got %+v", got)
	}
}
