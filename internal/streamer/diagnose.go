package streamer

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Diagnose runs a one-shot probe against the bundled ffmpeg binary and
// returns whatever it observes. Intended for the panel's "Diagnose"
// button on the Cameras tab — surfaces the raw outputs so a human can
// figure out whether ffmpeg is missing, DirectShow sees no devices,
// or something else has gone wrong (e.g. only MediaFoundation cameras).
//
// Never returns an error itself; all failure detail goes into the
// returned struct's *Error fields. That keeps the Wails binding simple.
func Diagnose(ctx context.Context, ffmpegPath string) FFmpegDiagnostics {
	out := FFmpegDiagnostics{FFmpegPath: ffmpegPath}
	if ffmpegPath == "" {
		out.VersionError = "ffmpeg path is empty (paths.FFmpegPath() returned \"\"; was the installer run?)"
		return out
	}
	if _, err := os.Stat(ffmpegPath); err != nil {
		if os.IsNotExist(err) {
			out.BinaryExists = false
			out.VersionError = "ffmpeg.exe not found at " + ffmpegPath + " — reinstall SerialHop to populate the bundled binary"
		} else {
			out.VersionError = "stat ffmpeg.exe: " + err.Error()
		}
		return out
	}
	out.BinaryExists = true

	// `ffmpeg -hide_banner -version` — quick liveness check.
	{
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(probeCtx, ffmpegPath, "-hide_banner", "-version") //nolint:gosec // ffmpegPath is paths.FFmpegPath(), server-controlled
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			out.VersionError = err.Error()
		} else {
			out.VersionLine = firstNonEmptyLine(stdout.String())
		}
	}

	// `ffmpeg -hide_banner -list_devices true -f dshow -i dummy` —
	// ffmpeg writes to stderr and exits non-zero (no input). Capture
	// stderr verbatim. This is the most useful diagnostic: if the
	// stderr shows "DirectShow video devices" followed by no entries,
	// the user's cameras are likely MediaFoundation-only.
	{
		listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(listCtx, ffmpegPath, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy") //nolint:gosec // ffmpegPath is paths.FFmpegPath()
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		_ = cmd.Run() // expected non-zero
		raw := stderr.String()
		if raw == "" {
			out.ListDevicesError = "ffmpeg emitted no stderr — dshow may be unavailable in this build"
		} else {
			// Cap at 8KB so a runaway log doesn't bloat the Wails channel.
			if len(raw) > 8192 {
				raw = raw[:8192] + "\n... (truncated)"
			}
			out.ListDevicesRaw = raw
		}
	}
	return out
}

func firstNonEmptyLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			return ln
		}
	}
	return ""
}
