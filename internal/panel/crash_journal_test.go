package panel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendCapped_AppendsLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "j.log")
	if err := appendCapped(p, []byte("alpha\n"), 1024); err != nil {
		t.Fatalf("appendCapped: %v", err)
	}
	if err := appendCapped(p, []byte("beta\n"), 1024); err != nil {
		t.Fatalf("appendCapped: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got, want := string(b), "alpha\nbeta\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestAppendCapped_TrimsToLastNBytesAtLineBoundary(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "j.log")
	line := strings.Repeat("x", 80) + "\n" // 81 bytes per line
	for i := 0; i < 200; i++ {
		if err := appendCapped(p, []byte(line), 1024); err != nil {
			t.Fatalf("appendCapped i=%d: %v", i, err)
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if int64(len(b)) > 1024 {
		t.Fatalf("file size %d exceeds cap 1024", len(b))
	}
	if !strings.HasSuffix(string(b), "\n") {
		t.Fatalf("file does not end with newline: tail=%q", string(b[len(b)-5:]))
	}
	// First surviving line should be a full 80-x line, not a fragment.
	first := strings.IndexByte(string(b), '\n')
	if first < 0 {
		t.Fatalf("no newline in trimmed content")
	}
	if first != 80 {
		t.Fatalf("first line length = %d, want 80 (a clean 80-x line)", first)
	}
}

func TestAppendCrashJournal_DisabledIsNoop(t *testing.T) {
	t.Setenv("SERIALHOP_PANEL_CRASH_JOURNAL_DISABLE", "1")
	// Must not panic and must not write anywhere.
	appendCrashJournal("msg", "src", "stack", "v0", time.Now())
}

func TestAppendCrashJournal_WritesJSONLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "j.log")
	t.Setenv("SERIALHOP_PANEL_CRASH_JOURNAL_PATH", p)
	t.Setenv("SERIALHOP_PANEL_CRASH_JOURNAL_DISABLE", "")
	now := time.Date(2026, 5, 15, 12, 34, 56, 0, time.UTC)
	appendCrashJournal("boom", "tab:devices", "at line 1", "0.20.0", now)

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got crashEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(b))), &got); err != nil {
		t.Fatalf("unmarshal: %v -- raw %q", err, b)
	}
	if got.Message != "boom" || got.Source != "tab:devices" ||
		got.Stack != "at line 1" || got.Version != "0.20.0" {
		t.Fatalf("unexpected entry: %+v", got)
	}
	if got.Time != now.Format(time.RFC3339Nano) {
		t.Fatalf("Time = %q, want %q", got.Time, now.Format(time.RFC3339Nano))
	}
}
