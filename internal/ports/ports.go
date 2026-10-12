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

// AccessChecker decides who may sign in (FR-8.3, FR-8.4). token is the visitor's own OAuth access
// token; repo is the repository whose permissions block answers the question.
type AccessChecker interface {
	// HasPushAccess reports whether the visitor identified by token has push access to repo. It
	// reports false with a non-nil error when it could not find out, including
	// ErrNoPermissionsBlock: a caller must never read an error as permission.
	HasPushAccess(ctx context.Context, token, repo string) (bool, error)
}

// IdentityReader names the visitor a token belongs to (FR-8.4): the login the session carries.
type IdentityReader interface {
	Login(ctx context.Context, token string) (string, error)
}

// TokenRevoker ends a member's token at GitHub when they sign out (FR-8.4, spec 2026-10-12 §9).
type TokenRevoker interface {
	Revoke(ctx context.Context, token string) error
}

// Repo is one repository a member may choose (FR-8.5).
type Repo struct {
	FullName, Description string
}

// RepoLister lists the public, non-archived repositories of an organisation, fetched with the
// member's own token (FR-8.5).
type RepoLister interface {
	PublicRepos(ctx context.Context, token, org string) ([]Repo, error)
}

// Clock reports the current time, so that callers needing time.Now can be tested with a fixed
// or fake clock instead.
type Clock interface{ Now() time.Time }

// SystemClock is the production Clock: it reports the real current time, in UTC.
type SystemClock struct{}

// Now returns the current time in UTC.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// Source is where the item list comes from. One call returns every open issue, pull request and
// Dependabot alert of every configured repository, and per repository what could be said about its
// alerts (FR-1.16); a partial failure returns what could be fetched together with a non-nil error,
// never one without the other.
type Source interface {
	Fetch(ctx context.Context) (domain.Fetched, error)
}

// SourceFunc adapts a function to Source.
type SourceFunc func(ctx context.Context) (domain.Fetched, error)

// Fetch calls f.
func (f SourceFunc) Fetch(ctx context.Context) (domain.Fetched, error) { return f(ctx) }
