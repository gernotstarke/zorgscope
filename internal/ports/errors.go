package ports

import (
	"errors"
	"fmt"
	"time"
)

// Sentinel errors adapters return so the scheduler can react (arc42 §8.7).
var (
	// ErrAuth signals that an upstream call failed authentication; the app layer turns this into
	// an "AUTH FAILED" attention item for the source (FR-11.3).
	ErrAuth = errors.New("authentication failed")
	// ErrTransient signals a temporary upstream failure that is expected to succeed on retry.
	ErrTransient = errors.New("transient upstream error")
	// ErrPermanent signals an upstream failure that will not succeed on retry (e.g. bad config).
	ErrPermanent = errors.New("permanent upstream error")
)

// RateLimitedError says when the upstream will accept requests again.
type RateLimitedError struct{ ResetAt time.Time }

// Error renders the message shown to operators, including the reset time.
func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("rate limited until %s", e.ResetAt.UTC().Format(time.RFC3339))
}

// AsRateLimited unwraps err to find a *RateLimitedError, following any %w wrapping.
func AsRateLimited(err error) (*RateLimitedError, bool) {
	var rl *RateLimitedError
	if errors.As(err, &rl) {
		return rl, true
	}
	return nil, false
}
