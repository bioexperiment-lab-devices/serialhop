package flasher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveBackup_WritesFileAndComputesSha(t *testing.T) {
	dir := t.TempDir()
	info, err := SaveBackup(dir, "COM3", ":00000001FF\n")
	if err != nil {
		t.Fatalf("SaveBackup: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(info.Path), "COM3-") {
		t.Errorf("filename: got %q, want prefix COM3-", info.Path)
	}
	if !strings.HasSuffix(info.Path, "Z.hex") {
		t.Errorf("filename: got %q, want suffix Z.hex", info.Path)
	}
	if _, err := os.Stat(info.Path); err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if info.SHA256 == "" {
		t.Error("sha256 empty")
	}
	if info.SizeBytes == 0 {
		t.Error("size_bytes zero")
	}
}

func TestLockBackup_RenamesWithLockedMarker(t *testing.T) {
	dir := t.TempDir()
	info, err := SaveBackup(dir, "COM3", "data")
	if err != nil {
		t.Fatal(err)
	}
	locked, err := LockBackup(info.Path)
	if err != nil {
		t.Fatalf("LockBackup: %v", err)
	}
	if !strings.Contains(filepath.Base(locked), "-LOCKED-") {
		t.Errorf("locked name: %q must contain -LOCKED-", locked)
	}
	if _, err := os.Stat(locked); err != nil {
		t.Fatalf("locked file missing: %v", err)
	}
	if _, err := os.Stat(info.Path); !os.IsNotExist(err) {
		t.Errorf("original file should be gone, stat err = %v", err)
	}
}

func TestPruneBackups_KeepsNNewest(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 6; i++ {
		name := filepath.Join(dir, "COM3-2026-05-12T14-22-"+padTwo(i)+"Z.hex")
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneBackups(dir, "COM3", 3); err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("got %d entries after prune, want 3", len(entries))
	}
	for _, e := range entries {
		suffix := e.Name()
		if !strings.Contains(suffix, "-03Z") && !strings.Contains(suffix, "-04Z") && !strings.Contains(suffix, "-05Z") {
			t.Errorf("kept unexpected file: %s", suffix)
		}
	}
}

func TestPruneBackups_SkipsLockedFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 4; i++ {
		name := filepath.Join(dir, "COM3-2026-05-12T14-22-"+padTwo(i)+"Z.hex")
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(dir, "COM3-LOCKED-2026-05-12T14-22-00Z.hex")
	if err := os.Rename(filepath.Join(dir, "COM3-2026-05-12T14-22-00Z.hex"), locked); err != nil {
		t.Fatal(err)
	}
	if err := PruneBackups(dir, "COM3", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(locked); err != nil {
		t.Errorf("locked file pruned: %v", err)
	}
}

func TestPruneBackups_KeepN_Zero_DoesNothing(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		name := filepath.Join(dir, "COM3-2026-05-12T14-22-"+padTwo(i)+"Z.hex")
		_ = os.WriteFile(name, []byte("x"), 0o600)
	}
	if err := PruneBackups(dir, "COM3", 0); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("keep_n=0 should preserve all; got %d", len(entries))
	}
}

func padTwo(i int) string {
	return fmt.Sprintf("%02d", i)
}
