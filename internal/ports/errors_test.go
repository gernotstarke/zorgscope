package ports

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestErrors(t *testing.T) {
	wrapped := fmt.Errorf("github: %w", ErrAuth)
	if !errors.Is(wrapped, ErrAuth) {
		t.Fatal("ErrAuth must survive wrapping")
	}
	rl := fmt.Errorf("github: %w", &RateLimitedError{ResetAt: time.Unix(100, 0)})
	got, ok := AsRateLimited(rl)
	if !ok || got.ResetAt.Unix() != 100 {
		t.Fatalf("AsRateLimited: %v %v", got, ok)
	}
	if _, ok := AsRateLimited(ErrTransient); ok {
		t.Fatal("transient is not rate limited")
	}
	if rl.Error() == "" {
		t.Fatal("message")
	}
}
