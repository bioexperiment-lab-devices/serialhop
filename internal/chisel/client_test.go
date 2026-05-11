package chisel

import (
	"slices"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

func TestRemotesIncludesForwardWhenAuthSet(t *testing.T) {
	got := buildRemotes(Config{User: "lab-1", RemotePort: 8081, LocalPort: 5000})
	want := []string{
		"R:8081:127.0.0.1:5000",
		"127.0.0.1:3100:loki:3100",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("remotes=%v, want %v", got, want)
	}
}

func TestRemotesOmitsForwardWhenNoAuth(t *testing.T) {
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
