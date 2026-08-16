package domain

import (
	"testing"
	"time"
)

var (
	now0  = time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	rules = DefaultRules()
)

func issue(ext, author string, created time.Time, lastBy string, lastAt time.Time) Item {
	return Item{ID: ItemID{SourceID: "github:o/r", ExternalID: ext}, Kind: KindIssue, Author: author,
		CreatedAt: created, UpdatedAt: lastAt, LastActivityBy: lastBy, LastActivityAt: lastAt}
}

func TestIsNew(t *testing.T) {
	prev := NewSnapshot("github:o/r", "2026-08-15", now0, []string{"issues/1"})
	old := issue("issues/1", "alice", now0.Add(-48*time.Hour), "", time.Time{})
	fresh := issue("issues/2", "bob", now0.Add(-time.Hour), "", time.Time{})
	if rules.IsNew(old, &prev, now0) {
		t.Fatal("item in previous snapshot is not new")
	}
	if !rules.IsNew(fresh, &prev, now0) {
		t.Fatal("item absent from previous snapshot is new")
	}
	// first run: no snapshot → only items younger than 24 h are new (FR-2.2 AC3)
	if rules.IsNew(old, nil, now0) || !rules.IsNew(fresh, nil, now0) {
		t.Fatal("first-run rule violated")
	}
	// snapshot of another source is ignored (treated as nil)
	other := NewSnapshot("github:x/y", "2026-08-15", now0, []string{"issues/1"})
	if !rules.IsNew(fresh, &other, now0) || rules.IsNew(old, &other, now0) {
		t.Fatal("foreign snapshot must behave like nil")
	}
}

