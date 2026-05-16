package api

import (
	"log/slog"
	"net/http"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(s int) {
	r.status = s
	r.ResponseWriter.WriteHeader(s)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// logMiddleware wraps a handler with one slog record per request.
// Level: INFO for 2xx/3xx, WARN for 4xx, ERROR for 5xx.
// Fields: route, method, remote_addr, status, bytes, dur.
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		level := slog.LevelInfo
		switch {
		case rec.status >= 500:
			level = slog.LevelError
		case rec.status >= 400:
			level = slog.LevelWarn
		}
		slog.LogAttrs(r.Context(), level, "api handler",
			slog.String("route", r.URL.Path),
			slog.String("method", r.Method),
			slog.String("remote_addr", r.RemoteAddr),
			slog.Int("status", rec.status),
			slog.Int("bytes", rec.bytes),
			slog.Duration("dur", time.Since(start)),
		)
	})
}
