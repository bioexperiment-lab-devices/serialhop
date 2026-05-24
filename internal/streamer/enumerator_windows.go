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
//	[dshow @ ...] "Friendly name"
//	[dshow @ ...]   Alternative name "@device:pnp:\\..."
//
// We stop appending devices once we see the audio-devices marker.
func parseListDevices(raw []byte) ([]Camera, error) {
	const audioMarker = "DirectShow audio devices"
	const altNamePrefix = "Alternative name "

	var cameras []Camera
	var pending *Camera // the camera whose Alternative name we expect next

	lines := bytes.Split(raw, []byte("\n"))
	inAudio := false
	for _, ln := range lines {
		s := string(bytes.TrimRight(ln, "\r"))
		if i := strings.Index(s, "] "); i >= 0 && strings.HasPrefix(s, "[dshow @") {
			s = strings.TrimSpace(s[i+2:])
		} else {
			continue
		}
		if strings.HasPrefix(s, audioMarker) {
			inAudio = true
			continue
		}
		if inAudio {
			continue
		}
		// Friendly name line: starts and ends with a quote.
		if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) && len(s) >= 2 {
			label := strings.TrimSuffix(strings.TrimPrefix(s, `"`), `"`)
			pending = &Camera{Label: label}
			cameras = append(cameras, *pending)
			pending = &cameras[len(cameras)-1]
			continue
		}
		// Alternative name line.
		if pending != nil && strings.HasPrefix(s, altNamePrefix) {
			rest := strings.TrimPrefix(s, altNamePrefix)
			rest = strings.TrimSpace(rest)
			rest = strings.TrimSuffix(strings.TrimPrefix(rest, `"`), `"`)
			pending.ID = rest
			pending = nil
		}
	}
	// Discard cameras that didn't get an Alternative name — without a stable
	// id we'd violate the protocol's id-stability contract.
	out := cameras[:0]
	for _, c := range cameras {
		if c.ID != "" {
			out = append(out, c)
		}
	}
	return out, nil
}
