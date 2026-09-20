package web

import (
	"strings"
	"testing"
)

// The epoch is what makes rotating the OAuth client secret reach a copy of the list sitting in a
// browser's localStorage. If it did not change with the secret, a rotation — this product's only
// sign-out beyond the Log out button (FR-8.3 AC4) — would leave every stored list readable for
// ever, which is precisely the state the rotation was performed to end.
func TestTheEpochChangesWithTheSecret(t *testing.T) {
	if newCacheEpoch("one") == newCacheEpoch("two") {
		t.Error("two secrets produced the same epoch; a rotation would not invalidate a stored list")
	}
	if newCacheEpoch("one") != newCacheEpoch("one") {
		t.Error("the epoch is not stable for one secret; every page view would discard the cache")
	}
}

// The epoch is published in the page's markup, and the session signing key is derived from the
// same secret. A reader of the epoch must learn nothing about that key, which is what the separate
// context string buys: two hashes of the same secret under different domains.
func TestTheEpochSaysNothingAboutTheSigningKey(t *testing.T) {
	const secret = "the-client-secret"
	epoch := newCacheEpoch(secret)
	key := newSessionCodec(secret).key

	if strings.Contains(string(key[:]), epoch) {
		t.Error("the epoch appears inside the session signing key")
	}
	// The epoch must not be reachable by hashing the session key's own input: it is a different
	// hash under a different context, not a truncation of the same one.
	if epoch == newCacheEpoch(sessionKeyContext+secret) {
		t.Error("the epoch is derivable by hashing the session key's own input")
	}
}

// Eight hex characters, so the attribute is short and a mismatch is decidable by string equality
// in the browser. 32 bits is not a secret and does not need to be: it is a version tag.
func TestTheEpochIsEightHexCharacters(t *testing.T) {
	e := newCacheEpoch("whatever")
	if len(e) != 8 {
		t.Errorf("epoch = %q, want 8 characters", e)
	}
	if strings.Trim(e, "0123456789abcdef") != "" {
		t.Errorf("epoch = %q, want lowercase hex only", e)
	}
}
