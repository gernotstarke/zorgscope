package web

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

func testMemberCaches(sources *stubMemberSources) *memberCaches {
	return newMemberCaches(sources.source, time.Hour, &ports.FixedClock{T: testNow}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
}

func memberSess(login string, repos ...string) session {
	return session{Role: roleMember, Login: login, Token: "tok-" + login, Repos: repos}
}

// Spec §8: one cache per login, reused while token and selection are unchanged, replaced when
// either changes; two members never share one.
func TestMemberCachesAreKeyedByLoginAndFingerprint(t *testing.T) {
	sources := &stubMemberSources{}
	m := testMemberCaches(sources)
	a := m.cacheFor(memberSess("a", "o/x"))
	if m.cacheFor(memberSess("a", "o/x")) != a || sources.built() != 1 {
		t.Fatal("the same session did not reuse its cache")
	}
	if m.cacheFor(memberSess("b", "o/x")) == a || sources.built() != 2 {
		t.Fatal("two members share a cache")
	}
	if m.cacheFor(memberSess("a", "o/x", "o/y")) == a || sources.built() != 3 {
		t.Fatal("a changed selection kept the old cache")
	}
	changedToken := memberSess("a", "o/x", "o/y")
	changedToken.Token = "tok-a-new"
	if m.cacheFor(changedToken) == a || sources.built() != 4 {
		t.Fatal("a new token kept the old cache")
	}
}

func TestMemberCachesEvictTheLeastRecentlyUsed(t *testing.T) {
	sources := &stubMemberSources{}
	clock := &ports.FixedClock{T: testNow}
	m := newMemberCaches(sources.source, time.Hour, clock, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	first := m.cacheFor(memberSess("m0", "o/x"))
	for i := 1; i < maxMemberCaches; i++ {
		clock.T = clock.T.Add(time.Second)
		m.cacheFor(memberSess("m"+strconv.Itoa(i), "o/x"))
	}
	clock.T = clock.T.Add(time.Second)
	m.cacheFor(memberSess("m0", "o/x")) // touch m0, so m1 is now the oldest
	clock.T = clock.T.Add(time.Second)
	m.cacheFor(memberSess("newcomer", "o/x"))
	if m.cacheFor(memberSess("m0", "o/x")) != first {
		t.Fatal("the recently used entry was evicted")
	}
	before := sources.built()
	m.cacheFor(memberSess("m1", "o/x"))
	if sources.built() != before+1 {
		t.Fatal("the least recently used entry survived")
	}
}

// QS-4.3: an error quoting the token comes out of the wrapper without it, and the log line that
// reports the failure carries the login, not the token.
func TestMemberSourceScrubsTheTokenAndLogsTheLogin(t *testing.T) {
	var logged bytes.Buffer
	src := ports.SourceFunc(func(context.Context) (domain.Fetched, error) {
		return domain.Fetched{}, errors.New("bad credentials: gho_secret rejected")
	})
	wrapped := memberSource(src, "gho_secret", "octocat", 2, slog.New(slog.NewTextHandler(&logged, nil)))
	_, err := wrapped.Fetch(context.Background())
	if err == nil || strings.Contains(err.Error(), "gho_secret") || !strings.Contains(err.Error(), "[token]") {
		t.Fatalf("err = %v", err)
	}
	out := logged.String()
	if strings.Contains(out, "gho_secret") || !strings.Contains(out, "member fetch failed") || !strings.Contains(out, "login=octocat") || !strings.Contains(out, "repos=2") {
		t.Fatalf("log = %s", out)
	}
}

// The page's error notice is the other consumer of a fetch error; it must be scrubbed too, by
// construction, because it reads the same error.
func TestAMemberFetchErrorReachesThePageScrubbed(t *testing.T) {
	sources := &stubMemberSources{err: errors.New("github rejected " + testMemberToken)}
	h := memberServer(t, sources)
	body := getSettled(t, h, "/", mintMemberSession("isaqb-org/glossary")).Body.String()
	if strings.Contains(body, testMemberToken) || !strings.Contains(body, "github rejected [token]") {
		t.Fatalf("page = %s", firstLineContaining(body, "github rejected"))
	}
}