func TestIsUnanswered(t *testing.T) {
	created := now0.Add(-10 * time.Hour)
	cases := []struct {
		name string
		it   Item
		want bool
	}{
		{"no comments, past grace", issue("i", "alice", created, "", time.Time{}), true},
		{"no comments, within grace", issue("i", "alice", now0.Add(-time.Hour), "", time.Time{}), false},
		{"last comment by opener", issue("i", "alice", created, "alice", now0.Add(-time.Hour)), true},
		{"last comment by opener, different case", issue("i", "Alice", created, "alice", now0), true},
		{"last comment by bot", issue("i", "alice", created, "github-actions[bot]", now0), true},
		{"last comment by dependabot", issue("i", "alice", created, "dependabot", now0), true},
		{"answered by maintainer", issue("i", "alice", created, "gernotstarke", now0), false},
		{"answered by anyone else", issue("i", "alice", created, "carol", now0), false},
	}
	for _, c := range cases {
		if got := rules.IsUnanswered(c.it, now0); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	task := Item{Kind: KindTask, CreatedAt: created}
	if rules.IsUnanswered(task, now0) {
		t.Fatal("only issues and PRs can be unanswered")
	}

	// D-11: activity by Me on an item Me authored counts as an answer.
	mine := DefaultRules()
	mine.Me = "gernotstarke"
	mineCases := []struct {
		name string
		it   Item
		want bool
	}{
		{"authored by me, last activity by me → answered (flips)", issue("i", "gernotstarke", created, "gernotstarke", now0), false},
		{"authored by me, last activity by me, different case → answered", issue("i", "GernotStarke", created, "gernotstarke", now0), false},
		{"authored by me, no activity at all → still unanswered", issue("i", "gernotstarke", created, "", time.Time{}), true},
		{"authored by me, last activity by a bot → still unanswered", issue("i", "gernotstarke", created, "dependabot", now0), true},
		{"authored by alice, last activity by alice, evaluated with mine → still unanswered", issue("i", "alice", created, "alice", now0), true},
	}
	for _, c := range mineCases {
		if got := mine.IsUnanswered(c.it, now0); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestEvaluateIssueLevels(t *testing.T) {
	prev := NewSnapshot("github:o/r", "2026-08-15", now0, []string{"issues/old", "issues/stale", "issues/answered"})
	newUnanswered := issue("issues/new", "alice", now0.Add(-6*time.Hour), "", time.Time{})
	oldUnanswered := issue("issues/old", "alice", now0.Add(-3*24*time.Hour), "", time.Time{})
	stale := issue("issues/stale", "alice", now0.Add(-60*24*time.Hour), "gernotstarke", now0.Add(-45*24*time.Hour))
	answered := issue("issues/answered", "alice", now0.Add(-3*24*time.Hour), "gernotstarke", now0.Add(-2*24*time.Hour))

	if ev := rules.Evaluate(newUnanswered, &prev, nil, now0); ev.Level != LevelNew || !ev.New || !ev.Unanswered || ev.Bucket != BucketLT24h {
		t.Fatalf("new+unanswered → New, got %+v", ev)
	}
	if ev := rules.Evaluate(oldUnanswered, &prev, nil, now0); ev.Level != LevelUnanswered || ev.Bucket != BucketLT7d {
		t.Fatalf("old unanswered → Unanswered, got %+v", ev)
	}
	if ev := rules.Evaluate(stale, &prev, nil, now0); ev.Level != LevelStale || !ev.Stale {
		t.Fatalf("stale → Stale, got %+v", ev)
	}
	if ev := rules.Evaluate(answered, &prev, nil, now0); ev.Level != LevelAged {
		t.Fatalf("answered → Aged, got %+v", ev)
	}
	// dismissal covers → None but flags still computed
	dis := &Dismissal{ID: oldUnanswered.ID, UpdatedAt: oldUnanswered.UpdatedAt}
	if ev := rules.Evaluate(oldUnanswered, &prev, dis, now0); ev.Level != LevelNone || !ev.Dismissed || !ev.Unanswered {
		t.Fatalf("dismissed → None, got %+v", ev)
	}
	// dismissal for another state does not cover
	stale2 := &Dismissal{ID: oldUnanswered.ID, UpdatedAt: oldUnanswered.UpdatedAt.Add(-time.Minute)}
	if ev := rules.Evaluate(oldUnanswered, &prev, stale2, now0); ev.Level != LevelUnanswered {
		t.Fatalf("outdated dismissal must not cover, got %+v", ev)
	}
}

func TestEvaluateOtherKinds(t *testing.T) {
	failed := Item{ID: ItemID{"github:o/r", "runs/1"}, Kind: KindWorkflowRun, CreatedAt: now0, UpdatedAt: now0,
		Payload: MustPayload(WorkflowRunPayload{Conclusion: "failure", Status: "completed"})}
	if ev := rules.Evaluate(failed, nil, nil, now0); ev.Level != LevelBuildFailed {
		t.Fatalf("failed run → BuildFailed, got %+v", ev)
	}
	ok := failed
	ok.Payload = MustPayload(WorkflowRunPayload{Conclusion: "success", Status: "completed"})
	if ev := rules.Evaluate(ok, nil, nil, now0); ev.Level != LevelNone {
		t.Fatalf("green run → None, got %+v", ev)
	}
	exp := now0.Add(10 * 24 * time.Hour)
	cred := Item{ID: ItemID{"watch:credentials", "c1"}, Kind: KindCredential, CreatedAt: now0, UpdatedAt: exp,
		Payload: MustPayload(CredentialPayload{Expires: &exp})}
	if ev := rules.Evaluate(cred, nil, nil, now0); ev.Level != LevelExpiring {
		t.Fatalf("expires in 10 d (warn 14) → Expiring, got %+v", ev)
	}
	past := now0.Add(-time.Hour)
	cred.Payload = MustPayload(CredentialPayload{Expires: &past})
	if ev := rules.Evaluate(cred, nil, nil, now0); ev.Level != LevelExpired {
		t.Fatalf("past → Expired, got %+v", ev)
	}
	cred.Payload = MustPayload(CredentialPayload{AuthFailed: true})
	if ev := rules.Evaluate(cred, nil, nil, now0); ev.Level != LevelAuthFailed {
		t.Fatalf("auth failed → AuthFailed, got %+v", ev)
	}
	down := Item{ID: ItemID{"watch:url:x", "x"}, Kind: KindHealthCheck, CreatedAt: now0,
		Payload: MustPayload(HealthCheckPayload{OK: false, ConsecutiveFailures: 2})}
	if ev := rules.Evaluate(down, nil, nil, now0); ev.Level != LevelDown {
		t.Fatalf("2 failures → Down, got %+v", ev)
	}
	down.Payload = MustPayload(HealthCheckPayload{OK: false, ConsecutiveFailures: 1})
	if ev := rules.Evaluate(down, nil, nil, now0); ev.Level != LevelNone {
		t.Fatalf("1 failure → None, got %+v", ev)
	}
	mention := Item{ID: ItemID{"github:mentions", "t1"}, Kind: KindMention, CreatedAt: now0.Add(-time.Hour)}
	if ev := rules.Evaluate(mention, nil, nil, now0); ev.Level != LevelNew {
		t.Fatalf("fresh mention → New, got %+v", ev)
	}
	task := Item{ID: ItemID{"todoist", "t"}, Kind: KindTask, CreatedAt: now0}
	if ev := rules.Evaluate(task, nil, nil, now0); ev.Level != LevelNone {
		t.Fatalf("task → None, got %+v", ev)
	}
}

func TestLevelMeta(t *testing.T) {
	if !LevelNew.NeedsAttention() || !LevelExpiring.NeedsAttention() || LevelStale.NeedsAttention() || LevelAged.NeedsAttention() {
		t.Fatal("NeedsAttention thresholds wrong")
	}
	if LevelBuildFailed.Badge() != "BUILD FAILED" || LevelNone.Badge() != "" || LevelNew.String() != "new" {
		t.Fatal("badge/string wrong")
	}
	if LevelAuthFailed <= LevelDown || LevelDown <= LevelExpired || LevelExpired <= LevelBuildFailed ||
		LevelBuildFailed <= LevelNew || LevelNew <= LevelUnanswered || LevelUnanswered <= LevelExpiring {
		t.Fatal("severity order wrong (used for sorting)")
	}
}
