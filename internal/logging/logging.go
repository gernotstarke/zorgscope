// Package logging builds the JSON slog logger with secret redaction (FR-10.2, QS-3.3).
package logging

import (
	"io"
	"log/slog"
	"strings"
	"sync"
)

// SecretSet is a live redaction list. Runtime configuration may rotate upstream credentials
// without restarting the process, so the logger must not retain only its startup values.
type SecretSet struct {
	mu     sync.RWMutex
	values []string
}

// NewSecretSet creates a live secret redaction set.
func NewSecretSet(values []string) *SecretSet {
	s := &SecretSet{}
	s.Replace(values)
	return s
}

// Replace atomically replaces the values redacted from future log attributes.
func (s *SecretSet) Replace(values []string) {
	clean := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			clean = append(clean, value)
		}
	}
	s.mu.Lock()
	s.values = clean
	s.mu.Unlock()
}

func (s *SecretSet) snapshot() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.values...)
}

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
	return NewDynamic(w, level, NewSecretSet(secrets))
}

// NewDynamic returns a logger backed by a live redaction set.
func NewDynamic(w io.Writer, level string, secrets *SecretSet) *slog.Logger {
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
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		clean := secrets.snapshot()
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
