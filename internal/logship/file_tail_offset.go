package logship

import (
	"encoding/json"
	"fmt"
	"os"
)

// offsetState is the on-disk shape persisted to paths.PanelLogOffsetPath.
// Size+MTimeUnixNano form a cheap signature: if either changes such that
// the saved ByteOffset can't be valid (file shrank or was replaced),
// the tailer resets to 0.
type offsetState struct {
	Size          int64 `json:"size"`
	MTimeUnixNano int64 `json:"mtime_unix_nano"`
	ByteOffset    int64 `json:"byte_offset"`
}

// readOffset reads the persisted state. Returns the underlying error;
// callers distinguish os.IsNotExist (cold start) from corruption.
func readOffset(path string) (offsetState, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path is paths.PanelLogOffsetPath()
	if err != nil {
		return offsetState{}, err
	}
	var s offsetState
	if err := json.Unmarshal(b, &s); err != nil {
		return offsetState{}, fmt.Errorf("decode offset state: %w", err)
	}
	return s, nil
}

// writeOffsetAtomic writes the new state to <path>.tmp then renames.
// On POSIX the rename is atomic; on Windows the call uses MoveFileEx
// semantics via os.Rename, which is atomic on NTFS for same-volume
// renames. The temp file is removed on rename failure.
func writeOffsetAtomic(path string, s offsetState) error {
	tmp := path + ".tmp"
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode offset state: %w", err)
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
