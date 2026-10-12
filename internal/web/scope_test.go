package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
)

// Spec 2026-10-12 §5: a member's scope is their selection, one generated site per repository in
// the twelve hues, the member as owner, no alerts, no group, no tag.
func TestMemberGitHubGeneratesOneSitePerRepository(t *testing.T) {
	base := config.GitHub{Repos: []string{"arc42/x"}, Owner: "zorg", CacheTTL: time.Minute, AuthRepo: testAuthRepo}
	sess := session{Role: roleMember, Login: "octocat", Token: "t", Repos: []string{"isaqb-org/glossary", "isaqb-org/curriculum-foundation"}}
	gh := memberGitHub(base, sess)
	if gh.Owner != "octocat" || strings.Join(gh.Repos, ",") != "isaqb-org/glossary,isaqb-org/curriculum-foundation" {
		t.Fatalf("gh = %+v", gh)
	}
	if len(gh.Sites) != 2 {
		t.Fatalf("sites = %+v", gh.Sites)
	}
	want := []config.Site{
		{Name: "glossary", URL: "https://github.com/isaqb-org/glossary", Repos: []string{"isaqb-org/glossary"}, Hue: config.MemberHues[0], Group: memberGroup, NoAlerts: true},
		{Name: "curriculum-foundation", URL: "https://github.com/isaqb-org/curriculum-foundation", Repos: []string{"isaqb-org/curriculum-foundation"}, Hue: config.MemberHues[1], Group: memberGroup, NoAlerts: true},
	}
	for i := range want {
		if !reflect.DeepEqual(gh.Sites[i], want[i]) {
			t.Errorf("site %d = %+v, want %+v", i, gh.Sites[i], want[i])
		}
	}
	if g := gh.Groups(); len(g) != 1 || g[0] != memberGroup || gh.CacheTTL != time.Minute || gh.AuthRepo != testAuthRepo {
		t.Fatalf("groups %v, ttl %v, auth %q", gh.Groups(), gh.CacheTTL, gh.AuthRepo)
	}
	// ADR-0018's fixed radars name arc42, iSAQB, systems42, aim42 and private — never the member
	// family — so no radar is offered and every site is a sector, with no switch.
	if offered := offeredRadars(gh); len(offered) != 0 {
		t.Fatalf("a member's sites are offered radars %v", offered)
	}
	if len(gh.ReposWithoutAlerts()) != 2 {
		t.Fatal("a member's repositories must all be alerts-off")
	}
}

func memberServer(t *testing.T, sources *stubMemberSources) http.Handler {
	t.Helper()
	return newTestServerWith(t, func(o *Options) {
		withMembers(o)
		o.MemberSource = sources.source
	}).Handler()
}

// FR-1.17: the member's list, tiles and radar show their repositories and nothing of the owner's;
// no Security tile, no group switch, no Other.
func TestAMemberSeesTheirRepositoriesOnListTilesAndRadar(t *testing.T) {
	sources := &stubMemberSources{items: map[string][]domain.Item{
		"isaqb-org/glossary":              {siteItem("isaqb-org/glossary", domain.KindIssue, 1, testNow)},
		"isaqb-org/curriculum-foundation": {siteItem("isaqb-org/curriculum-foundation", domain.KindPR, 2, testNow)},
	}}
	h := memberServer(t, sources)
	c := mintMemberSession("isaqb-org/glossary", "isaqb-org/curriculum-foundation")

	list := getSettled(t, h, "/", c).Body.String()
	if !strings.Contains(list, "isaqb-org/glossary") || strings.Contains(list, "org/repo") {
		t.Fatalf("list = %s", list)
	}
	sites := getSettled(t, h, "/sites", c).Body.String()
	if strings.Contains(sites, "tile-security-title") || strings.Contains(sites, otherTileName) {
		t.Fatalf("a member's tiles carry Security or Other: %s", sites)
	}
	first, second := tileSection(t, sites, 1), tileSection(t, sites, 2)
	if !strings.Contains(first, ">glossary<") || !strings.Contains(sites, `<section class="tile hue-`+config.MemberHues[0]+`" aria-labelledby="tile-1-title"`) {
		t.Fatalf("tile 1 = %s", first)
	}
	if !strings.Contains(second, ">curriculum-foundation<") || !strings.Contains(sites, `<section class="tile hue-`+config.MemberHues[1]+`" aria-labelledby="tile-2-title"`) {
		t.Fatalf("tile 2 = %s", second)
	}
	radar := getSettled(t, h, "/radar", c).Body.String()
	if strings.Contains(radar, "radar-groups") || strings.Contains(radar, otherTileName) || !strings.Contains(radar, "glossary") {
		t.Fatalf("radar = %s", radar)
	}
	if rec := getSettled(t, h, "/contributors", c); rec.Code != http.StatusOK {
		t.Fatalf("contributors = %d", rec.Code)
	}
	if sources.built() != 1 {
		t.Fatalf("built %d sources for one member, want 1", sources.built())
	}
}

