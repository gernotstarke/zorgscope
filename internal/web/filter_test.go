package web

import (
	"net/url"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

func TestParseFilterReadsEveryParameter(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	f := parseFilter(url.Values{"repo": {"arc42/a"}, "kind": {"pr"}, "since": {"2026-09-01"}, "q": {" header "}}, berlin)
	want := domain.Filter{Repo: "arc42/a", Kind: domain.KindPR, CreatedSince: time.Date(2026, 9, 1, 0, 0, 0, 0, berlin), Text: "header"}
	if f.Repo != want.Repo || f.Kind != want.Kind || !f.CreatedSince.Equal(want.CreatedSince) || f.Text != want.Text {
		t.Fatalf("parseFilter = %+v, want %+v", f, want)
	}
}

func TestParseFilterIgnoresWhatItCannotRead(t *testing.T) {
	f := parseFilter(url.Values{"kind": {"task"}, "since": {"yesterday"}}, time.UTC)
	if !f.Empty() {
		t.Fatalf("unreadable values should be ignored, got %+v", f)
	}
}

func TestParseFilterAcceptsIssueAndPRAndAll(t *testing.T) {
	for in, want := range map[string]domain.Kind{"issue": domain.KindIssue, "pr": domain.KindPR, "": ""} {
		if got := parseFilter(url.Values{"kind": {in}}, time.UTC).Kind; got != want {
			t.Errorf("kind %q parsed as %q", in, got)
		}
	}
}
