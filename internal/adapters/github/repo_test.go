package github

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
	githubfake "github.com/gernotstarke/zorgscope/test/fakes/github"
)

type sinkRec struct {
	name   string
	exp    *time.Time
	usedBy string
	calls  int
}

func (s *sinkRec) ReportCredential(name string, exp *time.Time, usedBy string) {
	s.name, s.exp, s.usedBy = name, exp, usedBy
	s.calls++
}

var now0 = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

func newFake(t *testing.T) (*githubfake.Server, *httptest.Server) {
	t.Helper()
	fs := githubfake.New()
	githubfake.Seed(fs, now0)
	srv := httptest.NewServer(fs.Handler())
	t.Cleanup(srv.Close)
	return fs, srv
}

func byExt(items []domain.Item) map[string]domain.Item {
	m := map[string]domain.Item{}
	for _, it := range items {
		m[it.ID.ExternalID] = it
	}
	return m
}

func TestRepoFetcherMapsIssuesPRsAndRun(t *testing.T) {
	_, srv := newFake(t)
	c := NewClient(srv.Client(), srv.URL, "tok", nil)
	f, err := NewRepoFetcher(c, "arc42/arc42-template")
	if err != nil {
		t.Fatal(err)
	}
	if f.ID() != "github:arc42/arc42-template" || f.Kind() != ports.KindGitHubRepo {
		t.Fatalf("id/kind: %s %s", f.ID(), f.Kind())
	}
	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := byExt(items)
	if len(m) != 5 { // 3 issues + 1 PR + 1 run
		t.Fatalf("items = %d: %v", len(m), m)
	}
	i236 := m["issues/236"]
	if i236.Kind != domain.KindIssue || i236.Author != "lwbt" || i236.LastActivityBy != "gernotstarke" || i236.URL != "https://github.com/arc42/arc42-template/issues/236" ||
		i236.ID.SourceID != f.ID() || i236.LastActivityAt.IsZero() {
		t.Fatalf("issue 236 = %+v", i236)
	}
	p, _ := domain.DecodePayload[domain.IssuePayload](i236)
	if p.Comments != 1 {
		t.Fatalf("issue payload = %+v", p)
	}
	i233 := m["issues/233"]
	if i233.LastActivityBy != "lwbt" || len(i233.Labels) != 1 || i233.Labels[0] != "enhancement" {
		t.Fatalf("issue 233 = %+v", i233)
	}
	pr := m["pulls/237"]
	if pr.Kind != domain.KindPR || pr.Author != "Sofeso" || pr.LastActivityBy != "" {
		t.Fatalf("pr = %+v", pr)
	}
	pp, _ := domain.DecodePayload[domain.PRPayload](pr)
	if pp.ReviewDecision != "REVIEW_REQUIRED" || pp.Draft {
		t.Fatalf("pr payload = %+v", pp)
	}
	run := m["runs/1001"]
	if run.Kind != domain.KindWorkflowRun || run.Title != "build" || run.URL == "" {
		t.Fatalf("run = %+v", run)
	}
	rp, _ := domain.DecodePayload[domain.WorkflowRunPayload](run)
	if rp.Conclusion != "success" || rp.Branch != "master" || rp.RunID != 1001 || rp.WorkflowName != "build" {
		t.Fatalf("run payload = %+v", rp)
	}
}

func TestRepoFetcherPaginates(t *testing.T) {
	fs, srv := newFake(t)
	c := NewClient(srv.Client(), srv.URL, "tok", nil)
	f, _ := NewRepoFetcher(c, "arc42/arc42-template")
	f.PageSize = 2
	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("items = %d", len(items))
	}
	if fs.Requests() < 3 { // ≥2 GraphQL pages + 1 runs request
		t.Fatalf("expected paginated requests, got %d", fs.Requests())
	}
}

func TestRepoFetcherErrors(t *testing.T) {
	fs, srv := newFake(t)
	ctx := context.Background()
	noTok := NewClient(srv.Client(), srv.URL, "", nil)
	f, _ := NewRepoFetcher(noTok, "arc42/arc42-template")
	if _, err := f.Fetch(ctx); !errors.Is(err, ports.ErrAuth) {
		t.Fatalf("missing token → ErrAuth, got %v", err)
	}
	c := NewClient(srv.Client(), srv.URL, "tok", nil)
	f, _ = NewRepoFetcher(c, "arc42/arc42-template")
	fs.FailNext(403, true)
	_, err := f.Fetch(ctx)
	if rl, ok := ports.AsRateLimited(err); !ok || rl.ResetAt.IsZero() {
		t.Fatalf("rate limit → RateLimitedError, got %v", err)
	}
	fs.FailNext(502, false)
	if _, err := f.Fetch(ctx); !errors.Is(err, ports.ErrTransient) {
		t.Fatalf("502 → ErrTransient, got %v", err)
	}
	unknown, _ := NewRepoFetcher(c, "nobody/nothing")
	if _, err := unknown.Fetch(ctx); !errors.Is(err, ports.ErrPermanent) {
		t.Fatalf("unknown repo → ErrPermanent, got %v", err)
	}
	if _, err := NewRepoFetcher(c, "not-a-repo"); err == nil {
		t.Fatal("bad name must fail")
	}
}

func TestTokenExpiryReported(t *testing.T) {
	fs, srv := newFake(t)
	exp := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	fs.SetTokenExpiry(exp)
	sink := &sinkRec{}
	c := NewClient(srv.Client(), srv.URL, "tok", sink)
	f, _ := NewRepoFetcher(c, "arc42/arc42-template")
	if _, err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sink.name != TokenCredentialName || sink.exp == nil || !sink.exp.Equal(exp) || sink.usedBy != "zorgscope" {
		t.Fatalf("sink = %+v", sink)
	}
	calls := sink.calls
	_, _ = f.Fetch(context.Background())
	if sink.calls != calls {
		t.Fatal("unchanged expiry must not be re-reported on every request")
	}
}
