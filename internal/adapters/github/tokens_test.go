package github_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/adapters/github"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

// ownerFake serves the fake GitHub's org/* fixtures under any owner, and records which bearer token
// each owner's requests carried. The fake knows only org/*, so the owner is rewritten to org before
// the fake sees the request; the adapter still builds its items from the owner it asked about.
type ownerFake struct {
	mu   sync.Mutex
	seen map[string]map[string]bool // owner -> Authorization headers
	srv  *httptest.Server
}

func newOwnerFake(t *testing.T) *ownerFake {
	t.Helper()
	o := &ownerFake{seen: map[string]map[string]bool{}}
	fake := fakesources.NewServer()
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var owner string
		switch {
		case r.URL.Path == "/graphql":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Variables map[string]any `json:"variables"`
			}
			_ = json.Unmarshal(body, &req)
			owner, _ = req.Variables["owner"].(string)
			body = bytes.Replace(body, []byte(`"owner":"`+owner+`"`), []byte(`"owner":"org"`), 1)
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
		case strings.HasPrefix(r.URL.Path, "/repos/"):
			owner = strings.Split(r.URL.Path, "/")[2]
			r.URL.Path = strings.Replace(r.URL.Path, "/repos/"+owner+"/", "/repos/org/", 1)
		}
		o.mu.Lock()
		if o.seen[owner] == nil {
			o.seen[owner] = map[string]bool{}
		}
		o.seen[owner][r.Header.Get("Authorization")] = true
		o.mu.Unlock()
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(o.srv.Close)
	return o
}

func (o *ownerFake) fetcher(ownerTokens map[string]string, repos ...string) *github.IssueFetcher {
	return github.NewIssueFetcher(github.Config{
		Token: "x", OwnerTokens: ownerTokens, BaseURL: o.srv.URL + "/graphql", RESTBaseURL: o.srv.URL, Repos: repos,
	}, o.srv.Client())
}

// A repository whose owner has a token of its own is read with that token, GraphQL and the REST
// alert probe alike; every other repository with Config.Token. Owners compare as GitHub compares
// logins, ignoring case.
func TestEachOwnerIsReadWithItsOwnToken(t *testing.T) {
	o := newOwnerFake(t)
	f := o.fetcher(map[string]string{"Other-Org": "y"}, "org/alerts-clean", "other-org/alerts-clean")

	got, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Coverage["other-org/alerts-clean"] != domain.CoverageOn {
		t.Errorf("other-org/alerts-clean coverage = %v, want on (its probe answered)", got.Coverage["other-org/alerts-clean"])
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	for owner, want := range map[string]string{"org": "Bearer x", "other-org": "Bearer y"} {
		if len(o.seen[owner]) != 1 || !o.seen[owner][want] {
			t.Errorf("requests about %s carried %v, want only %q", owner, o.seen[owner], want)
		}
	}
}

// A token refused the alert fields stops only its own repositories asking for them: the other
// token's repositories keep reading their alerts on every later fetch (ADR-0015, ADR-0017).
func TestARefusedTokenStopsOnlyItsOwnRepositoriesAsking(t *testing.T) {
	o := newOwnerFake(t)
	f := o.fetcher(map[string]string{"other-org": "y"}, "org/alerts", "other-org/alerts-scope")

	for i := range 2 {
		got, err := f.Fetch(context.Background())
		if err != nil {
			t.Fatalf("Fetch %d: %v", i+1, err)
		}
		alerts := 0
		for _, it := range got.Items {
			if it.Kind == domain.KindAlert && it.Repo == "org/alerts" {
				alerts++
			}
		}
		if alerts == 0 {
			t.Errorf("fetch %d: no alerts for org/alerts, though only other-org's token was refused", i+1)
		}
	}
}
