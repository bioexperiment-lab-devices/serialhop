package streamer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// ErrFFmpegUnavailable is returned by FFmpegResolver.Probe when the
// bundled ffmpeg binary is missing, unreachable, or reports an unexpected
// version banner. The manager surfaces this as a 503 with body
// `{"error":"ffmpeg unavailable"}` on /start, and as a UI banner.
var ErrFFmpegUnavailable = errors.New("streamer: ffmpeg unavailable")

// FFmpegResolver locates and validates the bundled ffmpeg.exe.
//
// Probe is safe for concurrent use; once it succeeds it caches the result
// for the process lifetime (per spec §7 "Ffmpeg version probe TTL").
type FFmpegResolver struct {
	Path string // absolute path to ffmpeg.exe

	mu         sync.Mutex
	probed     bool
	probeErr   error
	runVersion func(ctx context.Context, path string) (string, error) // injected in tests
}

// NewFFmpegResolver constructs a resolver for the given binary path.
func NewFFmpegResolver(path string) *FFmpegResolver {
	return &FFmpegResolver{
		Path:       path,
		runVersion: defaultRunVersion,
	}
}

// Probe checks the binary on first call and returns a cached result on
// subsequent calls within the same process.
func (r *FFmpegResolver) Probe(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.probed {
		return r.probeErr
	}
	r.probed = true
	if r.Path == "" {
		r.probeErr = fmt.Errorf("%w: empty path", ErrFFmpegUnavailable)
		return r.probeErr
	}
	out, err := r.runVersion(ctx, r.Path)
	if err != nil {
		r.probeErr = fmt.Errorf("%w: %v", ErrFFmpegUnavailable, err)
		return r.probeErr
	}
	if !strings.HasPrefix(out, PinnedFFmpegVersion) {
		r.probeErr = fmt.Errorf("%w: unexpected version banner: %q (want prefix %q)",
			ErrFFmpegUnavailable, firstLine(out), PinnedFFmpegVersion)
		return r.probeErr
	}
	r.probeErr = nil
	return nil
}

func defaultRunVersion(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, path, "-hide_banner", "-version")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
