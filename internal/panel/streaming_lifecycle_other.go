//go:build !windows

package panel

import "context"

// killOrphans is a no-op on non-Windows (dev hosts).
func killOrphans(_ context.Context) error { return nil }
