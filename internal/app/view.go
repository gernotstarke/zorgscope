package app

import (
	"fmt"
	"strings"
	"time"
)

// View is everything the page template needs (arc42 §12 "view model": no domain logic inside).
type View struct {
	Header      HeaderView
	Attention   AttentionView
	Repos       []RepoCard
	Tiles       []string
	GeneratedAt time.Time
}

// HeaderView feeds tile_header.html.
type HeaderView struct {
	Date           string // "Sun 16 Aug 2026"
	Time           string // "14:00"
	DataAsOf       string // "13:55" or "never"
	Sources        []SourceStatusView
	Refreshing     int
	AttentionCount int
}

// SourceStatusView is one staleness dot in the header.
type SourceStatusView struct {
	ID         string
	Kind       string
	Age        string
	Error      string
	Healthy    bool
	AuthFailed bool
	InFlight   bool
}

// AttentionView feeds tile_attention.html.
type AttentionView struct {
	Rows     []AttentionRow
	Overflow int
	Total    int
}

// AttentionRow is one line in the Attention tile.
type AttentionRow struct {
	ID          string // domain.ItemID.String()
	Source      string // "arc42/arc42-template", "GitHub mentions", ...
	SourceShort string // "arc42-template"
	Number      string // "#236" or ""
	Title       string
	URL         string
	Author      string
	Age         string
	Bucket      string // lt24h | lt7d | lt30d | ge30d
	Badge       string // NEW | UNANSWERED | ...
	Level       string // css class
	Kind        string
	UpdatedAt   int64 // unix seconds, sent back with dismiss
}

// RepoCard feeds tile_repos.html.
type RepoCard struct {
	Name       string
	ShortName  string
	URL        string
	OpenIssues int
	OpenPRs    int
	New        int
	Unanswered int
	Build      BuildView
}

// BuildView is the CI status dot of a repo (FR-3.2).
type BuildView struct {
	State    string // ok | failed | running | unknown
	Workflow string
	URL      string
	Age      string
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
