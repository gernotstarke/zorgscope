package ports

import (
	"errors"
	"fmt"
	"time"
)

// Sentinel errors adapters return so the scheduler can react (arc42 §8.7).
var (
	ErrAuth      = errors.New("authentication failed")
	ErrTransient = errors.New("transient upstream error")
	ErrPermanent = errors.New("permanent upstream error")
)

// RateLimitedError says when the upstream will accept requests again.
type RateLimitedError struct{ ResetAt time.Time }

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("rate limited until %s", e.ResetAt.UTC().Format(time.RFC3339))
}

// AsRateLimited unwraps a RateLimitedError.
func AsRateLimited(err error) (*RateLimitedError, bool) {
	var rl *RateLimitedError
	if errors.As(err, &rl) {
		return rl, true
	}
	return nil, false
}
