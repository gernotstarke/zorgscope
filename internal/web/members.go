package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
)

// maxMemberCaches bounds how many members' caches are held at once. The allowlist is a handful of
// people; this is a backstop against the map growing without limit, like the sign-in limiter's
// maxTrackedClients. Beyond it the least recently used entry is dropped — that member's next page
// view pays one fetch, the same thing a Machine waking from zero costs everyone.
const maxMemberCaches = 64

// memberCaches holds one snapshot.Cache per signed-in member (spec 2026-10-12 §8), keyed by
// login and fingerprint. The cache package is not changed: each member gets the owner's semantics — fetch on a
// stale page view, the wait page, partial failures kept — over their own repositories, fetched
// with their own token.
type memberCaches struct {
	mu        sync.Mutex
	entries   map[string]*memberEntry
	newSource func(token string, repos []string) ports.Source
	ttl       time.Duration
	clock     ports.Clock
	log       *slog.Logger
}

// memberEntry is one cache, for one login, token and selection. A session with another token or
// selection gets an entry of its own rather than replacing this one: one member signed in on two
// devices holds two tokens, and were each device's request to replace the other's cache, two pages
// polling the wait page would throw away each other's fetch on every poll, and neither would ever
// land. An entry no session asks for any more is simply the least recently used, and goes.
type memberEntry struct {
	cache    *snapshot.Cache
	lastUsed time.Time
}

func newMemberCaches(newSource func(token string, repos []string) ports.Source, ttl time.Duration, clock ports.Clock, log *slog.Logger) *memberCaches {
	return &memberCaches{entries: make(map[string]*memberEntry), newSource: newSource, ttl: ttl, clock: clock, log: log}
}

// cacheFor returns the cache for sess, building it when there is none yet for its login, token and
// selection. The login is part of the key although a token already names one person: two logins
// can then never meet in one cache, whatever a token turns out to be.
func (m *memberCaches) cacheFor(sess session) *snapshot.Cache {
	key := sess.Login + "\x00" + memberFingerprint(sess.Token, sess.Repos)
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now()
	if e, ok := m.entries[key]; ok {
		e.lastUsed = now
		return e.cache
	}
	m.evictLocked()
	src := memberSource(m.newSource(sess.Token, sess.Repos), sess.Token, sess.Login, len(sess.Repos), m.log)
	e := &memberEntry{cache: snapshot.New(src, m.ttl, m.clock), lastUsed: now}
	m.entries[key] = e
	return e.cache
}

// evictLocked drops the least recently used entries until there is room for one more. The caller
// holds the mutex.
func (m *memberCaches) evictLocked() {
	for len(m.entries) >= maxMemberCaches {
		oldestKey, oldest := "", time.Time{}
		for key, e := range m.entries {
			if oldest.IsZero() || e.lastUsed.Before(oldest) {
				oldestKey, oldest = key, e.lastUsed
			}
		}
		delete(m.entries, oldestKey)
	}
}

// memberFingerprint identifies a (token, selection) pair without keeping either in the clear.
func memberFingerprint(token string, repos []string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(token))
	for _, repo := range repos {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(repo))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// memberSource wraps a member's source for the two things every consumer of its result needs
// (spec §8, §10): a log line per fetch with the login, and no token in any error. The error text
// reaches both the log and the page's error notice, so it is scrubbed here, once, before either
// can see it (QS-4.3). The scrubbed error is a new value: nothing downstream matches on the
// original's type.
func memberSource(src ports.Source, token, login string, repos int, log *slog.Logger) ports.Source {
	return ports.SourceFunc(func(ctx context.Context) (domain.Fetched, error) {
		log.Info("member fetch", "login", login, "repos", repos)
		got, err := src.Fetch(ctx)
		if err != nil {
			err = scrubbedError(err, token)
			log.Warn("member fetch failed", "login", login, "repos", repos, "items", len(got.Items), "err", err.Error())
		}
		return got, err
	})
}

// scrubbedError is err's text with token replaced by "[token]", as a new error value. It is the
// one way a member's token leaves any error in this package (QS-4.3): fetch errors here, the
// repository list's and the revocation's in repos.go and dashboard.go.
func scrubbedError(err error, token string) error {
	text := err.Error()
	if token != "" {
		text = strings.ReplaceAll(text, token, "[token]")
	}
	return errors.New(text)
}
