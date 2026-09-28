package web

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// webAlert is a Dependabot alert in org/repo, as the adapter would map one (FR-1.16).
func webAlert(number int, s domain.Severity, fixPR int) domain.Item {
	return domain.Item{
		Kind: domain.KindAlert, Repo: "org/repo", Number: number, Author: "dependabot", State: "OPEN",
		Title:      "rubyzip path traversal vulnerability",
		Summary:    "rubyzip 2.3.2 → 3.4.0 · Gemfile.lock",
		URL:        "https://github.com/org/repo/security/dependabot/" + strconv.Itoa(number),
		Advisories: []string{"GHSA-47m2-wp7j-p9vc"},
		Alert:      &domain.AlertFacts{Severity: s, Package: "rubyzip", PatchedIn: "3.4.0", FixPR: fixPR},
		CreatedAt:  testNow.Add(-48 * time.Hour), UpdatedAt: testNow.Add(-48 * time.Hour),
	}
}

// FR-1.16 AC1, AC4: an alert is a row of its repository's group with its own chip, severity and
// mark; serious ones solid, the others outlined — and the header counts them, linking to them.
func TestTheListDrawsAlerts(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: []domain.Item{
		webAlert(62, domain.SeverityHigh, 0), webAlert(64, domain.SeverityLow, 0), ghItem(1, "Fix the header", testNow),
	}})
	body := getAuthed(t, h, "/").Body.String()
	for _, want := range []string{
		`class="item is-needed tier-alert sev-high"`,
		`class="item tier-alert sev-low"`,
		`tier-chip tier-chip-alert sev-high" title="Dependabot: rubyzip 2.3.2 → 3.4.0 · Gemfile.lock, GHSA-47m2-wp7j-p9vc"`,
		`Alert · high</span>`,
		`Alert · low</span>`,
		`<span class="item-kind item-kind-alert">Alert</span> · dependabot · raised`,
		`<a href="/?tier=alert">2 alerts (1 high)</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the list lacks %s", want)
		}
	}
}

// FR-1.16 AC7: kind=alert and tier=alert narrow the list to alerts; tier=security keeps them.
func TestTheFilterNarrowsToAlerts(t *testing.T) {
	src := &fakeSource{items: []domain.Item{
		webAlert(62, domain.SeverityHigh, 0),
		{Kind: domain.KindPR, Repo: "org/repo", Number: 5, Title: "Bump it", Advisories: []string{"CVE-2026-1"}, UpdatedAt: testNow},
		ghItem(1, "Fix the header", testNow),
	}}
	h := dashHandler(t, src)
	for path, want := range map[string][]int{
		"/?kind=alert":    {62},
		"/?tier=alert":    {62},
		"/?tier=security": {62, 5},
	} {
		body := getAuthed(t, h, path).Body.String()
		list := body[strings.Index(body, `id="items"`):]
		for _, n := range []int{62, 5, 1} {
			wanted := false
			for _, w := range want {
				wanted = wanted || w == n
			}
			has := strings.Contains(list, `<span class="item-number">#`+strconv.Itoa(n)+`</span>`)
			if has != wanted {
				t.Errorf("%s: #%d shown = %v, want %v", path, n, has, wanted)
			}
		}
	}
}

// FR-1.16 AC2: a serious alert leads the band, frames it in the alert colour, says its fix is
// ready and absorbs the fix pull request; a low alert needs nobody.
func TestTheBandLeadsWithSeriousAlerts(t *testing.T) {
	fix := domain.Item{Kind: domain.KindPR, Repo: "org/repo", Number: 30, Title: "Bump rubyzip", Author: "dependabot",
		Advisories: []string{"GHSA-47m2-wp7j-p9vc"}, UpdatedAt: testNow}
	h := needsHandler(t, []domain.Item{webAlert(62, domain.SeverityHigh, 30), webAlert(64, domain.SeverityLow, 0), fix})
	body := getAuthed(t, h, "/").Body.String()
	band := body[strings.Index(body, `<section class="needs`):]
	band = band[:strings.Index(band, "</section>")]
	for _, want := range []string{`class="needs has-alert"`, `need need-alert tier-alert sev-high`, `fix ready: #30`, `<span class="needs-count">1</span>`} {
		if !strings.Contains(band, want) {
			t.Errorf("the band lacks %s", want)
		}
	}
	if strings.Contains(band, "#30</span> <a") || strings.Contains(band, "#64") {
		t.Error("the band lists the fix pull request or the low alert")
	}
	if !strings.Contains(body, "1 needs you") && !strings.Contains(body, "1 need you") {
		t.Error("the top bar does not count the serious alert")
	}
}

