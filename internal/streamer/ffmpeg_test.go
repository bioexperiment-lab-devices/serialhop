package streamer

import (
	"context"
	"errors"
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
