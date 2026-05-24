package panel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
	"github.com/bioexperiment-lab-devices/serialhop/internal/streamer"
)

// StreamingLifecycle owns the streamer subsystem inside the panel
// process. It is started in App.startup and stopped in App.shutdown.
type StreamingLifecycle struct {
	endpointPath string
	armedPath    string
	ffmpegPath   string
	bearerFlag   string

	manager streamer.Manager
	srv     *http.Server
	listen  net.Listener
}

// NewStreamingLifecycle constructs an unstarted lifecycle.
func NewStreamingLifecycle(endpointPath, armedPath, ffmpegPath, bearerFlag string) *StreamingLifecycle {
	return &StreamingLifecycle{
		endpointPath: endpointPath,
		armedPath:    armedPath,
		ffmpegPath:   ffmpegPath,
		bearerFlag:   bearerFlag,
	}
}

// Start does the full panel-side initialization:
//  1. Kill orphans from the previous run (platform-specific).
//  2. Construct the manager.
//  3. Bind the localhost HTTP listener.
//  4. Write panel-endpoint.json.
//  5. Run an initial Refresh.
func (lc *StreamingLifecycle) Start(ctx context.Context) error {
	_ = killOrphans(ctx) // platform-specific; best-effort

	store := streamer.NewStore(lc.armedPath)
	resolver := streamer.NewFFmpegResolver(lc.ffmpegPath)
	mgr := streamer.NewManager(streamer.ManagerConfig{
		Store:       store,
		Enumerator:  streamer.NewEnumerator(),
		FFmpegPath:  lc.ffmpegPath,
		FFmpegReady: func() error { return resolver.Probe(context.Background()) },
		BearerFlag:  lc.bearerFlag,
	})
	if _, err := mgr.Refresh(ctx); err != nil {
		// Non-fatal — UI surfaces it.
		slog.Warn("streaming: initial enumeration failed", "err", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("streaming: bind listener: %w", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	srv := &http.Server{
		Handler:           streamingHandler(mgr),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		serveErr := srv.Serve(l)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			slog.Warn("streaming: HTTP listener exited", "err", serveErr)
		}
	}()
	if err := bootstrap.WritePanelEndpoint(lc.endpointPath, bootstrap.PanelEndpoint{
		Host:      "127.0.0.1",
		Port:      addr.Port,
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		_ = srv.Shutdown(context.Background())
		_ = l.Close()
		return fmt.Errorf("streaming: write endpoint: %w", err)
	}
	lc.manager = mgr
	lc.srv = srv
	lc.listen = l
	return nil
}

// Manager returns the running manager. Nil if Start has not been called.
func (lc *StreamingLifecycle) Manager() streamer.Manager { return lc.manager }

// Stop reverses Start. It is safe to call even if Start failed partway
// through — all fields are nil-checked.
func (lc *StreamingLifecycle) Stop(ctx context.Context) error {
	if lc.srv != nil {
		_ = lc.srv.Shutdown(ctx)
	}
	if lc.listen != nil {
		_ = lc.listen.Close()
	}
	if lc.manager != nil {
		_ = lc.manager.Shutdown(ctx)
	}
	_ = bootstrap.DeletePanelEndpoint(lc.endpointPath)
	return nil
}

// startStreamingForTest is a convenience constructor used by the
// lifecycle test in streaming_lifecycle_test.go.
func startStreamingForTest(ctx context.Context, endpointPath, armedPath string) (*StreamingLifecycle, error) {
	lc := NewStreamingLifecycle(endpointPath, armedPath, "", "-authorization")
	if err := lc.Start(ctx); err != nil {
		return nil, err
	}
	return lc, nil
}
