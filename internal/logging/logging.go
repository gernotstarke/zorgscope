// Package logging builds the JSON slog logger with secret redaction (FR-10.2, QS-3.3).
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// New returns a JSON logger at the given level ("debug", "info", "warn", "error"); any attribute
// whose rendered string form contains one of secrets is redacted.
//
// Redaction operates on a.Value.String() rather than being gated on a.Value.Kind() ==
// slog.KindString (deviation D-9, docs/plans/README.md): every "err", err log site in this
// codebase passes an error value (slog.KindAny), and a.Value.String() renders that — and any other
// kind — as text, so a secret embedded in an error's message is caught too. When no configured
// secret appears in an attribute's rendered form, the original Attr is returned unchanged, which
// keeps non-string kinds (time, level, numbers) encoded in their normal JSON form instead of being
// forced through slog.String.
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
		if len(clean) == 0 {
			return a
		}
		v := a.Value.String()
		changed := false
		for _, s := range clean {
			if strings.Contains(v, s) {
				v = strings.ReplaceAll(v, s, "[REDACTED]")
				changed = true
			}
		}
		if !changed {
			return a
		}
		return slog.String(a.Key, v)
	}})
	return slog.New(h)
}
