//go:build windows

package streamer

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
)

// NewEnumerator returns the Windows DirectShow enumerator backed by ffmpeg.
func NewEnumerator() Enumerator {
	return &dshowEnumerator{ffmpegPath: paths.FFmpegPath()}
}

type dshowEnumerator struct {
	ffmpegPath string
}

func (e *dshowEnumerator) List(ctx context.Context) ([]Camera, error) {
	if e.ffmpegPath == "" {
		return nil, fmt.Errorf("streamer: ffmpeg path unset")
	}
	// ffmpeg writes the list to stderr and exits non-zero (it has no real
	// input). We capture stderr and ignore the exit code.
	cmd := exec.CommandContext(ctx, e.ffmpegPath,
		"-hide_banner",
		"-list_devices", "true",
		"-f", "dshow",
		"-i", "dummy",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run() // expected non-zero exit
	return parseListDevices(stderr.Bytes())
}

// parseListDevices extracts the video-device list from ffmpeg dshow's
// -list_devices output.
//
// Format (one device occupies two consecutive lines):
//
//	[dshow @ ...] "Friendly name" (video)
//	[dshow @ ...]   Alternative name "@device:pnp:\\..."
//
// Audio devices appear with the suffix "(audio)" and are skipped. The
// "(video)" / "(audio)" tag is the discriminator; section headers
// (e.g. "DirectShow audio devices") are ignored entirely because their
// position relative to device lines is not load-bearing across ffmpeg
// builds.
//
// The error return is currently always nil but is reserved for future
// hard-failure cases (e.g. malformed input) so callers — notably
// dshowEnumerator.List and the manager that treats List failures as
// recoverable — don't have to change signature later.
func parseListDevices(raw []byte) ([]Camera, error) {
	const altNamePrefix = "Alternative name "
	const videoTag = "(video)"
	const audioTag = "(audio)"

	var cameras []Camera
	var pending *Camera // the camera whose Alternative name we expect next

	for _, ln := range bytes.Split(raw, []byte("\n")) {
		s := strings.TrimRight(string(ln), "\r")
		// Strip the "[dshow @ ...] " prefix.
		i := strings.Index(s, "] ")
		if i < 0 || !strings.HasPrefix(s, "[dshow @") {
			continue
		}
		s = strings.TrimSpace(s[i+2:])

		// Device line: ends with "(video)" or "(audio)".
		if strings.HasSuffix(s, videoTag) {
			label := extractQuotedLabel(strings.TrimSuffix(s, videoTag))
			if label == "" {
				pending = nil
				continue
			}
			cameras = append(cameras, Camera{Label: label})
			pending = &cameras[len(cameras)-1]
			continue
		}
		if strings.HasSuffix(s, audioTag) {
			pending = nil
			continue
		}

		// Alternative name line for the most recent device (video only;
		// pending is nil after an audio device).
		if pending != nil && strings.HasPrefix(s, altNamePrefix) {
			rest := strings.TrimPrefix(s, altNamePrefix)
			rest = strings.TrimSpace(rest)
			rest = strings.TrimSuffix(strings.TrimPrefix(rest, `"`), `"`)
			pending.ID = rest
			pending = nil
		}
	}

	// Discard cameras that didn't get an Alternative name — without a
	// stable id we'd violate the protocol's id-stability contract.
	out := cameras[:0]
	for _, c := range cameras {
		if c.ID != "" {
			out = append(out, c)
		}
	}
	return out, nil
}

// extractQuotedLabel returns the substring between the first and last
// double-quote in s. Whitespace outside the quotes is ignored.
func extractQuotedLabel(s string) string {
	s = strings.TrimSpace(s)
	first := strings.IndexByte(s, '"')
	last := strings.LastIndexByte(s, '"')
	if first < 0 || last <= first {
		return ""
	}
	return s[first+1 : last]
}
