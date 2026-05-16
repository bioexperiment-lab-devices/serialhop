package chisel

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

func TestRun_LogsStartAndExit(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = Run(ctx, Config{
		Server:     "127.0.0.1:1", // unreachable; chisel will fail or ctx will expire
		User:       "tester",
		RemotePort: 9000,
		LocalPort:  9001,
	})

	rec.AssertRecord(t, slog.LevelInfo, "chisel run starting", map[string]any{"auth": true})
}

func TestRun_LogsErrorOnInvalidServer(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx := context.Background()
	_ = Run(ctx, Config{
		Server:     "invalid", // missing port — SplitHostPort fails
		User:       "tester",
		RemotePort: 9000,
		LocalPort:  9001,
	})

	rec.AssertRecord(t, slog.LevelError, "chisel run starting", nil)
}

func TestRemotesIncludesAllForwardTunnels(t *testing.T) {
	got := buildRemotes(Config{
		User: "lab-1", RemotePort: 8081, LocalPort: 5000,
		ForwardTunnels: []labbridge.ForwardTunnel{
			{Name: "loki", Local: "127.0.0.1:3100", Remote: "loki:3100"},
		},
	})
	want := []string{
		"R:8081:127.0.0.1:5000",
		"127.0.0.1:3100:loki:3100",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("remotes=%v, want %v", got, want)
	}
}

func TestRemotesEmptyForwardTunnelsOnlyReverseRoute(t *testing.T) {
	got := buildRemotes(Config{User: "", RemotePort: 8081, LocalPort: 5000})
	want := []string{"R:8081:127.0.0.1:5000"}
	if !slices.Equal(got, want) {
		t.Fatalf("remotes=%v, want %v", got, want)
	}
}

func TestRemotesAppendsForwardTunnels(t *testing.T) {
	got := buildRemotes(Config{
		User: "", RemotePort: 8081, LocalPort: 5000,
		ForwardTunnels: []labbridge.ForwardTunnel{
			{Name: "loki", Local: "127.0.0.1:3100", Remote: "loki:3100"},
			{Name: "graf", Local: "127.0.0.1:3000", Remote: "grafana:3000"},
		},
	})
	want := []string{
		"R:8081:127.0.0.1:5000",
		"127.0.0.1:3100:loki:3100",
		"127.0.0.1:3000:grafana:3000",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("remotes=%v, want %v", got, want)
	}
}
