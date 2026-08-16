package domain

import (
	"strings"
	"time"
)

// Level says how urgently an item needs the user (arc42 §8.2). Higher = more urgent; the order is
// used for sorting.
type Level int

// Attention levels.
const (
	LevelNone Level = iota
	LevelAged
	LevelStale
	LevelExpiring
	LevelUnanswered
	LevelNew
	LevelBuildFailed
	LevelExpired
	LevelDown
	LevelAuthFailed
)

var levelNames = [...]string{"none", "aged", "stale", "expiring", "unanswered", "new", "build_failed", "expired", "down", "auth_failed"}
var levelBadges = [...]string{"", "", "STALE", "EXPIRING", "UNANSWERED", "NEW", "BUILD FAILED", "EXPIRED", "DOWN", "AUTH FAILED"}

// String is the CSS-friendly name.
func (l Level) String() string { return levelNames[l] }

// Badge is the text shown on the tile ("" for none/aged).
func (l Level) Badge() string { return levelBadges[l] }

// NeedsAttention reports whether the level puts the item into the Attention tile.
func (l Level) NeedsAttention() bool { return l >= LevelExpiring }

// Rules are the configurable parameters of attention detection.
type Rules struct {
	Grace      time.Duration // an issue/PR younger than this is not yet "unanswered" (FR-2.3)
	StaleAfter time.Duration // no activity for this long → stale (FR-2.4)
	Me         string        // the owner's GitHub login
	Bots       []string      // substrings identifying bot accounts, e.g. "[bot]"
	WarnDays   int           // default warning horizon for expiries (FR-11.1)
}

// DefaultRules returns the documented defaults (config may override).
func DefaultRules() Rules {
	return Rules{Grace: 4 * time.Hour, StaleAfter: 30 * 24 * time.Hour, Bots: []string{"[bot]", "dependabot", "renovate"}, WarnDays: 14}
}

// Evaluation is the result of Evaluate.
type Evaluation struct {
	Level      Level
	Bucket     Bucket
	New        bool
	Unanswered bool
	Stale      bool
	Dismissed  bool
}

// IsBot reports whether login looks like a bot account.
func (r Rules) IsBot(login string) bool {
	l := strings.ToLower(login)
	for _, b := range r.Bots {
		if strings.Contains(l, strings.ToLower(b)) {
			return true
		}
	}
	return false
}

// IsNew implements FR-2.2/FR-7.2: absent from the previous snapshot of the same source; without a
// usable previous snapshot, created within the last 24 h.
func (r Rules) IsNew(it Item, prev *Snapshot, now time.Time) bool {
	if prev == nil || prev.SourceID != it.ID.SourceID {
		return now.Sub(it.CreatedAt) < 24*time.Hour
	}
	return !prev.Contains(it.ID.ExternalID)
}

// IsUnanswered implements FR-2.3: an open issue/PR past the grace period whose last activity is by
// nobody, by the opener, or by a bot. Activity by Me on an item Me opened counts as an answer — if
// I spoke last, nothing on this item is waiting for me (D-11).
func (r Rules) IsUnanswered(it Item, now time.Time) bool {
	if it.Kind != KindIssue && it.Kind != KindPR {
		return false
	}
	if now.Sub(it.CreatedAt) < r.Grace {
		return false
	}
	by := it.LastActivityBy
	if by == "" {
		return true
	}
	if strings.EqualFold(by, it.Author) {
		return r.Me == "" || !strings.EqualFold(by, r.Me)
	}
	return r.IsBot(by)
}

// IsStale implements FR-2.4 AC2 for issues/PRs.
func (r Rules) IsStale(it Item, now time.Time) bool {
	if it.Kind != KindIssue && it.Kind != KindPR {
		return false
	}
	last := it.LastActivityAt
	if last.IsZero() {
		last = it.CreatedAt
	}
	return now.Sub(last) >= r.StaleAfter
}

func (r Rules) warnHorizon(days int) time.Duration {
	if days <= 0 {
		days = r.WarnDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// Evaluate computes the attention level of an item (arc42 §8.2). prev may be nil; dis may be nil.
func (r Rules) Evaluate(it Item, prev *Snapshot, dis *Dismissal, now time.Time) Evaluation {
	ev := Evaluation{Bucket: BucketOf(it.CreatedAt, now)}
	ev.Dismissed = dis != nil && dis.Covers(it)
	raise := func(l Level) { // dismissal suppresses attention levels, never informational ones
		if !ev.Dismissed && l > ev.Level {
			ev.Level = l
		}
	}
	switch it.Kind {
	case KindWorkflowRun:
		p, _ := DecodePayload[WorkflowRunPayload](it)
		if p.Conclusion == "failure" {
			raise(LevelBuildFailed)
		}
	case KindCredential:
		p, _ := DecodePayload[CredentialPayload](it)
		switch {
		case p.AuthFailed:
			raise(LevelAuthFailed)
		case p.Expires != nil && p.Expires.Before(now):
			raise(LevelExpired)
		case p.Expires != nil && p.Expires.Sub(now) <= r.warnHorizon(p.WarnDays):
			raise(LevelExpiring)
		}
	case KindHealthCheck:
		p, _ := DecodePayload[HealthCheckPayload](it)
		if !p.OK && p.ConsecutiveFailures >= 2 {
			raise(LevelDown)
		} else if p.CertExpires != nil && p.CertExpires.Sub(now) <= r.warnHorizon(0) {
			raise(LevelExpiring)
		}
	case KindMention, KindArticle:
		ev.New = r.IsNew(it, prev, now)
		if ev.New {
			raise(LevelNew)
		}
	case KindIssue, KindPR:
		ev.New = r.IsNew(it, prev, now)
		ev.Unanswered = r.IsUnanswered(it, now)
		ev.Stale = r.IsStale(it, now)
		switch {
		case ev.New:
			raise(LevelNew)
		case ev.Unanswered:
			raise(LevelUnanswered)
		case ev.Stale:
			raise(LevelStale)
		default:
			raise(LevelAged)
		}
	case KindTask, KindMetricSeries:
		// informational only
	}
	return ev
}
