package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/config"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports/memstore"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	y := "server:\n  base_url: http://localhost:8080\ngithub:\n  enabled: true\n  me: gernotstarke\n  repos: [arc42/arc42-template, arc42/arc42.org-site]\n"
	cfg, err := config.Parse(strings.NewReader(y), func(k string) string {
		return map[string]string{"GITHUB_TOKEN": "t", "AUTH_MODE": "dev"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func gh(repo, ext string, kind domain.Kind, author string, created time.Time, lastBy string, lastAt time.Time) domain.Item {
	return domain.Item{ID: domain.ItemID{SourceID: "github:" + repo, ExternalID: ext}, Kind: kind, Title: "Title " + ext,
		URL: "https://github.com/" + repo + "/" + ext, Author: author, CreatedAt: created, UpdatedAt: created,
		LastActivityBy: lastBy, LastActivityAt: lastAt}
}

func TestDashboardBuild(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	clk := clock.NewFake(t0) // 12:00 UTC = 14:00 Berlin, snapshot day 2026-08-16
	cfg := testConfig(t)
	tpl := "github:arc42/arc42-template"
	site := "github:arc42/arc42.org-site"
	// snapshot for the previous day contains issues/1 and pulls/3 (issues/2 is new)
	_ = st.PutSnapshot(ctx, domain.NewSnapshot(tpl, "2026-08-15", t0, []string{"issues/1", "pulls/3"}))
	items := []domain.Item{
		gh("arc42/arc42-template", "issues/1", domain.KindIssue, "alice", t0.Add(-3*24*time.Hour), "", time.Time{}),                // unanswered
		gh("arc42/arc42-template", "issues/2", domain.KindIssue, "bob", t0.Add(-2*time.Hour), "", time.Time{}),                     // new (within grace → not unanswered)
		gh("arc42/arc42-template", "pulls/3", domain.KindPR, "carol", t0.Add(-5*24*time.Hour), "gernotstarke", t0.Add(-time.Hour)), // answered → aged
	}
	failed := gh("arc42/arc42-template", "runs/99", domain.KindWorkflowRun, "", t0.Add(-30*time.Minute), "", time.Time{})
	failed.Title = "build"
	failed.Payload = domain.MustPayload(domain.WorkflowRunPayload{RunID: 99, WorkflowName: "build", Conclusion: "failure", Status: "completed", Branch: "main"})
	_ = st.ReplaceItems(ctx, tpl, append(items, failed), t0)
	_ = st.RecordStatus(ctx, domain.FetchStatus{SourceID: tpl, Kind: "github-repo", LastSuccess: t0.Add(-5 * time.Minute), ItemCount: 4})
	_ = st.RecordStatus(ctx, domain.FetchStatus{SourceID: site, Kind: "github-repo", LastError: t0.Add(-time.Minute), ErrorMsg: "401", AuthFailed: true})

	d := NewDashboard(st, clk, cfg)
	v, err := d.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v.Header.Date != "Sun 16 Aug 2026" || v.Header.Time != "14:00" || v.Header.DataAsOf != "13:55" {
		t.Fatalf("header = %+v", v.Header)
	}
	if len(v.Header.Sources) != 2 || v.Header.Sources[1].ID != site || !v.Header.Sources[1].AuthFailed || v.Header.Sources[0].Age != "5m" {
		t.Fatalf("sources = %+v", v.Header.Sources)
	}
	// attention: auth failed (site), build failed, new issue 2, unanswered issue 1 — in that order
	got := make([]string, 0, len(v.Attention.Rows))
	for _, r := range v.Attention.Rows {
		got = append(got, r.Badge)
	}
	if strings.Join(got, ",") != "AUTH FAILED,BUILD FAILED,NEW,UNANSWERED" {
		t.Fatalf("attention badges = %v", got)
	}
	if v.Attention.Total != 4 || v.Header.AttentionCount != 4 || v.Attention.Overflow != 0 {
		t.Fatalf("attention counts = %+v", v.Attention)
	}
	row := v.Attention.Rows[2]
	if row.Number != "#2" || row.SourceShort != "arc42-template" || row.Age != "2h" || row.Bucket != "lt24h" || row.URL == "" || row.UpdatedAt != t0.Add(-2*time.Hour).Unix() {
		t.Fatalf("row = %+v", row)
	}
	// repo cards: template first (attention 2), then site
	if len(v.Repos) != 2 || v.Repos[0].ShortName != "arc42-template" || v.Repos[0].OpenIssues != 2 || v.Repos[0].OpenPRs != 1 ||
		v.Repos[0].New != 1 || v.Repos[0].Unanswered != 1 || v.Repos[0].Build.State != "failed" || v.Repos[0].Build.Workflow != "build" {
		t.Fatalf("repos = %+v", v.Repos)
	}
	if v.Repos[1].Build.State != "unknown" {
		t.Fatalf("no run → unknown: %+v", v.Repos[1].Build)
	}
	// dismiss the unanswered one → disappears
	if err := Dismiss(ctx, st, clk, items[0].ID, items[0].UpdatedAt); err != nil {
		t.Fatal(err)
	}
	v, _ = d.Build(ctx)
	if v.Attention.Total != 3 || v.Repos[0].Unanswered != 0 {
		t.Fatalf("after dismiss: %+v", v.Attention)
	}
	// cap
	cfg.UI.AttentionCap = 2
	v, _ = d.Build(ctx)
	if len(v.Attention.Rows) != 2 || v.Attention.Overflow != 1 || v.Attention.Total != 3 {
		t.Fatalf("cap: %+v", v.Attention)
	}
}

func TestHumanAgeAndSourceLabel(t *testing.T) {
	cases := map[time.Duration]string{30 * time.Second: "now", 5 * time.Minute: "5m", 3 * time.Hour: "3h", 47 * time.Hour: "47h", 49 * time.Hour: "2d", 40 * 24 * time.Hour: "40d"}
	for d, want := range cases {
		if got := HumanAge(d); got != want {
			t.Errorf("HumanAge(%v) = %q want %q", d, got, want)
		}
	}
	if l, s := SourceLabel("github:arc42/arc42-template"); l != "arc42/arc42-template" || s != "arc42-template" {
		t.Fatalf("label %q %q", l, s)
	}
	if l, s := SourceLabel("github:mentions"); l != "GitHub mentions" || s != "mentions" {
		t.Fatalf("label %q %q", l, s)
	}
	if l, _ := SourceLabel("watch:credentials"); l != "watch:credentials" {
		t.Fatalf("fallback label %q", l)
	}
}
