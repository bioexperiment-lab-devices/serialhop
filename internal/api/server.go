package api

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Listen binds a TCP listener on 127.0.0.1:port. port=0 → OS picks free.
// Returns the listener and the actual port chosen.
func Listen(port int) (net.Listener, int, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, 0, err
	}
	return l, l.Addr().(*net.TCPAddr).Port, nil
}

// Serve runs the HTTP server until ctx is cancelled, then shuts it down
// with a 5-second grace period.
func Serve(ctx context.Context, l net.Listener, handler http.Handler) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(l) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}
