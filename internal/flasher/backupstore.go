package flasher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BackupInfo describes a saved pre-flash flash-memory backup.
type BackupInfo struct {
	Path      string
	SHA256    string
	SizeBytes int
}

// SaveBackup writes hex content to <dir>/<port>-<ISO8601-Z>.hex and returns
// the path, sha256, and size. ISO8601 uses hyphen separators in the time
// component because colons are illegal in Windows filenames; the format
// remains lexicographically sortable.
func SaveBackup(dir, port, hexText string) (BackupInfo, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return BackupInfo{}, fmt.Errorf("backup: mkdir %s: %w", dir, err)
	}
	stamp := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	name := fmt.Sprintf("%s-%s.hex", port, stamp)
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte(hexText), 0o644); err != nil {
		return BackupInfo{}, fmt.Errorf("backup: write %s: %w", full, err)
	}
	sum := sha256.Sum256([]byte(hexText))
	return BackupInfo{
		Path:      full,
		SHA256:    hex.EncodeToString(sum[:]),
		SizeBytes: len(hexText),
	}, nil
}

// LockBackup renames a backup file to insert the -LOCKED- marker so the
// pruner will skip it indefinitely. Returns the new path.
func LockBackup(path string) (string, error) {
	dir, base := filepath.Split(path)
	idx := strings.Index(base, "-")
	if idx < 0 {
		return "", fmt.Errorf("backup: malformed filename %q", base)
	}
	locked := base[:idx] + "-LOCKED" + base[idx:]
	newPath := filepath.Join(dir, locked)
	if err := os.Rename(path, newPath); err != nil {
		return "", fmt.Errorf("backup: lock %s: %w", path, err)
	}
	return newPath, nil
}

// PruneBackups deletes all but the newest keepN files matching
// <port>-<timestamp>.hex in dir. Files containing "-LOCKED-" in the name
// are never deleted. keepN == 0 disables pruning.
func PruneBackups(dir, port string, keepN int) error {
	if keepN == 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("backup: readdir %s: %w", dir, err)
	}
	prefix := port + "-"
	var candidates []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if !strings.HasSuffix(name, ".hex") {
			continue
		}
		if strings.Contains(name, "-LOCKED-") {
			continue
		}
		candidates = append(candidates, name)
	}
	if len(candidates) <= keepN {
		return nil
	}
	sort.Strings(candidates)
	for _, name := range candidates[:len(candidates)-keepN] {
		full := filepath.Join(dir, name)
		if err := os.Remove(full); err != nil {
			return fmt.Errorf("backup: prune %s: %w", full, err)
		}
	}
	return nil
}
