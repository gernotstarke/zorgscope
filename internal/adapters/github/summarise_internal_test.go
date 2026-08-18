// summarise is tested from inside the package: what it does to a body is a decision about what
// reaches the database, and there is no way to observe it through the exported surface except by
// storing an issue.
package github

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A GitHub body arrives as Markdown source with its markup stripped, which means it still arrives
// full of newlines, indentation and blank lines. Stored as it is, every one of those collapses to
// a single space in HTML anyway — so it is collapsed here, where the result is what is meant.
func TestSummariseCollapsesWhitespace(t *testing.T) {
	tests := []struct {
		name, give, want string
	}{
		{"empty", "", ""},
		{"only whitespace", "  \n\n\t ", ""},
		{"a single line", "Add a health endpoint.", "Add a health endpoint."},
		{
			name: "paragraphs and indentation",
			give: "The container needs a probe.\n\n  - GET /healthz\n  - returns 200\n",
			want: "The container needs a probe. - GET /healthz - returns 200",
		},
		{"leading and trailing space", "  padded  ", "padded"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarise(tc.give); got != tc.want {
				t.Errorf("summarise(%q) = %q, want %q", tc.give, got, tc.want)
			}
		})
	}
}

// A long body is cut, and cut short enough that eight repositories of issue bodies stay a rounding
// error in the database.
func TestSummariseCutsALongBody(t *testing.T) {
	got := summarise(strings.Repeat("a", maxSummaryLen*3))
	if len(got) != maxSummaryLen {
		t.Errorf("length = %d, want %d", len(got), maxSummaryLen)
	}
}

// The cut lands on a rune boundary. A body is arbitrary text — an issue title in German, Japanese
// or an emoji-laden checklist — and slicing mid-rune would store invalid UTF-8, which the template
// then renders as a replacement character.
func TestSummariseCutsOnARuneBoundary(t *testing.T) {
	// Three-byte runes do not divide maxSummaryLen evenly, so the naive cut lands inside one.
	body := strings.Repeat("ä", maxSummaryLen) // two bytes each
	got := summarise(body)

	if !utf8.ValidString(got) {
		t.Fatalf("summarise produced invalid UTF-8: %q", got)
	}
	if len(got) > maxSummaryLen {
		t.Errorf("length = %d, want at most %d", len(got), maxSummaryLen)
	}

	// And with a rune width that genuinely straddles the limit.
	got = summarise(strings.Repeat("あ", maxSummaryLen)) // three bytes each
	if !utf8.ValidString(got) {
		t.Fatalf("summarise produced invalid UTF-8: %q", got)
	}
}
