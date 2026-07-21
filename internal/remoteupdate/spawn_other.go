//go:build !windows

package remoteupdate

import (
	"fmt"
	"runtime"
)

// SpawnDetached is a stub on non-Windows: remote update is a Windows-only
// production feature. Tests inject a fake Spawn into the Manager, so this is
// never exercised by unit tests; it exists only so the package compiles and
// runs cross-platform per CLAUDE.md.
func SpawnDetached(_ string, _ []string) error {
	return fmt.Errorf("detached update spawn not supported on %s", runtime.GOOS)
}
