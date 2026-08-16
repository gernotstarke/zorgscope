package logging

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRedactsSecretsAndHonoursLevel(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "info", []string{"ghp_supersecret", ""})
	log.Debug("hidden")
	log.Info("token seen", "token", "ghp_supersecret", "other", "fine", "n", 3)
	log.Warn("nested", "err", "auth failed for ghp_supersecret")
	out := buf.String()
	if strings.Contains(out, "hidden") {
		t.Fatal("debug must be suppressed at info level")
	}
	if strings.Contains(out, "ghp_supersecret") {
		t.Fatalf("secret leaked: %s", out)
	}
	if !strings.Contains(out, `"token":"[REDACTED]"`) || !strings.Contains(out, `"other":"fine"`) || !strings.Contains(out, "auth failed for [REDACTED]") {
		t.Fatalf("unexpected output: %s", out)
	}
	if !strings.Contains(out, `"level":"INFO"`) || !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("expected JSON lines: %s", out)
	}
}

// TestRedactsSecretInErrorValue covers D-9: every error-log site in the codebase passes "err", err
// where err is an error (slog.KindAny), not a string. The naive ReplaceAttr that only inspects
// slog.KindString misses this entirely, so a secret embedded in an error's text would reach stdout
// unredacted. New must redact based on the attribute's rendered string form instead.
func TestRedactsSecretInErrorValue(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "info", []string{"ghp_SECRET"})
	err := fmt.Errorf("upstream said: %w", errors.New("token=ghp_SECRET"))
	log.Error("request failed", "err", err)
	out := buf.String()
	if strings.Contains(out, "ghp_SECRET") {
		t.Fatalf("secret leaked via error-valued attribute: %s", out)
	}
	if !strings.Contains(out, "token=[REDACTED]") {
		t.Fatalf("expected redacted error text in output: %s", out)
	}
}
