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

// Finding 1: the HealthCheck cert-expiry branch (`p.CertExpires != nil && p.CertExpires.Sub(now) <=
// r.warnHorizon(0) → LevelExpiring`) had no covering test. These pin it, including its precedence
// against the Down branch.
func TestEvaluateHealthCheckCertExpiry(t *testing.T) {
	base := Item{ID: ItemID{"watch:url:x", "x"}, Kind: KindHealthCheck, CreatedAt: now0}

	soon := now0.Add(5 * 24 * time.Hour) // inside the 14-day default warn horizon
	expiringSoon := base
	expiringSoon.Payload = MustPayload(HealthCheckPayload{OK: true, CertExpires: &soon})
	if ev := rules.Evaluate(expiringSoon, nil, nil, now0); ev.Level != LevelExpiring {
		t.Fatalf("cert expires in 5 d (warn 14) → Expiring, got %+v", ev)
	}

	far := now0.Add(90 * 24 * time.Hour) // well outside the warn horizon
	expiringFar := base
	expiringFar.Payload = MustPayload(HealthCheckPayload{OK: true, CertExpires: &far})
	if ev := rules.Evaluate(expiringFar, nil, nil, now0); ev.Level != LevelNone {
		t.Fatalf("cert expires in 90 d (warn 14) → None, got %+v", ev)
	}

	noCert := base
	noCert.Payload = MustPayload(HealthCheckPayload{OK: true})
	if ev := rules.Evaluate(noCert, nil, nil, now0); ev.Level != LevelNone {
		t.Fatalf("OK, no CertExpires → None, got %+v", ev)
	}

	// a down check with a soon-expiring cert must still report Down: the `else if` means
	// ConsecutiveFailures takes precedence over CertExpires.
	downAndExpiring := base
	downAndExpiring.Payload = MustPayload(HealthCheckPayload{OK: false, ConsecutiveFailures: 2, CertExpires: &soon})
	if ev := rules.Evaluate(downAndExpiring, nil, nil, now0); ev.Level != LevelDown {
		t.Fatalf("down + expiring cert → Down (precedence), got %+v", ev)
	}
}

// Finding 2: boundary tests for the three configurable thresholds. Each pins the exact comparison
// operator used in attention.go — an off-by-one (< vs <=, >= vs >) would flip one of these three
// points and fail here.
func TestIsUnansweredGraceBoundary(t *testing.T) {
	// attention.go: `if now.Sub(it.CreatedAt) < r.Grace { return false }` — strictly-less is the
	// "still within grace" case; age == Grace is already past grace.
	cases := []struct {
		name string
		it   Item
		want bool
	}{
		{"age == Grace exactly (boundary, not < Grace) → past grace, unanswered", issue("i", "alice", now0.Add(-rules.Grace), "", time.Time{}), true},
		{"age == Grace-1s (< Grace) → still within grace, not unanswered", issue("i", "alice", now0.Add(-(rules.Grace - time.Second)), "", time.Time{}), false},
		{"age == Grace+1s (> Grace) → past grace, unanswered", issue("i", "alice", now0.Add(-(rules.Grace + time.Second)), "", time.Time{}), true},
	}
	for _, c := range cases {
		if got := rules.IsUnanswered(c.it, now0); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestIsStaleBoundary(t *testing.T) {
	// attention.go: `return now.Sub(last) >= r.StaleAfter` — age == StaleAfter is already stale
	// (inclusive boundary on the ">=" side).
	created := now0.Add(-2 * rules.StaleAfter) // old enough that CreatedAt never masks LastActivityAt
	cases := []struct {
		name string
		it   Item
		want bool
	}{
		{"age == StaleAfter exactly (boundary, >= StaleAfter) → stale", issue("i", "alice", created, "bob", now0.Add(-rules.StaleAfter)), true},
		{"age == StaleAfter-1s (< StaleAfter) → not yet stale", issue("i", "alice", created, "bob", now0.Add(-(rules.StaleAfter - time.Second))), false},
		{"age == StaleAfter+1s (> StaleAfter) → stale", issue("i", "alice", created, "bob", now0.Add(-(rules.StaleAfter + time.Second))), true},
	}
	for _, c := range cases {
		if got := rules.IsStale(c.it, now0); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestCredentialExpiringBoundary(t *testing.T) {
	// attention.go: `case p.Expires != nil && p.Expires.Sub(now) <= r.warnHorizon(p.WarnDays): raise(LevelExpiring)`
	// — remaining == warnHorizon is already inside the warn window (inclusive "<=").
	horizon := time.Duration(rules.WarnDays) * 24 * time.Hour
	base := Item{ID: ItemID{"watch:credentials", "c1"}, Kind: KindCredential, CreatedAt: now0}

	atBoundary := horizon
	exp := now0.Add(atBoundary)
	cred := base
	cred.Payload = MustPayload(CredentialPayload{Expires: &exp})
	if ev := rules.Evaluate(cred, nil, nil, now0); ev.Level != LevelExpiring {
		t.Fatalf("remaining == warnHorizon exactly (boundary, <= horizon) → Expiring, got %+v", ev)
	}

	insideBoundary := horizon - time.Second
	exp2 := now0.Add(insideBoundary)
	cred.Payload = MustPayload(CredentialPayload{Expires: &exp2})
	if ev := rules.Evaluate(cred, nil, nil, now0); ev.Level != LevelExpiring {
		t.Fatalf("remaining == warnHorizon-1s (< horizon) → still Expiring, got %+v", ev)
	}

	outsideBoundary := horizon + time.Second
	exp3 := now0.Add(outsideBoundary)
	cred.Payload = MustPayload(CredentialPayload{Expires: &exp3})
	if ev := rules.Evaluate(cred, nil, nil, now0); ev.Level != LevelNone {
		t.Fatalf("remaining == warnHorizon+1s (> horizon) → None, got %+v", ev)
	}
}

// Finding 3: DecodePayload's error is deliberately discarded in Evaluate (see the comment above the
// first DecodePayload call). This pins that as intended behaviour rather than an oversight: payload
// bytes that are not valid JSON at all must not panic, and must evaluate as if the payload were the
// zero value, i.e. LevelNone.
func TestEvaluateMalformedPayloadYieldsNoneNotPanic(t *testing.T) {
	it := Item{ID: ItemID{"github:o/r", "runs/1"}, Kind: KindWorkflowRun, CreatedAt: now0, UpdatedAt: now0,
		Payload: []byte("{not json")}
	ev := rules.Evaluate(it, nil, nil, now0) // must not panic
	if ev.Level != LevelNone {
		t.Fatalf("malformed payload → decode error discarded, zero payload → None, got %+v", ev)
	}
}
