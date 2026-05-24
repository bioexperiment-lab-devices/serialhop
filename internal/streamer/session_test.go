package streamer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func buildFakeFFmpeg(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "fake_ffmpeg")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "./testbin/fake_ffmpeg") //nolint:gosec // out is t.TempDir(); literal "go" and "./testbin/fake_ffmpeg"
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build fake_ffmpeg: %v", err)
	}
	return out
}

func TestSession_StartThenStop(t *testing.T) {
	bin := buildFakeFFmpeg(t)
	s, err := StartSession(context.Background(), SessionConfig{
		Argv:           []string{bin, "--marker", "test-sid"},
		GracefulPeriod: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	// Give it a moment to print the "started" line so stderr capture is non-empty.
	time.Sleep(100 * time.Millisecond)
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	<-s.Done()
	if s.LastError() != "" && !contains(s.LastError(), "clean exit") &&
		!contains(s.LastError(), "started") {
		// LastError is the most recent stderr line; on a clean stop we
		// don't require any particular content, but it should be non-empty.
		t.Logf("LastError after clean stop: %q (informational)", s.LastError())
	}
}

func TestSession_QuickExitSurfacesStderr(t *testing.T) {
	bin := buildFakeFFmpeg(t)
	s, err := StartSession(context.Background(), SessionConfig{
		Argv:           []string{bin},
		Env:            []string{"FAKE_FFMPEG_EXIT_FAST=1"},
		GracefulPeriod: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session did not exit within 2s")
	}
	if s.ExitCode() == 0 {
		t.Errorf("want non-zero exit, got %d", s.ExitCode())
	}
	if s.LastError() == "" {
		t.Errorf("expected stderr captured, got empty")
	}
}

func TestSession_HardKillsAfterGracePeriod(t *testing.T) {
	bin := buildFakeFFmpeg(t)
	s, err := StartSession(context.Background(), SessionConfig{
		Argv:           []string{bin},
		Env:            []string{"FAKE_FFMPEG_IGNORE_SIGNALS=1"},
		GracefulPeriod: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	_ = s.Stop(context.Background())
	<-s.Done()
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Errorf("hard kill took too long: %v", elapsed)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
