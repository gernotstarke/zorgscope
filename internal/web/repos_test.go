package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/ports"
)

func reposServer(t *testing.T, repos *stubRepos) http.Handler {
	t.Helper()
	return newTestServerWith(t, func(o *Options) {
		withMembers(o)
		o.Repos = repos
	}).Handler()
}

// FR-8.5: the organisation's public repositories as checkboxes, the chosen ones checked, a count
// line, the account named.
func TestReposListsTheOrganisationWithTheSelectionChecked(t *testing.T) {
	h := reposServer(t, &stubRepos{list: fourteenRepos()})
	body := getAs(h, "/repos", mintMemberSession(testMembersOrg+"/repo-03")).Body.String()
	if strings.Count(body, `type="checkbox" name="repo"`) != 14 {
		t.Fatalf("checkboxes: %s", body)
	}
	if !strings.Contains(body, `value="isaqb-org/repo-03" checked`) || strings.Contains(body, `value="isaqb-org/repo-04" checked`) {
		t.Fatalf("checked state: %s", body)
	}
	if !strings.Contains(body, "1 of 12 chosen") || !strings.Contains(body, "@octocat") || !strings.Contains(body, "Repository 03") {
		t.Fatalf("page: %s", body)
	}
}

// Saving keeps what is in the list, drops duplicates and strangers, orders as the list does, and
// re-mints the cookie with the same token and login.
func TestSavingReposReMintsTheCookie(t *testing.T) {
	h := reposServer(t, &stubRepos{list: fourteenRepos()})
	c := mintMemberSession()
	form := url.Values{"repo": {"isaqb-org/repo-09", "isaqb-org/repo-02", "isaqb-org/repo-02", "evil/other"}}
	rec := postAs(h, "/repos", form, c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("POST = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	sess, ok := newSessionCodec(testClientSecret).decode(cookieNamed(rec, sessionCookieName).Value, testNow)
	if !ok || sess.Token != testMemberToken || sess.Login != testMemberLogin || strings.Join(sess.Repos, ",") != "isaqb-org/repo-02,isaqb-org/repo-09" {
		t.Fatalf("session = %+v", sess)
	}
	if !sess.Expiry.Equal(testNow.Add(memberSessionTTL)) {
		t.Fatalf("saving changed the expiry: %v", sess.Expiry)
	}
}

func TestSavingThirteenReposIsRefused(t *testing.T) {
	h := reposServer(t, &stubRepos{list: fourteenRepos()})
	form := url.Values{}
	for i := 1; i <= 13; i++ {
		form.Add("repo", fourteenRepos()[i-1].FullName)
	}
	rec := postAs(h, "/repos", form, mintMemberSession(testMembersOrg+"/repo-01"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "at most 12") || cookieNamed(rec, sessionCookieName) != nil {
		t.Fatalf("POST = %d, cookie %v, body %s", rec.Code, cookieNamed(rec, sessionCookieName), firstLineContaining(rec.Body.String(), "at most"))
	}
}

func TestSavingNothingLeadsBackToRepos(t *testing.T) {
	h := reposServer(t, &stubRepos{list: fourteenRepos()})
	rec := postAs(h, "/repos", url.Values{}, mintMemberSession(testMembersOrg+"/repo-01"))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/repos?saved=1" {
		t.Fatalf("POST = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	sess, _ := newSessionCodec(testClientSecret).decode(cookieNamed(rec, sessionCookieName).Value, testNow)
	if len(sess.Repos) != 0 {
		t.Fatalf("repos = %v", sess.Repos)
	}
}

// The owner has no selection: the page and the post both lead to /.
func TestTheOwnerIsSentAwayFromRepos(t *testing.T) {
	h := reposServer(t, &stubRepos{list: fourteenRepos()})
	c := signIn(t, h)
	if rec := getAs(h, "/repos", c); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("GET = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := postAs(h, "/repos", url.Values{"repo": {"isaqb-org/repo-01"}}, c); rec.Code != http.StatusSeeOther || cookieNamed(rec, sessionCookieName) != nil {
		t.Fatalf("POST = %d, cookie %v", rec.Code, cookieNamed(rec, sessionCookieName))
	}
}

// Spec §7: the list is fetched once an hour, shared.
func TestTheOrganisationListIsCachedForAnHour(t *testing.T) {
	repos := &stubRepos{list: fourteenRepos()}
	clock := &ports.FixedClock{T: testNow}
	h := newTestServerWith(t, func(o *Options) {
		withMembers(o)
		o.Repos = repos
		o.Clock = clock
	}).Handler()
	getAs(h, "/repos", mintMemberSession())
	getAs(h, "/repos", mintMemberSession())
	if repos.calls != 1 {
		t.Fatalf("calls = %d after two views", repos.calls)
	}
	clock.T = clock.T.Add(orgReposTTL + time.Second)
	getAs(h, "/repos", mintMemberSession())
	if repos.calls != 2 {
		t.Fatalf("calls = %d after the TTL", repos.calls)
	}
}

// Review focus 3: a failing list still shows the current selection, and saves nothing.
func TestAFailingListShowsTheSelectionAndSavesNothing(t *testing.T) {
	h := reposServer(t, &stubRepos{err: errors.New("GitHub answered 502 for " + testMemberToken)})
	c := mintMemberSession(testMembersOrg + "/repo-05")
	body := getAs(h, "/repos", c).Body.String()
	if !strings.Contains(body, `value="isaqb-org/repo-05" checked`) || !strings.Contains(body, "502") || strings.Contains(body, testMemberToken) {
		t.Fatalf("page = %s", body)
	}
	rec := postAs(h, "/repos", url.Values{"repo": {"isaqb-org/repo-05"}}, c)
	if rec.Code != http.StatusOK || cookieNamed(rec, sessionCookieName) != nil {
		t.Fatalf("POST = %d, cookie %v", rec.Code, cookieNamed(rec, sessionCookieName))
	}
}

func TestNewRequiresARepoListerWhenMembersAreEnabled(t *testing.T) {
	o := testOptions()
	withMembers(&o)
	o.Repos = nil
	if _, err := New(o); err == nil {
		t.Fatal("New() accepted members without a repo lister")
	}
}
