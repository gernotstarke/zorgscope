package logging

import (
	"bytes"
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
