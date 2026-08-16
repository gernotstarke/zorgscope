// Package logging builds the JSON slog logger with secret redaction (FR-10.2, QS-3.3).
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// New returns a JSON logger at the given level ("debug", "info", "warn", "error"); any string
// attribute containing one of secrets is redacted.
func New(w io.Writer, level string, secrets []string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	clean := make([]string, 0, len(secrets))
	for _, s := range secrets {
		if s != "" {
			clean = append(clean, s)
		}
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Value.Kind() != slog.KindString {
			return a
		}
		v := a.Value.String()
		for _, s := range clean {
			if strings.Contains(v, s) {
				v = strings.ReplaceAll(v, s, "[REDACTED]")
			}
		}
		return slog.String(a.Key, v)
	}})
	return slog.New(h)
}
