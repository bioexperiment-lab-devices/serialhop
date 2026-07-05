package device

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeState struct {
	SchemaVersion int     `json:"schema_version"`
	MlPerStep     float64 `json:"ml_per_step"`
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir, "pump-26-025")
	var got fakeState
	found, err := st.Load(&got)
	if err != nil || found {
		t.Fatalf("Load on missing file: found=%v err=%v", found, err)
	}
	if err := st.Save(fakeState{SchemaVersion: 1, MlPerStep: 0.000424}); err != nil {
		t.Fatal(err)
	}
	found, err = st.Load(&got)
	if err != nil || !found {
		t.Fatalf("Load after Save: found=%v err=%v", found, err)
	}
	if got.MlPerStep != 0.000424 || got.SchemaVersion != 1 {
		t.Errorf("got %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "pump-26-025.json")); err != nil {
		t.Errorf("expected state file: %v", err)
	}
}

func TestStoreSaveOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir, "k")
	if err := st.Save(fakeState{SchemaVersion: 1, MlPerStep: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.Save(fakeState{SchemaVersion: 1, MlPerStep: 2}); err != nil {
		t.Fatal(err)
	}
	var got fakeState
	if _, err := st.Load(&got); err != nil || got.MlPerStep != 2 {
		t.Fatalf("got %+v err %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestStoreKeySanitized(t *testing.T) {
	st := NewStore("/tmp/x", `valve-COM/7:b`)
	base := filepath.Base(st.Path())
	if strings.ContainsAny(base, `/\:`) || base != "valve-COM_7_b.json" {
		t.Errorf("path not sanitized: %s", st.Path())
	}
}

func TestStoreLoadCorruptFileErrors(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir, "bad")
	if err := os.WriteFile(st.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got fakeState
	if _, err := st.Load(&got); err == nil {
		t.Fatal("expected error on corrupt state file")
	}
}
