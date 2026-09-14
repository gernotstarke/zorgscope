package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/adapters/github"
	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

// The fixture server answers the same permissions block GitHub does, so this exercises every level
// the rule has to recognise — including "maintain implies push" and a missing block (design
// 2026-09-14 §8), which is the case the check has to fail closed on.
func TestHasPushAccessReadsThePermissionsBlock(t *testing.T) {
	fake := httptest.NewServer(fakesources.NewServer())
	defer fake.Close()
	a := github.NewAccessChecker(fake.URL, "gernotstarke/zorgscope", fake.Client())
	for perm, want := range map[string]bool{"admin": true, "maintain": true, "push": true, "pull": false, "none": false, "absent": false} {
		control(t, fake.URL+"/_control/oauth-user?permission="+perm)
		got, err := a.HasPushAccess(context.Background(), "fake-token")
		if err != nil {
			t.Fatalf("%s: %v", perm, err)
		}
		if got != want {
			t.Errorf("%s: HasPushAccess = %v, want %v", perm, got, want)
		}
	}
}

// QS-4.3: the visitor's token is a credential like any other, so no error may quote it — and a
// refused request is an error *and* no access, never a silent "true".
func TestHasPushAccessFailsClosedOnAnErrorAndNeverQuotesTheToken(t *testing.T) {
	fake := httptest.NewServer(fakesources.NewServer())
	defer fake.Close()
	a := github.NewAccessChecker(fake.URL, "gernotstarke/zorgscope", fake.Client())
	got, err := a.HasPushAccess(context.Background(), "wrong-token-canary")
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

	a := github.NewAccessChecker(url, "gernotstarke/zorgscope", client)
	got, err := a.HasPushAccess(context.Background(), "fake-token")
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
