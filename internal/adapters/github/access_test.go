package github_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/adapters/github"
	"github.com/gernotstarke/zorgscope/internal/fakesources"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// The fixture server answers the same permissions block GitHub does, so this exercises every level
// the rule has to recognise — including "maintain implies push" and a missing block (design
// 2026-09-14 §8), which is the case the check has to fail closed on.
func TestHasPushAccessReadsThePermissionsBlock(t *testing.T) {
	fake := httptest.NewServer(fakesources.NewServer())
	defer fake.Close()
	a := github.NewAccess(fake.URL, "client-id", "client-secret", fake.Client())
	// "absent" is refused like "pull", but it is refused for a different reason and says so: only
	// a missing block is fixed by asking the OAuth App for a scope (design 2026-09-14 §8).
	for perm, want := range map[string]struct {
		access  bool
		wantErr error
	}{
		"admin":    {access: true},
		"maintain": {access: true},
		"push":     {access: true},
		"pull":     {},
		"none":     {},
		"absent":   {wantErr: ports.ErrNoPermissionsBlock},
	} {
		control(t, fake.URL+"/_control/oauth-user?permission="+perm)
		got, err := a.HasPushAccess(context.Background(), "fake-token", "gernotstarke/zorgscope")
		if !errors.Is(err, want.wantErr) {
			t.Fatalf("%s: err = %v, want %v", perm, err, want.wantErr)
		}
		if got != want.access {
			t.Errorf("%s: HasPushAccess = %v, want %v", perm, got, want.access)
		}
	}
}

// QS-4.3: the visitor's token is a credential like any other, so no error may quote it — and a
// refused request is an error *and* no access, never a silent "true".
func TestHasPushAccessFailsClosedOnAnErrorAndNeverQuotesTheToken(t *testing.T) {
	fake := httptest.NewServer(fakesources.NewServer())
	defer fake.Close()
	a := github.NewAccess(fake.URL, "client-id", "client-secret", fake.Client())
	got, err := a.HasPushAccess(context.Background(), "wrong-token-canary", "gernotstarke/zorgscope")
	if got || err == nil {
		t.Fatalf("got %v, err %v; a 401 must be an error and no access", got, err)
	}
	if strings.Contains(err.Error(), "wrong-token-canary") {
		t.Fatal("the error quotes the token")
	}
}

// An upstream that cannot be reached at all must refuse rather than admit, and must say so without
// the token: a transport error carries the request URL, and the check is the security boundary.
func TestHasPushAccessFailsClosedWhenGitHubCannotBeReached(t *testing.T) {
	fake := httptest.NewServer(fakesources.NewServer())
	url := fake.URL
	client := fake.Client()
	fake.Close() // nothing is listening there any more

	a := github.NewAccess(url, "client-id", "client-secret", client)
	got, err := a.HasPushAccess(context.Background(), "fake-token", "gernotstarke/zorgscope")
	if got || err == nil {
		t.Fatalf("got %v, err %v; an unreachable GitHub must be an error and no access", got, err)
	}
	if strings.Contains(err.Error(), "fake-token") {
		t.Fatal("the error quotes the token")
	}
}

// control drives one of the fake's /_control routes and fails the test if it will not take it.
func control(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Post(url, "", nil) //nolint:noctx // a test helper against a local fixture server
	if err != nil {
		t.Fatalf("control %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("control %s: status %d", url, resp.StatusCode)
	}
}

// FR-8.4: the same adapter answers for any repository it is asked about — the owner's and the
// members' — so the repository is a parameter, not a field.
func TestHasPushAccessAsksTheRepositoryItIsGiven(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"permissions":{"admin":false,"push":true}}`))
	}))
	defer srv.Close()
	a := github.NewAccess(srv.URL, "id", "secret", srv.Client())
	if ok, err := a.HasPushAccess(context.Background(), "t", "gernotstarke/zorgscope-members"); err != nil || !ok {
		t.Fatalf("HasPushAccess = %v, %v", ok, err)
	}
	if len(asked) != 1 || asked[0] != "/repos/gernotstarke/zorgscope-members" {
		t.Fatalf("asked %v", asked)
	}
}

func TestLoginReadsTheAuthenticatedUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer t" {
			http.Error(w, "wrong", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"octocat","id":1}`))
	}))
	defer srv.Close()
	a := github.NewAccess(srv.URL, "id", "secret", srv.Client())
	login, err := a.Login(context.Background(), "t")
	if err != nil || login != "octocat" {
		t.Fatalf("Login = %q, %v", login, err)
	}
	if _, err := a.Login(context.Background(), "wrong-token"); err == nil || strings.Contains(err.Error(), "wrong-token") {
		t.Fatalf("a refused login must fail without quoting the token: %v", err)
	}
}

// Spec §9: revocation is DELETE /applications/{client_id}/token with the App's credentials as
// basic auth and the token in the body. 204 is success; anything else is an error that does not
// quote the token.
func TestRevokeDeletesTheToken(t *testing.T) {
	var gotUser, gotPass, gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		gotPath = r.Method + " " + r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	a := github.NewAccess(srv.URL, "the-id", "the-secret", srv.Client())
	if err := a.Revoke(context.Background(), "gho_abc"); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if gotPath != "DELETE /applications/the-id/token" || gotUser != "the-id" || gotPass != "the-secret" {
		t.Fatalf("request = %s as %s:%s", gotPath, gotUser, gotPass)
	}
	if !strings.Contains(gotBody, `"access_token":"gho_abc"`) {
		t.Fatalf("body = %s", gotBody)
	}
}

func TestRevokeFailsWithoutQuotingTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope gho_abc", http.StatusUnprocessableEntity)
	}))
	defer srv.Close()
	a := github.NewAccess(srv.URL, "id", "secret", srv.Client())
	err := a.Revoke(context.Background(), "gho_abc")
	if err == nil || strings.Contains(err.Error(), "gho_abc") || !strings.Contains(err.Error(), "422") {
		t.Fatalf("err = %v", err)
	}
}

// FR-8.5: the organisation's public repositories, every page followed, archived ones dropped.
func TestPublicReposFollowsPaginationAndDropsArchived(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orgs/isaqb-org/repos" || r.URL.Query().Get("type") != "public" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "", "1":
			w.Header().Set("Link", `<`+srv.URL+`/orgs/isaqb-org/repos?type=public&per_page=100&page=2>; rel="next"`)
			_, _ = w.Write([]byte(`[{"full_name":"isaqb-org/a","description":"A","archived":false},{"full_name":"isaqb-org/old","description":"","archived":true}]`))
		case "2":
			_, _ = w.Write([]byte(`[{"full_name":"isaqb-org/b","description":null,"archived":false}]`))
		}
	}))
	defer srv.Close()
	a := github.NewAccess(srv.URL, "id", "secret", srv.Client())
	got, err := a.PublicRepos(context.Background(), "t", "isaqb-org")
	if err != nil {
		t.Fatalf("PublicRepos() error = %v", err)
	}
	want := []ports.Repo{{FullName: "isaqb-org/a", Description: "A"}, {FullName: "isaqb-org/b"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}
