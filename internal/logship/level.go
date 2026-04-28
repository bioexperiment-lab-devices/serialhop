package logship

import "log/slog"

// ParseLogLevel maps the config string ("debug"|"info"|"warn"|"error")
// to a slog.Level. Unknown values fall through to slog.LevelInfo.
func ParseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