// FR-1.16 AC5: the Security tile lists alerts first, counts them, names the repositories it cannot
// vouch for, and is "All clear" only when nothing is open and every repository's alerts were read.
func TestTheSecurityTileShowsAlertsAndCoverage(t *testing.T) {
	h := sitesHandler(t, &fakeSource{
		items: []domain.Item{func() domain.Item { a := webAlert(62, domain.SeverityHigh, 0); a.Repo = "arc42/quality"; return a }()},
		coverage: map[string]domain.Coverage{
			"arc42/org": domain.CoverageOn, "arc42/de": domain.CoverageOn, "arc42/quality": domain.CoverageOn,
			"arc42/template": domain.CoverageOff, "gernotstarke/zorgscope": domain.CoverageUnavailable,
		},
	})
	body := getAuthed(t, h, "/sites").Body.String()
	for _, want := range []string{
		`tile tile-tier is-alerted"`,
		`<span class="tile-count">1 high alert</span>`,
		`<h3 class="tile-kind">Dependabot alerts</h3>`,
		`Alerts off: template`,
		`Alerts unavailable to this token: zorgscope`,
		`raised <time`,
		`1 high alert<span class="visually-hidden"> of quality.arc42.org</span>`,
		`href="/?kind=alert&amp;repo=arc42%2Fquality"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/sites lacks %s", want)
		}
	}
	if strings.Contains(body, "All clear") {
		t.Error("the tile says All clear with an alert open")
	}

	off := sitesHandler(t, &fakeSource{coverage: map[string]domain.Coverage{"arc42/org": domain.CoverageOff}})
	body = getAuthed(t, off, "/sites").Body.String()
	if strings.Contains(body, "All clear") || !strings.Contains(body, "Alerts off: org") ||
		!strings.Contains(body, "No security or dependency items, no Dependabot alerts open.") {
		t.Error("a tile with a repository's alerts off claims more than it knows")
	}
}

// FR-1.16 AC4: on the radar an alert is a triangle; a serious one is ringed in crosshatch, pulses
// and is tagged; a low one is ringed thinly; neither fades, and they are counted apart from issues.
func TestTheRadarDrawsAlerts(t *testing.T) {
	gh := radarGitHub
	gh.Owner = testOwner
	high := webAlert(62, domain.SeverityHigh, 0)
	high.Repo = gh.Repos[0]
	low := webAlert(64, domain.SeverityLow, 0)
	low.Repo = gh.Repos[0]
	low.UpdatedAt = testNow.AddDate(-1, 0, 0)
	v := buildRadar([]domain.Item{high, low}, gh, testNow)
	if v.Alerts != 2 || v.Serious != 1 || v.Issues != 0 || v.PRs != 0 {
		t.Errorf("Alerts, Serious, Issues, PRs = %d, %d, %d, %d; want 2, 1, 0, 0", v.Alerts, v.Serious, v.Issues, v.PRs)
	}
	marks := map[string]radarBlip{}
	for _, b := range v.Blips {
		marks[b.Mark] = b
	}
	h, l := marks["alert"], marks["alert-low"]
	if !strings.HasPrefix(h.Shape, "M") || !strings.HasSuffix(h.Shape, ",0Z") || h.Tag != "▲ #62" || h.Ring != 15 {
		t.Errorf("serious alert blip = %+v", h)
	}
	if l.Tag != "" || l.Ring != 12 || l.Age != "age-0" {
		t.Errorf("low alert blip = %+v: untagged, thin ring, never faded", l)
	}
	if v.Blips[len(v.Blips)-1].Mark != "alert" {
		t.Error("the serious alert is not drawn on top")
	}
	if !strings.HasPrefix(h.Card.Head, "ALERT · ") || !strings.Contains(h.Card.Reason.Text, "Alert · high: rubyzip") {
		t.Errorf("card = %q / %q", h.Card.Head, h.Card.Reason.Text)
	}
}

// FR-12.1: "alert" narrows a search to alerts, which are found by package.
func TestSearchFindsAlertsByKeyword(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: []domain.Item{webAlert(62, domain.SeverityHigh, 0), ghItem(1, "rubyzip upgrade notes", testNow)}})
	body := getAuthed(t, h, "/search?q=alert+rubyzip").Body.String()
	if !strings.Contains(body, "Dependabot alert") || !strings.Contains(body, " tier-alert sev-high") {
		t.Error("the search does not show the alert")
	}
	if strings.Contains(body, "rubyzip upgrade notes") {
		t.Error("the alert keyword let an issue through")
	}
}
