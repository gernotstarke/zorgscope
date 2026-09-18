package web

import (
	"strings"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// FR-12.2: one row per author, the busiest first, each linking to GitHub and to the search for
// their login; the unknown author is named and links nowhere.
func TestContributorsPageListsAuthors(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: []domain.Item{
		{Kind: domain.KindPR, Repo: "o/a", Number: 1, Author: "amy", UpdatedAt: testNow},
		{Kind: domain.KindIssue, Repo: "o/b", Number: 2, Author: "amy", UpdatedAt: testNow.Add(-time.Hour)},
		{Kind: domain.KindIssue, Repo: "o/a", Number: 3, Author: "", UpdatedAt: testNow},
	}})
	body := getAuthed(t, h, "/contributors").Body.String()
	for _, want := range []string{
		"<title>Contributors · zorgscope</title>", "2 contributors",
		`href="https://github.com/amy"`, `href="/search?q=amy"`, "o/a, o/b",
		`class="contributor-unknown"`, `aria-current="page"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("contributors page lacks %s", want)
		}
	}
	if strings.Index(body, "github.com/amy") > strings.Index(body, "contributor-unknown") {
		t.Error("the unknown author is not last")
	}
	if strings.Contains(body, `href="https://github.com/"`) || strings.Contains(body, `q=">`) {
		t.Error("the unknown author links somewhere")
	}
}

func TestContributorsPageWithNothingOpen(t *testing.T) {
	body := getAuthed(t, dashHandler(t, &fakeSource{}), "/contributors").Body.String()
	if !strings.Contains(body, "No contributors yet.") || strings.Contains(body, "<table") {
		t.Error("an empty snapshot should say so and draw no table")
	}
}

// QS-2.3: the Contributors page over the representative fixture stays inside its budget.
func TestContributorsPageStaysInsideItsBudget(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})
	body := getAuthed(t, h, "/contributors").Body.String()
	if n := len(body); n > 150*1024 {
		t.Errorf("Contributors view is %d bytes, budget is 150 kB (QS-2.3)", n)
	} else {
		t.Logf("Contributors view is %d bytes of the 150 kB budget (QS-2.3)", n)
	}
}
