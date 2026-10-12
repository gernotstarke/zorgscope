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

// Spec §8: one cache per login, token and selection, reused while all three are unchanged; two
// members never share one.
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
}

// One member signed in on two devices holds two tokens. Each device keeps its own cache: were
// the one replacing the other, both polling the wait page would throw away each other's fetch
// on every poll and start another, and neither fetch would ever land in a cache still in use.
func TestOneMemberOnTwoDevicesKeepsBothCaches(t *testing.T) {
	sources := &stubMemberSources{}
	m := testMemberCaches(sources)
	phone, laptop := memberSess("a", "o/x"), memberSess("a", "o/x")
	laptop.Token = "tok-a-laptop"
	first := map[string]any{}
	for range 10 {
		for _, sess := range []session{phone, laptop} {
			c := m.cacheFor(sess)
			if prev, ok := first[sess.Token]; ok && prev != any(c) {
				t.Fatalf("the cache of token %s was replaced", sess.Token)
			}
			first[sess.Token] = c
		}
	}
	if sources.built() != 2 {
		t.Fatalf("built %d sources for two devices, want 2", sources.built())
	}
	if m.cacheFor(phone) == m.cacheFor(laptop) {
		t.Fatal("two tokens share a cache")
	}
	changed := phone
	changed.Repos = []string{"o/x", "o/y"}
	if c := m.cacheFor(changed); c == first[phone.Token] || sources.built() != 3 {
		t.Fatalf("a changed selection: built %d, want 3 and a cache of its own", sources.built())
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
