package streamer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "armed-cameras.json")
	s := NewStore(p)

	cams, err := s.Load()
	if err != nil {
		t.Fatalf("Load on empty: %v", err)
	}
	if len(cams) != 0 {
		t.Fatalf("want empty, got %+v", cams)
	}

	want := []ArmedCamera{
		{ID: "id-1", Label: "Cam One"},
		{ID: "id-2", Label: "Cam Two"},
	}
	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if len(got) != 2 || got[0].ID != "id-1" || got[1].Label != "Cam Two" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestStore_VersionMismatchTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "armed-cameras.json")
	if err := os.WriteFile(p, []byte(`{"version": 99, "cameras":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(p)
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("version mismatch should yield empty list, got %+v", got)
	}
}

func TestStore_CorruptFileTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "armed-cameras.json")
	if err := os.WriteFile(p, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(p)
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("corrupt file should yield empty list, got %+v", got)
	}
}

func TestStore_SaveAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "armed-cameras.json")
	s := NewStore(p)
	if err := s.Save([]ArmedCamera{{ID: "a"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temp file leaked: %s", e.Name())
		}
	}
}
