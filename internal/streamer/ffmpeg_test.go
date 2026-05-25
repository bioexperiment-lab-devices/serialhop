package streamer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestProbeFFmpeg_OK(t *testing.T) {
	r := &FFmpegResolver{
		Path: "/dev/null", // unused — overridden via runVersion
		runVersion: func(_ context.Context, _ string) (string, error) {
			return "ffmpeg version 7.1-essentials_build-www.gyan.dev Copyright (c) ...", nil
		},
	}
	if err := r.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
}

func TestProbeFFmpeg_MissingBinary(t *testing.T) {
	r := &FFmpegResolver{
		Path: "",
		runVersion: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("not used")
		},
	}
	err := r.Probe(context.Background())
	if !errors.Is(err, ErrFFmpegUnavailable) {
		t.Fatalf("want ErrFFmpegUnavailable, got %v", err)
	}
}

func TestProbeFFmpeg_VersionMismatch(t *testing.T) {
	r := &FFmpegResolver{
		Path: "/tmp/ffmpeg",
		runVersion: func(_ context.Context, _ string) (string, error) {
			return "ffmpeg version 4.0-wrong build", nil
		},
	}
	err := r.Probe(context.Background())
	if !errors.Is(err, ErrFFmpegUnavailable) {
		t.Fatalf("want ErrFFmpegUnavailable, got %v", err)
	}
}

func TestProbeFFmpeg_RunFailed(t *testing.T) {
	r := &FFmpegResolver{
		Path: "/tmp/ffmpeg",
		runVersion: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("permission denied")
		},
	}
	err := r.Probe(context.Background())
	if !errors.Is(err, ErrFFmpegUnavailable) {
		t.Fatalf("want ErrFFmpegUnavailable, got %v", err)
	}
}

func TestBuildWHIPArgs(t *testing.T) {
	args := BuildWHIPArgs(WHIPArgs{
		CameraLabel:  "Logitech HD Pro Webcam C920",
		SessionID:    "01HXYZ8K2NQM4R6V9P3T1W5Z7B",
		WHIPURL:      "https://lab.example.com/streamer/whip/01HXYZ8K2NQM4R6V9P3T1W5Z7B",
		BearerFlag:   "-authorization",
		BearerToken:  "test-token-value-123",
		Width:        1280,
		Height:       720,
		Framerate:    24,
		BitrateKbps:  1500,
		KeyframeIntv: 48,
	})

	// BuildWHIPArgs deliberately does NOT include the binary path — the
	// caller passes it to the exec layer separately so the trust
	// boundary stays explicit (binary path is server-controlled, args
	// may contain externally-supplied values that we validate at the
	// Manager boundary).
	mustHave := []string{
		"-f", "dshow",
		"-video_size", "1280x720",
		"-framerate", "24",
		"-i", `video=Logitech HD Pro Webcam C920`,
		"-c:v", "libx264",
		"-profile:v", "baseline",
		"-b:v", "1500k",
		"-g", "48",
		"-metadata", "serialhop_session=01HXYZ8K2NQM4R6V9P3T1W5Z7B",
		"-f", "whip",
		"-authorization", "Bearer test-token-value-123",
		"https://lab.example.com/streamer/whip/01HXYZ8K2NQM4R6V9P3T1W5Z7B",
	}
	for _, want := range mustHave {
		found := false
		for _, a := range args {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("args missing %q\nactual: %q", want, args)
		}
	}

	// URL must be the last positional.
	if got := args[len(args)-1]; got != "https://lab.example.com/streamer/whip/01HXYZ8K2NQM4R6V9P3T1W5Z7B" {
		t.Errorf("URL must be last positional; last arg = %q", got)
	}

	// Bearer flag and "Bearer <token>" must be adjacent.
	foundPair := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-authorization" && args[i+1] == "Bearer test-token-value-123" {
			foundPair = true
			break
		}
	}
	if !foundPair {
		t.Errorf("expected -authorization immediately followed by 'Bearer <token>', got argv: %q", args)
	}
}

func TestBuildWHIPArgs_TokenNotInOrderedLog(t *testing.T) {
	args := BuildWHIPArgs(WHIPArgs{
		CameraLabel: "Cam",
		SessionID:   "S",
		WHIPURL:     "u",
		BearerFlag:  "-authorization",
		BearerToken: "SECRET",
	})
	// The exported helper RedactedArgs hides the token for logging.
	red := RedactedArgs(args)
	for _, a := range red {
		if strings.Contains(a, "SECRET") {
			t.Fatalf("token leaked into redacted args: %q", red)
		}
	}

	// Redaction must replace the token slot with the canonical mask.
	foundMask := false
	for _, a := range red {
		if a == "Bearer ****" {
			foundMask = true
			break
		}
	}
	if !foundMask {
		t.Errorf("expected 'Bearer ****' to appear in redacted args, got %q", red)
	}
}
