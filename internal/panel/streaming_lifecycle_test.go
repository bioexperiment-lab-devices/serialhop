package panel

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
)

func TestStreamingLifecycle_StartStop(t *testing.T) {
	dir := t.TempDir()
	endpoint := filepath.Join(dir, "panel-endpoint.json")
	armed := filepath.Join(dir, "armed-cameras.json")

	lc, err := startStreamingForTest(context.Background(), endpoint, armed)
	if err != nil {
		t.Fatalf("startStreamingForTest: %v", err)
	}
	ep, err := bootstrap.ReadPanelEndpoint(endpoint)
	if err != nil {
		t.Fatalf("ReadPanelEndpoint: %v", err)
	}
	if ep.Port == 0 || ep.PID == 0 {
		t.Fatalf("bad endpoint: %+v", ep)
	}
	if err := lc.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := os.Stat(endpoint); !os.IsNotExist(err) {
		t.Fatalf("endpoint file should be removed: %v", err)
	}
}
