// Package agentinfo gathers the runtime self-description served by
// GET /agent/info. The endpoint exists so the lab-bridge server can pull
// version / OS / machine identity from each connected client; see
// docs/superpowers/specs/2026-05-18-agent-info-endpoint-design.md.
package agentinfo

import (
	"os"
	"runtime"
	"strings"
	"time"

	internalversion "github.com/bioexperiment-lab-devices/serialhop/internal/version"
)

// Info is the JSON payload returned by GET /agent/info.
type Info struct {
	Version       string `json:"version"`
	BuildSHA      string `json:"build_sha,omitempty"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Hostname      string `json:"hostname"`
	MachineID     string `json:"machine_id,omitempty"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// extractBuildSHA returns everything after the first '+' in v, or "" if
// there is no '+'. Mirrors the format produced by tools/buildcmd which
// concatenates the assets/version.json string with a git-describe suffix.
func extractBuildSHA(v string) string {
	i := strings.IndexByte(v, '+')
	if i < 0 {
		return ""
	}
	return v[i+1:]
}

// startedAt is captured at package init. The agent imports agentinfo from
// the long-running app.Run path, so this is a close approximation of
// process start. Off by at most the time between binary entry and the
// first agentinfo reference.
var startedAt = time.Now()

// Snapshot returns the current Info. Each field is gathered independently
// — a failure in one (e.g. os.Hostname returning an error) sets that field
// to its zero value and continues. The endpoint is best-effort and must
// never fail.
func Snapshot() Info {
	host, _ := os.Hostname() // empty on error — handler still returns 200
	return Info{
		Version:       internalversion.Version,
		BuildSHA:      extractBuildSHA(internalversion.Version),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Hostname:      host,
		UptimeSeconds: int64(time.Since(startedAt).Seconds()),
	}
}
