package web

import (
	"crypto/sha256"
	"encoding/hex"
)

// How a rotated client secret reaches a list sitting in a browser's localStorage.
//
// requireSession sets Cache-Control: no-store on every session route precisely so that rotating
// GITHUB_OAUTH_CLIENT_SECRET is a real revocation — a page the browser stored cannot outlive the
// credential it was served against (FR-8.3 AC4). A copy the page itself put in localStorage would
// escape that entirely: no header reaches it, and the server has no way to clear it. The epoch is
// the answer. It is stamped on the document, the browser stores it beside the list, and the list
// is restored only when the two agree. Rotate the secret and every stored list, in every browser,
// becomes unreadable at once — without a session table, a revocation list, or anything else that
// would have to survive the Machine being stopped (ADR-0013).
//
// cacheEpochContext domain-separates this value from the session signing key, which is derived
// from the same secret. The epoch is published in the page's markup; the signing key must never be
// derivable from it, and a distinct context string is what guarantees one hash says nothing about
// the other. It says v1 because no epoch has been minted under another.
const cacheEpochContext = "zorgscope-cache-epoch-v1"

// cacheEpochLen is how many bytes of the hash become the epoch. Four — eight hex characters — is a
// version tag rather than a secret: it only has to differ between secrets, which 32 bits does with
// a margin nothing here will ever test.
const cacheEpochLen = 4

// newCacheEpoch returns the epoch for secret: a short, stable, one-way tag that changes when and
// only when the secret does.
func newCacheEpoch(secret string) string {
	sum := sha256.Sum256([]byte(cacheEpochContext + secret))
	return hex.EncodeToString(sum[:cacheEpochLen])
}
