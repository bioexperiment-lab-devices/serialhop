package device

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store persists one device's driver state as a single JSON file with
// atomic replace-on-save. Drivers own the schema and must include a
// schema_version field (spec §5).
type Store struct {
	path string
}

// NewStore builds a store at dir/<sanitized key>.json.
func NewStore(dir, key string) *Store {
	return &Store{path: filepath.Join(dir, sanitizeKey(key)+".json")}
}

func (st *Store) Path() string { return st.path }

// Load reads the state into v. Returns (false, nil) when no state exists.
func (st *Store) Load(v any) (bool, error) {
	data, err := os.ReadFile(st.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("device store read %s: %w", st.path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("device store decode %s: %w", st.path, err)
	}
	return true, nil
}

// Save writes v atomically: temp file in the same directory, then rename.
func (st *Store) Save(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("device store encode: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(st.path), 0o700); err != nil {
		return fmt.Errorf("device store mkdir: %w", err)
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("device store write: %w", err)
	}
	if err := os.Rename(tmp, st.path); err != nil {
		return fmt.Errorf("device store rename: %w", err)
	}
	return nil
}

func sanitizeKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
