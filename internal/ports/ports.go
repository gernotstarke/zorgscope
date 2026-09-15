// Package ports declares the interfaces that separate zorgscope's domain from the outside world:
// fetching from upstream sources, checking who may sign in, and reading the current time. It
// imports only the standard library and internal/domain (QS-5.1); every adapter that implements
// one of these interfaces lives elsewhere.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// ErrNoPermissionsBlock is what an AccessChecker reports when the upstream answered without
// saying what the visitor may do at all.
//
// It is a distinct error rather than a plain "no access" because the two call for different
// actions from an operator. A visitor GitHub says has only pull access is simply not a
// collaborator, and nothing is wrong. An answer carrying no permissions block at all means the
// token was not scoped to see them, and the fix is a scope on the OAuth App — which nobody will
// think of if the log says the visitor could not push (design 2026-09-14 §8). Either way the
// visitor is refused: the check fails closed in both cases.
//
// It lives here rather than in the adapter so that the web layer can recognise it without
// importing an adapter, which ADR-0001 does not allow.
var ErrNoPermissionsBlock = errors.New("the upstream answered without a permissions block")

// AccessChecker decides who may sign in (FR-8.3). token is the visitor's own OAuth access token;
// it is used for one request and never stored.
type AccessChecker interface {
	// HasPushAccess reports whether the visitor identified by token has push access to the
	// repository or organisation that gates sign-in. It reports false with a non-nil error when
	// it could not find out, including ErrNoPermissionsBlock: a caller must never read an error
	// as permission.
	HasPushAccess(ctx context.Context, token string) (bool, error)
}

// Clock reports the current time, so that callers needing time.Now can be tested with a fixed
// or fake clock instead.
type Clock interface{ Now() time.Time }

// SystemClock is the production Clock: it reports the real current time, in UTC.
type SystemClock struct{}

// Now returns the current time in UTC.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// Source is where the item list comes from. One call returns every open issue and pull request
// of every configured repository; a partial failure returns the items that could be fetched
// together with a non-nil error, never one without the other.
type Source interface {
	Fetch(ctx context.Context) ([]domain.Item, error)
}

// SourceFunc adapts a function to Source.
type SourceFunc func(ctx context.Context) ([]domain.Item, error)

func (f SourceFunc) Fetch(ctx context.Context) ([]domain.Item, error) { return f(ctx) }
