package logship

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOffsetState_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "panel-log.offset")
	want := offsetState{Size: 1024, MTimeUnixNano: 1_700_000_000_000, ByteOffset: 800}
	if err := writeOffsetAtomic(p, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readOffset(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestOffsetState_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := readOffset(filepath.Join(dir, "absent"))
	if !os.IsNotExist(err) {
		t.Errorf("err = %v, want IsNotExist", err)
	}
}

func TestOffsetState_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "panel-log.offset")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := readOffset(p)
	if err == nil {
		t.Fatal("want error on corrupt JSON, got nil")
	}
}

func TestOffsetState_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "panel-log.offset")
	if err := writeOffsetAtomic(p, offsetState{Size: 1, ByteOffset: 1}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeOffsetAtomic(p, offsetState{Size: 2, ByteOffset: 2}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	// Temp file must be cleaned up.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file leaked: %q", e.Name())
		}
	}
}
