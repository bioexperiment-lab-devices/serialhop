//go:build windows

package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"time"
)

// shortDeviceID returns the first 8 hex chars of sha256(id). Stable per
// raw id, low collision risk for the small number of devices a single
// lab attaches in a session. Used in slog attributes to avoid logging
// raw device identifiers that may carry lab-internal context.
func shortDeviceID(id string) string {
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:4])
}

// logAction emits one "panel action start" INFO record and returns a
// closure that emits the matching end record. On success the end
// record is "panel action ok" INFO; on error it is "panel action
// failed" ERROR with the error string in the "err" attribute. Both
// end records carry the elapsed duration in the "dur" attribute.
// Extra attrs are merged into the end record (the start record gets
// only the `action` name + extras passed at start time).
//
// Usage:
//
//	done := a.logAction("install")
//	res := a.svc.Install(...)
//	done(installErr(res), slog.Bool("cancelled", res.Cancelled))
func (a *App) logAction(name string, startAttrs ...slog.Attr) func(err error, extra ...slog.Attr) {
	ctx := context.Background()
	start := time.Now()
	attrs := append([]slog.Attr{slog.String("action", name)}, startAttrs...)
	slog.LogAttrs(ctx, slog.LevelInfo, "panel action start", attrs...)
	return func(err error, extra ...slog.Attr) {
		end := append([]slog.Attr{
			slog.String("action", name),
			slog.Duration("dur", time.Since(start)),
		}, extra...)
		if err != nil {
			end = append(end, slog.String("err", err.Error()))
			slog.LogAttrs(ctx, slog.LevelError, "panel action failed", end...)
			return
		}
		slog.LogAttrs(ctx, slog.LevelInfo, "panel action ok", end...)
	}
}
