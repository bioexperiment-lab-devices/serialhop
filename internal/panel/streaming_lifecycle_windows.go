//go:build windows

package panel

import (
	"context"
	"log/slog"
	"os/exec"
)

// killOrphans finds ffmpeg.exe processes whose command line carries our
// session marker and force-kills them. Used at panel startup to recover
// from a previous panel that crashed without graceful shutdown.
func killOrphans(ctx context.Context) error {
	// wmic is deprecated but still ubiquitous; use it to find candidates by command line.
	cmd := exec.CommandContext(ctx, "wmic", "process", "where",
		"name='ffmpeg.exe' and CommandLine like '%serialhop_session=%'",
		"get", "ProcessId", "/format:value")
	out, err := cmd.CombinedOutput()
	if err != nil {
		slog.Info("streamer: orphan scan failed (no orphans assumed)", "err", err)
		return nil
	}
	pids := parseWmicPids(string(out))
	for _, pid := range pids {
		k := exec.CommandContext(ctx, "taskkill", "/pid", pid, "/T", "/F")
		if err := k.Run(); err != nil {
			slog.Info("streamer: orphan kill failed", "pid", pid, "err", err)
		} else {
			slog.Info("streamer: killed orphan ffmpeg", "pid", pid)
		}
	}
	return nil
}

func parseWmicPids(s string) []string {
	var out []string
	for _, ln := range splitLines(s) {
		const prefix = "ProcessId="
		if len(ln) > len(prefix) && ln[:len(prefix)] == prefix {
			out = append(out, ln[len(prefix):])
		}
	}
	return out
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			ln := s[start:i]
			if n := len(ln); n > 0 && ln[n-1] == '\r' {
				ln = ln[:n-1]
			}
			if ln != "" {
				out = append(out, ln)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		ln := s[start:]
		if n := len(ln); n > 0 && ln[n-1] == '\r' {
			ln = ln[:n-1]
		}
		if ln != "" {
			out = append(out, ln)
		}
	}
	return out
}
