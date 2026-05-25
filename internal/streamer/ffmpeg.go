package streamer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
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

// WHIPArgs is the per-session input that determines the ffmpeg argv.
//
// Note: the binary path is intentionally NOT a field here — it is
// passed separately to the exec layer (SessionConfig.BinaryPath) so the
// trust boundary stays clear: WHIPArgs values may originate from
// external sources, BinaryPath never does.
type WHIPArgs struct {
	CameraLabel string
	SessionID   string
	WHIPURL     string

	// BearerFlag is the ffmpeg WHIP-muxer flag name that carries the
	// bearer token (e.g. "-authorization"). The exact name depends on the
	// pinned ffmpeg build's WHIP muxer; the implementer confirms it
	// against the binary picked in Task 4.
	BearerFlag  string
	BearerToken string

	Width        int
	Height       int
	Framerate    int
	BitrateKbps  int
	KeyframeIntv int
}

// BuildWHIPArgs produces the argument list (everything AFTER the binary
// path) for a WHIP publish session. The caller passes BinaryPath
// separately to the exec layer; keeping them apart makes the trust
// boundary visible in the type system — BinaryPath is server-controlled,
// the returned args may carry values originating from the lab-bridge
// request body. Note: all externally-supplied values appear as VALUES
// to fixed flags (`-metadata serialhop_session=<sid>`, `<bearer-flag>
// "Bearer <tok>"`, `-i video=<label>`) or as the final positional URL.
// They cannot be reinterpreted as flag names by ffmpeg in a non-shell
// exec.
func BuildWHIPArgs(in WHIPArgs) []string {
	w := in.Width
	if w == 0 {
		w = DefaultVideoWidth
	}
	h := in.Height
	if h == 0 {
		h = DefaultVideoHeight
	}
	fps := in.Framerate
	if fps == 0 {
		fps = DefaultFramerate
	}
	br := in.BitrateKbps
	if br == 0 {
		br = DefaultBitrateKbps
	}
	g := in.KeyframeIntv
	if g == 0 {
		g = DefaultKeyframeInterval
	}
	return []string{
		"-hide_banner",
		// Use info-level logging so dshow/codec/whip init messages
		// reach the panel log even when the session succeeds. With
		// `error` we get ONLY actual errors, which leaves zero
		// context when ffmpeg fails before its error-emitting code
		// path runs (e.g. argv parse failure on Windows when a
		// muxer is missing from the build). info adds about 20
		// lines of startup chatter — tolerable given the cap on
		// stderr capture.
		"-loglevel", "info",
		"-f", "dshow",
		"-rtbufsize", "256M",
		"-framerate", strconv.Itoa(fps),
		"-video_size", strconv.Itoa(w) + "x" + strconv.Itoa(h),
		"-i", "video=" + in.CameraLabel,
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-tune", "zerolatency",
		"-profile:v", "baseline",
		"-level", "3.1",
		"-pix_fmt", "yuv420p",
		"-b:v", strconv.Itoa(br) + "k",
		"-maxrate", strconv.Itoa(br) + "k",
		"-bufsize", strconv.Itoa(2*br) + "k",
		"-g", strconv.Itoa(g),
		"-keyint_min", strconv.Itoa(g),
		"-metadata", "serialhop_session=" + in.SessionID,
		"-f", "whip",
		// ffmpeg's WHIP muxer takes the RAW token here — it formats
		// the full `Authorization: Bearer <token>` header itself.
		// Verified against `ffmpeg -h muxer=whip` on both 7.1 and 8.x:
		// the option doc reads "The optional Bearer token for WHIP
		// Authorization". Passing "Bearer <token>" doubles the
		// scheme on the wire and lab-bridge rejects the request.
		in.BearerFlag, in.BearerToken,
		in.WHIPURL,
	}
}

// RedactedArgs returns a copy of argv suitable for logging — bearer
// tokens replaced with `****`. We mask values both by adjacency to a
// known bearer flag and by `Bearer ` prefix, so a future change to the
// WHIP-muxer flag name (or a renderer that wraps with "Bearer ") still
// gets redacted.
func RedactedArgs(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i := 0; i < len(out); i++ {
		if i < len(out)-1 && (out[i] == "-authorization" || out[i] == "-bearer_token") {
			out[i+1] = "****"
		}
		if strings.HasPrefix(out[i], "Bearer ") {
			out[i] = "Bearer ****"
		}
	}
	return out
}
