package web

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/snapshot"
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

// The date field is read in the configured timezone, not in UTC. A visitor who types a date into
// it means their own day: an item opened at one in the morning in Berlin is on the day they named,
// and reading the boundary as UTC would drop it off a "since today" filter for two hours every
// morning.
func TestTheSinceDateIsReadInTheConfiguredTimezone(t *testing.T) {
	// 2026-08-16 23:00 UTC is 2026-08-17 01:00 in Berlin, so it is on the named day there and on
	// the day before it in UTC.
	item := ghItem(1, "Opened in the small hours", testNow.Add(-11*time.Hour))
	src := &fakeSource{items: []domain.Item{item}}

	for zone, want := range map[string]bool{"Europe/Berlin": true, "UTC": false} {
		t.Run(zone, func(t *testing.T) {
			h := newTestServerWith(t, func(o *Options) {
				o.Config.Timezone = zone
				o.Cache = snapshot.New(src, time.Hour, o.Clock)
			}).Handler()

			shown := strings.Contains(
				getAuthed(t, h, "/?since=2026-08-17").Body.String(), "Opened in the small hours")
			if shown != want {
				t.Errorf("in %s the item is shown = %v, want %v: the date boundary is being read "+
					"in the wrong zone", zone, shown, want)
			}
		})
	}
}

// A timezone that will not load is a start-up failure rather than a silent fall back to UTC, for
// the reason the test above demonstrates: the date boundary is read in it, and a dashboard quietly
// filtering by the wrong day is worse than one that refuses to start.
func TestNewRejectsATimezoneItCannotLoad(t *testing.T) {
	o := testOptions()
	o.Config.Timezone = "Mars/Olympus_Mons"
	if _, err := New(o); err == nil {
		t.Fatal("New() error = nil, want an error for an unloadable timezone")
	}
}
