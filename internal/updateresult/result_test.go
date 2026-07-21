package updateresult

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "update_result.json")
	in := Result{State: StateInstalling, From: "2.2.0", To: "2.3.0", StartedAt: "2026-07-21T10:00:00Z"}
	if err := Write(p, in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(p)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != in {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, in)
	}
	// no stray .partial left behind
	if _, err := os.Stat(p + ".partial"); !os.IsNotExist(err) {
		t.Errorf(".partial should not persist after Write")
	}
}

func TestReadMissingReturnsNone(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("Read missing should not error, got %v", err)
	}
	if got.State != StateNone {
		t.Errorf("missing file State = %q, want %q", got.State, StateNone)
	}
}

func TestReadMalformedErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(p); err == nil {
		t.Error("malformed file should surface an error")
	}
}
