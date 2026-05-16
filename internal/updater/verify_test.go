package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

func writeTestFile(t *testing.T, dir, name string, body []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestVerifyFile_OK(t *testing.T) {
	dir := t.TempDir()
	body := []byte("hello world")
	p := writeTestFile(t, dir, "SerialHop-v0.7.0.exe", body)

	sums := sha256Hex(body) + "  SerialHop-v0.7.0.exe\n" +
		sha256Hex([]byte("other")) + "  other.exe\n"

	if err := VerifyFile(p, sums, "SerialHop-v0.7.0.exe"); err != nil {
		t.Errorf("VerifyFile: %v", err)
	}
}

func TestVerifyFile_Mismatch(t *testing.T) {
	dir := t.TempDir()
	p := writeTestFile(t, dir, "SerialHop-v0.7.0.exe", []byte("hello world"))

	sums := sha256Hex([]byte("DIFFERENT")) + "  SerialHop-v0.7.0.exe\n"
	err := VerifyFile(p, sums, "SerialHop-v0.7.0.exe")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("err: %v", err)
	}
}

func TestVerifyFile_FilenameNotInSums(t *testing.T) {
	dir := t.TempDir()
	p := writeTestFile(t, dir, "SerialHop-v0.7.0.exe", []byte("hello world"))

	sums := sha256Hex([]byte("anything")) + "  SerialHop-v0.6.0.exe\n"
	err := VerifyFile(p, sums, "SerialHop-v0.7.0.exe")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err: %v", err)
	}
}

func TestVerifyFile_MalformedLineSkipped(t *testing.T) {
	// A malformed line should not crash the parser; the well-formed entry
	// after it should still resolve.
	dir := t.TempDir()
	body := []byte("hello world")
	p := writeTestFile(t, dir, "SerialHop-v0.7.0.exe", body)

	sums := "this line is junk\n" +
		sha256Hex(body) + "  SerialHop-v0.7.0.exe\n"

	if err := VerifyFile(p, sums, "SerialHop-v0.7.0.exe"); err != nil {
		t.Errorf("VerifyFile: %v", err)
	}
}

func TestVerifyFile_LogsInfoOnOK(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	body := []byte("hello world")
	p := writeTestFile(t, dir, "SerialHop-v0.7.0.exe", body)
	sums := sha256Hex(body) + "  SerialHop-v0.7.0.exe\n"

	if err := VerifyFile(p, sums, "SerialHop-v0.7.0.exe"); err != nil {
		t.Fatalf("VerifyFile: %v", err)
	}

	rec.AssertRecord(t, slog.LevelInfo, "updater verify start", map[string]any{"path": p})
	rec.AssertRecord(t, slog.LevelInfo, "updater verify ok", map[string]any{"path": p})
}

func TestVerifyFile_LogsWarnOnMismatch(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	p := writeTestFile(t, dir, "SerialHop-v0.7.0.exe", []byte("hello world"))
	sums := sha256Hex([]byte("DIFFERENT")) + "  SerialHop-v0.7.0.exe\n"

	_ = VerifyFile(p, sums, "SerialHop-v0.7.0.exe")

	rec.AssertRecord(t, slog.LevelWarn, "updater checksum mismatch", map[string]any{"path": p})
}
