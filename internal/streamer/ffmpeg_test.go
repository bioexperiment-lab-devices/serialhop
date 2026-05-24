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
		BinaryPath:   "C:\\Program Files\\SerialHop\\ffmpeg.exe",
		CameraLabel:  "Logitech HD Pro Webcam C920",
		SessionID:    "01HXYZ8K2NQM4R6V9P3T1W5Z7B",
		WHIPURL:      "https://lab.example.com/streamer/whip/01HXYZ8K2NQM4R6V9P3T1W5Z7B",
		BearerFlag:   "-authorization",
		BearerToken:  "tk_F2k9q_secret",
		Width:        1280,
		Height:       720,
		Framerate:    24,
		BitrateKbps:  1500,
		KeyframeIntv: 48,
	})

	mustHave := []string{
		"C:\\Program Files\\SerialHop\\ffmpeg.exe",
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
		"-authorization", "Bearer tk_F2k9q_secret",
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
}

func TestBuildWHIPArgs_TokenNotInOrderedLog(t *testing.T) {
	args := BuildWHIPArgs(WHIPArgs{
		BinaryPath:  "ffmpeg",
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
}
