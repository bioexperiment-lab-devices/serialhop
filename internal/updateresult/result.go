// Package updateresult is the shared, restart-surviving record of the last
// remote-update outcome. Written by the service (progress) and by the elevated
// swap child (terminal state); read by GET /agent/update/status. Leaf package:
// stdlib only, so both internal/remoteupdate and internal/winsvc can import it
// without a cycle.
package updateresult

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Update result states.
const (
	StateNone        = "none"
	StateDownloading = "downloading"
	StateVerifying   = "verifying"
	StateInstalling  = "installing"
	StateSucceeded   = "succeeded"
	StateRolledBack  = "rolled_back"
	StateFailed      = "failed"
)

// Result is the JSON body of GET /agent/update/status.
type Result struct {
	State      string `json:"state"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	Pct        int    `json:"pct,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// Read returns the persisted result, or {State: "none"} if the file is absent.
// A malformed file is an error (surfaced so a status read can report it).
func Read(path string) (Result, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path is a fixed ProgramData location
	if errors.Is(err, os.ErrNotExist) {
		return Result{State: StateNone}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("read update result %s: %w", path, err)
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return Result{}, fmt.Errorf("parse update result %s: %w", path, err)
	}
	return r, nil
}

// Write atomically persists r (write to <path>.partial, fsync, rename) so a
// concurrent Read never sees a torn file.
func Write(path string, r Result) error {
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal update result: %w", err)
	}
	partial := path + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // fixed location
	if err != nil {
		return fmt.Errorf("create %s: %w", partial, err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(partial)
		return fmt.Errorf("write %s: %w", partial, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(partial)
		return fmt.Errorf("fsync %s: %w", partial, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("close %s: %w", partial, err)
	}
	if err := os.Rename(partial, path); err != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("rename %s -> %s: %w", partial, path, err)
	}
	return nil
}
