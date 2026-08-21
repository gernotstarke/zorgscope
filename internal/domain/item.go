// Package domain holds zorgscope's data model and business rules. It has no I/O
// and no dependency outside the standard library (ADR-0002).
package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Kind classifies an Item.
type Kind string

// Item kinds.
const (
	KindIssue        Kind = "issue"
	KindPR           Kind = "pr"
	KindWorkflowRun  Kind = "workflow_run"
	KindMention      Kind = "mention"
	KindTask         Kind = "task"
	KindArticle      Kind = "article"
	KindMetricSeries Kind = "metric_series"
	KindCredential   Kind = "credential"
	KindHealthCheck  Kind = "health_check"
)

// ItemID identifies an item globally: the source it came from plus the source's own id.
type ItemID struct {
	SourceID   string
	ExternalID string
}

const idSeparator = "|"

// String renders "sourceID|externalID". Source ids never contain '|'.
func (id ItemID) String() string { return id.SourceID + idSeparator + id.ExternalID }

// ParseItemID is the inverse of String.
func ParseItemID(s string) (ItemID, error) {
	src, ext, ok := strings.Cut(s, idSeparator)
	if !ok || src == "" || ext == "" {
		return ItemID{}, errors.New("domain: invalid item id " + s)
	}
	return ItemID{SourceID: src, ExternalID: ext}, nil
}

// Item is the normalised unit of information shown on tiles (arc42 §8.1).
type Item struct {
	ID             ItemID
	Kind           Kind
	Title          string
	URL            string
	Author         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastActivityBy string
	LastActivityAt time.Time
	Labels         []string
	Payload        json.RawMessage // kind-specific, see *Payload types
	FirstSeen      time.Time       // set by the store on first insert
}

// Payload types — one per Kind that carries extra data.
type (
	// IssuePayload holds issue extras.
	IssuePayload struct {
		Comments int `json:"comments"`
	}
	// PRPayload holds pull request extras.
	PRPayload struct {
		Draft          bool   `json:"draft"`
		ReviewDecision string `json:"review_decision"`
		Comments       int    `json:"comments"`
	}
	// WorkflowRunPayload describes the latest workflow run of a repo.
	WorkflowRunPayload struct {
		RunID        int64  `json:"run_id"`
		WorkflowName string `json:"workflow_name"`
		Conclusion   string `json:"conclusion"` // success | failure | cancelled | ... | "" while running
		Status       string `json:"status"`     // completed | in_progress | queued
		Branch       string `json:"branch"`
	}
	// MentionPayload describes a notification thread.
	MentionPayload struct {
		Reason      string `json:"reason"`
		Repo        string `json:"repo"`
		SubjectType string `json:"subject_type"`
	}
	// TaskPayload holds Todoist task extras.
	TaskPayload struct {
		Project    string    `json:"project"`
		Priority   int       `json:"priority"` // 1 (highest) .. 4
		Due        time.Time `json:"due"`
		DueHasTime bool      `json:"due_has_time"`
		Recurring  bool      `json:"recurring"`
	}
	// ArticlePayload holds feed article extras.
	ArticlePayload struct {
		Summary  string `json:"summary"`
		Topic    string `json:"topic"`
		FeedName string `json:"feed_name"`
	}
	// MetricPage is one top page of a site.
	MetricPage struct {
		Path     string `json:"path"`
		Visitors int    `json:"visitors"`
	}
	// MetricSeriesPayload holds Plausible numbers for one site.
	MetricSeriesPayload struct {
		Visitors7d       int          `json:"visitors_7d"`
		Visitors30d      int          `json:"visitors_30d"`
		Pageviews30d     int          `json:"pageviews_30d"`
		DeltaVisitors7d  float64      `json:"delta_visitors_7d"` // fraction, e.g. 0.12 = +12 %
		DeltaVisitors30d float64      `json:"delta_visitors_30d"`
		Daily            []int        `json:"daily"`
		TopPages         []MetricPage `json:"top_pages"`
	}
	// CredentialPayload describes a watched credential (FR-11.x).
	CredentialPayload struct {
		Expires      *time.Time `json:"expires,omitempty"`
		WarnDays     int        `json:"warn_days"`
		UsedBy       string     `json:"used_by"`
		URL          string     `json:"url"`
		AutoDetected bool       `json:"auto_detected"`
		AuthFailed   bool       `json:"auth_failed"` // synthetic "AUTH FAILED" item for a source
	}
	// HealthCheckPayload describes a watched URL.
	HealthCheckPayload struct {
		StatusCode          int        `json:"status_code"`
		LatencyMs           int64      `json:"latency_ms"`
		OK                  bool       `json:"ok"`
		ConsecutiveFailures int        `json:"consecutive_failures"`
		CertExpires         *time.Time `json:"cert_expires,omitempty"`
		LastOK              time.Time  `json:"last_ok"`
	}
)

// DecodePayload decodes an item's payload into T. An empty payload yields the zero value.
func DecodePayload[T any](it Item) (T, error) {
	var v T
	if len(it.Payload) == 0 {
		return v, nil
	}
	err := json.Unmarshal(it.Payload, &v)
	return v, err
}

// EncodePayload marshals a payload struct.
func EncodePayload(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// MustPayload is EncodePayload for tests and adapters with static structs; panics on error.
func MustPayload(v any) json.RawMessage {
	b, err := EncodePayload(v)
	if err != nil {
		panic(err)
	}
	return b
}
