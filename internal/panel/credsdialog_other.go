//go:build !windows

package panel

import "github.com/bioexperiment-lab-devices/serialhop/internal/config"

// runCredsDialog is implemented only on Windows. On other platforms the
// panel itself doesn't compile (panel.go is windows-only) but firstrun
// helpers are cross-platform; this stub keeps the package's exported
// API consistent and the tests buildable.
func runCredsDialog(_ string, _ config.Config) bool { return false }
