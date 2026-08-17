package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// DashboardSchemaVersion changes only when the client-facing dashboard representation changes
// incompatibly. Clients can reject a newer contract before interpreting individual sections.
const DashboardSchemaVersion = 1

// View is everything the page template needs (arc42 §12 "view model": no domain logic inside).
type View struct {
	SchemaVersion int           `json:"schema_version"`
	Header        HeaderView    `json:"header"`
	Attention     AttentionView `json:"attention"`
	Repos         []RepoCard    `json:"repositories"`
	Sites         []SiteView    `json:"sites"`
	Watch         []WatchView   `json:"watch"`
	Tiles         []string      `json:"tiles"`
	GeneratedAt   time.Time     `json:"generated_at"`
}

// HeaderView feeds tile_header.html.
type HeaderView struct {
	Date           string             `json:"date"`       // "Sun 16 Aug 2026"
	Time           string             `json:"time"`       // "14:00"
	DataAsOf       string             `json:"data_as_of"` // "13:55" or "never"
	Sources        []SourceStatusView `json:"sources"`
	Refreshing     int                `json:"refreshing"`
	AttentionCount int                `json:"attention_count"`
}

// SourceStatusView is one staleness dot in the header.
type SourceStatusView struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Age        string `json:"age"`
	Error      string `json:"error,omitempty"`
	Healthy    bool   `json:"healthy"`
	AuthFailed bool   `json:"auth_failed"`
	InFlight   bool   `json:"in_flight"`
}

// AttentionView feeds tile_attention.html.
type AttentionView struct {
	Rows     []AttentionRow `json:"rows"`
	Overflow int            `json:"overflow"`
	Total    int            `json:"total"`
}

// AttentionRow is one line in the Attention tile.
type AttentionRow struct {
	ID          string `json:"id"`               // domain.ItemID.String()
	Source      string `json:"source"`           // "arc42/arc42-template", "GitHub mentions", ...
	SourceShort string `json:"source_short"`     // "arc42-template"
	Number      string `json:"number,omitempty"` // "#236" or ""
	Title       string `json:"title"`
	URL         string `json:"url"`
	Author      string `json:"author,omitempty"`
	Age         string `json:"age"`
	Bucket      string `json:"bucket"` // lt24h | lt7d | lt30d | ge30d
	Badge       string `json:"badge"`  // NEW | UNANSWERED | ...
	Level       string `json:"level"`  // css class
	Kind        string `json:"kind"`
	UpdatedAt   int64  `json:"updated_at"` // unix seconds, sent back with dismiss
}

// RepoCard feeds tile_repos.html.
type RepoCard struct {
	Name       string    `json:"name"`
	ShortName  string    `json:"short_name"`
	URL        string    `json:"url"`
	OpenIssues int       `json:"open_issues"`
	OpenPRs    int       `json:"open_prs"`
	New        int       `json:"new"`
	Unanswered int       `json:"unanswered"`
	Build      BuildView `json:"build"`
}

// BuildView is the CI status dot of a repo (FR-3.2).
type BuildView struct {
	State              string `json:"state"` // ok | failed | running | unknown
	PreviousConclusion string `json:"previous_conclusion,omitempty"`
	Workflow           string `json:"workflow,omitempty"`
	URL                string `json:"url,omitempty"`
	Age                string `json:"age,omitempty"`
}

// SiteView is one Plausible site's cached aggregate and sparkline series.
type SiteView struct {
	Site             string              `json:"site"`
	URL              string              `json:"url"`
	Visitors7d       int                 `json:"visitors_7d"`
	Visitors30d      int                 `json:"visitors_30d"`
	Pageviews30d     int                 `json:"pageviews_30d"`
	DeltaVisitors7d  float64             `json:"delta_visitors_7d"`
	DeltaVisitors30d float64             `json:"delta_visitors_30d"`
	Daily            []int               `json:"daily"`
	TopPages         []domain.MetricPage `json:"top_pages"`
}

// WatchView is one credential or endpoint certificate/health state.
type WatchView struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Kind                string `json:"kind"`
	State               string `json:"state"`
	Badge               string `json:"badge,omitempty"`
	URL                 string `json:"url,omitempty"`
	UsedBy              string `json:"used_by,omitempty"`
	ExpiresAt           string `json:"expires_at,omitempty"`
	RemainingDays       *int   `json:"remaining_days,omitempty"`
	WarnDays            int    `json:"warn_days,omitempty"`
	AutoDetected        bool   `json:"auto_detected,omitempty"`
	AuthFailed          bool   `json:"auth_failed,omitempty"`
	OK                  *bool  `json:"ok,omitempty"`
	StatusCode          int    `json:"status_code,omitempty"`
	LatencyMs           int64  `json:"latency_ms,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures,omitempty"`
	LastOK              string `json:"last_ok,omitempty"`
	CheckedAt           string `json:"checked_at,omitempty"`
	Issuer              string `json:"issuer,omitempty"`
	HostnameValid       *bool  `json:"hostname_valid,omitempty"`
}

// HumanAge renders a duration compactly: now, 5m, 3h, 47h, 2d.
func HumanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// SourceLabel derives display names from a source id.
func SourceLabel(sourceID string) (label, short string) {
	switch {
	case sourceID == "github:mentions":
		return "GitHub mentions", "mentions"
	case strings.HasPrefix(sourceID, "github:"):
		full := strings.TrimPrefix(sourceID, "github:")
		_, name, _ := strings.Cut(full, "/")
		return full, name
	default:
		return sourceID, sourceID
	}
}
