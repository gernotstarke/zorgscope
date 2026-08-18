// The GitHub tile's display limit is tested from inside the package: how many items the tile
// shows is a constant this package owns, and a test that restated the number instead of reading it
// would keep passing after the constant changed.
package domain

import (
	"strconv"
	"testing"
	"time"
)

// The GitHub tile shows its top few and says how many it is not showing. A dashboard that listed
// every open issue across eight repositories would answer a question nobody asked, and one that
// listed five out of forty without saying so would be worse: it would look like there were five.
func TestTheGitHubTileShowsItsTopFewAndSaysHowManyMoreThereAre(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	lastVisit := now.Add(-time.Hour)

	var items []Item
	const total = 12
	for i := range total {
		items = append(items, Item{
			Source:     "github",
			ExternalID: "issue:org/repo#" + strconv.Itoa(i),
			Kind:       KindIssue,
			Number:     i,
			UpdatedAt:  now.Add(-time.Duration(i) * time.Minute),
			// The first three are new, and all three are inside the displayed window; the
			// count must come from the whole set regardless.
			FirstSeenAt: now.Add(-2 * time.Hour),
		})
	}
	const newOnes = 7
	for i := range newOnes {
		items[i].FirstSeenAt = now.Add(-time.Minute)
	}

	d := BuildDashboard(DashboardInput{
		Now:         now,
		LastVisitAt: lastVisit,
		StaleAfter:  time.Hour,
		Items:       items,
		States:      map[string]SourceState{"github": {LastSuccessAt: now}},
	})

	var tile Tile
	for _, tl := range d.Tiles {
		if tl.Name == "github" {
			tile = tl
		}
	}

	if len(tile.Items) != githubTileLimit {
		t.Errorf("the tile shows %d items, want %d", len(tile.Items), githubTileLimit)
	}
	if tile.Total != total {
		t.Errorf("Total = %d, want %d — the tile cannot say how many it left out", tile.Total, total)
	}
	// The badge counts the source, not the screen. Counting after truncation would make the
	// dashboard report fewer new items the more of them there were.
	if tile.NewCount != newOnes {
		t.Errorf("NewCount = %d, want %d — the count was taken after the list was cut",
			tile.NewCount, newOnes)
	}
	if d.NewTotal != newOnes {
		t.Errorf("NewTotal = %d, want %d", d.NewTotal, newOnes)
	}
}

// A source with fewer items than the limit shows all of them and reports no remainder, so the
// tile never says "and 0 more".
func TestAShortGitHubTileHasNoRemainder(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	items := []Item{
		{Source: "github", ExternalID: "issue:org/repo#1", Kind: KindIssue, UpdatedAt: now},
		{Source: "github", ExternalID: "issue:org/repo#2", Kind: KindIssue, UpdatedAt: now},
	}

	d := BuildDashboard(DashboardInput{
		Now: now, StaleAfter: time.Hour, Items: items,
		States: map[string]SourceState{"github": {LastSuccessAt: now}},
	})

	for _, tl := range d.Tiles {
		if tl.Name != "github" {
			continue
		}
		if len(tl.Items) != 2 || tl.Total != 2 {
			t.Errorf("Items = %d, Total = %d, want 2 and 2", len(tl.Items), tl.Total)
		}
	}
}