// The owner's pages are untouched by members existing: the configuration, the one cache, the
// Security tile.
func TestTheOwnerStillRendersTheConfiguration(t *testing.T) {
	h := memberServer(t, &stubMemberSources{})
	// testOptions watches org/repo with no sites, so it lands on the Other tile, whose markup does
	// not name the repository; the Other tile itself is the marker of the configuration.
	sites := getAuthed(t, h, "/sites").Body.String()
	if !strings.Contains(sites, "tile-security-title") || !strings.Contains(sites, otherTileName) {
		t.Fatalf("owner tiles = %s", sites)
	}
}

// FR-1.14 for a member: "needs you" means them. A pull request asking octocat for a review is on
// the band for octocat.
func TestTheNeedsYouBandMeansTheMember(t *testing.T) {
	pr := siteItem("isaqb-org/glossary", domain.KindPR, 3, testNow)
	pr.ReviewRequested = []string{testMemberLogin}
	h := memberServer(t, &stubMemberSources{items: map[string][]domain.Item{"isaqb-org/glossary": {pr}}})
	body := getSettled(t, h, "/", mintMemberSession("isaqb-org/glossary")).Body.String()
	if !strings.Contains(body, `<span class="needs-count">1</span>`) {
		t.Fatalf("the band does not count the member's review: %s", firstLineContaining(body, "needs-title"))
	}
}

// Spec §5: a member who has chosen nothing is sent to /repos from every page, 204 + HX-Redirect
// from the fragment, and from Refresh — and nothing is fetched for them.
func TestAMemberWithNothingChosenIsSentToRepos(t *testing.T) {
	sources := &stubMemberSources{}
	h := memberServer(t, sources)
	c := mintMemberSession()
	for _, path := range []string{"/", "/sites", "/radar", "/contributors", "/search?q=x"} {
		rec := getAs(h, path, c)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/repos" {
			t.Errorf("GET %s = %d %s", path, rec.Code, rec.Header().Get("Location"))
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	req.Header.Set("HX-Request", "true")
	req.AddCookie(c)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("HX-Redirect") != "/repos" {
		t.Errorf("GET /items = %d %s", rec.Code, rec.Header().Get("HX-Redirect"))
	}
	if rec := postAs(h, "/refresh", url.Values{"return": {"/"}}, c); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/repos" {
		t.Errorf("POST /refresh = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if sources.built() != 0 {
		t.Fatalf("built %d sources for a member with no selection", sources.built())
	}
}

// The settings panel offers Repositories to a member and not to the owner.
func TestTheSettingsPanelOffersRepositoriesToAMember(t *testing.T) {
	h := memberServer(t, &stubMemberSources{})
	if body := getSettled(t, h, "/", mintMemberSession("isaqb-org/glossary")).Body.String(); !strings.Contains(body, `href="/repos"`) {
		t.Fatal("no Repositories link for a member")
	}
	if body := getAuthed(t, h, "/").Body.String(); strings.Contains(body, `href="/repos"`) {
		t.Fatal("a Repositories link for the owner")
	}
}

// Spec §10: every page view of a signed-in person is one Info line with login, role and path;
// the fragment poll is not.
func TestAPageViewIsLoggedWithLoginAndRole(t *testing.T) {
	var logged bytes.Buffer
	h := newTestServerWith(t, func(o *Options) {
		withMembers(o)
		o.Log = slog.New(slog.NewTextHandler(&logged, nil))
	}).Handler()
	getSettled(t, h, "/sites", mintMemberSession("isaqb-org/glossary"))
	line := firstLineContaining(logged.String(), "page view")
	if !strings.Contains(line, "login=octocat") || !strings.Contains(line, "role=member") || !strings.Contains(line, "path=/sites") {
		t.Fatalf("log = %s", logged.String())
	}
	logged.Reset()
	getAs(h, "/items", mintMemberSession("isaqb-org/glossary"))
	if strings.Contains(logged.String(), "page view") {
		t.Fatal("the fragment was logged as a page view")
	}
}

func TestNewRequiresAMemberSourceWhenMembersAreEnabled(t *testing.T) {
	o := testOptions()
	withMembers(&o)
	o.MemberSource = nil
	if _, err := New(o); err == nil {
		t.Fatal("New() accepted members without a MemberSource")
	}
}

// scopeOf on a request requireSession never admitted sees the zero session, which is nobody's —
// not the owner's. It refuses rather than hand out the configuration and the owner's cache, so a
// handler wired to the wrong auth kind fails closed.
func TestScopeOfRefusesARequestWithoutASession(t *testing.T) {
	s := newTestServer(t)
	for _, htmx := range []bool{false, true} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/items", nil)
		if htmx {
			req.Header.Set("HX-Request", "true")
		}
		if _, ok := s.scopeOf(rec, req); ok {
			t.Fatalf("htmx=%v: scopeOf admitted a request with no session", htmx)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("htmx=%v: status = %d, want 401", htmx, rec.Code)
		}
		if htmx && rec.Header().Get("HX-Redirect") != "/login" {
			t.Fatalf("htmx: HX-Redirect = %q, want /login", rec.Header().Get("HX-Redirect"))
		}
	}
}
