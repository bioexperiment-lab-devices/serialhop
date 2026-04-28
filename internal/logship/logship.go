// Package logship streams the client's slog output and stderr to the
// in-VPS Loki via the chisel forward tunnel.
//
// It also owns the durable on-disk log files (lab_devices_client.log,
// lab_devices_client_stderr.log) so disabling the shipper does not
// affect on-disk logging.
package logship

import (
	"context"
	"log/slog"
)

// LogFileName is the basename of the rotated slog file.
const LogFileName = "lab_devices_client.log"

// StderrLogFileName is the basename of the rotated stderr file.
const StderrLogFileName = "lab_devices_client_stderr.log"

// Manager owns the capture taps, ring buffer, and shipper goroutine.
//
// Construct it once at process start with Init. Call StartShipper after
// the chisel user is known. Call Shutdown before exit.
type Manager struct {
	// Filled in by later tasks.
	_ slog.Level
	_ context.Context
}
